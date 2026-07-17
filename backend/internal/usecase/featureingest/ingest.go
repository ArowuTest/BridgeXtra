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
		HTTPClient: &http.Client{Timeout: 60 * time.Second}}
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
	return s.ingest(ctx, telcoID, "telco:feature-file", raw)
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

// ingest lands raw file bytes. Exported path for tests and for a future
// file-drop (SFTP) source: the pipeline is source-agnostic once bytes arrive.
func (s *Service) ingest(ctx context.Context, telcoID, source string, raw []byte) (Summary, error) {
	var file fileShape
	if err := json.Unmarshal(raw, &file); err != nil {
		return Summary{}, fmt.Errorf("feature file does not parse: %w", err)
	}
	if file.AsOf.IsZero() {
		return Summary{}, fmt.Errorf("feature file has no as_of — refusing an undated data cut (V2-SCR-002)")
	}
	hash := sha256.Sum256(raw)
	sum := Summary{FeatureFileID: platform.NewID("ffl"), AsOf: file.AsOf, Rows: len(file.Rows)}

	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
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

		snaps := repo.FeatureSnapshots{}
		subs := repo.Subscribers{}
		for i, row := range file.Rows {
			if reason := validateRow(row); reason != "" {
				sum.Quarantined++
				s.Log.Warn("feature row quarantined", "file", sum.FeatureFileID,
					"row", i, "token", row.MSISDNToken, "reason", reason)
				continue
			}
			sub, err := subs.EnsureByToken(ctx, tx, telcoID, row.MSISDNToken, platform.NewID("sub"))
			if err != nil {
				return fmt.Errorf("row %d (%s): %w", i, row.MSISDNToken, err)
			}
			features, quality, err := canonicalRow(row)
			if err != nil {
				return fmt.Errorf("row %d (%s): %w", i, row.MSISDNToken, err)
			}
			rowHash := sha256.Sum256(features)
			written, err := snaps.Upsert(ctx, tx, entity.FeatureSnapshot{
				FeatureSnapshotID: platform.NewID("ftr"), TelcoID: telcoID,
				SubscriberAccountID: sub.SubscriberAccountID, FeatureFileID: sum.FeatureFileID,
				AsOf: file.AsOf, Features: features, Quality: quality,
				ContentHash: hex.EncodeToString(rowHash[:]),
			})
			if err != nil {
				return err
			}
			if written {
				sum.Written++
			} else {
				sum.Skipped++
			}
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

// validateRow enforces the canonical contract; a violation quarantines the
// row with a reason (never a silent drop, never a partial guess).
func validateRow(r rowShape) string {
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
