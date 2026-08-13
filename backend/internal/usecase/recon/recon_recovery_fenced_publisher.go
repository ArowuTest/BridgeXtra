package recon

// BX-MED-004-A2: the fenced freshness publisher. Design: design_MED-004-A2_v3.md (3 rounds of
// adversarial critique on the architecture, then implemented directly against real Postgres per
// the round-3 pivot — see project memory for the full history).
//
// Threat model (restated briefly; see the migration's own header for the full statement): this does
// NOT defend against a tcp_app role that has achieved arbitrary DML capability — no design can,
// since recon_runs itself is written by that role. What it DOES defend: a bug or an unintended code
// path advancing the money gate without genuine, re-verified evidence. FencedControlPublisher never
// trusts a caller-supplied threshold or freshness window (RecoveryEvidence.ArmFreshnessMaxSeconds is
// a MED-004-A1 compatibility field it must never read) — it always independently re-resolves the
// CURRENTLY effective governed recon.recovery config and recomputes the confirmation decision from
// recon_runs' own persisted numbers, never from the qualification row's audit-only copies.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// FencedControlPublisher is, from MED-004-A2 Phase 2 cutover onward, the sole structural writer of
// recon_layer_arming.last_recon_at/arm_freshness_max_seconds. During Phase 1, LegacyRecoveryPublisher
// also exists and is still the live production path (see TestMED004A2_AdvanceFreshnessCallSitesAreAllowlisted
// for the Phase-1 compensating control on AdvanceFreshness's continued exportedness). Runs on a
// dedicated tcp_freshness pool — a role with NO authority beyond reading evidence, reading the
// scoped recon.recovery config view, and publishing freshness.
type FencedControlPublisher struct {
	Pool *pgxpool.Pool // tcp_freshness
}

