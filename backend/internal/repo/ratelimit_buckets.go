package repo

// BX-MED-006: the cross-replica aggregate rate-limit store.
//
// One statement per spend. The shape is load-bearing and each constraint below was a real defect
// found in review, so none of it is incidental style:
//
//   * ONE `INSERT ... ON CONFLICT DO UPDATE ... RETURNING`, with EVERY read of the row expressed as
//     a column reference inside DO UPDATE. Under READ COMMITTED, ON CONFLICT DO UPDATE is the
//     documented exception that sees and locks the LATEST committed row version — but a CTE (the
//     tempting way to factor out the repeated CASE expressions) reads the STATEMENT SNAPSHOT
//     instead. Mixing the two over-grants by the concurrency factor: ten replicas whose CTEs all
//     snapshot tokens=5000 each compute 5000-1000 and all ten commit 4000, spending one token's
//     worth of budget on ten grants. The repetition below is the price of atomicity.
//
//   * clock_timestamp(), not now(). now() is TRANSACTION start time. Measured on this project's own
//     Postgres, clock_timestamp()-now() reached 355ms inside a single transaction — so under the
//     lock contention a hot bucket row produces, a later-arriving transaction that BEGAN earlier
//     computes a NEGATIVE elapsed against a last_refill_at another replica just advanced. That
//     debits tokens on top of the request's own cost and walks last_refill_at BACKWARD, starving a
//     partner through a pure clock artefact. GREATEST(0, ...) is the second half of that fix.
//
//   * NUMERIC arithmetic throughout. `make_interval(secs => 2/3)` is 00:00:00 in Postgres — integer
//     division truncates before the cast to double. Computing the refill's time advance that way
//     silently rounds it to zero, so the bucket refills without ever consuming time and the limiter
//     becomes inoperative under sustained load.
//
//   * last_refill_at advances by the time represented by the COMPUTED floored refill, and is left
//     UNCHANGED when that floor is zero. Advancing it unconditionally discards the fractional
//     remainder, and at any governed rate where FLOOR(elapsed*rate) is 0 per request under sustained
//     load the bucket then NEVER refills and the partner is locked out permanently. Using the
//     refill actually ADDED after the burst clamp is the opposite error: an idle bucket banks
//     unbounded time and later refunds a token per request, so the effective burst becomes
//     burst + idle_seconds*rate with no ceiling.
//
//   * Exhaustion is a BOOLEAN column returned from an UNCONDITIONAL update — never zero rows. The
//     natural `DO UPDATE ... WHERE tokens_milli >= 1000` makes an over-limit request return
//     pgx.ErrNoRows, which is indistinguishable from the store being broken: ordinary, expected 429
//     traffic would then be counted as store failure and trip the unavailability fallback, i.e. a
//     partner exceeding their quota would DISABLE the control that is refusing them.
//
//   * The quota lives on the row and the newest-effective config adopts it, with the token count
//     clamped on adopt. Passing the quota per-process into every call is what lets a rolling deploy
//     redefine a shared ceiling — the stale replica's larger burst wins. Clamping matters
//     independently: without LEAST(tokens, burst) a tightening would not bind until the pre-existing
//     surplus drained, so a 240-token bucket keeps serving 239 instant requests under a new burst of
//     120.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
)

// RateLimitBuckets is the Postgres-backed aggregate bucket store.
type RateLimitBuckets struct{ Pool *pgxpool.Pool }

