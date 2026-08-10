package handler_test

// BX-HIGH-001-F1: once a source_event_id has entered the HELD maker-checker workflow, every
// subsequent webhook delivery must replay its disposition and NEVER auto-book — only
// ApproveRelease may feed a held event to recovery. The defect: a DAILY_CEILING hold deleted
// its per-event reservation claim "so it can retry when capacity frees", so a later retry
// re-reserved and booked automatically, bypassing the maker-checker while the hold stayed HELD.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/rechargehold"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

// activateFeedCeiling re-activates the telco:SIM_NG recharge feed with a SMALL daily ceiling so
// two ordinary events trip it (the shared helper hardcodes a 50B ceiling).
func activateFeedCeiling(t *testing.T, db *testutil.DB, perEventMax, dailyCeiling int64) {
	t.Helper()
	ctx := context.Background()
	cfgW := configsvc.New(db.Worker)
	content := fmt.Sprintf(`{"enabled":true,"transport":"webhook_push","auth":"hmac_sha256","key_id_header":"X-Bx-Key-Id","signature_header":"X-Bx-Signature","timestamp_header":"X-Bx-Timestamp","replay_window_seconds":120,"future_skew_seconds":60,"max_body_bytes":65536,"expected_currency":"NGN","per_event_amount_max_minor":%d,"per_telco_daily_ceiling_minor":%d,"recovery_max_backdate_seconds":1209600,"recovery_max_future_skew_seconds":60}`, perEventMax, dailyCeiling)
	c, err := cfgW.CreateDraft(ctx, "telco.recharge_feed", "telco:SIM_NG", "alice", "small ceiling", []byte(content))
	if err != nil {
		t.Fatal(err)
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

func heldStatusBySrc(t *testing.T, db *testutil.DB, src string) string {
	t.Helper()
	var s string
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT status FROM held_recharge_events WHERE source_event_id=$1`, src).Scan(&s); err != nil {
		t.Fatalf("held status for %s: %v", src, err)
	}
	return s
}

func recoveryCountBySrc(t *testing.T, db *testutil.DB, src string) int {
	t.Helper()
	var n int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id=$1`, src).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// freeDailyCapacity simulates a UTC-day rollover / freed budget: the running counter resets, so
// a fresh reservation of the held event WOULD succeed — which is exactly when the old code
// re-admitted and booked it.
func freeDailyCapacity(t *testing.T, db *testutil.DB) {
	t.Helper()
	if _, err := db.Admin.Exec(context.Background(),
		`DELETE FROM recharge_daily_reservation WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}
}

// holdService builds a rechargehold.Service over the same DB the webhook books into.
func holdService(db *testutil.DB) *rechargehold.Service {
	appCfg := configsvc.New(db.App)
	return rechargehold.New(db.App, recovery.New(db.App, appCfg, ledger.New(appCfg), slog.Default()), slog.Default())
}

// End-to-end concurrent webhook-level proof (reviewer HIGH-001 follow-up): many concurrent
// DISTINCT webhook deliveries against a small governed ceiling admit no more than the ceiling,
// the excess become HELD, and the total booked recovery never exceeds the ceiling.
func TestBXHIGH001_ConcurrentWebhooksRespectCeiling(t *testing.T) {
	f := newWebhookFixture(t, "h001_e2e_ceiling", true, 10_000, true)
	activateFeedCeiling(t, f.db, 10_000, 10_000) // ceiling 10000
	occ := time.Now().UTC().Add(-time.Hour)
	const n = 6
	const amt = int64(4_000) // 2 fit (8000 <= 10000); a 3rd would be 12000 > 10000

	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt(fmt.Sprintf("ce%d", i), amt, occ))
			codes[i] = resp.StatusCode
			_ = resp.Body.Close()
		}(i)
	}
	close(start)
	wg.Wait()

	booked, held := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			booked++
		case http.StatusAccepted:
			held++
		default:
			t.Fatalf("unexpected webhook status %d", c)
		}
	}
	// Admitted events book no more than the ceiling (2 x 4000 = 8000; a 3rd would exceed).
	if booked > 2 {
		t.Fatalf("no more than 2 events may book under a 10000 ceiling, got %d booked", booked)
	}
	if booked+held != n {
		t.Fatalf("every event must be booked or held, got booked=%d held=%d of %d", booked, held, n)
	}
	if held == 0 {
		t.Fatalf("with %d events of %d against ceiling 10000, the excess must be HELD, got 0 held", n, amt)
	}
	var bookedMinor int64
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount_minor),0) FROM recovery_events WHERE telco_id='SIM_NG' AND source_event_id LIKE 'wh:ce%'`).Scan(&bookedMinor); err != nil {
		t.Fatal(err)
	}
	if bookedMinor > 10_000 {
		t.Fatalf("total booked %d exceeds the governed ceiling 10000 — the ceiling was raced past", bookedMinor)
	}
}

// Fill the ceiling -> E becomes HELD/DAILY_CEILING -> free capacity -> retry E: still HELD, zero
// recovery booking. This is the exact money-safety bug. Mutation proof: remove the disposition
// short-circuit in the handler and the retry re-reserves and books (recovery count 1).
func TestBXHIGH001F1_HeldCeilingEventNeverAutoBooksOnRetry(t *testing.T) {
	f := newWebhookFixture(t, "h001f1_retry", true, 50_000_000, true)
	activateFeedCeiling(t, f.db, 10_000, 10_000) // daily ceiling 10000
	occ := time.Now().UTC().Add(-time.Hour)

	// E1 (8000) books within the ceiling.
	if resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e1", 8_000, occ)); resp.StatusCode != http.StatusOK {
		t.Fatalf("E1 must book (200), got %d", resp.StatusCode)
	}
	// E2 (8000) would exceed 10000 -> HELD/DAILY_CEILING.
	if resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("E2 must be HELD (202), got %d", resp.StatusCode)
	}
	if s := heldStatusBySrc(t, f.db, "wh:e2"); s != "HELD" {
		t.Fatalf("E2 must be HELD, got %s", s)
	}
	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 0 {
		t.Fatalf("a HELD event must book nothing, got %d", n)
	}

	// Capacity frees (new UTC day / budget reset). A fresh reservation would now succeed.
	freeDailyCapacity(t, f.db)

	// Telco retries E2. It must STILL be HELD and book NOTHING — only ApproveRelease may book it.
	if resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("E2 retry must replay HELD (202), got %d", resp.StatusCode)
	}
	if s := heldStatusBySrc(t, f.db, "wh:e2"); s != "HELD" {
		t.Fatalf("E2 must remain HELD after retry, got %s", s)
	}
	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 0 {
		t.Fatalf("a HELD event retried after capacity freed must book NOTHING — money bypassed maker-checker (BX-HIGH-001-F1), got %d recovery events", n)
	}
}

