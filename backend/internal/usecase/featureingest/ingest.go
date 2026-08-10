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
	Pool    *pgxpool.Pool // tcp_app
	Config  *configsvc.Service
	Log     *slog.Logger
	Fetcher FeedFetcher
}

// FeedFetcher fetches a telco feed URL with the telco's governed outbound partner
// auth applied (fail-closed) through the SSRF-safe egress client. Satisfied by
// *mno.HTTPAdapter — the feature feed is fetched with the SAME authentication as
// fulfilment, never unauthenticated (BX-HIGH-006).
type FeedFetcher interface {
	AuthenticatedGet(ctx context.Context, telcoID, url string) (*http.Response, error)
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, fetcher FeedFetcher, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Fetcher: fetcher, Log: log}
}

// fileShape mirrors the canonical simulator/telco feature-file contract.
type fileShape struct {
	TelcoID string     `json:"telco_id"`
	AsOf    time.Time  `json:"as_of"`
	Rows    []rowShape `json:"rows"`
	// RowCount (BX-HIGH-006): the feed's self-declared control total. When present it MUST
	// equal len(Rows) — a mismatch means a truncated/corrupted/partial cut, which is refused
	// rather than silently ingested. Mandatory for a real (non-synthetic) telco; a synthetic
	// telco may omit it. The full partner-SIGNED manifest remains gated on the MTN contract
	// (BX-HIGH-015).
	RowCount *int `json:"row_count"`
}

