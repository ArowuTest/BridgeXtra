package recon

// R-P0-6 Slice E: the signed, reproducible evidence pack for one reconciliation
// run. The pack is a canonical statement — run header manifests, outcome counts,
// and every break item with its two-actor resolution — carrying a content hash
// that "signs" it: recompute the hash from the persisted run state and it must
// match, so a tampered pack (or a tampered item) is detectable. This is a
// deterministic self-verifying hash, NOT a private-key signature; cryptographic
// signing with a managed key is deferred to the KMS track (like the disclosure
// telco signature, DD-06) — the hash gives reproducibility + tamper-evidence
// without introducing a key surface.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// The count and the control total are DIFFERENT populations by design: the
// record count is over ALL telco records in the window, the source control total
// sums only credible SUCCESS amounts (a failed/void record counts toward the
// count and the hash but carries no money). Surfacing this on the pack so the
// two are never read as the same population (Slice-A forward note b).
const evidencePopulationNote = "source.record_count counts ALL telco records in the window; source.control_total_minor sums credible SUCCESS records only — different populations by design (a failed/void record is counted and hashed but carries no money)."

// evidenceManifest is one side's manifest in the pack.
type evidenceManifest struct {
	RecordCount       int64  `json:"record_count"`
	ControlTotalMinor int64  `json:"control_total_minor"`
	Hash              string `json:"hash,omitempty"`
}

// evidenceBreak is one break item and its two-actor resolution state (E1).
type evidenceBreak struct {
	ReconItemID          string `json:"recon_item_id"`
	MatchKey             string `json:"match_key,omitempty"`
	Status               string `json:"status"`
	ResolutionProposedBy string `json:"resolution_proposed_by,omitempty"`
	ResolvedBy           string `json:"resolved_by,omitempty"`
	Resolution           string `json:"resolution,omitempty"`
	ResolvedAt           string `json:"resolved_at,omitempty"`
}

// EvidencePack is the reproducible, self-verifying statement of one run.
type EvidencePack struct {
	RunID          string           `json:"run_id"`
	TelcoID        string           `json:"telco_id"`
	ProgrammeID    string           `json:"programme_id"`
	Layer          string           `json:"layer"`
	State          string           `json:"state"`
	PeriodStart    time.Time        `json:"period_start"`
	PeriodEnd      time.Time        `json:"period_end"`
	Source         evidenceManifest `json:"source"`
	Platform       evidenceManifest `json:"platform"`
	MatchedCount   int64            `json:"matched_count"`
	BreakCount     int64            `json:"break_count"`
	Breaks         []evidenceBreak  `json:"breaks"`
	PopulationNote string           `json:"population_note"`
	PackHash       string           `json:"pack_hash"`
}

// packHash is the deterministic sha256 over the pack with PackHash zeroed — the
// self-verifying signature. Field order is fixed by the struct and Breaks are
// ordered by recon_item_id, so the encoding is stable and reproducible.
func packHash(p EvidencePack) (string, error) {
	p.PackHash = ""
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// EvidencePack builds the reproducible evidence pack for a run from its persisted
// state (header + break items + resolutions). Calling it again on the same state
// yields an identical PackHash; any change to the run's items or resolutions
// changes the hash.
func (s *Service) EvidencePack(ctx context.Context, telcoID, runID string) (EvidencePack, error) {
	p := EvidencePack{RunID: runID, TelcoID: telcoID, PopulationNote: evidencePopulationNote}
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT programme_id, layer, state, period_start, period_end,
			       source_record_count, source_control_total_minor, source_hash,
			       platform_record_count, platform_control_total_minor,
			       matched_count, break_count
			FROM recon_runs WHERE run_id=$1`, runID).Scan(
			&p.ProgrammeID, &p.Layer, &p.State, &p.PeriodStart, &p.PeriodEnd,
			&p.Source.RecordCount, &p.Source.ControlTotalMinor, &p.Source.Hash,
			&p.Platform.RecordCount, &p.Platform.ControlTotalMinor,
			&p.MatchedCount, &p.BreakCount); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("recon run %q not found: %w", runID, repo.ErrNotFound)
			}
			return err
		}
		// Every break item and its two-actor resolution, ordered for determinism.
		rows, err := tx.Query(ctx, `
			SELECT recon_item_id, COALESCE(match_key,''), status,
			       COALESCE(resolution_proposed_by,''), COALESCE(resolved_by,''),
			       COALESCE(resolution,''),
			       COALESCE(to_char(resolved_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),'')
			FROM recon_items
			WHERE run_id=$1 AND status LIKE 'BREAK_%'
			ORDER BY recon_item_id`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b evidenceBreak
			if err := rows.Scan(&b.ReconItemID, &b.MatchKey, &b.Status,
				&b.ResolutionProposedBy, &b.ResolvedBy, &b.Resolution, &b.ResolvedAt); err != nil {
				return err
			}
			p.Breaks = append(p.Breaks, b)
		}
		return rows.Err()
	})
	if err != nil {
		return EvidencePack{}, err
	}
	h, err := packHash(p)
	if err != nil {
		return EvidencePack{}, err
	}
	p.PackHash = h
	return p, nil
}

// VerifyEvidencePack recomputes the pack for the run and confirms the supplied
// hash matches — the self-verification an auditor runs against an archived pack.
func (s *Service) VerifyEvidencePack(ctx context.Context, telcoID, runID, expectHash string) (bool, error) {
	p, err := s.EvidencePack(ctx, telcoID, runID)
	if err != nil {
		return false, err
	}
	return p.PackHash == expectHash, nil
}
