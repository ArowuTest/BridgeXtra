// Package recon reconciles platform records against telco-side records
// (M1: the simulator's transaction log) — V2-REC-001..007. M1 scope is the
// FULFILMENT layer: platform-approved advances vs telco credits, both
// directions, with amount comparison under the governed tolerance (seeded
// ZERO). Breaks are recorded, never auto-resolved (recon.tolerance
// auto_resolve=false floor), never force-matched (V1-FIN-005).
package recon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// reconLayer is the layer a run reconciles. Slice A ships FULFILMENT; the same
// header/manifest machinery extends to the others in Slice D.
const layerFulfilment = "FULFILMENT"

// reconItem buffers one classified comparison before it is written — the
// classification is pure (no DB writes) so the run header can be inserted with
// final control totals and outcome counts BEFORE its items, satisfying the
// header FK and keeping the header a write-once summary.
type reconItem struct {
	matchKey    string // R-P0-6 Slice B: the logical thing being reconciled
	itemType    string
	platformRef string
	telcoRef    string
	status      string
	detail      map[string]any
}

// addChecked adds two int64 and reports whether the result overflowed.
func addChecked(a, b int64) (int64, bool) {
	s := a + b
	if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s > 0) {
		return 0, false
	}
	return s, true
}

// sourceManifest is the R-P0-6 manifest of the telco-side set ingested for a
// run: the full record count, a monetary control total, and a sha256 over the
// canonical, order-independent source set. The hash covers the MATERIAL fields
// (amount, currency, status) — not just IDs — so a feed that alters an amount
// while keeping IDs still changes the hash; and it hashes the multiset (a
// duplicated record appears twice), so the count catches a duplicate the hash
// alone might collapse. The control total sums only credible SUCCESS amounts
// (the R-P0-4 ceiling, one layer up) with CHECKED addition: summing many
// near-ceiling values can overflow int64 even when each passes the per-record
// ceiling, so an overflowing feed is refused rather than wrapped to a bogus
// negative total.
func sourceManifest(recs []telcoTransaction, maxAmountMinor int64) (count int64, controlTotal int64, hash string, err error) {
	ordered := make([]telcoTransaction, len(recs))
	copy(ordered, recs)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].PlatformRequestID != ordered[j].PlatformRequestID {
			return ordered[i].PlatformRequestID < ordered[j].PlatformRequestID
		}
		return ordered[i].TelcoReference < ordered[j].TelcoReference
	})
	canonical := make([]struct {
		ID       string `json:"id"`
		Ref      string `json:"ref"`
		Amount   int64  `json:"amount_minor"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}, 0, len(ordered))
	for _, r := range ordered {
		count++
		if r.Status == "SUCCESS" && credibleAmount(r.FaceValueMinor, maxAmountMinor) {
			var ok bool
			if controlTotal, ok = addChecked(controlTotal, r.FaceValueMinor); !ok {
				return 0, 0, "", fmt.Errorf("source control total overflows int64 — feed too large to reconcile safely")
			}
		}
		canonical = append(canonical, struct {
			ID       string `json:"id"`
			Ref      string `json:"ref"`
			Amount   int64  `json:"amount_minor"`
			Currency string `json:"currency"`
			Status   string `json:"status"`
		}{r.PlatformRequestID, r.TelcoReference, r.FaceValueMinor, r.Currency, r.Status})
	}
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return count, controlTotal, hex.EncodeToString(sum[:]), nil
}

type Service struct {
	Pool   *pgxpool.Pool // tcp_app (tenant-scoped writes to recon_items)
	Config *configsvc.Service
	Log    *slog.Logger
	// HTTPClient fetches the telco-side records; injectable for tests.
	HTTPClient *http.Client
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	// R-P0-5: the telco-records fetch is a config-driven outbound door — the
	// FOURTH the VR-32 SSRF work did not cover. Route it through the shared
	// egress guard (resolved-IP check + connection pinning) like the other three.
	return &Service{Pool: pool, Config: cfg, Log: log, HTTPClient: egress.SafeClient(10 * time.Second)}
}

// telcoTransaction is the canonical telco-side record (simulator /sim/transactions).
type telcoTransaction struct {
	PlatformRequestID string    `json:"platform_request_id"`
	TelcoReference    string    `json:"telco_transaction_reference"`
	FaceValueMinor    int64     `json:"face_value_minor"`
	Currency          string    `json:"currency"`
	Status            string    `json:"status"`
	CreditedAt        time.Time `json:"credited_at"` // R-P0-6 Slice C: telco-side event time (period bound)
}

// platformRecord is the platform side of the fulfilment layer.
type platformRecord struct {
	AdvanceID      string
	State          string
	FaceValueMinor int64
	Currency       string
	TelcoReference string
}

// Summary reports one reconciliation run (V2-REC-007 control totals).
type Summary struct {
	RunID            string
	Matched          int
	MissingPlatform  int // telco credited, platform has no money-bearing advance
	MissingTelco     int // platform believes credited, telco has no record
	AmountMismatch   int
	CurrencyMismatch int // R-P0-3: same amount, different currency
	Malformed        int // R-P0-4: telco record amount/currency out of credible range
	DuplicateTelco   int // R-P0-6 Slice B: a telco success record repeated for one key
	Contradictory    int // R-P0-6 Slice D1 (EDG-006): a key reported both FAILED and SUCCESS
	TelcoRecords     int
	PlatformRecords  int
	// R-P0-6 run manifest (recorded on the immutable recon_runs header).
	SourceHash                string
	SourceControlTotalMinor   int64
	PlatformControlTotalMinor int64
	// Rejected is true when the run failed the completeness floor and was
	// recorded as REJECTED without superseding the prior ACTIVE run.
	Rejected bool
	// R-P0-6 Slice C: the bounded window this run reconciled. NothingToReconcile
	// is true when the window was empty (no settled time since the watermark) —
	// no run is written.
	PeriodStart        time.Time
	PeriodEnd          time.Time
	NothingToReconcile bool
	// Unchanged is set by ReconcileRecentPeriods when a re-swept window's telco
	// source is byte-identical to its recorded run — nothing new arrived, so the
	// prior run is left ACTIVE untouched (no supersession, no money-trail churn).
	Unchanged bool
}

type toleranceCfg struct {
	AmountToleranceMinor int64 `json:"amount_tolerance_minor"`
	AutoResolve          bool  `json:"auto_resolve"`
	// MaxAmountMinor (R-P0-4) bounds a credible face value. It is BOTH a
	// plausibility ceiling and the overflow guard: recon compares int64
	// amounts from external telco JSON, and an unbounded value can overflow
	// the difference. Kept well below MaxInt64 so any |p-t| within bound is
	// representable and abs64 is always safe.
	MaxAmountMinor int64 `json:"max_amount_minor"`
	// MinCompletenessRatio (R-P0-6): a rerun must carry at least this fraction
	// of the prior ACTIVE run's source record count to supersede it. An empty or
	// truncated feed is REJECTED, never allowed to wipe a good reconciliation.
	MinCompletenessRatio float64 `json:"min_completeness_ratio"`
	// ReconLagSeconds (R-P0-6 Slice C): the settling lag. The reconciliation
	// window ends at now-lag, so records still in flight are not reconciled
	// prematurely. 0 = no lag.
	ReconLagSeconds int `json:"recon_lag_seconds"`
	// RereconcileLookbackSeconds (R-P0-6 Slice D2, VR-50-F1): how far back the
	// scheduled re-reconcile re-sweeps. A settled window that ended within this
	// horizon is re-reconciled so a late-arriving telco credit is recovered
	// rather than stranded as a missing-telco break. Required and positive.
	RereconcileLookbackSeconds int `json:"rereconcile_lookback_seconds"`
}

// RunFulfilment reconciles the next incremental fulfilment window
// [watermark, now-lag) for one telco/programme. The watermark is the max
// period_end of prior ACTIVE runs for this scope (genesis = epoch), so the
// first run bounds all settled history and later runs are incremental. Distinct
// periods coexist as separate ACTIVE runs.
func (s *Service) RunFulfilment(ctx context.Context, telcoID, programmeID string) (Summary, error) {
	tol, telcoRecords, err := s.loadForRecon(ctx, telcoID, programmeID)
	if err != nil {
		return Summary{RunID: platform.NewID("run")}, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(tol.ReconLagSeconds) * time.Second)

	var periodStart time.Time
	tctx := platform.WithTenant(ctx, telcoID)
	if err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(period_end), to_timestamp(0))
			FROM recon_runs WHERE telco_id=$1 AND programme_id=$2 AND layer=$3 AND state='ACTIVE'`,
			telcoID, programmeID, layerFulfilment).Scan(&periodStart)
	}); err != nil {
		return Summary{}, err
	}
	if !cutoff.After(periodStart) {
		// No settled time has elapsed since the watermark — nothing to reconcile.
		s.Log.Info("reconciliation window empty — nothing settled since watermark",
			"telco", telcoID, "programme", programmeID, "watermark", periodStart.UTC().Format(time.RFC3339))
		return Summary{RunID: platform.NewID("run"), PeriodStart: periodStart, PeriodEnd: cutoff, NothingToReconcile: true}, nil
	}
	return s.reconcile(ctx, telcoID, programmeID, periodStart, cutoff, telcoRecords, tol)
}