// HELD -> REJECTED -> webhook retry can never book it.
func TestBXHIGH001F1_RejectedThenRetryNeverBooks(t *testing.T) {
	f := newWebhookFixture(t, "h001f1_reject", true, 50_000_000, true)
	activateFeedCeiling(t, f.db, 10_000, 10_000)
	occ := time.Now().UTC().Add(-time.Hour)
	ctx := context.Background()

	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e1", 8_000, occ)) // fill
	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)) // HELD

	// Reject the held event.
	var heldID string
	if err := f.db.Admin.QueryRow(ctx, `SELECT held_id FROM held_recharge_events WHERE source_event_id='wh:e2'`).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	if err := holdService(f.db).Reject(ctx, "SIM_NG", heldID, "maker", "withdrawn"); err != nil {
		t.Fatal(err)
	}
	freeDailyCapacity(t, f.db)

	// Retry a REJECTED event: replays REJECTED, books nothing.
	if resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry of a REJECTED event must replay its disposition (202), got %d", resp.StatusCode)
	}
	if s := heldStatusBySrc(t, f.db, "wh:e2"); s != "REJECTED" {
		t.Fatalf("E2 must remain REJECTED, got %s", s)
	}
	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 0 {
		t.Fatalf("a REJECTED event must NEVER book, got %d recovery events", n)
	}
}

// HELD -> maker-checker RELEASED -> subsequent webhook retry is idempotent (no 2nd economic effect).
func TestBXHIGH001F1_ReleasedThenRetryIdempotent(t *testing.T) {
	f := newWebhookFixture(t, "h001f1_release", true, 50_000_000, true)
	activateFeedCeiling(t, f.db, 10_000, 10_000)
	occ := time.Now().UTC().Add(-time.Hour)
	ctx := context.Background()

	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e1", 8_000, occ)) // fill
	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)) // HELD

	var heldID string
	if err := f.db.Admin.QueryRow(ctx, `SELECT held_id FROM held_recharge_events WHERE source_event_id='wh:e2'`).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	hs := holdService(f.db)
	if err := hs.RequestRelease(ctx, "SIM_NG", heldID, "maker", "verified genuine"); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.ApproveRelease(ctx, "SIM_NG", heldID, "checker"); err != nil {
		t.Fatal(err)
	}
	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 1 {
		t.Fatalf("maker-checker release must book exactly one recovery, got %d", n)
	}
	freeDailyCapacity(t, f.db)

	// Retry after release: idempotent, NO second economic effect.
	if resp := f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry of a RELEASED event must be idempotent (200), got %d", resp.StatusCode)
	}
	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 1 {
		t.Fatalf("a released event retried must create NO second recovery, got %d", n)
	}
}

// Concurrent webhook retry vs ApproveRelease must not produce two bookings or a contradictory
// hold state: exactly one recovery, and the hold ends RELEASED.
func TestBXHIGH001F1_ConcurrentRetryVsApprove_NoDoubleBook(t *testing.T) {
	f := newWebhookFixture(t, "h001f1_concurrent", true, 50_000_000, true)
	activateFeedCeiling(t, f.db, 10_000, 10_000)
	occ := time.Now().UTC().Add(-time.Hour)
	ctx := context.Background()

	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e1", 8_000, occ)) // fill
	f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)) // HELD

	var heldID string
	if err := f.db.Admin.QueryRow(ctx, `SELECT held_id FROM held_recharge_events WHERE source_event_id='wh:e2'`).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	hs := holdService(f.db)
	if err := hs.RequestRelease(ctx, "SIM_NG", heldID, "maker", "verified"); err != nil {
		t.Fatal(err)
	}
	freeDailyCapacity(t, f.db)

	// Fire the approve and a webhook retry concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = hs.ApproveRelease(ctx, "SIM_NG", heldID, "checker") }()
	go func() { defer wg.Done(); _ = f.post(t, "SIM_NG", whKeyID, whSecret, nowTS(), rechargeBodyAt("e2", 8_000, occ)).Body.Close() }()
	wg.Wait()

	if n := recoveryCountBySrc(t, f.db, "wh:e2"); n != 1 {
		t.Fatalf("concurrent retry vs approve must produce EXACTLY one recovery, got %d", n)
	}
	if s := heldStatusBySrc(t, f.db, "wh:e2"); s != "RELEASED" {
		t.Fatalf("hold must end RELEASED (the approve wins; the webhook never books), got %s", s)
	}
}
