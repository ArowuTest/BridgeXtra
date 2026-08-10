// Package e2e_test is the V3-DLV-003 walking-skeleton demonstration: every
// component wired — channel HTTP, saga, resolver, outbox dispatcher, recovery,
// reconciliation, invariant checker — and the full money story proven against
// the simulator, ending with clean books.
//
// The BC-3 property test here is the randomized-history form the reviewer
// verifies at G1: seeded random subscribers, scenario tokens, duplicate keys,
// interleaved resolver runs, random recovery amounts (under/exact/over,
// duplicated source ids) — and after all of it, ZERO invariant violations and
// ZERO reconciliation breaks. Examples prove presence; this proves absence.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/invariants"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/fulfilmentresolver"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/outboxdispatch"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
	"github.com/ArowuTest/telco-credit-platform/simulator/sim"
)

const (
	e2eWhKeyID     = "e2e-kid"
	e2eWhSecretEnv = "TCP_E2E_WH_SECRET"
	e2eWhSecret    = "e2e-hmac-shared-secret"
)

type stack struct {
	db         *testutil.DB
	api        *httptest.Server
	sim        *sim.Simulator
	resolver   *fulfilmentresolver.Service
	dispatcher *outboxdispatch.Dispatcher
	recon      *recon.Service
	checker    *invariants.Checker
	events     map[string]int // dispatched outbox events by type
	baseTime   time.Time      // pinned occurred_at so replayed recovery src ids stay byte-identical
}

func newStack(t *testing.T, suffix string, simHold time.Duration, adapterTimeoutMs int) *stack {
	t.Helper()
	t.Setenv(e2eWhSecretEnv, e2eWhSecret)
	db := testutil.MustSetup(t, suffix)
	db.SeedTelco(t, "SIM_NG", "e2e-api-key")

	simulator := sim.New(slog.Default(), "e2e", simHold)
	simSrv := httptest.NewServer(simulator.Handler())
	t.Cleanup(simSrv.Close)

	// Point the adapter at this simulator through the governed lifecycle.
	cfgW := configsvc.New(db.Worker)
	ctx := context.Background()
	content := fmt.Sprintf(`{"fulfilment_url":%q,"request_timeout_ms":%d,"retry_budget":0,"circuit_error_threshold_pct":50,"circuit_min_requests":20,"circuit_cooldown_seconds":30,"max_weekly_recharge_minor":100000000}`, simSrv.URL, adapterTimeoutMs)
	c, err := cfgW.CreateDraft(ctx, "telco.adapter", "telco:SIM_NG", "alice", "e2e sim", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return cfgW.Submit(ctx, c.ConfigVersionID, "alice") },
		func() error { return cfgW.Approve(ctx, c.ConfigVersionID, "bob") },
		func() error { return cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	appCfg := configsvc.New(db.App)
	led := ledger.New(appCfg)
	adapter := mno.NewHTTPAdapter(appCfg)
	orig := origination.New(db.App, appCfg, led, adapter, slog.Default())
	rec := recovery.New(db.App, appCfg, led, slog.Default())
	resolver := fulfilmentresolver.New(db.App, appCfg, adapter, orig, slog.Default())

	// Full HTTP surface, exactly as cmd/api mounts it.
	telcos := &repo.Telcos{Pool: db.App}
	auth := &handler.TenantAuth{Telcos: telcos, Pool: db.App, Log: slog.Default()}
	mux := http.NewServeMux()
	(&handler.Channel{Origination: orig, Recovery: rec, Limiter: ratelimit.New(map[string]ratelimit.Limit{
		"channel":    {RatePerMinute: 1e9, Burst: 1e9},
		"channel_ip": {RatePerMinute: 1e9, Burst: 1e9},
	}), Log: slog.Default()}).Mount(mux, auth)

	// BX-P0-002: recovery has exactly ONE external ingress — the HMAC-signed
	// recharge webhook. Mount it on the SAME surface, arm the recon-live gate and
	// activate the governed feed config so E2E recoveries flow through the real
	// money door ("wh:" namespacing + per-event/daily HELD clamps) that EOD recon
	// covers (recon selects LIKE 'wh:%'), never a backdoor.
	activateRechargeFeedE2E(t, cfgW, "global", 50_000_000)
	activateRechargeFeedE2E(t, cfgW, "telco:SIM_NG", 50_000_000)
	if err := (&repo.WebhookCredentials{Pool: db.Admin}).Create(ctx, e2eWhKeyID, "SIM_NG", e2eWhSecretEnv, "e2e"); err != nil {
		t.Fatal(err)
	}
	arm := &repo.ReconArming{Pool: db.Admin}
	if err := arm.SetLive(ctx, "SIM_NG", repo.ReconLayerRecovery); err != nil {
		t.Fatal(err)
	}
	if _, err := arm.AdvanceFreshness(ctx, "SIM_NG", repo.ReconLayerRecovery, 172800); err != nil {
		t.Fatal(err)
	}
	(&handler.RechargeWebhook{
		Recovery: rec, Config: appCfg,
		Creds: &repo.WebhookCredentials{Pool: db.App}, Recon: &repo.ReconArming{Pool: db.App},
		Pool: db.App, Auth: rechargewebhook.NewHMACSHA256Adapter(), Mapper: rechargewebhook.NewJSONMapper(),
		Limiter: ratelimit.New(map[string]ratelimit.Limit{
			"channel":    {RatePerMinute: 1e9, Burst: 1e9},
			"channel_ip": {RatePerMinute: 1e9, Burst: 1e9},
		}), Log: slog.Default(),
	}).Mount(mux)
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)

	// Dispatcher with handlers for every M1 event type (counting sink).
	events := map[string]int{}
	d := outboxdispatch.New(db.Worker, cfgW, slog.Default())
	for _, et := range []string{
		"advance.FulfilmentConfirmed", "advance.FulfilmentFailed",
		"advance.FulfilmentUnknown", "advance.RecoveryApplied",
	} {
		et := et
		d.Register(et, func(ctx context.Context, e entity.OutboxEvent) error {
			events[et]++
			return nil
		})
	}

	return &stack{
		db: db, api: api, sim: simulator, resolver: resolver, dispatcher: d,
		recon:    recon.New(db.App, appCfg, slog.Default()),
		checker:  &invariants.Checker{Pool: db.Worker},
		events:   events,
		baseTime: time.Now().UTC(),
	}
}

