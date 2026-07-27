package main

// Loan-book state driver for cmd/seed-dev. It populates the states a Wave B loan-book
// view renders — delinquent, written-off, deferred-fee — WITHOUT writing any business
// OUTCOME directly:
//   - Delinquent is a bucket overlay (advances.delinquency_bucket), not an FSM state.
//     RunLoop originates fresh-today advances (dpd=0 => CURRENT), and the classification
//     usecase hard-codes now() as the as-of, so the only lever is to age the origination
//     INPUT: backdate advances.activated_at (a non-state, non-money origination
//     timestamp — the exact lever the collections tests use, written here via the admin
//     pool). The delinquency BUCKET is then stamped solely by the real collections.Classify
//     usecase + the seeded ladder.
//   - Written-off is produced only by the real maker-checker (RequestWriteOff by one
//     actor, ApproveWriteOff by a DISTINCT actor) once an advance sits at DPD_90_PLUS.
//   - Deferred-fee needs no work: the platform's global default fee_recognition is
//     DEFERRED and prg_sim_airtime01 has no override, so EVERY originated advance is
//     already FeeRecognition=DEFERRED carrying a live UNEARNED_FEE liability.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/collections"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const loanStatesProgramme = "prg_sim_airtime01"

// loanStateAgeDays backdates each selected defaulter advance so the seeded ladder
// (CURRENT@0, DPD_1_7@1, DPD_8_30@8, DPD_31_60@31, DPD_61_90@61, DPD_90_PLUS@91) stamps a
// spread of buckets; the 95-day one becomes write-off-eligible (min_bucket=DPD_90_PLUS).
var loanStateAgeDays = []int{8, 35, 65, 95}

func driveLoanStates(ctx context.Context, adminPool, appPool *pgxpool.Pool) error {
	telco := simseed.SyntheticTelco // "SIM_NG"

	// Pick defaulter advances (ACTIVE, still outstanding) deterministically by id.
	ids, err := selectAgeableAdvances(ctx, adminPool, telco, len(loanStateAgeDays))
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		log.Print("seed-dev: no ageable advances for loan-book states (run the population first)")
		return nil
	}

	// Age each: backdate activated_at (origination INPUT, not state/money) so the real
	// classifier sees real days-past-due. Uses the admin pool (superuser), matching the
	// collections tests' backdate() lever.
	for i, id := range ids {
		if _, err := adminPool.Exec(ctx,
			`UPDATE advances SET activated_at = now() - make_interval(days => $2) WHERE advance_id = $1`,
			id, loanStateAgeDays[i]); err != nil {
			return fmt.Errorf("age advance %s: %w", id, err)
		}
	}

	// REAL classification usecase stamps the delinquency buckets from the seeded ladder.
	appCfg := configsvc.New(appPool)
	col := collections.New(appPool, appCfg, ledger.New(appCfg), seedLogger())
	if _, err := col.Classify(ctx, telco, loanStatesProgramme); err != nil {
		return fmt.Errorf("classify delinquency: %w", err)
	}

	// Write off ONE DPD_90_PLUS advance through the real maker-checker (idempotent).
	if err := writeOffOne(ctx, adminPool, col, telco); err != nil {
		return err
	}

	log.Printf("seed-dev: loan-book states — aged %d advances into delinquency buckets; "+
		"deferred-fee is the platform default (every advance carries UNEARNED_FEE)", len(ids))
	return nil
}

func selectAgeableAdvances(ctx context.Context, pool *pgxpool.Pool, telco string, n int) ([]string, error) {
	// Only age advances not already bucketed, so re-runs converge (an already-delinquent
	// advance is never re-aged to a different bucket).
	rows, err := pool.Query(ctx,
		`SELECT advance_id FROM advances
		  WHERE telco_id = $1 AND programme_id = $2 AND state = 'ACTIVE' AND outstanding_minor > 0
		    AND (delinquency_bucket IS NULL OR delinquency_bucket = 'CURRENT')
		  ORDER BY advance_id LIMIT $3`,
		telco, loanStatesProgramme, n)
	if err != nil {
		return nil, fmt.Errorf("select ageable advances: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// writeOffOne drives one DPD_90_PLUS advance to WRITTEN_OFF via the real maker-checker.
// Idempotent: if a WRITTEN_OFF advance already exists, it does nothing.
func writeOffOne(ctx context.Context, adminPool *pgxpool.Pool, col *collections.Service, telco string) error {
	var already bool
	if err := adminPool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM advances WHERE telco_id = $1 AND state = 'WRITTEN_OFF')`,
		telco).Scan(&already); err != nil {
		return fmt.Errorf("probe written-off: %w", err)
	}
	if already {
		return nil
	}
	var advID string
	err := adminPool.QueryRow(ctx,
		`SELECT advance_id FROM advances
		  WHERE telco_id = $1 AND programme_id = $2 AND state = 'ACTIVE' AND delinquency_bucket = 'DPD_90_PLUS'
		  ORDER BY advance_id LIMIT 1`,
		telco, loanStatesProgramme).Scan(&advID)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Print("seed-dev: no DPD_90_PLUS advance to write off (skipping)")
		return nil
	}
	if err != nil {
		return fmt.Errorf("find write-off candidate: %w", err)
	}
	wo, err := col.RequestWriteOff(ctx, telco, advID, "seed-dev-maker", "seed-dev synthetic write-off")
	if err != nil {
		return fmt.Errorf("request write-off %s: %w", advID, err)
	}
	if err := col.ApproveWriteOff(ctx, telco, wo.WriteOffID, "seed-dev-checker", "corr-seed-writeoff-"+advID); err != nil {
		return fmt.Errorf("approve write-off %s: %w", advID, err)
	}
	log.Printf("seed-dev: wrote off advance %s (WRITTEN_OFF via maker-checker)", advID)
	return nil
}

// seedLogger is a quiet slog logger for the collections usecase (warnings+ only, so the
// seeder's own log lines stay legible).
func seedLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
