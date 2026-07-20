// Package recon reconciles platform records against telco-side records
// (M1: the simulator's transaction log) — V2-REC-001..007. M1 scope is the
// FULFILMENT layer: platform-approved advances vs telco credits, both
// directions, with amount comparison under the governed tolerance (seeded
// ZERO). Breaks are recorded, never auto-resolved (recon.tolerance
// auto_resolve=false floor), never force-matched (V1-FIN-005).
package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

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

	telcoRecords, err := s.fetchTelcoRecords(ctx, telcoID)
	if err != nil {
		return sum, err
	}
	sum.TelcoRecords = len(telcoRecords)

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

		writeItem := func(itemType, platformRef, telcoRef, status string, detail map[string]any) error {
			dj, err := json.Marshal(detail)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO recon_items (recon_item_id, run_id, telco_id, item_type,
				  platform_ref, telco_ref, status, detail)
				VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8)`,
				platform.NewID("rci"), runID, telcoID, itemType, platformRef, telcoRef, status, dj)
			return err
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
