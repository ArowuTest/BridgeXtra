package origination_test

// M2e pack: real-time overlays (V2-SCR-015), decision validity at the
// boundary (EDG-014), consent evidence in the confirm tx (V2-REG-001), and
// notification evidence + delivery via the canonical SMS contract (§10.2).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/notify"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

func (f *fixture) raiseFlag(t *testing.T, subID, flag string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO subscriber_flags (flag_id, telco_id, subscriber_account_id, flag, source)
		VALUES ('flg_'||$1||'_'||$2, 'SIM_NG', $1, $2, 'test')`, subID, flag); err != nil {
		t.Fatal(err)
	}
}

func TestM2E_OverlayBlocksOfferAndConfirm(t *testing.T) {
	f := newFixture(t, "m2e_overlay", 0, 2_000)
	f.seedSubscriber(t, "sub_ovl", "tok_ovl", 50_000)

	// Offers exist BEFORE the flag (so the confirm path is reachable).
	offers := f.offersFor(t, "tok_ovl")

	f.raiseFlag(t, "sub_ovl", "FRAUD_SUSPECT")

	// OFFER checkpoint blocked.
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_ovl"); !errors.Is(err, origination.ErrOverlayBlocked) {
		t.Fatalf("flagged subscriber must be overlay-blocked at OFFER, got %v", err)
	}
	// CONFIRM checkpoint blocked — the money-moving moment.
	_, err := f.svc.Confirm(tenantCtx(), acceptFor(offers[0], "tok_ovl", "ovl-confirm-1", "cor-ovl-1"))
	if !errors.Is(err, origination.ErrOverlayBlocked) {
		t.Fatalf("flagged subscriber must be overlay-blocked at CONFIRM, got %v", err)
	}

	// Clearing the flag restores service.
	if _, err := f.db.Admin.Exec(context.Background(), `
		UPDATE subscriber_flags SET effective_to = now() WHERE subscriber_account_id='sub_ovl'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(tenantCtx(), acceptFor(offers[0], "tok_ovl", "ovl-confirm-2", "cor-ovl-2")); err != nil {
		t.Fatalf("cleared flag must restore service: %v", err)
	}
}

func TestM2E_SimSwapCooloff_ExpiresByConfig(t *testing.T) {
	f := newFixture(t, "m2e_simswap", 0, 2_000)
	f.seedSubscriber(t, "sub_swp", "tok_swp", 50_000)

	// A SIM swap raised 100h ago: outside the seeded 72h cool-off.
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO subscriber_flags (flag_id, telco_id, subscriber_account_id, flag, source, effective_from)
		VALUES ('flg_swp_old', 'SIM_NG', 'sub_swp', 'SIM_SWAP', 'test', now() - interval '100 hours')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_swp"); err != nil {
		t.Fatalf("SIM swap outside cool-off must not block: %v", err)
	}

	// A fresh SIM swap (inside 72h) blocks.
	f.seedSubscriber(t, "sub_swp2", "tok_swp2", 50_000)
	f.raiseFlag(t, "sub_swp2", "SIM_SWAP")
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_swp2"); !errors.Is(err, origination.ErrOverlayBlocked) {
		t.Fatalf("fresh SIM swap must block inside cool-off, got %v", err)
	}
}

func TestM2E_EDG014_ExpiredDecision_NoOfferNoConfirm(t *testing.T) {
	f := newFixture(t, "m2e_stale", 0, 2_000)
	f.seedSubscriber(t, "sub_stale", "tok_stale", 50_000)
	offers := f.offersFor(t, "tok_stale")

	// Expire the decision AFTER the offer was minted (menu-to-confirm gap).
	if _, err := f.db.Admin.Exec(context.Background(), `
		UPDATE decision_snapshots SET valid_until = now() - interval '1 minute'
		WHERE subscriber_account_id = 'sub_stale'`); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Confirm(tenantCtx(), acceptFor(offers[0], "tok_stale", "stale-confirm-1", "cor-stale-1"))
	if !errors.Is(err, origination.ErrDecisionUnavailable) {
		t.Fatalf("EDG-014: confirming against an expired decision must refuse, got %v", err)
	}
	// Ladder regeneration from the expired decision must also refuse: expire
	// the standing offers to force regeneration through the decision gate.
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE offers SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_stale"); !errors.Is(err, origination.ErrDecisionUnavailable) {
		t.Fatalf("EDG-014: ladder regeneration from an expired decision must refuse, got %v", err)
	}
}

