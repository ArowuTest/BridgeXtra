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
	"errors"
	"fmt"

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
// evidence timestamp is recon_recovery_qualifications.created_at, re-read fresh in Step 3's own
// publication statement — even a caller that has forged evidence.EvidenceAt (a stray Go bug, a bad
// retry, a malicious value) cannot influence what gets stamped, only which qualification/run pair is
// looked up.
//
// Monotonic: a delayed or reclaimed publisher replaying evidence older than (or identical to) what
// is already stamped is a no-op success, never an error — the gate is already at least as fresh as
// this evidence would make it, and evidence-time freshness means a late publisher must never claim
// fresher evidence than it actually has.
//
// BX-MED-004-A2 finding F3b — the governed config is resolved, the confirmation re-verified, and the
// freshness advanced ALL INSIDE ONE FINAL PUBLICATION STATEMENT (Step 3), at a single
// statement_timestamp() taken AFTER the arming-row lock (Step 2). F3a already moved config
// resolution onto Postgres's per-statement clock; F3b closes the residual gap F3a leaves under lock
// contention. Previously the config was resolved (and the confirmation evaluated) in earlier,
// separate statements — BEFORE the FOR UPDATE lock. A publisher that then blocked on that lock
// behind a concurrent writer would carry a now-stale threshold/window across the block: a governed
// tightening that committed while it was blocked would be missed and the money gate advanced anyway.
// Resolving+confirming+writing in one post-lock statement means any governed policy committed before
// that statement begins is honoured, and the confirmation and the window written can never disagree.
func (p *FencedControlPublisher) Publish(ctx context.Context, telcoID string, evidence RecoveryEvidence) error {
	return repo.WithTenantTx(platform.WithTenant(ctx, telcoID), p.Pool, func(tx pgx.Tx) error {
		// Step 1: a binding PRE-CHECK for precise, early errors — proves the qualification/run pair
		// exists, scope matches, layer==RECOVERY, and the run carries persisted control totals (a
		// pre-A2 row does not). This is not the authority: it reads at this transaction's snapshot,
		// so a run it sees ACTIVE could still be superseded before Step 3 runs — which is exactly why
		// Step 3 re-binds independently. Step 1 only ever ADDS refusals (a strictly more conservative
		// gate), never causes an accept. evidence.QualificationID/evidence.RunID are the lookup KEY;
		// nothing else on the caller-supplied struct is trusted.
		var runTelcoID, layer, state string
		var matchedTotal, platformTotal *int64
		err := tx.QueryRow(ctx, `
			SELECT rr.telco_id, rr.layer, rr.state,
			       rr.matched_control_total_minor, rr.platform_control_total_minor
			FROM recon_recovery_qualifications q
			JOIN recon_runs rr ON rr.run_id = q.run_id
			WHERE q.qualification_id = $1 AND q.run_id = $2`,
			evidence.QualificationID, evidence.RunID).
			Scan(&runTelcoID, &layer, &state, &matchedTotal, &platformTotal)
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

		// Step 2: take the arming-row lock as its OWN statement, BEFORE the final publication
		// statement. This ordering is the F3b fix: the final statement (Step 3) is issued only after
		// this lock is acquired, so its statement_timestamp() falls AFTER any concurrent publisher's
		// contention has drained — letting it honour a governed policy that committed while this
		// publisher was blocked here. FOR UPDATE serializes concurrent publishers for the same
		// telco/layer; the loser blocks until the winner commits, then Step 3 re-reads just-committed
		// state under READ COMMITTED. A missing row is disarmed mid-publish — a distinct, more
		// alarming condition than "not yet fresh enough", so it is an error, never a silent no-op.
		var locked bool
		err = tx.QueryRow(ctx,
			`SELECT true FROM recon_layer_arming WHERE telco_id=$1 AND layer=$2 FOR UPDATE`,
			telcoID, layerRecovery).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("disarmed mid-publish, refusing: no recon_layer_arming row for %s/%s", telcoID, layerRecovery)
		}
		if err != nil {
			return err
		}

		// Step 3: the single final publication statement (BX-MED-004-A2 finding F3b). At ONE snapshot,
		// taken after the lock above, it:
		//   - re-binds the qualification to a STILL-ACTIVE run and reads recon_runs' OWN persisted
		//     totals (bind) — catching a supersession that committed while Step 2 was blocked;
		//   - resolves the CURRENTLY effective governed config by Postgres's own statement_timestamp()
		//     (cfg), telco scope winning over global, ACTIVE|SUPERSEDED with effective_from/to
		//     windowing — the exact shape configsvc.ActiveAt / repo.GetActiveAt use;
		//   - re-verifies confirmation from those trusted totals against that freshly-resolved
		//     threshold, with the same fail-closed floors loadForRecovery re-asserts (a raw seed
		//     bypasses the validator, so the floor lives here too);
		//   - and, only if all of that still holds, advances BOTH freshness columns — but ONLY on
		//     strictly-newer evidence (the CASE), stamping the qualification's OWN created_at (never a
		//     live clock — finding F1) and the freshly-resolved governed window.
		// The ADVANCED classification (is_newer) is computed from the PRE-update arming value (armnow,
		// read under the lock this tx already holds). A matched row means authorized: RETURNING true =
		// advanced, false = equal/older replay that changed neither column but still succeeds. Zero
		// rows (ErrNoRows) means the run is no longer ACTIVE, no longer confirms at the CURRENT
		// governed policy, or that policy is unavailable — refuse.
		var advanced bool
		err = tx.QueryRow(ctx, `
			WITH
			armnow AS (
				SELECT last_recon_at AS prev_last
				FROM recon_layer_arming
				WHERE telco_id = $1 AND layer = $2
			),
			bind AS (
				SELECT rr.state,
				       rr.platform_record_count        AS prc,
				       rr.source_record_count          AS src,
				       rr.matched_control_total_minor  AS matched,
				       rr.platform_control_total_minor AS platform,
				       q.created_at                    AS qual_created_at
				FROM recon_recovery_qualifications q
				JOIN recon_runs rr ON rr.run_id = q.run_id
				WHERE q.qualification_id = $3 AND q.run_id = $4
				  AND rr.telco_id = $1 AND rr.layer = $2
			),
			cfg AS (
				SELECT (content->>'min_confirmation_ratio')::float8 AS min_ratio,
				       (content->>'arm_freshness_max_seconds')::int  AS window_s
				FROM config_versions_recon_recovery
				WHERE scope IN ('telco:'||$1, 'global')
				  AND state IN ('ACTIVE','SUPERSEDED')
				  AND effective_from <= statement_timestamp()
				  AND (effective_to IS NULL OR effective_to > statement_timestamp())
				ORDER BY (scope = 'telco:'||$1) DESC, effective_from DESC
				LIMIT 1
			),
			decision AS (
				SELECT b.qual_created_at,
				       c.window_s,
				       (an.prev_last IS NULL OR b.qual_created_at > an.prev_last) AS is_newer
				FROM armnow an, bind b, cfg c
				WHERE b.state = 'ACTIVE'
				  AND b.matched IS NOT NULL AND b.platform IS NOT NULL
				  AND c.min_ratio > 0 AND c.min_ratio <= 1
				  AND c.window_s BETWEEN 3600 AND 604800
				  AND (CASE WHEN b.prc = 0
				            THEN b.src = 0
				            ELSE b.matched >= ceil(c.min_ratio * b.platform)::bigint
				       END)
			)
			UPDATE recon_layer_arming a
			SET last_recon_at =
			      CASE WHEN d.is_newer THEN d.qual_created_at ELSE a.last_recon_at END,
			    arm_freshness_max_seconds =
			      CASE WHEN d.is_newer THEN d.window_s ELSE a.arm_freshness_max_seconds END
			FROM decision d
			WHERE a.telco_id = $1 AND a.layer = $2
			RETURNING d.is_newer`,
			telcoID, layerRecovery, evidence.QualificationID, evidence.RunID).Scan(&advanced)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("re-verification failed: run %s is no longer ACTIVE, no longer confirms at the "+
				"CURRENT governed threshold, or its governed config is unavailable — refusing", evidence.RunID)
		}
		if err != nil {
			return err
		}
		// advanced==true stamped newer evidence; advanced==false was an equal/older replay left
		// unchanged. Both are success — the classification is derived in-SQL from the pre-update
		// arming value, never a caller-supplied one.
		_ = advanced
		return nil
	})
}
