// Package featureingest pulls the canonical batch feature file from the telco
// (M2b, V2-SCR-001/002) and lands it in the feature store.
//
// Discipline:
//   - file-level idempotency by content hash: re-ingesting the same file is a
//     recorded no-op (the schema arbitrates, not this code);
//   - per-row convergence: a resumed partial ingest upserts, never doubles;
//   - malformed rows are QUARANTINED with counts on the file record — a row
//     is never silently dropped;
//   - every stored feature quantity is an integer (minor units / day counts):
//     the canonical contract has no floats to begin with (BC-1).
package featureingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type Service struct {
	Pool       *pgxpool.Pool // tcp_app
	Config     *configsvc.Service
	Log        *slog.Logger
	HTTPClient *http.Client
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log,
		HTTPClient: egress.SafeClient(60 * time.Second)} // SSRF egress guard (VR-32)
}

// fileShape mirrors the canonical simulator/telco feature-file contract.
type fileShape struct {
	TelcoID string     `json:"telco_id"`
	AsOf    time.Time  `json:"as_of"`
	Rows    []rowShape `json:"rows"`
}

type rowShape struct {
	MSISDNToken         string   `json:"msisdn_token"`
	TenureDays          *int     `json:"tenure_days"`
	ActivityDays30d     *int     `json:"activity_days_30d"`
	ActiveDays90d       *int     `json:"active_days_90d"`
	WeeklyRechargeMinor []int64  `json:"weekly_recharge_minor"`
	Currency            string   `json:"currency"`
	QualityFlags        []string `json:"quality_flags"`
}

// Summary reports one ingest (control totals — a partial ingest is visible).
type Summary struct {
	FeatureFileID string
	AsOf          time.Time
	Rows          int
	Written       int
	Skipped       int // already-present (subscriber, as_of) rows
	Quarantined   int
	Duplicate     bool // whole file previously ingested
}

// Run fetches the telco's current feature file and ingests it.
func (s *Service) Run(ctx context.Context, telcoID string) (Summary, error) {
	raw, err := s.fetch(ctx, telcoID)
	if err != nil {
		return Summary{}, err
	}
	return s.IngestRaw(ctx, telcoID, "telco:feature-file", raw)
}

// fetch pulls the file from the endpoint in governed telco.adapter config —
// the base URL is config; the path is the canonical contract.
func (s *Service) fetch(ctx context.Context, telcoID string) ([]byte, error) {
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
	url := fmt.Sprintf("%s/v1/telcos/%s/feature-file", ac.FulfilmentURL, telcoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feature file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feature file endpoint returned %d", resp.StatusCode)
	}
	return raw, nil
}

// IngestRaw lands raw file bytes. Exported path for tests and for a future
// file-drop (SFTP) source: the pipeline is source-agnostic once bytes arrive.
func (s *Service) IngestRaw(ctx context.Context, telcoID, source string, raw []byte) (Summary, error) {
	var file fileShape
	if err := json.Unmarshal(raw, &file); err != nil {
		return Summary{}, fmt.Errorf("feature file does not parse: %w", err)
	}
	if file.AsOf.IsZero() {
		return Summary{}, fmt.Errorf("feature file has no as_of — refusing an undated data cut (V2-SCR-002)")
	}

	// G2-F3: the plausibility ceiling from governed config. FAIL CLOSED — a
	// telco config without a ceiling refuses the whole feed rather than
	// letting a corrupt value near int64-max score garbage.
	ceiling, err := s.plausibilityCeiling(ctx, telcoID)
	if err != nil {
		return Summary{}, err
	}
	hash := sha256.Sum256(raw)
	sum := Summary{FeatureFileID: platform.NewID("ffl"), AsOf: file.AsOf, Rows: len(file.Rows)}

	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		files := repo.FeatureFiles{}
		existingID, err := files.Insert(ctx, tx, entity.FeatureFile{
			FeatureFileID: sum.FeatureFileID, TelcoID: telcoID, Source: source,
			AsOf: file.AsOf, ContentHash: hex.EncodeToString(hash[:]), Status: "INGESTED",
		})
		if err != nil {
			if existingID != "" {
				sum.Duplicate = true
				sum.FeatureFileID = existingID
				return nil // recorded no-op: the file is already in
			}
			return err
		}

		// Chunked set-based ingestion: validate + canonicalise in code, then
		// one bulk subscriber-ensure and one bulk snapshot-upsert per chunk
		// (the 1M-row nightly file cannot afford per-row round trips).
		const chunkSize = 5_000
		snaps := repo.FeatureSnapshots{}
		subs := repo.Subscribers{}
		for start := 0; start < len(file.Rows); start += chunkSize {
			end := min(start+chunkSize, len(file.Rows))
			chunk := file.Rows[start:end]

			tokens := make([]string, 0, len(chunk))
			newIDs := make([]string, 0, len(chunk))
			type prepared struct {
				token             string
				features, quality []byte
				hash              string
			}
			preps := make([]prepared, 0, len(chunk))
			for i, row := range chunk {
				if reason := validateRow(row, ceiling); reason != "" {
					sum.Quarantined++
					s.Log.Warn("feature row quarantined", "file", sum.FeatureFileID,
						"row", start+i, "token", row.MSISDNToken, "reason", reason)
					continue
				}
				features, quality, err := canonicalRow(row)
				if err != nil {
					return fmt.Errorf("row %d (%s): %w", start+i, row.MSISDNToken, err)
				}
				rowHash := sha256.Sum256(features)
				tokens = append(tokens, row.MSISDNToken)
				newIDs = append(newIDs, platform.NewID("sub"))
				preps = append(preps, prepared{token: row.MSISDNToken,
					features: features, quality: quality, hash: hex.EncodeToString(rowHash[:])})
			}
			if len(preps) == 0 {
				continue
			}
			subIDs, err := subs.BulkEnsureByToken(ctx, tx, telcoID, tokens, newIDs)
			if err != nil {
				return err
			}
			batch := make([]entity.FeatureSnapshot, 0, len(preps))
			for _, p := range preps {
				subID, ok := subIDs[p.token]
				if !ok {
					return fmt.Errorf("token %q did not resolve to a subscriber", p.token)
				}
				batch = append(batch, entity.FeatureSnapshot{
					FeatureSnapshotID: platform.NewID("ftr"), TelcoID: telcoID,
					SubscriberAccountID: subID, FeatureFileID: sum.FeatureFileID,
					AsOf: file.AsOf, Features: p.features, Quality: p.quality,
					ContentHash: p.hash,
				})
			}
			written, err := snaps.BulkUpsert(ctx, tx, batch)
			if err != nil {
				return err
			}
			sum.Written += int(written)
			sum.Skipped += len(batch) - int(written)
		}
		status := "INGESTED"
		if sum.Quarantined > 0 && sum.Written == 0 && sum.Skipped == 0 {
			status = "QUARANTINED"
		}
		return files.Finalize(ctx, tx, sum.FeatureFileID, sum.Rows, sum.Quarantined, status)
	})
	if err != nil {
		return sum, err
	}
	if sum.Duplicate {
		s.Log.Info("feature file already ingested — recorded no-op",
			"file", sum.FeatureFileID, "telco", telcoID)
	} else {
		s.Log.Info("feature file ingested", "file", sum.FeatureFileID, "telco", telcoID,
			"rows", sum.Rows, "written", sum.Written, "skipped", sum.Skipped,
			"quarantined", sum.Quarantined)
	}
	return sum, nil
}

