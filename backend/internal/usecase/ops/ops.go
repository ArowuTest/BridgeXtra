// Package ops is the M3f operational surface: the breaks workflow
// (V2-REC-008..012 — breaks are resolved with reasons, never edited away),
// the complaints register (V1-CUS — resolution required to close), and the
// bureau export producer (V1-REG — a REAL pipeline whose sender is
// deliberately absent: batches stage with a reproducible file hash, and the
// schema's STAGED-only state makes transmission structurally impossible
// until licensing arms it).
package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Log    *slog.Logger

	breaks        repo.Breaks
	complaints    repo.Complaints
	bureau        repo.BureauBatches
	audit         repo.Audit
	attempts      repo.Attempts
	subscribers   repo.Subscribers
	statusActions repo.StatusActions
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log}
}

// --- M4e ambiguity queues ---------------------------------------------------

// QueuesConfig is the governed ops.queues threshold set. Zero-config floor
// (C3): an absent or non-positive threshold REFUSES the queue read — absent
// config is never "nothing is stale".
type QueuesConfig struct {
	StaleSentAfterSeconds int `json:"stale_sent_after_seconds"`
	MaxPageSize           int `json:"max_page_size"`
}

// QueuesConfig loads the active ops.queues thresholds (global scope).
func (s *Service) QueuesConfig(ctx context.Context) (QueuesConfig, error) {
	var qc QueuesConfig
	cv, err := s.Config.ActiveAt(ctx, "ops.queues", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return qc, fmt.Errorf("ops.queues config: %w", err)
	}
	if err := json.Unmarshal(cv.Content, &qc); err != nil {
		return qc, fmt.Errorf("ops.queues config: %w", err)
	}
	if qc.StaleSentAfterSeconds <= 0 || qc.MaxPageSize <= 0 {
		return qc, fmt.Errorf("ops.queues config invalid — refusing (absent threshold is not 'never stale')")
	}
	return qc, nil
}

// EnquireNow reschedules one ambiguous fulfilment attempt for immediate
// resolver enquiry, with an audit row. The repo predicate (state IN
// UNKNOWN|SENT) is the C2 guard; the portal never resolves attempt state.
func (s *Service) EnquireNow(ctx context.Context, telcoID, attemptID, actor string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.attempts.EnquireNow(ctx, tx, attemptID); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "fulfilment.enquire_now", TargetType: "fulfilment_attempt", TargetID: attemptID,
			Reason: "operator requested immediate enquiry",
		})
	})
}

// --- breaks workflow -------------------------------------------------------

// BreakAction applies one workflow action to a reconciliation break with an
// append-only action log entry and an audit row.
func (s *Service) BreakAction(ctx context.Context, telcoID, reconItemID, action, actor, reason string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.breaks.Action(ctx, tx, telcoID, reconItemID, action, actor, reason); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "break." + action, TargetType: "recon_item", TargetID: reconItemID,
			Reason: reason,
		})
	})
}

// AgedBreaks lists unresolved breaks older than the governed aging threshold
// (recon.tolerance.break_aging_alert_hours) — the operator alert feed.
func (s *Service) AgedBreaks(ctx context.Context, telcoID, programmeID string) ([]repo.AgedBreak, error) {
	cv, err := s.Config.ActiveAt(ctx, "recon.tolerance", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("recon.tolerance config: %w", err)
	}
	var tc struct {
		BreakAgingAlertHours int `json:"break_aging_alert_hours"`
	}
	if err := json.Unmarshal(cv.Content, &tc); err != nil {
		return nil, err
	}
	if tc.BreakAgingAlertHours <= 0 {
		return nil, fmt.Errorf("recon.tolerance has no break_aging_alert_hours — refusing (absent threshold is not 'never alert')")
	}
	var out []repo.AgedBreak
	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		out, e = s.breaks.ListAged(ctx, tx, time.Duration(tc.BreakAgingAlertHours)*time.Hour)
		return e
	})
	return out, err
}

// --- complaints ------------------------------------------------------------