type rowShape struct {
	MSISDNToken         string   `json:"msisdn_token"`
	TenureDays          *int     `json:"tenure_days"`
	ActivityDays30d     *int     `json:"activity_days_30d"`
	ActiveDays90d       *int     `json:"active_days_90d"`
	WeeklyRechargeMinor []int64  `json:"weekly_recharge_minor"`
	Currency            string   `json:"currency"`
	QualityFlags        []string `json:"quality_flags"`
	// NINVerified (Build 2): MTN's identity-verification flag for this subscriber.
	// nil = feed did not carry it this cut (leave the stored value untouched). The
	// raw NIN is NEVER sent or stored — only this boolean. Upserted in place onto
	// subscriber_accounts (no history), NOT part of the scored feature vector.
	NINVerified *bool `json:"nin_verified"`
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
	// BX-HIGH-006: fetch WITH the telco's governed partner auth (fail-closed), not the
	// bare SSRF client — the feed carries the same authentication as fulfilment.
	resp, err := s.Fetcher.AuthenticatedGet(ctx, telcoID, url)
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
	// BX-HIGH-003: telco kill-switch — refuse a SUSPENDED (or otherwise non-ACTIVE) telco's
	// feed up front, before parsing or writing anything. This is the feed-ingestion boundary
	// of the same suspension gate enforced at channel + webhook auth. telcos is a global
	// non-RLS registry, so a plain SELECT (tcp_app has the grant) is correct here.
	telcoStatus, isSynthetic, err := (&repo.Telcos{Pool: s.Pool}).OperationalState(ctx, telcoID)
	if err != nil {
		return Summary{}, fmt.Errorf("telco lookup for %s: %w", telcoID, err)
	}
	if telcoStatus != "ACTIVE" {
		return Summary{}, fmt.Errorf("telco %s is %s (not ACTIVE) — feature-feed ingestion refused (BX-HIGH-003)", telcoID, telcoStatus)
	}
	var file fileShape
	if err := json.Unmarshal(raw, &file); err != nil {
		return Summary{}, fmt.Errorf("feature file does not parse: %w", err)
	}
	// BX-HIGH-006: feed-integrity control total. A declared row_count must match the actual
	// rows — a truncated/corrupted/partial cut is refused, never silently ingested. For a REAL
	// (non-synthetic) telco the control total is MANDATORY: a real MNO feed must be
	// integrity-checkable end to end. A synthetic telco may omit it. The full partner-SIGNED
	// manifest (cryptographic provenance) remains gated on the MTN interface contract (BX-HIGH-015).
	switch {
	case file.RowCount != nil && *file.RowCount != len(file.Rows):
		return Summary{}, fmt.Errorf("feature feed control total mismatch: declared row_count %d != %d actual rows — refusing a truncated/corrupted cut (BX-HIGH-006)", *file.RowCount, len(file.Rows))
	case file.RowCount == nil && !isSynthetic:
		return Summary{}, fmt.Errorf("a real telco's feature feed must declare a row_count control total — refusing an unverifiable cut (BX-HIGH-006)")
	}
	// BX-HIGH-006: the feed's self-declared telco_id MUST match the authenticated
	// telco we fetched it for. Without this, a feed (or a misrouted / compromised
	// endpoint) claiming another telco's id would land under the wrong tenant.
	if file.TelcoID != telcoID {
		return Summary{}, fmt.Errorf("feature file telco_id %q does not match the authenticated telco %q — refusing (BX-HIGH-006)", file.TelcoID, telcoID)
	}
	if file.AsOf.IsZero() {
		return Summary{}, fmt.Errorf("feature file has no as_of — refusing an undated data cut (V2-SCR-002)")
	}
	// BX-HIGH-007: refuse a FUTURE-dated cut. A future as_of would (a) poison the
	// nin_verified monotonic guard — a far-future "true" durably blocks later real
	// revocations — and (b) score as artificially FRESH (negative age). The tolerance
	// is governed config (telco.adapter.feature_as_of_max_future_skew_seconds); absent
	// = 0, so any future date is refused outright (zero-config safe floor).
	skew, err := s.featureAsOfMaxFutureSkew(ctx, telcoID)
	if err != nil {
		return Summary{}, err
	}
	if file.AsOf.After(time.Now().UTC().Add(skew)) {
		return Summary{}, fmt.Errorf("feature file as_of %s is dated in the future beyond the governed skew %s — refusing the cut (BX-HIGH-007)",
			file.AsOf.UTC().Format(time.RFC3339), skew)
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
				ninVerified       *bool // Build 2: identity flag, upserted in place (not a feature)
			}
			preps := make([]prepared, 0, len(chunk))
			revokeTokens := make([]string, 0) // BX-HIGH-008: revocations carried by quarantined rows
			for i, row := range chunk {
				if reason := validateRow(row, ceiling); reason != "" {
					sum.Quarantined++
					s.Log.Warn("feature row quarantined", "file", sum.FeatureFileID,
						"row", start+i, "token", platform.MaskToken(row.MSISDNToken), "reason", reason)
					// BX-HIGH-008: a REVOCATION (nin_verified=false) must land even when
					// the row's CREDIT features are quarantined — otherwise corrupting a
					// credit field silently suppresses the revocation and a stale "true"
					// keeps the subscriber lending-eligible. Verification (true) is the
					// dangerous direction and is NOT honoured from a quarantined row.
					if row.MSISDNToken != "" && row.NINVerified != nil && !*row.NINVerified {
						revokeTokens = append(revokeTokens, row.MSISDNToken)
					}
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
					features: features, quality: quality, hash: hex.EncodeToString(rowHash[:]),
					ninVerified: row.NINVerified})
			}
			// BX-HIGH-008: apply revocations from quarantined rows FIRST (by token,
			// existing subscribers only, under the monotonic as_of guard). This precedes
			// the clean-row early-out so a chunk of ONLY quarantined rows still revokes.
			if err := subs.BulkRevokeNINByToken(ctx, tx, telcoID, revokeTokens, file.AsOf); err != nil {
				return err
			}
			if len(preps) == 0 {
				continue
			}
			subIDs, err := subs.BulkEnsureByToken(ctx, tx, telcoID, tokens, newIDs)
			if err != nil {
				return err
			}
			batch := make([]entity.FeatureSnapshot, 0, len(preps))
			ninIDs := make([]string, 0, len(preps)) // Build 2: subscribers whose flag this cut carries
			ninFlags := make([]bool, 0, len(preps))
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
				if p.ninVerified != nil {
					ninIDs = append(ninIDs, subID)
					ninFlags = append(ninFlags, *p.ninVerified)
				}
			}
			written, err := snaps.BulkUpsert(ctx, tx, batch)
			if err != nil {
				return err
			}
			sum.Written += int(written)
			sum.Skipped += len(batch) - int(written)
			// Build 2: upsert MTN's nin_verified flag IN PLACE (no history) for the
			// subscribers this cut carried it for. Feature-quarantined rows are not in
			// preps, so their identity flag is left untouched (fail-closed: an
			// unverified subscriber cannot borrow until a clean cut sets the flag).
			if err := subs.BulkSetNINVerified(ctx, tx, ninIDs, ninFlags, file.AsOf); err != nil {
				return err
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

// featureAsOfMaxFutureSkew reads the governed clock-skew tolerance for a feature
// file's as_of (BX-HIGH-007) from telco.adapter config. Absent or negative → 0: a
// future-dated cut is then refused outright (zero-config safe floor). Permissive
// read (ignores other fields), unlike the strict config validator.
func (s *Service) featureAsOfMaxFutureSkew(ctx context.Context, telcoID string) (time.Duration, error) {
	cv, err := s.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("telco.adapter config: %w", err)
	}
	var ac struct {
		FeatureAsOfMaxFutureSkewSeconds int `json:"feature_as_of_max_future_skew_seconds"`
	}
	if err := json.Unmarshal(cv.Content, &ac); err != nil {
		return 0, err
	}
	if ac.FeatureAsOfMaxFutureSkewSeconds <= 0 {
		return 0, nil
	}
	return time.Duration(ac.FeatureAsOfMaxFutureSkewSeconds) * time.Second, nil
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
