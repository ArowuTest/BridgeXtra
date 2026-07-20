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
	PlatformRequestID string `json:"platform_request_id"`
	TelcoReference    string `json:"telco_transaction_reference"`
	FaceValueMinor    int64  `json:"face_value_minor"`
	Currency          string `json:"currency"`
	Status            string `json:"status"`
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
	TelcoRecords     int
	PlatformRecords  int
	// R-P0-6 run manifest (recorded on the immutable recon_runs header).
	SourceHash                string
	SourceControlTotalMinor   int64
	PlatformControlTotalMinor int64
	// Rejected is true when the run failed the completeness floor and was
	// recorded as REJECTED without superseding the prior ACTIVE run.
	Rejected bool
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
}

// RunFulfilment reconciles the fulfilment layer for one telco/programme.
func (s *Service) RunFulfilment(ctx context.Context, telcoID, programmeID string) (Summary, error) {
	runID := platform.NewID("run")
	sum := Summary{RunID: runID}

	// Tolerance from governed config (zero + no auto-resolve floor).
	cv, err := s.Config.ActiveAt(ctx, "recon.tolerance", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return sum, fmt.Errorf("recon.tolerance config: %w", err)
	}
	var tol toleranceCfg
	if err := json.Unmarshal(cv.Content, &tol); err != nil {
		return sum, err
	}
	// R-P0-4 fail-closed floor: without a credible-amount ceiling there is no
	// safe way to compare external telco amounts. Refuse rather than risk an
	// overflow or accept an absurd value.
	if tol.MaxAmountMinor <= 0 {
		return sum, fmt.Errorf("recon.tolerance has no max_amount_minor — refusing (unbounded external amounts are not reconcilable)")
	}
	// R-P0-6 fail-closed floor: without a completeness ratio there is no rule
	// protecting a good run from being wiped by an empty/truncated rerun. Refuse.
	if tol.MinCompletenessRatio <= 0 || tol.MinCompletenessRatio > 1 {
		return sum, fmt.Errorf("recon.tolerance min_completeness_ratio must be in (0,1] — refusing (no rerun-completeness protection)")
	}

	telcoRecords, err := s.fetchTelcoRecords(ctx, telcoID)
	if err != nil {
		return sum, err
	}
	sum.TelcoRecords = len(telcoRecords)

	// R-P0-6: the source manifest is computed over exactly what was ingested,
	// before any comparison — it is the run's provenance record. A control-total
	// overflow is refused here (never a wrapped total).
	srcCount, srcTotal, srcHash, err := sourceManifest(telcoRecords, tol.MaxAmountMinor)
	if err != nil {
		return sum, err
	}
	sum.SourceControlTotalMinor, sum.SourceHash = srcTotal, srcHash

	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		// Platform side: money-bearing advances + their confirmed references.
		plat := map[string]platformRecord{}
		rows, err := tx.Query(ctx, `
			SELECT a.advance_id, a.state, a.face_value_minor, a.currency, COALESCE(fa.telco_reference,'')
			FROM advances a
			LEFT JOIN fulfilment_attempts fa
			  ON fa.advance_id = a.advance_id AND fa.state = 'CONFIRMED'
			WHERE a.programme_id = $1
			  AND a.state IN ('ACTIVE','PARTIALLY_RECOVERED','CLOSED')`, programmeID)
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
		writeItem := func(itemType, platformRef, telcoRef, status string, detail map[string]any) error {
			items = append(items, reconItem{itemType, platformRef, telcoRef, status, detail})
			return nil
		}

		seen := map[string]bool{}
		for _, tr := range telcoRecords {
			if tr.Status != "SUCCESS" {
				continue // failed telco records carry no credit to reconcile
			}
			seen[tr.PlatformRequestID] = true
			p, ok := plat[tr.PlatformRequestID]
			switch {
			case !ok:
				// EDG-027 class: telco says credited, platform has no
				// money-bearing advance. NEVER force-matched.
				sum.MissingPlatform++
				if err := writeItem("FULFILMENT", tr.PlatformRequestID, tr.TelcoReference,
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
				if err := writeItem("FULFILMENT", p.AdvanceID, tr.TelcoReference,
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
				if err := writeItem("FULFILMENT", p.AdvanceID, tr.TelcoReference,
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
				if err := writeItem("FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"BREAK_AMOUNT_MISMATCH", map[string]any{
						"platform_minor": p.FaceValueMinor, "telco_minor": tr.FaceValueMinor,
						"currency": p.Currency,
					}); err != nil {
					return err
				}
			default:
				sum.Matched++
				if err := writeItem("FULFILMENT", p.AdvanceID, tr.TelcoReference,
					"MATCHED", map[string]any{"amount_minor": p.FaceValueMinor, "currency": p.Currency}); err != nil {
					return err
				}
			}
		}
		// Reverse direction: platform-credited without a telco record.
		for id, p := range plat {
			if !seen[id] {
				sum.MissingTelco++
				if err := writeItem("FULFILMENT", p.AdvanceID, p.TelcoReference,
					"BREAK_MISSING_TELCO", map[string]any{"platform_minor": p.FaceValueMinor}); err != nil {
					return err
				}
			}
		}
		sum.PlatformControlTotalMinor = platTotal
		breaks := sum.MissingPlatform + sum.MissingTelco + sum.AmountMismatch + sum.CurrencyMismatch + sum.Malformed

		// Completeness gate (R-P0-6): a rerun must carry at least
		// min_completeness_ratio of the prior ACTIVE run's source record count.
		// An empty or truncated feed must NEVER supersede a good run — otherwise
		// a transient failed fetch wipes reconciliation state. Read the prior
		// active count; if this run falls below the floor, record it as REJECTED
		// (for audit) and leave the prior ACTIVE untouched.
		var priorCount int64
		priorExists := true
		if err := tx.QueryRow(ctx, `
			SELECT source_record_count FROM recon_runs
			WHERE telco_id=$1 AND programme_id=$2 AND layer=$3 AND state='ACTIVE'`,
			telcoID, programmeID, layerFulfilment).Scan(&priorCount); err != nil {
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
					VALUES ($1,$2,$3,$4, to_timestamp(0), now(),
					  $5,$6,$7, $8,$9, 0,0, 'REJECTED', 'worker:recon')`,
					runID, telcoID, programmeID, layerFulfilment,
					srcCount, srcTotal, srcHash, int64(len(plat)), platTotal); err != nil {
					return err
				}
				// REJECTED: no items, no supersession — the prior run stays live.
				return nil
			}
		}

		// Supersede the prior ACTIVE run for this scope, then write the immutable
		// header. The partial unique index (one ACTIVE per telco/programme/layer)
		// makes this fail-closed: if a rerun ever skipped the supersede, the
		// header INSERT would violate the index and abort — two live
		// reconciliations of the same scope can never exist.
		if _, err := tx.Exec(ctx, `
			UPDATE recon_runs SET state='SUPERSEDED', superseded_by=$1
			WHERE telco_id=$2 AND programme_id=$3 AND layer=$4 AND state='ACTIVE'`,
			runID, telcoID, programmeID, layerFulfilment); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO recon_runs (run_id, telco_id, programme_id, layer,
			  period_start, period_end,
			  source_record_count, source_control_total_minor, source_hash,
			  platform_record_count, platform_control_total_minor,
			  matched_count, break_count, created_by)
			VALUES ($1,$2,$3,$4, to_timestamp(0), now(),
			  $5,$6,$7, $8,$9, $10,$11, 'worker:recon')`,
			runID, telcoID, programmeID, layerFulfilment,
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
				  platform_ref, telco_ref, status, detail)
				VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8)`,
				platform.NewID("rci"), runID, telcoID, it.itemType, it.platformRef, it.telcoRef, it.status, dj); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sum, err
	}
	if breaks := sum.MissingPlatform + sum.MissingTelco + sum.AmountMismatch + sum.CurrencyMismatch + sum.Malformed; breaks > 0 {
		s.Log.Error("reconciliation breaks found — operator attention required (V2-REC-012)",
			"run_id", runID, "breaks", breaks, "matched", sum.Matched)
	} else {
		s.Log.Info("reconciliation clean", "run_id", runID, "matched", sum.Matched)
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