// plausibilityCeiling reads the feed's maximum credible weekly recharge from
// governed telco.adapter config (G2-F3). Absent or non-positive = refuse:
// "no ceiling" must never mean "unlimited".
func (s *Service) plausibilityCeiling(ctx context.Context, telcoID string) (int64, error) {
	cv, err := s.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("telco.adapter config: %w", err)
	}
	var ac struct {
		MaxWeeklyRechargeMinor int64 `json:"max_weekly_recharge_minor"`
	}
	if err := json.Unmarshal(cv.Content, &ac); err != nil {
		return 0, err
	}
	if ac.MaxWeeklyRechargeMinor <= 0 {
		return 0, fmt.Errorf("telco.adapter for %s has no max_weekly_recharge_minor — refusing the feed (G2-F3: absent ceiling is not unlimited)", telcoID)
	}
	return ac.MaxWeeklyRechargeMinor, nil
}

// validateRow enforces the canonical contract; a violation quarantines the
// row with a reason (never a silent drop, never a partial guess).
func validateRow(r rowShape, ceilingMinor int64) string {
	switch {
	case r.MSISDNToken == "":
		return "missing msisdn_token"
	case r.TenureDays == nil || *r.TenureDays < 0:
		return "tenure_days missing or negative"
	case r.ActivityDays30d == nil || *r.ActivityDays30d < 0 || *r.ActivityDays30d > 30:
		return "activity_days_30d out of range"
	case r.ActiveDays90d == nil || *r.ActiveDays90d < 0 || *r.ActiveDays90d > 90:
		return "active_days_90d out of range"
	case len(r.WeeklyRechargeMinor) != 13:
		return "weekly_recharge_minor must have exactly 13 weeks"
	case len(r.Currency) != 3:
		return "currency must be ISO alpha-3"
	}
	for _, w := range r.WeeklyRechargeMinor {
		if w < 0 {
			return "negative weekly recharge amount"
		}
		if w > ceilingMinor {
			// G2-F3: implausible value (feed corruption / unit error) — the
			// row is quarantined, never scored.
			return fmt.Sprintf("weekly recharge %d exceeds plausibility ceiling %d", w, ceilingMinor)
		}
	}
	return ""
}

// canonicalRow re-marshals the row into the CANONICAL stored form with sorted
// keys — the bytes the content hash (and therefore BC-4 replay) pins.
func canonicalRow(r rowShape) (features, quality []byte, err error) {
	f := map[string]any{
		"tenure_days":           *r.TenureDays,
		"activity_days_30d":     *r.ActivityDays30d,
		"active_days_90d":       *r.ActiveDays90d,
		"weekly_recharge_minor": r.WeeklyRechargeMinor,
		"currency":              r.Currency,
	}
	q := map[string]any{"flags": r.QualityFlags}
	if r.QualityFlags == nil {
		q["flags"] = []string{}
	}
	var fb, qb bytes.Buffer
	fe := json.NewEncoder(&fb) // map keys marshal sorted — canonical
	fe.SetEscapeHTML(false)
	if err := fe.Encode(f); err != nil {
		return nil, nil, err
	}
	qe := json.NewEncoder(&qb)
	qe.SetEscapeHTML(false)
	if err := qe.Encode(q); err != nil {
		return nil, nil, err
	}
	return bytes.TrimSpace(fb.Bytes()), bytes.TrimSpace(qb.Bytes()), nil
}