// ReconcilePeriod re-reconciles an EXPLICIT window [periodStart, periodEnd) —
// the operator re-run path for a corrected or late telco file covering a past
// period. It supersedes the prior ACTIVE run for exactly that period, guarded
// by the completeness floor so a truncated re-run cannot wipe the good one.
func (s *Service) ReconcilePeriod(ctx context.Context, telcoID, programmeID string, periodStart, periodEnd time.Time) (Summary, error) {
	if !periodEnd.After(periodStart) {
		return Summary{}, fmt.Errorf("recon period end must be after start")
	}
	tol, telcoRecords, err := s.loadForRecon(ctx, telcoID, programmeID)
	if err != nil {
		return Summary{RunID: platform.NewID("run")}, err
	}
	return s.reconcile(ctx, telcoID, programmeID, periodStart, periodEnd, telcoRecords, tol)
}

// ReconcileRecentPeriods re-reconciles every ACTIVE fulfilment window that
// ended within the governed rereconcile_lookback_seconds — the scheduled
// recovery path for late-arriving telco records (VR-50-F1 / REC-006).
//
// The incremental RunFulfilment advances the watermark past a window based on
// the telco records present AT RUN TIME. A telco credit that lands AFTER its
// window was reconciled is never revisited by future incremental runs (they
// start at the advanced watermark), so it would be stranded forever as a
// BREAK_MISSING_TELCO. This re-sweeps recent settled windows: a window whose
// telco source is byte-identical to its recorded run is SKIPPED (nothing new
// arrived — no supersession, no money-trail churn), and a window whose source
// changed is re-reconciled, superseding the prior run so the stranded break
// becomes a MATCHED. The completeness floor still guards each re-reconcile
// inside reconcile(), so a shrunk/truncated feed for a window is REJECTED and
// the good run kept. Returns one Summary per recent window (Unchanged=true for
// the skipped ones).
func (s *Service) ReconcileRecentPeriods(ctx context.Context, telcoID, programmeID string) ([]Summary, error) {
	tol, telcoRecords, err := s.loadForRecon(ctx, telcoID, programmeID)
	if err != nil {
		return nil, err
	}
	horizon := time.Now().UTC().Add(-time.Duration(tol.RereconcileLookbackSeconds) * time.Second)

	// Snapshot the recent ACTIVE windows up front. Each period_start is unique
	// among ACTIVE runs (the partial unique index), and re-reconciling one
	// supersedes only that period, so iterating the snapshot processes each
	// window exactly once even as reconcile() writes successor runs.
	type periodRow struct {
		periodStart, periodEnd time.Time
		sourceHash             string
	}
	var periods []periodRow
	tctx := platform.WithTenant(ctx, telcoID)
	if err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT period_start, period_end, source_hash
			FROM recon_runs
			WHERE telco_id=$1 AND programme_id=$2 AND layer=$3 AND state='ACTIVE'
			  AND period_end >= $4
			ORDER BY period_start`,
			telcoID, programmeID, layerFulfilment, horizon)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p periodRow
			if err := rows.Scan(&p.periodStart, &p.periodEnd, &p.sourceHash); err != nil {
				return err
			}
			periods = append(periods, p)
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}

	out := make([]Summary, 0, len(periods))
	for _, p := range periods {
		// Cheap gate: window the telco source to [periodStart, periodEnd) and
		// hash it. If the manifest is identical to the recorded run, nothing new
		// arrived for this window — skip it (no supersession, no trail churn).
		windowed := make([]telcoTransaction, 0, len(telcoRecords))
		for _, tr := range telcoRecords {
			if !tr.CreditedAt.Before(p.periodStart) && tr.CreditedAt.Before(p.periodEnd) {
				windowed = append(windowed, tr)
			}
		}
		_, _, h, mErr := sourceManifest(windowed, tol.MaxAmountMinor)
		if mErr != nil {
			return out, mErr
		}
		if h == p.sourceHash {
			out = append(out, Summary{PeriodStart: p.periodStart, PeriodEnd: p.periodEnd, Unchanged: true})
			continue
		}
		sum, err := s.reconcile(ctx, telcoID, programmeID, p.periodStart, p.periodEnd, telcoRecords, tol)
		if err != nil {
			return out, err
		}
		out = append(out, sum)
	}
	if len(periods) > 0 {
		s.Log.Info("scheduled re-reconcile swept recent settled windows (VR-50-F1)",
			"telco", telcoID, "programme", programmeID, "windows", len(periods),
			"horizon", horizon.Format(time.RFC3339))
	}
	return out, nil
}

// loadForRecon reads the governed tolerance (all fail-closed floors enforced)
// and fetches the telco-side records.
func (s *Service) loadForRecon(ctx context.Context, telcoID, programmeID string) (toleranceCfg, []telcoTransaction, error) {
	var tol toleranceCfg
	cv, err := s.Config.ActiveAt(ctx, "recon.tolerance", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return tol, nil, fmt.Errorf("recon.tolerance config: %w", err)
	}
	if err := json.Unmarshal(cv.Content, &tol); err != nil {
		return tol, nil, err
	}
	// R-P0-4: a credible-amount ceiling is required — the overflow guard for
	// comparing external telco amounts.
	if tol.MaxAmountMinor <= 0 {
		return tol, nil, fmt.Errorf("recon.tolerance has no max_amount_minor — refusing (unbounded external amounts are not reconcilable)")
	}
	// R-P0-6: a completeness ratio is required — the rerun-wipe protection.
	if tol.MinCompletenessRatio <= 0 || tol.MinCompletenessRatio > 1 {
		return tol, nil, fmt.Errorf("recon.tolerance min_completeness_ratio must be in (0,1] — refusing (no rerun-completeness protection)")
	}
	// R-P0-6 Slice C: the settling lag bounds the window end; negative is refused.
	if tol.ReconLagSeconds < 0 {
		return tol, nil, fmt.Errorf("recon.tolerance recon_lag_seconds must be >= 0")
	}
	// R-P0-6 Slice D2: a positive re-reconcile lookback is required — a zero/absent
	// horizon leaves late-arriving telco credits stranded as missing-telco breaks
	// (armed-but-dead), defeating the whole point of the scheduled re-reconcile.
	if tol.RereconcileLookbackSeconds <= 0 {
		return tol, nil, fmt.Errorf("recon.tolerance rereconcile_lookback_seconds must be > 0 — refusing (late arrivals would be stranded)")
	}
	recs, err := s.fetchTelcoRecords(ctx, telcoID)
	if err != nil {
		return tol, nil, err
	}
	return tol, recs, nil
}

// reconcile reconciles a bounded window [periodStart, periodEnd) and writes the
// immutable run header + items (or a REJECTED header if the windowed source is
// below the completeness floor for a re-reconcile of the same period).
func (s *Service) reconcile(ctx context.Context, telcoID, programmeID string, periodStart, periodEnd time.Time, telcoRecords []telcoTransaction, tol toleranceCfg) (Summary, error) {
	runID := platform.NewID("run")
	sum := Summary{RunID: runID, TelcoRecords: len(telcoRecords), PeriodStart: periodStart, PeriodEnd: periodEnd}
	var srcCount int64
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {

		// Bound the telco side to the window by credited_at, then compute the
		// manifest over exactly the windowed source set (overflow refused).
		windowed := make([]telcoTransaction, 0, len(telcoRecords))
		for _, tr := range telcoRecords {
			if !tr.CreditedAt.Before(periodStart) && tr.CreditedAt.Before(periodEnd) {
				windowed = append(windowed, tr)
			}
		}
		var srcTotal int64
		var srcHash string
		var mErr error
		if srcCount, srcTotal, srcHash, mErr = sourceManifest(windowed, tol.MaxAmountMinor); mErr != nil {
			return mErr
		}
		sum.SourceControlTotalMinor, sum.SourceHash = srcTotal, srcHash

		// Platform side, bounded to the window by activation time.
		plat := map[string]platformRecord{}
		rows, err := tx.Query(ctx, `
			SELECT a.advance_id, a.state, a.face_value_minor, a.currency, COALESCE(fa.telco_reference,'')
			FROM advances a
			LEFT JOIN fulfilment_attempts fa
			  ON fa.advance_id = a.advance_id AND fa.state = 'CONFIRMED'
			WHERE a.programme_id = $1
			  AND a.state IN ('ACTIVE','PARTIALLY_RECOVERED','CLOSED')
			  AND a.activated_at >= $2 AND a.activated_at < $3`, programmeID, periodStart, periodEnd)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p platformRecord
			if err := rows.Scan(&p.AdvanceID, &p.State, &p.FaceValueMinor, &p.Currency, &p.TelcoReference); err != nil {
				rows.Close()
				return err
			}
			plat[p.AdvanceID] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		sum.PlatformRecords = len(plat)
		var platTotal int64
		for _, p := range plat {
			platTotal += p.FaceValueMinor
		}

		// Classification is buffered (pure) so the immutable run header can be
		// inserted with final control totals + outcome counts BEFORE its items.
		var items []reconItem
		writeItem := func(matchKey, itemType, platformRef, telcoRef, status string, detail map[string]any) error {
			items = append(items, reconItem{matchKey, itemType, platformRef, telcoRef, status, detail})
			return nil
		}

		// R-P0-6 Slice D1 (EDG-006): a key reported BOTH FAILED and SUCCESS in
		// the window is internally contradictory. Pre-scan the FAILED keys so the
		// SUCCESS is flagged rather than reconciled as clean.
		hasFailed := map[string]bool{}
		for _, tr := range windowed {
			if tr.Status == "FAILED" {
				hasFailed[tr.PlatformRequestID] = true
			}
		}

		seen := map[string]bool{}
		for _, tr := range windowed {
			if tr.Status != "SUCCESS" {
				continue // failed telco records carry no credit to reconcile
			}
			// R-P0-6 Slice B: match_key is the logical fulfilment being
			// reconciled. A telco success record for a key ALREADY seen this run
			// is a duplicate source record (R-P2-5) — classified as such, never
			// silently double-counted into a second MATCHED. The first record for
			// a key is the canonical classification below.
			key := tr.PlatformRequestID
			if seen[key] {
				sum.DuplicateTelco++
				if err := writeItem(key, "FULFILMENT", key, tr.TelcoReference,
					"BREAK_DUPLICATE_TELCO_RECORD", map[string]any{
						"telco_amount_minor": tr.FaceValueMinor, "telco_currency": tr.Currency,
					}); err != nil {
					return err
				}
				continue
			}
			seen[key] = true
			// EDG-006: the same key was also reported FAILED — do NOT reconcile
			// this SUCCESS as clean; it is a contradictory data-quality break.
			if hasFailed[key] {
				sum.Contradictory++
				platformRef := key
				if p, ok := plat[key]; ok {
					platformRef = p.AdvanceID
				}
				if err := writeItem(key, "FULFILMENT", platformRef, tr.TelcoReference,
					"BREAK_CONTRADICTORY_TELCO_STATUS", map[string]any{
						"telco_amount_minor": tr.FaceValueMinor, "telco_currency": tr.Currency,
						"note": "same key reported both FAILED and SUCCESS in this window",
					}); err != nil {
					return err
				}
				continue
			}
			p, ok := plat[key]
			switch {
			case !ok:
				// EDG-027 class: telco says credited, platform has no
				// money-bearing advance. NEVER force-matched.
				sum.MissingPlatform++
				if err := writeItem(key, "FULFILMENT", key, tr.TelcoReference,
					"BREAK_MISSING_PLATFORM", map[string]any{
						"telco_amount_minor": tr.FaceValueMinor, "telco_currency": tr.Currency,
					}); err != nil {
					return err
				}
			case !isISOCurrency(tr.Currency) || !credibleAmount(tr.FaceValueMinor, tol.MaxAmountMinor):
				// R-P0-4: an out-of-range or malformed telco amount/currency is
				// NEVER fed to the numeric compare (overflow-safe) — it is a
				// data-integrity break for ops, both values recorded.
				sum.Malformed++
				if err := writeItem(key, "FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"BREAK_MALFORMED_TELCO_RECORD", map[string]any{
						"platform_minor": p.FaceValueMinor, "platform_currency": p.Currency,
						"telco_minor": tr.FaceValueMinor, "telco_currency": tr.Currency,
					}); err != nil {
					return err
				}
			case p.Currency != tr.Currency:
				// R-P0-3: currency BEFORE amount — NGN 1,000 and USD 1,000 are
				// not a match. Compared as raw strings; no cross-rate is ever
				// applied in reconciliation.
				sum.CurrencyMismatch++
				if err := writeItem(key, "FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"BREAK_CURRENCY_MISMATCH", map[string]any{
						"platform_currency": p.Currency, "telco_currency": tr.Currency,
						"platform_minor": p.FaceValueMinor, "telco_minor": tr.FaceValueMinor,
					}); err != nil {
					return err
				}
			case abs64(p.FaceValueMinor-tr.FaceValueMinor) > tol.AmountToleranceMinor:
				// Both amounts are now range-validated and same-currency, so
				// the subtraction cannot overflow.
				sum.AmountMismatch++
				if err := writeItem(key, "FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"BREAK_AMOUNT_MISMATCH", map[string]any{
						"platform_minor": p.FaceValueMinor, "telco_minor": tr.FaceValueMinor,
						"currency": p.Currency,
					}); err != nil {
					return err
				}
			default:
				sum.Matched++
				if err := writeItem(key, "FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"MATCHED", map[string]any{"amount_minor": p.FaceValueMinor, "currency": p.Currency}); err != nil {
					return err
				}
			}
		}
		// Reverse direction: platform-credited without a telco record.
		for id, p := range plat {
			if !seen[id] {
				sum.MissingTelco++
				if err := writeItem(id, "FULFILMENT", p.AdvanceID, p.TelcoReference,
					"BREAK_MISSING_TELCO", map[string]any{"platform_minor": p.FaceValueMinor}); err != nil {
					return err
				}
			}
		}
		sum.PlatformControlTotalMinor = platTotal
		breaks := sum.MissingPlatform + sum.MissingTelco + sum.AmountMismatch + sum.CurrencyMismatch + sum.Malformed + sum.DuplicateTelco + sum.Contradictory

		// Completeness gate (R-P0-6): a rerun must carry at least
		// min_completeness_ratio of the prior ACTIVE run's source record count.
		// An empty or truncated feed must NEVER supersede a good run — otherwise
		// a transient failed fetch wipes reconciliation state. Read the prior
		// active count; if this run falls below the floor, record it as REJECTED
		// (for audit) and leave the prior ACTIVE untouched.
		// The prior baseline is the ACTIVE run FOR THE SAME PERIOD (a re-reconcile
		// of this window), not a different period — so a low-volume next period is
		// never compared against a busy prior one.
		var priorCount int64
		priorExists := true
		if err := tx.QueryRow(ctx, `
			SELECT source_record_count FROM recon_runs
			WHERE telco_id=$1 AND programme_id=$2 AND layer=$3 AND state='ACTIVE' AND period_start=$4`,
			telcoID, programmeID, layerFulfilment, periodStart).Scan(&priorCount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				priorExists = false
			} else {
				return err
			}
		}
		if priorExists {
			floor := int64(math.Ceil(float64(priorCount) * tol.MinCompletenessRatio))
			if srcCount < floor {
				sum.Rejected = true
				if _, err := tx.Exec(ctx, `
					INSERT INTO recon_runs (run_id, telco_id, programme_id, layer,
					  period_start, period_end,
					  source_record_count, source_control_total_minor, source_hash,
					  platform_record_count, platform_control_total_minor,
					  matched_count, break_count, state, created_by)
					VALUES ($1,$2,$3,$4, $5,$6,
					  $7,$8,$9, $10,$11, 0,0, 'REJECTED', 'worker:recon')`,
					runID, telcoID, programmeID, layerFulfilment, periodStart, periodEnd,
					srcCount, srcTotal, srcHash, int64(len(plat)), platTotal); err != nil {
					return err
				}
				// REJECTED: no items, no supersession — the prior run stays live.
				return nil
			}
		}

		// Supersede the prior ACTIVE run FOR THIS PERIOD (a re-reconcile of this
		// window), then write the immutable header. The partial unique index (one
		// ACTIVE per telco/programme/layer/period_start) makes this fail-closed:
		// if a re-reconcile ever skipped the supersede, the header INSERT would
		// violate the index and abort — two live reconciliations of the same
		// period can never exist. Distinct periods coexist as separate ACTIVE runs.
		if _, err := tx.Exec(ctx, `
			UPDATE recon_runs SET state='SUPERSEDED', superseded_by=$1
			WHERE telco_id=$2 AND programme_id=$3 AND layer=$4 AND state='ACTIVE' AND period_start=$5`,
			runID, telcoID, programmeID, layerFulfilment, periodStart); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO recon_runs (run_id, telco_id, programme_id, layer,
			  period_start, period_end,
			  source_record_count, source_control_total_minor, source_hash,
			  platform_record_count, platform_control_total_minor,
			  matched_count, break_count, created_by)
			VALUES ($1,$2,$3,$4, $5,$6,
			  $7,$8,$9, $10,$11, $12,$13, 'worker:recon')`,
			runID, telcoID, programmeID, layerFulfilment, periodStart, periodEnd,
			srcCount, srcTotal, srcHash,
			int64(len(plat)), platTotal,
			int64(sum.Matched), int64(breaks)); err != nil {
			return err
		}

		// Items are FK-linked to the header just written (recon_items_run_fk).
		for _, it := range items {
			dj, err := json.Marshal(it.detail)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type,
				  platform_ref, telco_ref, status, detail, match_key)
				VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9)`,
				platform.NewID("rci"), runID, telcoID, it.itemType, it.platformRef, it.telcoRef, it.status, dj, it.matchKey); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sum, err
	}
	switch {
	case sum.Rejected:
		s.Log.Error("reconciliation re-reconcile REJECTED — source below completeness floor; prior run kept",
			"run_id", runID, "source_records", srcCount)
	default:
		if breaks := sum.MissingPlatform + sum.MissingTelco + sum.AmountMismatch + sum.CurrencyMismatch + sum.Malformed + sum.DuplicateTelco + sum.Contradictory; breaks > 0 {
			s.Log.Error("reconciliation breaks found — operator attention required (V2-REC-012)",
				"run_id", runID, "breaks", breaks, "matched", sum.Matched)
		} else {
			s.Log.Info("reconciliation clean", "run_id", runID, "matched", sum.Matched)
		}
	}
	return sum, nil
}

// fetchTelcoRecords pulls the telco-side log from the endpoint configured in
// telco.adapter (the simulator's /sim/transactions in M1; a real operator's
// reconciliation file exchange replaces this behind the same canonical shape).
func (s *Service) fetchTelcoRecords(ctx context.Context, telcoID string) ([]telcoTransaction, error) {
	cv, err := s.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("telco.adapter config: %w", err)
	}
	var ac struct {
		FulfilmentURL string `json:"fulfilment_url"`
	}
	if err := json.Unmarshal(cv.Content, &ac); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ac.FulfilmentURL+"/sim/transactions", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch telco records: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telco records endpoint: HTTP %d", resp.StatusCode)
	}
	var out []telcoTransaction
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// credibleAmount guards the numeric compare against external telco JSON
// (R-P0-4): a face value must be non-negative and within the governed
// ceiling. The ceiling is far below MaxInt64, so any difference between two
// credible amounts is representable and abs64 never hits the MinInt64 trap.
func credibleAmount(minor, ceiling int64) bool {
	return minor >= 0 && minor <= ceiling
}

// isISOCurrency accepts a plausible ISO-4217 alpha-3 code (three A–Z letters).
// Reconciliation compares currencies as opaque codes; it never converts.
func isISOCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}