// Publish independently verifies RecoveryEvidence's binding to a real, ACTIVE RECOVERY recon_runs
// row, recomputes the confirmation decision from that row's own persisted numbers against the
// CURRENTLY effective governed threshold (never the qualification row's audit-only copy, never
// evidence.ArmFreshnessMaxSeconds), and — only if it still holds — atomically advances freshness
// with the qualification row's OWN created_at and the freshly-resolved governed window.
//
// evidence.EvidenceAt is NEVER read here (BX-MED-004-A2 finding F1): a caller-supplied timestamp is
// exactly the kind of unverified input this whole design exists to refuse. The sole authoritative
// evidence timestamp is recon_recovery_qualifications.created_at, re-read fresh in Step 1 below —
// even a caller that has forged evidence.EvidenceAt (a stray Go bug, a bad retry, a malicious value)
// cannot influence what gets stamped, only which qualification/run pair is looked up.
//
// Monotonic: a delayed or reclaimed publisher replaying evidence older than (or identical to) what
// is already stamped is a no-op success, never an error — the gate is already at least as fresh as
// this evidence would make it, and evidence-time freshness means a late publisher must never claim
// fresher evidence than it actually has.
func (p *FencedControlPublisher) Publish(ctx context.Context, telcoID string, evidence RecoveryEvidence) error {
	return repo.WithTenantTx(platform.WithTenant(ctx, telcoID), p.Pool, func(tx pgx.Tx) error {
		// Step 1: independently verify the evidence binding AND read the qualification's OWN
		// created_at — the only evidence timestamp this function will ever act on. Proves run exists,
		// scope matches, layer==RECOVERY, state==ACTIVE, and the qualification genuinely names this
		// run — reading recon_runs' OWN persisted totals, never the qualification row's claims (it
		// carries none). evidence.QualificationID/evidence.RunID are the lookup KEY; nothing else on
		// the caller-supplied struct is trusted.
		var runTelcoID, layer, state string
		var platformRecordCount, sourceRecordCount int64
		var matchedTotal, platformTotal *int64
		var qualificationCreatedAt time.Time
		err := tx.QueryRow(ctx, `
			SELECT rr.telco_id, rr.layer, rr.state,
			       rr.platform_record_count, rr.source_record_count,
			       rr.matched_control_total_minor, rr.platform_control_total_minor,
			       q.created_at
			FROM recon_recovery_qualifications q
			JOIN recon_runs rr ON rr.run_id = q.run_id
			WHERE q.qualification_id = $1 AND q.run_id = $2`,
			evidence.QualificationID, evidence.RunID).
			Scan(&runTelcoID, &layer, &state, &platformRecordCount, &sourceRecordCount, &matchedTotal, &platformTotal,
				&qualificationCreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("evidence binding failed: no qualification/run pair for qualification_id=%s run_id=%s",
				evidence.QualificationID, evidence.RunID)
		}
		if err != nil {
			return err
		}
		if runTelcoID != telcoID || layer != layerRecovery || state != "ACTIVE" {
			return fmt.Errorf("evidence binding failed: telco=%s layer=%s state=%s (want %s/%s/ACTIVE)",
				runTelcoID, layer, state, telcoID, layerRecovery)
		}
		if matchedTotal == nil || platformTotal == nil {
			return fmt.Errorf("evidence binding failed: run %s has no persisted control totals (pre-A2 row?)", evidence.RunID)
		}

		// Step 2: independently resolve the CURRENTLY effective governed threshold — never trust
		// anything the caller supplied. Not yet holding any lock: this and step 1 read only
		// immutable/append-only data (recon_runs only ever transitions ACTIVE->SUPERSEDED;
		// qualifications are insert-once), so there is nothing to serialize against here.
		minConfirmationRatio, armFreshnessMaxSeconds, err := p.currentRecoveryCfg(ctx, tx, telcoID)
		if err != nil {
			return err
		}

		// Step 3: recompute the confirmation decision from TRUSTED numbers (recon_runs' own) against
		// the CURRENT threshold — HIGH-016's guard, independently re-derived, not inherited from
		// RunRecoveryControl's in-process decision. If a threshold tightened between qualification
		// and publish, this can correctly refuse evidence that qualified under an older, looser
		// threshold (design_MED-004-A2_v3.md §6 item 3).
		if !confirmationHolds(platformRecordCount, sourceRecordCount, *matchedTotal, *platformTotal, minConfirmationRatio) {
			return fmt.Errorf("re-verification failed: run %s no longer confirms at the current threshold %v "+
				"(matched=%d platform=%d platform_records=%d source_records=%d)",
				evidence.RunID, minConfirmationRatio, *matchedTotal, *platformTotal, platformRecordCount, sourceRecordCount)
		}

		// Step 4: only now, immediately before the write, take the row lock — minimizing how long
		// any concurrent Publish() for the same telco/layer is blocked. FOR UPDATE serializes
		// concurrent publishers: the second one to reach here blocks until the first commits, then
		// re-reads the just-committed last_recon_at under READ COMMITTED, so the monotonicity check
		// below always compares against genuinely current state, never a stale read.
		var currentLastReconAt *time.Time
		err = tx.QueryRow(ctx,
			`SELECT last_recon_at FROM recon_layer_arming WHERE telco_id=$1 AND layer=$2 FOR UPDATE`,
			telcoID, layerRecovery).Scan(&currentLastReconAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("disarmed mid-publish, refusing: no recon_layer_arming row for %s/%s", telcoID, layerRecovery)
		}
		if err != nil {
			return err
		}
		if currentLastReconAt != nil && !qualificationCreatedAt.After(*currentLastReconAt) {
			// Superseded by newer evidence already published, or an exact-replay retry of this same
			// evidence (crash/reclaim resubmission) — both are success-but-no-op. Compared against the
			// freshly re-read qualificationCreatedAt, never a caller-supplied value.
			return nil
		}

		ct, err := tx.Exec(ctx, `
			UPDATE recon_layer_arming SET last_recon_at = $3, arm_freshness_max_seconds = $4
			WHERE telco_id = $1 AND layer = $2`,
			telcoID, layerRecovery, qualificationCreatedAt, armFreshnessMaxSeconds)
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 1 {
			// Invariant assertion, not a live race: the FOR UPDATE lock taken above is held for the
			// remainder of this transaction, so no concurrent writer can delete or alter this exact
			// row between the lock and this UPDATE. Reachable only if that locking invariant is ever
			// violated (e.g. a future refactor drops the FOR UPDATE) — fail loudly rather than
			// silently proceed on an assumption that no longer holds.
			return fmt.Errorf("recon_layer_arming row for %s/%s changed unexpectedly under lock — refusing", telcoID, layerRecovery)
		}
		return nil
	})
}

