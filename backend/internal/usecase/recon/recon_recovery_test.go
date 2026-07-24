package recon

// S3-A RECOVERY recon adversarial pack (build/PHASE1_S3_DESIGN.md §3). Drives the
// real engine against seeded recovery_events + recovery_eod_feed. The recon.recovery
// and telco.recovery_feed(mock, SIM_NG) configs and SIM_NG.is_synthetic are seeded
// by migration 0053, so a fixture needs no config setup.
//
// Money-safety focus: MISSING_TELCO = a phantom/forged recovery we booked;
// MISSING_PLATFORM = a dropped recovery MTN reports; and the reversal-aware NET
// figure (I1/I2) which is the money-loss BLOCKER fix.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

const recBusinessDate = "2026-06-15"

// recOccurredAt sits inside the Lagos business day 2026-06-15, whose UTC window is
// [2026-06-14T23:00Z, 2026-06-15T23:00Z).
func recOccurredAt() time.Time { return time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC) }

func newRecoveryFixture(t *testing.T, suffix string) *reconFixture {
	t.Helper()
	db := testutil.MustSetup(t, suffix)
	return &reconFixture{db: db, svc: New(db.App, configsvc.New(db.App), slog.Default())}
}

// seedRecoveryEvent inserts one wh:% recovery event with a token (subID "" => NULL
// subscriber, the unmatched-at-ingest case).
func (f *reconFixture) seedRecoveryEvent(t *testing.T, evID, token string, minor int64, subID string) {
	t.Helper()
	var sub any
	if subID != "" {
		sub = subID
	}
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recovery_events (recovery_event_id, telco_id, source_event_id,
		  subscriber_account_id, msisdn_token, amount_minor, currency, state, occurred_at)
		VALUES ($1,'SIM_NG',$2,$3,$4,$5,'NGN','ALLOCATED',$6)`,
		evID, "wh:"+evID, sub, token, minor, recOccurredAt()); err != nil {
		t.Fatalf("seed recovery event: %v", err)
	}
}

// reverseAllocation books a NEGATIVE allocation row against an event+advance — the
// clawback the reversal-aware NET must net out.
func (f *reconFixture) reverseAllocation(t *testing.T, evID, advID string, negMinor int64) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recovery_allocations (allocation_id, recovery_event_id, advance_id, component, amount_minor, currency)
		VALUES ('alloc_'||$1||'_rev', $1, $2, 'PRINCIPAL', $3, 'NGN')`, evID, advID, negMinor); err != nil {
		t.Fatalf("seed reversal allocation: %v", err)
	}
}

