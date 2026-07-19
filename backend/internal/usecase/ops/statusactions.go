package ops

// M4e-2: the privileged subscriber-status action (VR-35-F1 closure). A
// maker-checker journey — REQUEST records the intended transition against the
// subscriber's CURRENT status; a DISTINCT second actor approves (apply) or
// rejects. The apply is compare-and-set on that recorded from_status (C2): a
// subscriber whose status drifted between request and approval refuses
// loudly. The transition set is governed config with a structural conduct
// floor: the validator AND the schema both refuse SELF_EXCLUDED — an
// operator can never set or override the customer's own exclusion (EDG-030).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// ErrTransitionNotAllowed: the governed ops.status_actions set does not
// permit this from->to (or the config is absent — C3 floor: refuse).
var ErrTransitionNotAllowed = errors.New("status transition not allowed by governed config")

// ErrSameActor: maker-checker refusal before the schema CHECK even fires.
var ErrSameActor = errors.New("a status action cannot be decided by its requester")

type statusActionsCfg struct {
	AllowedTransitions []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"allowed_transitions"`
}

// transitionAllowed loads the governed set and answers for one transition.
// C3 zero-config floor: absent/invalid config refuses EVERY transition.
func (s *Service) transitionAllowed(ctx context.Context, from, to string) error {
	cv, err := s.Config.ActiveAt(ctx, "ops.status_actions", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("ops.status_actions config: %w (refusing — absent config is not 'allow'): %w", err, ErrTransitionNotAllowed)
	}
	var cfg statusActionsCfg
	if err := json.Unmarshal(cv.Content, &cfg); err != nil {
		return fmt.Errorf("ops.status_actions config: %w: %w", err, ErrTransitionNotAllowed)
	}
	for _, tr := range cfg.AllowedTransitions {
		if tr.From == from && tr.To == to {
			return nil
		}
	}
	return fmt.Errorf("%s -> %s: %w", from, to, ErrTransitionNotAllowed)
}

// RequestStatusAction opens a maker-checker status action against the
// subscriber's LIVE identity, recording the current status as from_status.
func (s *Service) RequestStatusAction(ctx context.Context, telcoID, msisdnToken, toStatus, reason, actor string) (repo.StatusAction, error) {
	var a repo.StatusAction
	if reason == "" {
		return a, fmt.Errorf("reason is required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, msisdnToken)
		if err != nil {
			return err
		}
		if err := s.transitionAllowed(ctx, sub.Status, toStatus); err != nil {
			return err
		}
		a = repo.StatusAction{
			ActionID:            platform.NewID("ssa"),
			TelcoID:             telcoID,
			SubscriberAccountID: sub.SubscriberAccountID,
			FromStatus:          sub.Status,
			ToStatus:            toStatus,
			Reason:              reason,
			RequestedBy:         actor,
			State:               "REQUESTED",
		}
		if err := s.statusActions.Insert(ctx, tx, a); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "subscriber_status.request", TargetType: "subscriber_status_action", TargetID: a.ActionID,
			Reason: fmt.Sprintf("%s -> %s: %s", sub.Status, toStatus, reason),
		})
	})
	return a, err
}

// DecideStatusAction approves (applies) or rejects one open action as a
// distinct second actor. Approval re-checks the governed transition set at
// decision time and applies via compare-and-set: drifted status -> loud
// refusal, action stays open for re-evaluation.
func (s *Service) DecideStatusAction(ctx context.Context, telcoID, actionID, actor string, approve bool) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		a, err := s.statusActions.ClaimRequestedByID(ctx, tx, actionID)
		if err != nil {
			return err
		}
		if a.RequestedBy == actor {
			return fmt.Errorf("action %s: %w", actionID, ErrSameActor)
		}
		if !approve {
			if err := s.statusActions.Decide(ctx, tx, actionID, actor, "REJECTED"); err != nil {
				return err
			}
			return s.audit.Insert(ctx, tx, entity.AuditEvent{
				ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
				Action: "subscriber_status.reject", TargetType: "subscriber_status_action", TargetID: actionID,
				Reason: fmt.Sprintf("%s -> %s rejected", a.FromStatus, a.ToStatus),
			})
		}
		// The config may have tightened since the request — re-check (C3).
		if err := s.transitionAllowed(ctx, a.FromStatus, a.ToStatus); err != nil {
			return err
		}
		// C2 compare-and-set: only the recorded from_status may be replaced.
		if err := s.subscribers.SetStatusCAS(ctx, tx, a.SubscriberAccountID, a.FromStatus, a.ToStatus); err != nil {
			return err
		}
		if err := s.statusActions.Decide(ctx, tx, actionID, actor, "APPLIED"); err != nil {
			return err
		}
		s.Log.Info("subscriber status action applied (M4e-2)",
			"action", actionID, "subscriber", a.SubscriberAccountID,
			"from", a.FromStatus, "to", a.ToStatus, "requested_by", a.RequestedBy, "approved_by", actor)
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor,
			Action: "subscriber_status.apply", TargetType: "subscriber_status_action", TargetID: actionID,
			Reason: fmt.Sprintf("%s -> %s applied", a.FromStatus, a.ToStatus),
		})
	})
}