// takeSQL spends one token from (scope, telco_id), refilling first. See the file header for why
// each piece is written this way.
//
// $1 scope, $2 telco_id, $3 burst_milli, $4 refill_milli_per_sec, $5 quota_effective_from.
const takeSQL = `
INSERT INTO rate_limit_buckets AS b
  (scope, telco_id, tokens_milli, burst_milli, refill_milli_per_sec,
   quota_effective_from, last_refill_at, last_grant)
VALUES
  ($1, $2, $3 - 1000, $3, $4, $5, clock_timestamp(), true)
ON CONFLICT (scope, telco_id) DO UPDATE SET
  -- Adopt the quota only from a STRICTLY newer effective config; otherwise keep the row's own.
  -- A not-yet-restarted replica carrying an older, larger burst can never re-raise the ceiling.
  burst_milli = CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                     THEN excluded.burst_milli ELSE b.burst_milli END,
  refill_milli_per_sec = CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                     THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END,
  quota_effective_from = GREATEST(b.quota_effective_from, excluded.quota_effective_from),

  -- Refill, then clamp to the EFFECTIVE burst (so a tightening binds immediately), then spend one
  -- token if one is available.
  tokens_milli = LEAST(
      CASE WHEN excluded.quota_effective_from > b.quota_effective_from
           THEN excluded.burst_milli ELSE b.burst_milli END,
      b.tokens_milli + FLOOR(
        GREATEST(0, EXTRACT(EPOCH FROM (clock_timestamp() - b.last_refill_at)))::numeric
        * (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric)
    ) - CASE WHEN LEAST(
      CASE WHEN excluded.quota_effective_from > b.quota_effective_from
           THEN excluded.burst_milli ELSE b.burst_milli END,
      b.tokens_milli + FLOOR(
        GREATEST(0, EXTRACT(EPOCH FROM (clock_timestamp() - b.last_refill_at)))::numeric
        * (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric)
    ) >= 1000 THEN 1000 ELSE 0 END,

  -- Advance the clock by exactly the time the COMPUTED floored refill represents; leave it alone
  -- when that refill floors to zero, so the remainder is carried rather than discarded.
  last_refill_at = CASE
    WHEN FLOOR(GREATEST(0, EXTRACT(EPOCH FROM (clock_timestamp() - b.last_refill_at)))::numeric
         * (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                 THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric) > 0
    THEN b.last_refill_at + (
         FLOOR(GREATEST(0, EXTRACT(EPOCH FROM (clock_timestamp() - b.last_refill_at)))::numeric
           * (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                   THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric)
         / (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                 THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric
       ) * INTERVAL '1 second'
    ELSE b.last_refill_at END,

  last_grant = LEAST(
      CASE WHEN excluded.quota_effective_from > b.quota_effective_from
           THEN excluded.burst_milli ELSE b.burst_milli END,
      b.tokens_milli + FLOOR(
        GREATEST(0, EXTRACT(EPOCH FROM (clock_timestamp() - b.last_refill_at)))::numeric
        * (CASE WHEN excluded.quota_effective_from > b.quota_effective_from
                THEN excluded.refill_milli_per_sec ELSE b.refill_milli_per_sec END)::numeric)
    ) >= 1000
RETURNING last_grant`

// Take spends one token from the aggregate bucket for (scope, telcoID), refilling by elapsed time
// first. It reports whether the request is within the SHARED quota — the same row every replica
// spends from.
//
// The statement runs as a single implicit transaction: no explicit BEGIN, so the row lock is held
// only for the statement rather than across a round trip. That is deliberate. The channel route
// performs a credential lookup on the same pool between its two limiter wrappers, so holding an
// open transaction across next.ServeHTTP would let ten concurrent requests each hold one connection
// and each need a second — a total pool deadlock at MaxConns=10, on every route.
func (r RateLimitBuckets) Take(ctx context.Context, scope, telcoID string, burstMilli, refillMilliPerSec int64, quotaFrom time.Time) (bool, error) {
	if scope == "" || telcoID == "" {
		return false, fmt.Errorf("rate limit take: empty scope or telco")
	}
	var granted bool
	err := r.Pool.QueryRow(ctx, takeSQL, scope, telcoID, burstMilli, refillMilliPerSec, quotaFrom).Scan(&granted)
	if err != nil {
		return false, classifyTakeErr(err)
	}
	return granted, nil
}

// classifyTakeErr separates CONTENTION from every other failure.
//
// Classification is by SQLSTATE, not by errors.Is(context.DeadlineExceeded). A lock wait that
// exceeds its budget surfaces as a *pgconn.PgError with code 55P03 and does NOT match
// context.DeadlineExceeded — so a naive check would file the primary contention signal under
// "connection failure", which is the one class that is allowed to fall back to local-only limiting.
// That inversion would let an attacker who can induce contention move the limiter to its weaker
// setting, which is the opposite of what a control should do under attack.
func classifyTakeErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "55P03", // lock_not_available
			"57014", // query_canceled (statement cancelled, incl. by ctx expiry server-side)
			"40P01", // deadlock_detected
			"40001": // serialization_failure
			return fmt.Errorf("%w: %s", ratelimit.ErrContended, pgErr.Code)
		}
	}
	// A client-side deadline on a statement that never came back is contention on this bucket until
	// proven otherwise: the connection itself was healthy enough to send.
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: client deadline", ratelimit.ErrContended)
	}
	return err
}

// Level reports the current token level for a bucket, for operator visibility. Returns ok=false when
// the bucket has never been touched.
func (r RateLimitBuckets) Level(ctx context.Context, scope, telcoID string) (tokensMilli, burstMilli int64, ok bool, err error) {
	err = r.Pool.QueryRow(ctx,
		`SELECT tokens_milli, burst_milli FROM rate_limit_buckets WHERE scope = $1 AND telco_id = $2`,
		scope, telcoID).Scan(&tokensMilli, &burstMilli)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return tokensMilli, burstMilli, true, nil
}