func (f *reconFixture) seedFeedRow(t *testing.T, token string, minor int64) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO recovery_eod_feed (telco_id, business_date, msisdn_token, recovery_deducted_minor, currency)
		VALUES ('SIM_NG', DATE '2026-06-15', $1, $2, 'NGN')`, token, minor); err != nil {
		t.Fatalf("seed feed row: %v", err)
	}
}

func (f *reconFixture) reconcileDay(t *testing.T) Summary {
	t.Helper()
	sum, err := f.svc.ReconcileRecoveryDay(context.Background(), "SIM_NG", recBusinessDate)
	if err != nil {
		t.Fatalf("ReconcileRecoveryDay: %v", err)
	}
	return sum
}

// Clean day: every booked recovery is confirmed by the feed → all MATCHED.
func TestS3A_Recovery_HappyMatched(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_happy")
	f.seedRecoveryEvent(t, "ev1", "tok1", 500, "")
	f.seedRecoveryEvent(t, "ev2", "tok2", 300, "")
	f.seedFeedRow(t, "tok1", 500)
	f.seedFeedRow(t, "tok2", 300)

	sum := f.reconcileDay(t)
	if sum.Matched != 2 || sum.MissingTelco != 0 || sum.MissingPlatform != 0 || sum.AmountMismatch != 0 {
		t.Fatalf("clean day must be all-matched, got %+v", sum)
	}
	if sum.MatchedControlTotalMinor != 800 {
		t.Fatalf("matched control total must be 800, got %d", sum.MatchedControlTotalMinor)
	}
}

// A recovery we booked that the feed does NOT confirm = a phantom/forged recovery.
func TestS3A_Recovery_MissingTelco_Phantom(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_phantom")
	f.seedRecoveryEvent(t, "ev1", "tok1", 500, "")
	// feed is empty — MTN reports no deduction for tok1.
	sum := f.reconcileDay(t)
	if sum.MissingTelco != 1 || sum.Matched != 0 {
		t.Fatalf("a booked-but-unconfirmed recovery must be BREAK_MISSING_TELCO, got %+v", sum)
	}
	if n := f.activeStatusCount(t, "BREAK_MISSING_TELCO"); n != 1 {
		t.Fatalf("want 1 MISSING_TELCO item, got %d", n)
	}
}

// A deduction the feed reports that we did NOT book = a dropped recovery.
func TestS3A_Recovery_MissingPlatform_Dropped(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_dropped")
	f.seedFeedRow(t, "tokX", 500)
	// no recovery_events — we never booked tokX's deduction.
	sum := f.reconcileDay(t)
	if sum.MissingPlatform != 1 || sum.Matched != 0 {
		t.Fatalf("a feed-reported-but-unbooked recovery must be BREAK_MISSING_PLATFORM, got %+v", sum)
	}
}

// I1 — the money-loss BLOCKER: a same-day reversal must NOT mask a discrepancy into
// a false MATCH. tok_m1 booked 1000 then fully reversed (net 0); the gross feed says
// 1000. NET => platform 0 vs feed 1000 => AMOUNT_MISMATCH (surfaced). A gross SUM
// would have MATCHED 1000==1000 and hidden it.
func TestS3A_Recovery_ReversalDoesNotMaskDrop(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_i1")
	f.seedConfirmedAdvance(t, "m1", 1000, "NGN", "tr_m1") // advance m1, subscriber sub_m1, token tok_m1
	f.seedRecoveryEvent(t, "ev_m1", "tok_m1", 1000, "sub_m1")
	f.reverseAllocation(t, "ev_m1", "m1", -1000) // net platform for tok_m1 = 0
	f.seedFeedRow(t, "tok_m1", 1000)             // gross feed

	sum := f.reconcileDay(t)
	if sum.Matched != 0 || sum.AmountMismatch != 1 {
		t.Fatalf("a fully-reversed recovery vs a gross feed must MISMATCH, not MATCH, got %+v", sum)
	}
}

// I2 — a partial reversal is netted (no state<>'REVERSED' shortcut): booked 1000,
// reversed 300 => net 700; a net feed of 700 MATCHES.
func TestS3A_Recovery_PartialReversalNetted(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_i2")
	f.seedConfirmedAdvance(t, "m2", 1000, "NGN", "tr_m2")
	f.seedRecoveryEvent(t, "ev_m2", "tok_m2", 1000, "sub_m2")
	f.reverseAllocation(t, "ev_m2", "m2", -300) // net = 700
	f.seedFeedRow(t, "tok_m2", 700)

	sum := f.reconcileDay(t)
	if sum.Matched != 1 || sum.AmountMismatch != 0 {
		t.Fatalf("partial reversal must net to 700 and MATCH a 700 feed, got %+v", sum)
	}
}

// I9 — token-keying handles an intra-day port AND unmatched-at-ingest without a
// double break: two same-token deductions recombine into ONE matched key; a
// NULL-subscriber event still keys by its token and matches.
func TestS3A_Recovery_PortAndNullSubscriber(t *testing.T) {
	f := newRecoveryFixture(t, "s3a_i9")
	// tokP deducted twice the same day (a port: SA1 then SA2 — subscriber differs,
	// token constant). Both are NULL-subscriber here for simplicity; the point is
	// they recombine under the token.
	f.seedRecoveryEvent(t, "ev_p1", "tokP", 300, "")
	f.seedRecoveryEvent(t, "ev_p2", "tokP", 200, "")
	f.seedFeedRow(t, "tokP", 500)
	// tokU: a single unmatched-at-ingest (NULL subscriber) event.
	f.seedRecoveryEvent(t, "ev_u", "tokU", 100, "")
	f.seedFeedRow(t, "tokU", 100)

	sum := f.reconcileDay(t)
	if sum.Matched != 2 || sum.MissingTelco != 0 || sum.MissingPlatform != 0 {
		t.Fatalf("port (recombined) + null-subscriber must both MATCH with no double break, got %+v", sum)
	}
}
