// Package fulfilmentresolver resolves ambiguous fulfilments (V2-ADV-009,
// EDG-005/007/008): claims due UNKNOWN attempts — and stale SENT attempts
// (the crash-between-tx1-and-tx2 window) — enquires the telco's definitive
// status, and applies the outcome through origination.ResolveOutcome, the
// EXACT function the saga uses, so the two paths can never drift.
//
// Enquiries are read-only and safely repeatable; still-unknown cycles
// reschedule quietly on the config backoff (VR-7b: no event flood) and
// escalate to the operator log past the configured threshold.
package fulfilmentresolver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

type Service struct {
	Pool        *pgxpool.Pool // tcp_app (RLS enforced per-advance tenant ctx)
	Config      *configsvc.Service
	Adapter     mno.Client
	Origination *origination.Service
	Log         *slog.Logger

	attempts repo.Attempts
	advances repo.Advances
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, adapter mno.Client, orig *origination.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Adapter: adapter, Origination: orig, Log: log}
}

type fulfilmentCfg struct {
	StatusEnquiryDelaysSeconds []int `json:"status_enquiry_delays_seconds"`
	UnknownEscalationMinutes   int   `json:"unknown_escalation_minutes"`
}

type due struct {
	attempt entity.FulfilmentAttempt
	telcoID string
}

// RunOnce claims and resolves one batch of due attempts. Returns how many
// reached a definitive outcome.
func (s *Service) RunOnce(ctx context.Context, telcoID string, limit int) (resolved int, err error) {
	cfg, err := s.telcoCfg(ctx, telcoID)
	if err != nil {
		return 0, err
	}
	staleSentBefore := time.Now().UTC().Add(-time.Duration(cfg.StatusEnquiryDelaysSeconds[0]) * time.Second)

	// Claim under the tenant context (short tx: claim + read advance refs;
	// enquiry happens OUTSIDE the transaction, same discipline as the saga).
	tctx := platform.WithTenant(ctx, telcoID)
	var claims []due
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		attempts, err := s.attempts.DueEnquiries(ctx, tx, time.Now().UTC(), staleSentBefore, limit)
		if err != nil {
			return err
		}
		for _, at := range attempts {
			claims = append(claims, due{attempt: at, telcoID: telcoID})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	for _, c := range claims {
		ok, rerr := s.resolveOne(tctx, c, cfg)
		if rerr != nil {
			s.Log.Error("resolver: attempt resolution failed", "attempt", c.attempt.AttemptID, "err", rerr)
			continue
		}
		if ok {
			resolved++
		}
	}
	return resolved, nil
}

func (s *Service) resolveOne(tctx context.Context, c due, cfg fulfilmentCfg) (definitive bool, err error) {
	// Enquiry OUTSIDE any transaction (read-only, safely repeatable).
	res, err := s.Adapter.EnquireStatus(tctx, c.telcoID, c.attempt.AdvanceID)
	if err != nil {
		return false, err
	}

	switch res.Outcome {
	case mno.OutcomeConfirmed, mno.OutcomeFailed, mno.OutcomeNotFound:
		// Definitive: apply through the SAME code path as the saga.
		// NotFound = the instruction provably never landed (EDG-008) —
		// ResolveOutcome maps it to FULFILMENT_FAILED + release.
		if _, err := s.Origination.ResolveOutcome(tctx, c.attempt.AdvanceID, c.attempt.AttemptID, res); err != nil {
			return false, err
		}
		s.Log.Info("resolver: ambiguity resolved", "attempt", c.attempt.AttemptID,
			"advance", c.attempt.AdvanceID, "outcome", string(res.Outcome))
		return true, nil

	case mno.OutcomeUnknown:
		// Still unknown: reschedule on the config backoff, quietly (VR-7b).
		return false, repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
			delays := cfg.StatusEnquiryDelaysSeconds
			idx := c.attempt.EnquiryCount + 1
			if idx >= len(delays) {
				idx = len(delays) - 1 // stay on the longest backoff step
			}
			next := time.Now().UTC().Add(time.Duration(delays[idx]) * time.Second)
			if err := s.attempts.RescheduleEnquiry(tctx, tx, c.attempt.AttemptID, next); err != nil {
				return err
			}
			// Operator escalation past the threshold (V2-ADV-005/V3-AFO-004):
			// loud log now; the ops queue view lands with M4 portals.
			age := time.Since(c.attempt.SubmittedAt)
			if age > time.Duration(cfg.UnknownEscalationMinutes)*time.Minute {
				s.Log.Error("resolver: FULFILMENT_UNKNOWN past escalation threshold — operator attention required",
					"attempt", c.attempt.AttemptID, "advance", c.attempt.AdvanceID,
					"age", age.String(), "enquiries", c.attempt.EnquiryCount+1)
			}
			return nil
		})

	default:
		return false, fmt.Errorf("unrecognised enquiry outcome %q", res.Outcome)
	}
}

func (s *Service) telcoCfg(ctx context.Context, telcoID string) (fulfilmentCfg, error) {
	cv, err := s.Config.ActiveAt(ctx, "advance.fulfilment", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return fulfilmentCfg{}, fmt.Errorf("advance.fulfilment config: %w", err)
	}
	var fc fulfilmentCfg
	if err := json.Unmarshal(cv.Content, &fc); err != nil {
		return fulfilmentCfg{}, err
	}
	return fc, nil
}
