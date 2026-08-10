package recon

// Phase 1 S3-C3 — the FOUR-EYES arming path for a telco's RECOVERY recon layer.
// This is the ONLY production caller of SetLive: arming the recon layer is what
// makes the recharge webhook's IsLayerLive gate satisfiable (build/PHASE1_S3_DESIGN.md
// §S3-C). Opening the money door needs a maker proposal + a DISTINCT checker
// approval; disarming (closing the door) is single-actor, the fail-safe direction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

var (
	ErrArmTelcoNotActive = errors.New("recon: telco is not ACTIVE — cannot arm")
	ErrArmFeedMissing    = errors.New("recon: telco.recovery_feed is not configured for this telco — cannot arm")
	ErrArmMockOnReal     = errors.New("recon: a mock recovery feed may not arm a non-synthetic telco")
	ErrArmBasisRequired  = errors.New("recon: arm requires confirmed_feed_reversal_basis (GROSS|NET_SAME_DAY) and confirmed_business_date_basis")
	ErrArmAlreadyPending = errors.New("recon: an arm request is already PENDING for this telco/layer")
	ErrArmSelfApprove    = errors.New("recon: an arm request checker must differ from its proposer (two-person rule)")
	ErrArmNotPending     = errors.New("recon: arm request is not PENDING")
)

// ArmProposal is a maker's request to arm a telco's RECOVERY recon layer.
type ArmProposal struct {
	TelcoID                    string
	ProposedBy                 string
	Reason                     string
	ConfirmedFeedReversalBasis string // GROSS | NET_SAME_DAY
	ConfirmedBusinessDateBasis string
}

// ProposeArmRecovery is the MAKER step. Preconditions, all fail-closed: the telco
// is ACTIVE; telco.recovery_feed is configured for it; a mock feed is permitted
// ONLY on a synthetic telco (telcos.is_synthetic) — the circularity guard; and the
// human maker supplied the confirmed feed bases (no safe default — ARM cannot
// proceed without an explicit reversal + business-date basis). Captures the
// governed arm_freshness_max_seconds. Returns the request id for the checker.
func (s *Service) ProposeArmRecovery(ctx context.Context, p ArmProposal) (string, error) {
	if p.Reason == "" {
		return "", fmt.Errorf("arm proposal requires a reason")
	}
	if p.ConfirmedFeedReversalBasis != "GROSS" && p.ConfirmedFeedReversalBasis != "NET_SAME_DAY" {
		return "", ErrArmBasisRequired
	}
	if p.ConfirmedBusinessDateBasis == "" {
		return "", ErrArmBasisRequired
	}
	var status string
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM telcos WHERE telco_id=$1`, p.TelcoID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("telco %q not found", p.TelcoID)
		}
		return "", err
	}
	if status != "ACTIVE" {
		return "", ErrArmTelcoNotActive
	}
	// telco.recovery_feed must be configured; a mock source needs a synthetic telco.
	cv, err := s.Config.ActiveAt(ctx, "telco.recovery_feed", "telco:"+p.TelcoID, time.Now().UTC())
	if err != nil {
		return "", ErrArmFeedMissing
	}
	var fc struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(cv.Content, &fc); err != nil {
		return "", err
	}
	if fc.Source == "mock" {
		var synthetic bool
		if err := s.Pool.QueryRow(ctx, `SELECT is_synthetic FROM telcos WHERE telco_id=$1`, p.TelcoID).Scan(&synthetic); err != nil {
			return "", err
		}
		if !synthetic {
			return "", ErrArmMockOnReal
		}
	}
	// The governed freshness window written onto the arming row at approve.
	rcv, err := s.Config.ActiveAt(ctx, "recon.recovery", "telco:"+p.TelcoID, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("recon.recovery config: %w", err)
	}
	var rc struct {
		ArmFreshnessMaxSeconds int `json:"arm_freshness_max_seconds"`
	}
	if err := json.Unmarshal(rcv.Content, &rc); err != nil {
		return "", err
	}
	if rc.ArmFreshnessMaxSeconds < 3600 || rc.ArmFreshnessMaxSeconds > 604_800 {
		return "", fmt.Errorf("recon.recovery arm_freshness_max_seconds out of range — refusing")
	}
	requestID := platform.NewID("rar")
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO recon_layer_arming_requests
		  (request_id, telco_id, layer, proposed_by, reason, freshness_max_seconds,
		   confirmed_feed_reversal_basis, confirmed_business_date_basis)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		requestID, p.TelcoID, repo.ReconLayerRecovery, p.ProposedBy, p.Reason, rc.ArmFreshnessMaxSeconds,
		p.ConfirmedFeedReversalBasis, p.ConfirmedBusinessDateBasis); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // one-pending unique index
			return "", ErrArmAlreadyPending
		}
		return "", err
	}
	s.Log.Info("recovery arm proposed (maker)", "request", requestID, "telco", p.TelcoID, "by", p.ProposedBy)
	return requestID, nil
}

// ApproveArmRecovery is the CHECKER step: a DISTINCT actor approves; the arming row
// is written (SetLiveArmed) in the SAME tx as the state flip — atomic. The schema
// two-actor CHECK arbitrates self-approval (-> ErrArmSelfApprove). last_recon_at
// stays NULL, so the layer is armed but NOT live until its first confirmed recon.
func (s *Service) ApproveArmRecovery(ctx context.Context, requestID, approvedBy string) error {
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var telcoID, layer string
		var freshness int
		err := tx.QueryRow(ctx, `
			UPDATE recon_layer_arming_requests
			SET state='APPROVED', approved_by=$2, decided_at=now()
			WHERE request_id=$1 AND state='PENDING'
			RETURNING telco_id, layer, freshness_max_seconds`, requestID, approvedBy).Scan(&telcoID, &layer, &freshness)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" { // two-actor CHECK
				return ErrArmSelfApprove
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrArmNotPending
			}
			return err
		}
		if err := s.Arming.SetLiveArmed(ctx, tx, telcoID, layer, freshness); err != nil {
			return err
		}
		s.Log.Info("recovery arm approved (checker) — live on next confirmed recon",
			"request", requestID, "telco", telcoID, "by", approvedBy)
		return nil
	})
}

// DisarmRecovery immediately disarms a telco's RECOVERY layer. Single-actor —
// closing a money door is the fail-safe direction. SetDown deletes the arming row,
// which closes the webhook on the next request.
func (s *Service) DisarmRecovery(ctx context.Context, telcoID, actor, reason string) error {
	// BX-MED-004: a manual money-door control must record WHO and WHY, durably. Disarming a telco's
	// RECOVERY layer turns off a live money-safety pipeline; the arm path persists
	// proposer/approver/reason to recon_layer_arming_requests, but disarm previously only logged to
	// stdout and DELETE-and-forgot — no auditable trail. Require a reason and write a durable
	// audit_events row ATOMICALLY with the disarm, so the record and reality never diverge.
	if actor == "" || reason == "" {
		return fmt.Errorf("recovery disarm requires an actor and a reason (BX-MED-004)")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.Arming.SetDownTx(ctx, tx, telcoID, repo.ReconLayerRecovery); err != nil {
		return err
	}
	if err := (repo.Audit{}).Insert(ctx, tx, entity.AuditEvent{
		ID:         platform.NewID("aud"),
		TelcoID:    telcoID,
		Actor:      actor,
		Action:     "recon.recovery.disarmed",
		TargetType: "recon_layer",
		TargetID:   "recovery:" + telcoID,
		Reason:     reason,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.Log.Info("recovery layer DISARMED (single-actor)", "telco", telcoID, "by", actor, "reason", reason)
	return nil
}