// currentRecoveryCfg independently resolves the CURRENTLY effective governed recon.recovery
// threshold and freshness window, inside the CALLER's own transaction (tx — never a second pool
// connection; tcp_freshness has no grant that would let a second connection do anything useful, and
// holding one connection locked while blocked acquiring another from the same pool is a self-
// inflicted starvation risk under concurrent Publish() calls). Reads config_versions_recon_recovery
// (the security-barrier view scoped to domain='recon.recovery'), since tcp_freshness has no grant on
// the base config_versions table.
//
// Mirrors configsvc.Service.ActiveAt's exact two-tier scope resolution (telco-specific, falling back
// to global) and repo.ConfigVersions.GetActiveAt's exact query shape — including matching
// state IN ('ACTIVE','SUPERSEDED') with effective_from/effective_to windowing and newest-effective-
// from-wins, NOT a naive state='ACTIVE' filter, which is NOT what the real config-resolution path
// uses (verified against backend/internal/repo/config.go's actual GetActiveAt before writing this).
//
// "Currently effective" is resolved by POSTGRES'S OWN now() inside the query below (BX-MED-004-A2
// finding F2), never a Go-side time.Now(). The publisher's process clock and the database's clock are
// two different clocks; a skewed worker could otherwise select an older/looser config than the one
// the database itself considers effective right now — exactly the kind of gap this whole design
// exists to close. No time.Time value is threaded into this call chain at all, so there is nothing a
// caller (or a future refactor) could pass to influence which config version resolves.
func (p *FencedControlPublisher) currentRecoveryCfg(ctx context.Context, tx pgx.Tx, telcoID string) (minConfirmationRatio float64, armFreshnessMaxSeconds int, err error) {
	raw, err := recoveryConfigContentAt(ctx, tx, "telco:"+telcoID)
	if errors.Is(err, pgx.ErrNoRows) {
		raw, err = recoveryConfigContentAt(ctx, tx, "global")
	}
	if err != nil {
		return 0, 0, fmt.Errorf("recon.recovery config (tcp_freshness): %w", err)
	}
	var parsed struct {
		MinConfirmationRatio   float64 `json:"min_confirmation_ratio"`
		ArmFreshnessMaxSeconds int     `json:"arm_freshness_max_seconds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, 0, err
	}
	// Same fail-closed floors loadForRecovery re-asserts at load — a raw seed bypasses the
	// validator, so the floor lives here too, independently, matching A1's own established doctrine.
	if parsed.MinConfirmationRatio <= 0 || parsed.MinConfirmationRatio > 1 {
		return 0, 0, fmt.Errorf("recon.recovery min_confirmation_ratio out of range — refusing")
	}
	if parsed.ArmFreshnessMaxSeconds < 3600 || parsed.ArmFreshnessMaxSeconds > 604_800 {
		return 0, 0, fmt.Errorf("recon.recovery arm_freshness_max_seconds out of range — refusing")
	}
	return parsed.MinConfirmationRatio, parsed.ArmFreshnessMaxSeconds, nil
}

// recoveryConfigContentAt resolves scope's currently-effective recon.recovery content using
// POSTGRES'S OWN now() — never a bound parameter — so the instant a config becomes/stops being
// effective is always judged by the same clock that stamps effective_from/effective_to in the first
// place (BX-MED-004-A2 finding F2).
func recoveryConfigContentAt(ctx context.Context, tx pgx.Tx, scope string) ([]byte, error) {
	var content []byte
	err := tx.QueryRow(ctx, `
		SELECT content FROM config_versions_recon_recovery
		WHERE scope=$1 AND state IN ('ACTIVE','SUPERSEDED')
		  AND effective_from <= now() AND (effective_to IS NULL OR effective_to > now())
		ORDER BY effective_from DESC
		LIMIT 1`, scope).Scan(&content)
	return content, err
}