func TestM2E_ConsentEvidence_WrittenInConfirmTx(t *testing.T) {
	f := newFixture(t, "m2e_consent", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	ov := offers[0]

	res, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_sim_0001", "cns-confirm-1", "cor-cns-1"))
	if err != nil {
		t.Fatal(err)
	}

	// R-P0-7: the consent record binds the disclosure snapshot the customer was
	// shown (not a server reconstruction) plus the channel/session/acceptance
	// evidence, and its content hash equals the snapshot's — proving the terms
	// accepted are the terms disclosed.
	var terms []byte
	var hash, snapID, channel, sessionID string
	var acceptedAt time.Time
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT disclosed_terms, content_hash, disclosure_snapshot_id, channel, session_id, accepted_at
		FROM consents WHERE advance_id = $1`,
		res.Advance.AdvanceID).Scan(&terms, &hash, &snapID, &channel, &sessionID, &acceptedAt); err != nil {
		t.Fatalf("V2-REG-001: an advance must carry consent evidence: %v", err)
	}
	if snapID != ov.Disclosure.DisclosureSnapshotID {
		t.Fatalf("consent must bind the disclosure the customer was shown: %s vs %s", snapID, ov.Disclosure.DisclosureSnapshotID)
	}
	if hash != ov.Disclosure.ContentHash {
		t.Fatalf("consent content hash must equal the disclosure snapshot hash: %s vs %s", hash, ov.Disclosure.ContentHash)
	}
	if channel != "USSD" || sessionID != "sess-cns-confirm-1" || acceptedAt.IsZero() {
		t.Fatalf("consent must record real channel/session/acceptance evidence: %s/%s/%v", channel, sessionID, acceptedAt)
	}
	var tv struct {
		FaceValueMinor int64  `json:"face_value_minor"`
		RepaymentMinor int64  `json:"repayment_minor"`
		RenderedBody   string `json:"rendered_body"`
	}
	if err := json.Unmarshal(terms, &tv); err != nil {
		t.Fatal(err)
	}
	if tv.FaceValueMinor != ov.Offer.FaceValue.Amount() || tv.RepaymentMinor != ov.Offer.Repayment.Amount() || tv.RenderedBody == "" {
		t.Fatalf("consent must record the EXACT disclosed terms + rendered text: %s", terms)
	}

	// Replay does not duplicate consent (UNIQUE(advance_id)).
	if _, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_sim_0001", "cns-confirm-1", "cor-cns-1")); err != nil {
		t.Fatal(err)
	}
	var consents int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM consents`).Scan(&consents); err != nil {
		t.Fatal(err)
	}
	if consents != 1 {
		t.Fatalf("replayed confirm must not duplicate consent: %d", consents)
	}
}

func TestM2E_NotificationEvidence_SentViaCanonicalSMS(t *testing.T) {
	f := newFixture(t, "m2e_notify", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	res, err := f.svc.Confirm(tenantCtx(), acceptFor(offers[0], "tok_sim_0001", "ntf-confirm-1", "cor-ntf-1"))
	if err != nil {
		t.Fatal(err)
	}

	notifier := notify.New(f.db.App, configsvc.New(f.db.App), slog.Default())
	if err := notifier.AdvanceConfirmed(context.Background(), "SIM_NG", res.Advance.AdvanceID); err != nil {
		t.Fatal(err)
	}
	// Replay is a no-op (evidence idempotent, telco idempotent).
	if err := notifier.AdvanceConfirmed(context.Background(), "SIM_NG", res.Advance.AdvanceID); err != nil {
		t.Fatal(err)
	}

	var state, providerRef, tplVersion string
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT state, provider_ref, template_version FROM notifications
		WHERE advance_id = $1 AND kind = 'ADVANCE_CONFIRMED'`,
		res.Advance.AdvanceID).Scan(&state, &providerRef, &tplVersion); err != nil {
		t.Fatal(err)
	}
	if state != "SENT" || providerRef == "" || tplVersion != "v1" {
		t.Fatalf("evidence must be SENT with provider ref + template version: %s/%s/%s", state, providerRef, tplVersion)
	}

	// Telco-side evidence: exactly ONE message, rendered from the governed
	// template with display amounts.
	raw := testutil.HTTPGet(t, f.simURL+"/sim/sms")
	var msgs []struct {
		MSISDNToken string `json:"msisdn_token"`
		SenderID    string `json:"sender_id"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("exactly one SMS must reach the telco (idempotent replay), got %d", len(msgs))
	}
	if msgs[0].SenderID != "BridgeXtra" || msgs[0].MSISDNToken != "tok_sim_0001" {
		t.Fatalf("sender/recipient from governed config: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Body, "NGN 50.00") {
		t.Fatalf("body must carry display amounts (face NGN 50.00): %q", msgs[0].Body)
	}
}