// activateRechargeFeedE2E activates the governed telco.recharge_feed config so the
// signed recharge webhook accepts + ingests (kill-switch enabled, generous clamps).
func activateRechargeFeedE2E(t *testing.T, cfgW *configsvc.Service, scope string, perEventMax int64) {
	t.Helper()
	ctx := context.Background()
	content := fmt.Sprintf(`{"enabled":true,"transport":"webhook_push","auth":"hmac_sha256","key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp","replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN","per_event_amount_max_minor":%d,"per_telco_daily_ceiling_minor":50000000000,"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}`, perEventMax)
	c, err := cfgW.CreateDraft(ctx, "telco.recharge_feed", scope, "alice", "e2e", []byte(content))
	if err != nil {
		t.Fatalf("recharge_feed draft %s: %v", scope, err)
	}
	if err := cfgW.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cfgW.Activate(ctx, c.ConfigVersionID, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// postRecovery books a recovery through the ONLY external recovery ingress — the
// HMAC-signed recharge webhook (BX-P0-002). occurred_at is pinned to the stack's
// base time so a replayed src is byte-identical (idempotent, never a divergent-
// duplicate refusal). Returns HTTP status + body; callers needing ingestion assert
// 200 (see (*stack).recover).
func (s *stack) postRecovery(t *testing.T, token, eventID string, amount int64) (int, []byte) {
	t.Helper()
	body := fmt.Sprintf(`{"event_id":%q,"msisdn_token":%q,"amount_minor":%d,"currency":"NGN","occurred_at":%q}`,
		eventID, token, amount, s.baseTime.Format(time.RFC3339))
	ts := fmt.Sprintf("%d", time.Now().UTC().Unix())
	sig := rechargewebhook.Sign(rechargewebhook.NewHMACSHA256Adapter(), []byte(e2eWhSecret), e2eWhKeyID, ts, []byte(body))
	req, err := http.NewRequest(http.MethodPost, s.api.URL+"/v1/telcos/SIM_NG/recharge-webhook", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Bx-Key-Id", e2eWhKeyID)
	req.Header.Set("X-Bx-Timestamp", ts)
	req.Header.Set("X-Bx-Signature", sig)
	resp, err := s.api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (s *stack) seedSubscriber(t *testing.T, subID, token string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.Admin.Exec(ctx, `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status, nin_verified)
		VALUES ($1,'SIM_NG',$2,'ACTIVE',true)`, subID, token); err != nil { // Build 2: verified by default
		t.Fatal(err)
	}
	if _, err := s.db.Admin.Exec(ctx, `
		INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		  max_face_value_minor, currency, config_version_id)
		VALUES ('dec_'||$1,'SIM_NG',$1,1000000,'NGN','cfg_seed_product_airtime_v1')`, subID); err != nil {
		t.Fatal(err)
	}
}

func (s *stack) http(t *testing.T, method, path, idemKey string, body any) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, s.api.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "e2e-api-key")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := s.api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (s *stack) offersFor(t *testing.T, token string) []struct {
	OfferID       string `json:"offer_id"`
	DisclosureRef string `json:"disclosure_ref"`
} {
	t.Helper()
	code, body := s.http(t, http.MethodPost, "/v1/offers", "",
		map[string]string{"programme_id": "prg_sim_airtime01", "msisdn_token": token})
	if code != http.StatusOK {
		t.Fatalf("offers %s: %d %s", token, code, body)
	}
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.Unmarshal(body, &offers); err != nil {
		t.Fatal(err)
	}
	return offers
}

// confirmBody builds a confirm request carrying the R-P0-7 disclosure/channel
// acceptance evidence for a presented offer — exactly as the real channel does.
func confirmBody(offerID, disclosureRef, token, session string) map[string]any {
	return map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offerID, "msisdn_token": token,
		"disclosure_ref": disclosureRef, "channel": "USSD",
		"session_id": session, "accepted_at": time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *stack) makeEnquiriesDue(t *testing.T) {
	t.Helper()
	if _, err := s.db.Admin.Exec(context.Background(),
		`UPDATE fulfilment_attempts SET next_enquiry_at = now() - interval '1 second'
		 WHERE state = 'UNKNOWN'`); err != nil {
		t.Fatal(err)
	}
}

func (s *stack) assertClean(t *testing.T, wantMatched int) {
	t.Helper()
	// Invariants: zero violations, always.
	violations, err := s.checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("INVARIANT VIOLATION: %s", v)
	}
	// Reconciliation: zero breaks. R-P0-6 Slice C bounds a run to settled
	// activity (now - lag); this E2E just created its money moments ago, so
	// reconcile an EXPLICIT window that spans it rather than waiting out the lag.
	sum, err := s.recon.ReconcilePeriod(context.Background(), "SIM_NG", "prg_sim_airtime01",
		time.Unix(0, 0).UTC(), time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sum.MissingPlatform+sum.MissingTelco+sum.AmountMismatch != 0 {
		t.Fatalf("reconciliation breaks: %+v", sum)
	}
	if wantMatched >= 0 && sum.Matched != wantMatched {
		t.Fatalf("recon matched=%d want %d", sum.Matched, wantMatched)
	}
}

// TestWalkingSkeleton_E2E is the DLV-003 demo: the full money story with an
// ambiguity in the middle, every worker doing its real job, clean books at
// the end.
func TestWalkingSkeleton_E2E(t *testing.T) {
	s := newStack(t, "e2e_demo", 2*time.Second, 300)
	s.seedSubscriber(t, "sub_ok", "tok_e2e_ok")
	s.seedSubscriber(t, "sub_to", "tok_TIMEOUT_e2e")

	// 1. Happy subscriber: confirm -> 201 ACTIVE... but adapter timeout is
	// 300ms and the sim answers instantly for normal tokens.
	okOffers := s.offersFor(t, "tok_e2e_ok")
	code, body := s.http(t, http.MethodPost, "/v1/advances", "e2e-ok-1",
		confirmBody(okOffers[0].OfferID, okOffers[0].DisclosureRef, "tok_e2e_ok", "sess-e2e-ok"))
	if code != http.StatusCreated {
		t.Fatalf("happy confirm: %d %s", code, body)
	}

	// 2. Timeout subscriber: 202 PROCESSING (credit happened telco-side).
	toOffers := s.offersFor(t, "tok_TIMEOUT_e2e")
	code, body = s.http(t, http.MethodPost, "/v1/advances", "e2e-to-1",
		confirmBody(toOffers[0].OfferID, toOffers[0].DisclosureRef, "tok_TIMEOUT_e2e", "sess-e2e-to"))
	if code != http.StatusAccepted {
		t.Fatalf("timeout confirm must be 202: %d %s", code, body)
	}

	// 3. Resolver closes the ambiguity (EDG-005 continuation).
	s.makeEnquiriesDue(t)
	if n, err := s.resolver.RunOnce(context.Background(), "SIM_NG", 10); err != nil || n != 1 {
		t.Fatalf("resolver: n=%d err=%v", n, err)
	}

	// 4. Dispatcher drains the outbox (per-aggregate FIFO, real handlers).
	for i := 0; i < 5; i++ {
		if _, err := s.dispatcher.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var unpublished int
	if err := s.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 0 {
		t.Fatalf("outbox must drain, %d left", unpublished)
	}
	if s.events["advance.FulfilmentConfirmed"] != 2 {
		t.Fatalf("both fulfilments must emit Confirmed events, got %d", s.events["advance.FulfilmentConfirmed"])
	}

	// 5. Recoveries close both advances over the wire — through the signed recharge
	// webhook, the only external recovery ingress (BX-P0-002); recon covers "wh:".
	for i, tok := range []string{"tok_e2e_ok", "tok_TIMEOUT_e2e"} {
		if code, respBody := s.postRecovery(t, tok, fmt.Sprintf("e2e-src-%d", i), 10000); code != http.StatusOK {
			t.Fatalf("recovery %s: %d %s", tok, code, respBody)
		}
	}

	// 6. Books clean: zero violations, recon fully matched, receivable zero,
	// pool fully released.
	s.assertClean(t, 2)
	var receivable, reserved, utilised int64
	if err := s.db.Admin.QueryRow(context.Background(), `
		SELECT (SELECT COALESCE(SUM(debit_minor-credit_minor),0) FROM journal_entries
		        WHERE account_code='SUBSCRIBER_RECEIVABLE'),
		       (SELECT reserved_minor FROM funding_pools WHERE pool_id='pool_sim_01'),
		       (SELECT utilised_minor FROM funding_pools WHERE pool_id='pool_sim_01')`).
		Scan(&receivable, &reserved, &utilised); err != nil {
		t.Fatal(err)
	}
	if receivable != 0 || reserved != 0 || utilised != 0 {
		t.Fatalf("books not clean: receivable=%d reserved=%d utilised=%d", receivable, reserved, utilised)
	}
}

// TestBC3_RandomizedHistories_InvariantsAlwaysHold is the property form:
// randomized operation histories, not curated examples. Deterministic seed —
// any failure replays exactly.
func TestBC3_RandomizedHistories_InvariantsAlwaysHold(t *testing.T) {
	s := newStack(t, "e2e_property", 2*time.Second, 300)
	rng := rand.New(rand.NewSource(20260718)) // deterministic

	const subscribers = 24
	tokens := make([]string, subscribers)
	for i := 0; i < subscribers; i++ {
		var tok string
		switch rng.Intn(4) {
		case 0:
			tok = fmt.Sprintf("tok_FAIL_p%02d", i) // telco rejects
		case 1:
			tok = fmt.Sprintf("tok_TIMEOUT_p%02d", i) // timeout-after-success
		default:
			tok = fmt.Sprintf("tok_ok_p%02d", i)
		}
		tokens[i] = tok
		s.seedSubscriber(t, fmt.Sprintf("sub_p%02d", i), tok)
	}

	// Phase 1: randomized confirms — some duplicated keys, interleaved
	// resolver runs.
	for i, tok := range tokens {
		offers := s.offersFor(t, tok)
		key := fmt.Sprintf("p-key-%02d", i)
		reps := 1 + rng.Intn(3) // duplicates exercise EDG-001 constantly
		for r := 0; r < reps; r++ {
			off := offers[rng.Intn(len(offers))]
			s.http(t, http.MethodPost, "/v1/advances", key,
				confirmBody(off.OfferID, off.DisclosureRef, tok, "sess-"+key))
		}
		if i%5 == 0 {
			s.makeEnquiriesDue(t)
			if _, err := s.resolver.RunOnce(context.Background(), "SIM_NG", 50); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Resolve all remaining ambiguity.
	for i := 0; i < 3; i++ {
		s.makeEnquiriesDue(t)
		if _, err := s.resolver.RunOnce(context.Background(), "SIM_NG", 50); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: randomized recoveries — under/exact/over amounts, duplicated
	// source ids (EDG-018/020 under randomness).
	for i, tok := range tokens {
		nEvents := rng.Intn(3) // 0..2 recovery events per subscriber
		for e := 0; e < nEvents; e++ {
			amt := int64(500 + rng.Intn(7000)) // spans partial..over
			src := fmt.Sprintf("p-src-%02d-%d", i, e)
			reps := 1 + rng.Intn(2) // telco replays
			for r := 0; r < reps; r++ {
				// Individual outcome ignored — asserted collectively at assertClean.
				s.postRecovery(t, tok, src, amt)
			}
		}
	}

	// Phase 3: drain the dispatcher, then THE assertion — every invariant
	// holds and reconciliation is break-free, whatever history randomness
	// produced.
	for i := 0; i < 8; i++ {
		if _, err := s.dispatcher.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s.assertClean(t, -1) // matched count varies with the random history
}