// OpenComplaint registers a complaint (PII-lean: tokenised subscriber ref).
func (s *Service) OpenComplaint(ctx context.Context, telcoID, msisdnToken, advanceID, channel, category, narrative string) (repo.Complaint, error) {
	if channel == "" || category == "" || narrative == "" {
		return repo.Complaint{}, fmt.Errorf("channel, category and narrative are required")
	}
	c := repo.Complaint{
		ComplaintID: platform.NewID("cmp"), TelcoID: telcoID,
		AdvanceID: advanceID, Channel: channel, Category: category, Narrative: narrative,
	}
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if msisdnToken != "" {
			sub, err := (repo.Subscribers{}).GetLiveByToken(ctx, tx, msisdnToken)
			if err != nil {
				return err
			}
			c.SubscriberAccountID = sub.SubscriberAccountID
		}
		if err := s.complaints.Insert(ctx, tx, c); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: "channel:" + channel,
			Action: "complaint.opened", TargetType: "complaint", TargetID: c.ComplaintID,
			Reason: category,
		})
	})
	return c, err
}

// ProgressComplaint moves the lifecycle; RESOLVED/REJECTED require a
// resolution (the schema CHECK arbitrates).
func (s *Service) ProgressComplaint(ctx context.Context, telcoID, complaintID, from, to, actor, resolution string) error {
	if actor == "" {
		return fmt.Errorf("actor is required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.complaints.Transition(ctx, tx, complaintID, from, to, resolution); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "complaint." + to, TargetType: "complaint", TargetID: complaintID,
			Reason: resolution,
		})
	})
}

// --- bureau export (DORMANT producer) --------------------------------------

// bureauFile is the canonical staged document; its hash is the batch record.
type bureauFile struct {
	TelcoID     string           `json:"telco_id"`
	PeriodStart time.Time        `json:"period_start"`
	PeriodEnd   time.Time        `json:"period_end"`
	Rows        []repo.BureauRow `json:"rows"`
}

// ProduceBureauBatch stages one period's performance file: deterministic
// rows, canonical bytes, recorded hash. NOTHING transmits — the schema's
// STAGED-only CHECK is the structural dormancy (V1-REG until licensing).
func (s *Service) ProduceBureauBatch(ctx context.Context, telcoID string, periodStart, periodEnd time.Time) (repo.BureauBatch, error) {
	if !periodEnd.After(periodStart) {
		return repo.BureauBatch{}, fmt.Errorf("period_end must be after period_start")
	}
	// Truncate to Postgres timestamp precision (microseconds) BEFORE hashing:
	// the regeneration path reads these back from the database, and the
	// canonical bytes must be identical in both directions.
	batch := repo.BureauBatch{
		BatchID: platform.NewID("bur"), TelcoID: telcoID,
		PeriodStart: periodStart.UTC().Truncate(time.Microsecond),
		PeriodEnd:   periodEnd.UTC().Truncate(time.Microsecond),
	}
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := s.bureau.PerformanceRows(ctx, tx, batch.PeriodStart, batch.PeriodEnd)
		if err != nil {
			return err
		}
		file, err := canonicalBureauBytes(telcoID, batch.PeriodStart, batch.PeriodEnd, rows)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(file)
		batch.RowCount = len(rows)
		batch.FileHash = hex.EncodeToString(sum[:])
		return s.bureau.Insert(ctx, tx, batch)
	})
	if err == nil {
		s.Log.Info("bureau batch STAGED (dormant — no transmitter exists)",
			"batch", batch.BatchID, "rows", batch.RowCount)
	}
	return batch, err
}

// RegenerateBureauFile re-derives the staged file's canonical bytes; the
// caller compares the hash — the file is derivable, never merely asserted.
func (s *Service) RegenerateBureauFile(ctx context.Context, telcoID, batchID string) ([]byte, error) {
	var out []byte
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		b, err := s.bureau.Get(ctx, tx, batchID)
		if err != nil {
			return err
		}
		rows, err := s.bureau.PerformanceRows(ctx, tx, b.PeriodStart, b.PeriodEnd)
		if err != nil {
			return err
		}
		out, err = canonicalBureauBytes(telcoID, b.PeriodStart, b.PeriodEnd, rows)
		return err
	})
	return out, err
}

func canonicalBureauBytes(telcoID string, from, to time.Time, rows []repo.BureauRow) ([]byte, error) {
	return json.Marshal(bureauFile{TelcoID: telcoID, PeriodStart: from, PeriodEnd: to, Rows: rows})
}
