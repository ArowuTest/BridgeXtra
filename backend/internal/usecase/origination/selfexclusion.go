package origination

// Self-exclusion (R1-MUST, pre-pilot): a responsible-lending control letting a
// subscriber opt OUT of being offered/receiving credit. The self_exclusions
// register owns the lifecycle and the governed cool-off; the subscriber status
// mirror keeps the existing eligibility gate working. Enforcement is
// register-authoritative (assertNotSelfExcluded), so the control never depends
// on the mirror being in sync.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

// SelfExclusionResult reports the outcome of a self-exclusion request.
type SelfExclusionResult struct {
	ExclusionID     string
	MinUntil        time.Time // earliest reinstatement (cool-off end)
	AlreadyExcluded bool      // idempotent: a live exclusion already existed
}

type selfExclusionCfg struct {
	MinExclusionDays int      `json:"min_exclusion_days"`
	AllowedChannels  []string `json:"allowed_channels"`
	// RequireOperatorReinstatement (default false): when true, a subscriber may
	// NOT reinstate themselves — an operator must lift the exclusion. Kept a
	// governed toggle (no hardcoding) so a jurisdiction that requires operator
	// sign-off is a config flip, not a rebuild. Live and fail-closed: while true,
	// self-service reinstatement is refused (the operator maker-checker path is a
	// future build gated by this flag).
	RequireOperatorReinstatement bool `json:"require_operator_reinstatement"`
}

// loadSelfExclusionCfg reads the governed terms, fail-closed: without a positive
// cool-off the feature refuses rather than accepting an instantly-reversible
// (i.e. worthless) self-exclusion.
func (s *Service) loadSelfExclusionCfg(ctx context.Context, programmeID string) (selfExclusionCfg, error) {
	cv, err := s.Config.ActiveAt(ctx, "origination.self_exclusion", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return selfExclusionCfg{}, fmt.Errorf("origination.self_exclusion config: %w", err)
	}
	var c selfExclusionCfg
	if err := json.Unmarshal(cv.Content, &c); err != nil {
		return selfExclusionCfg{}, err
	}
	if c.MinExclusionDays < 1 {
		return selfExclusionCfg{}, fmt.Errorf("self-exclusion cool-off (min_exclusion_days) not configured — refusing (an instantly-reversible exclusion is not a control)")
	}
	if len(c.AllowedChannels) == 0 {
		return selfExclusionCfg{}, fmt.Errorf("self-exclusion allowed_channels not configured — refusing")
	}
	return c, nil
}

func channelAllowed(allowed []string, channel string) bool {
	for _, a := range allowed {
		if a == channel {
			return true
		}
	}
	return false
}

// assertNotSelfExcluded refuses a subscriber with an ACTIVE self-exclusion. This
// is the authoritative enforcement read used in the offer and confirm gates.
func (s *Service) assertNotSelfExcluded(ctx context.Context, tx pgx.Tx, subscriberAccountID string) error {
	if _, ok, err := s.selfExclusions.GetActiveBySubscriber(ctx, tx, subscriberAccountID); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: self-excluded", ErrSubscriberIneligible)
	}
	return nil
}

// RequestSelfExclusion records a subscriber's opt-out. Idempotent: a subscriber
// already excluded gets their existing exclusion back (never a duplicate). The
// register row and the status mirror are written in one tx.
func (s *Service) RequestSelfExclusion(ctx context.Context, programmeID, msisdnToken, channel, reason string) (SelfExclusionResult, error) {
	cfg, err := s.loadSelfExclusionCfg(ctx, programmeID)
	if err != nil {
		return SelfExclusionResult{}, err
	}
	if !channelAllowed(cfg.AllowedChannels, channel) {
		return SelfExclusionResult{}, fmt.Errorf("%w: %q", ErrSelfExclusionChannelNotAllowed, channel)
	}
	minUntil := time.Now().UTC().AddDate(0, 0, cfg.MinExclusionDays)

	var res SelfExclusionResult
	err = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, msisdnToken)
		if err != nil {
			return err
		}
		if ex, ok, err := s.selfExclusions.GetActiveBySubscriber(ctx, tx, sub.SubscriberAccountID); err != nil {
			return err
		} else if ok {
			res = SelfExclusionResult{ExclusionID: ex.ExclusionID, MinUntil: ex.MinUntil, AlreadyExcluded: true}
			return nil
		}
		exID := platform.NewID("sxc")
		if err := s.selfExclusions.Insert(ctx, tx, repo.SelfExclusion{
			ExclusionID: exID, TelcoID: sub.TelcoID, SubscriberAccountID: sub.SubscriberAccountID,
			Channel: channel, Reason: reason, MinUntil: minUntil,
		}); err != nil {
			return err
		}
		// Mirror the enforcement status. Only an ACTIVE subscriber can self-exclude
		// (a BARRED/CLOSED account is already ineligible); the from-guard enforces it.
		if err := s.subscribers.SetStatus(ctx, tx, sub.SubscriberAccountID, "ACTIVE", "SELF_EXCLUDED"); err != nil {
			return fmt.Errorf("cannot self-exclude a non-active subscriber: %w", err)
		}
		res = SelfExclusionResult{ExclusionID: exID, MinUntil: minUntil}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: sub.TelcoID, Actor: "channel:" + channel,
			Action: "subscriber.self_excluded", TargetType: "subscriber_account", TargetID: sub.SubscriberAccountID,
			Reason: reason,
		})
	})
	if err != nil {
		return SelfExclusionResult{}, err
	}
	return res, nil
}

// ReinstateSelfExclusion lifts a self-exclusion — but never before the governed
// cool-off has elapsed. The cool-off is enforced both here (for a clear error)
// and structurally in MarkReinstated (min_until <= now in the UPDATE), so a race
// cannot slip a reinstatement through early.
func (s *Service) ReinstateSelfExclusion(ctx context.Context, programmeID, msisdnToken, channel string) error {
	cfg, err := s.loadSelfExclusionCfg(ctx, programmeID)
	if err != nil {
		return err
	}
	// Governed policy: if operator reinstatement is required, self-service is
	// refused (fail-closed). The operator maker-checker path is a future build.
	if cfg.RequireOperatorReinstatement {
		return ErrOperatorReinstatementRequired
	}
	return repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, msisdnToken)
		if err != nil {
			return err
		}
		ex, ok, err := s.selfExclusions.GetActiveBySubscriber(ctx, tx, sub.SubscriberAccountID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotSelfExcluded
		}
		if time.Now().UTC().Before(ex.MinUntil) {
			return fmt.Errorf("%w (not before %s)", ErrCoolOffNotElapsed, ex.MinUntil.Format(time.RFC3339))
		}
		done, err := s.selfExclusions.MarkReinstated(ctx, tx, ex.ExclusionID, channel)
		if err != nil {
			return err
		}
		if !done {
			return ErrCoolOffNotElapsed // structural cool-off guard
		}
		if err := s.subscribers.SetStatus(ctx, tx, sub.SubscriberAccountID, "SELF_EXCLUDED", "ACTIVE"); err != nil {
			return err
		}
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: sub.TelcoID, Actor: "channel:" + channel,
			Action: "subscriber.self_exclusion_reinstated", TargetType: "subscriber_account", TargetID: sub.SubscriberAccountID,
			Reason: "cool-off elapsed; subscriber reinstated",
		})
	})
}
