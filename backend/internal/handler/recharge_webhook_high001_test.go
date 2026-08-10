package handler_test

// BX-HIGH-001 end-to-end: concurrent signed webhook deliveries against a small governed daily
// ceiling. Admitted events book no more than the ceiling; excess events become HELD (never
// dropped); and a retry of the same event (even under a different MAC/timestamp) does not
// consume ceiling capacity twice. This exercises the whole path — HMAC auth, recon gate,
// per-event clamp, the atomic reservation, and the per-event dedup — not just the repo unit.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// activateFeedWithCeiling supersedes the telco feed config with a specific daily ceiling.
func activateFeedWithCeiling(t *testing.T, cfgW *configsvc.Service, scope string, perEventMax, dailyCeiling int64) {
	t.Helper()
	ctx := context.Background()
	content := fmt.Sprintf(`{"enabled":true,"transport":"webhook_push","auth":"hmac_sha256","key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp","replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN","per_event_amount_max_minor":%d,"per_telco_daily_ceiling_minor":%d,"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}`, perEventMax, dailyCeiling)
	c, err := cfgW.CreateDraft(ctx, "telco.recharge_feed", scope, "alice", "small ceiling", []byte(content))
	if err != nil {
		t.Fatalf("draft: %v", err)
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

func TestBXHIGH001_ConcurrentWebhook_CeilingHolds_ExcessHeld(t *testing.T) {
	f := newWebhookFixture(t, "wh_high001_ceiling", true, 20_000, true)
	ctx := context.Background()
	// Small ceiling: 30000 kobo, events of 10000 -> exactly 3 may book, the rest are HELD.
	activateFeedWithCeiling(t, configsvc.New(f.db.Worker), "telco:SIM_NG", 20_000, 30_000)

	const n = 8
	const amount = int64(10_000)
	codes := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Inline the signed POST (f.post uses t.Fatal, unsafe from a goroutine).
			body := rechargeBody(fmt.Sprintf("evt-%d", i), amount)
			ts := nowTS()
			sig := rechargewebhook.Sign(rechargewebhook.NewHMACSHA256Adapter(), []byte(whSecret), whKeyID, ts, []byte(body))
			req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/telcos/SIM_NG/recharge-webhook", bytes.NewReader([]byte(body)))
			req.Header.Set("X-Bx-Key-Id", whKeyID)
			req.Header.Set("X-Bx-Timestamp", ts)
			req.Header.Set("X-Bx-Signature", sig)
			resp, err := f.srv.Client().Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			codes[i] = resp.StatusCode
			_ = resp.Body.Close()
		}(i)
	}
	close(start)
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("request %d failed: %v", i, e)
		}
	}

	// Booked recoveries never exceed the ceiling.
	var bookedCount, bookedSum int64
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*), COALESCE(SUM(amount_minor),0) FROM recovery_events WHERE telco_id='SIM_NG' AND source_event_id LIKE 'wh:%'`).
		Scan(&bookedCount, &bookedSum); err != nil {
		t.Fatal(err)
	}
	if bookedSum > 30_000 {
		t.Fatalf("booked %d exceeds the daily ceiling 30000 — the ceiling was raced (BX-HIGH-001)", bookedSum)
	}
	if bookedCount != 3 {
		t.Fatalf("exactly 3 events must book within the 30000/10000 ceiling, got %d (booked_minor=%d)", bookedCount, bookedSum)
	}
	// Excess events are HELD (DAILY_CEILING), never dropped — every event is accounted for.
	var heldCount int64
	if err := f.db.Admin.QueryRow(ctx,
		`SELECT count(*) FROM held_recharge_events WHERE telco_id='SIM_NG' AND reason='DAILY_CEILING'`).Scan(&heldCount); err != nil {
		t.Fatal(err)
	}
	if bookedCount+heldCount != n {
		t.Fatalf("all %d events must be booked or held (none lost): booked=%d held=%d", n, bookedCount, heldCount)
	}
}

func TestBXHIGH001_WebhookRetry_DoesNotConsumeCapacityTwice(t *testing.T) {
	f := newWebhookFixture(t, "wh_high001_retry", true, 20_000, true)
	ctx := context.Background()
	activateFeedWithCeiling(t, configsvc.New(f.db.Worker), "telco:SIM_NG", 20_000, 30_000)

	// A single event books and reserves 10000. Capture the EXACT body so the retry is
	// byte-identical (the recovery hash covers occurred_at).
	body := rechargeBody("evt-retry", 10_000)
	resp1 := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), body)
	_ = resp1.Body.Close()

	reserved := func() int64 {
		var v int64
		if err := f.db.Admin.QueryRow(ctx,
			`SELECT COALESCE(SUM(reserved_minor),0) FROM recharge_daily_reservation WHERE telco_id='SIM_NG'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	booked := func() int64 {
		var v int64
		if err := f.db.Admin.QueryRow(ctx,
			`SELECT count(*) FROM recovery_events WHERE telco_id='SIM_NG' AND source_event_id LIKE 'wh:%'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	reservedBefore, bookedBefore := reserved(), booked()
	if reservedBefore != 10_000 || bookedBefore != 1 {
		t.Fatalf("after the first delivery: reserved=%d (want 10000) booked=%d (want 1)", reservedBefore, bookedBefore)
	}

	// Retry the SAME body under a DIFFERENT signing timestamp (a fresh MAC — the cross-MAC
	// replay the reviewer called out). Idempotent: no new booking, no second reservation.
	resp2 := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), body)
	if resp2.StatusCode >= 500 {
		t.Fatalf("a retry must be idempotent, got HTTP %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()

	if got := reserved(); got != reservedBefore {
		t.Fatalf("a retry must NOT consume ceiling capacity twice: reserved before=%d after=%d (BX-HIGH-001 crash hardening)", reservedBefore, got)
	}
	if got := booked(); got != bookedBefore {
		t.Fatalf("a retry must NOT create a second booking: booked before=%d after=%d", bookedBefore, got)
	}
}
