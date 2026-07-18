// Package collections is the M3c delinquency + write-off engine (V2 §15).
//
//   - Classification is an OVERLAY job: buckets come from the governed aging
//     ladder, are stamped set-based (owner scale rule), and never touch the
//     FSM — delinquency describes an advance, it does not move it (SRS D-2).
//   - Write-off is the ONLY path that crystallises a loss: maker-checker at
//     the schema, eligibility gated by the configured minimum bucket, and
//     the ledger movement (WRITE_OFF_EXPENSE against the receivable) posts
//     in the same transaction as the state change — books and book agree or
//     nothing happens.
package collections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// Typed errors (BC-7).
var (
	ErrNotEligible   = errors.New("collections: advance not eligible for write-off (bucket below policy minimum)")
	ErrNotWritable   = errors.New("collections: advance not in a write-off-able state")
	ErrSelfApproval  = repo.ErrSelfApproval
	ErrAlreadyExists = repo.ErrWriteOffExists
)

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Ledger *ledger.Service
	Log    *slog.Logger

	advances  repo.Advances
	writeoffs repo.WriteOffs
	pools     repo.FundingPools
	audit     repo.Audit
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, led *ledger.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Ledger: led, Log: log}
}

type bucketsCfg struct {
	Buckets []struct {
		Code           string `json:"code"`
		MinDaysPastDue int    `json:"min_days_past_due"`
	} `json:"buckets"`
	GraceDays int `json:"grace_days"`
}

type writeoffCfg struct {
	MinBucket string `json:"min_bucket"`
}

// Classify stamps delinquency buckets for every open advance in the
// programme from the governed ladder. Returns the number of advances whose
// bucket CHANGED.
func (s *Service) Classify(ctx context.Context, telcoID, programmeID string) (int64, error) {
	cv, err := s.Config.ActiveAt(ctx, "delinquency.buckets", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("delinquency.buckets config: %w", err)
	}
	var bc bucketsCfg
	if err := json.Unmarshal(cv.Content, &bc); err != nil {
		return 0, err
	}
	ladder := make([]repo.DelinquencyBucket, len(bc.Buckets))
	for i, b := range bc.Buckets {
		ladder[i] = repo.DelinquencyBucket{Code: b.Code, MinDaysPastDue: b.MinDaysPastDue}
	}

	var changed int64
	tctx := platform.WithTenant(ctx, telcoID)
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		changed, e = s.advances.ClassifyDelinquency(ctx, tx, programmeID, ladder, bc.GraceDays, time.Now().UTC())
		return e
	})
	if err != nil {
		return 0, err
	}
	if changed > 0 {
		s.Log.Info("delinquency buckets updated", "programme", programmeID, "changed", changed)
	}
	return changed, nil
}

// RequestWriteOff opens a maker-checker write-off for an advance whose
// bucket has reached the policy minimum. The request crystallises NOTHING —
// only approval does.
func (s *Service) RequestWriteOff(ctx context.Context, telcoID, advanceID, actor, reason string) (repo.WriteOff, error) {
	if actor == "" || reason == "" {
		return repo.WriteOff{}, fmt.Errorf("actor and reason are required")
	}
	var out repo.WriteOff
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		adv, err := s.advances.Get(ctx, tx, advanceID)
		if err != nil {
			return err
		}
		if adv.State != entity.AdvActive && adv.State != entity.AdvPartiallyRecovered {
			return fmt.Errorf("%w: state %s", ErrNotWritable, adv.State)
		}
		if err := s.checkBucketEligibility(ctx, tx, adv); err != nil {
			return err
		}

		// Component split of the remaining obligation: fee remainder first
		// (mirrors the recovery waterfall's component totals), principal is
		// the rest — the write-off journal itemises what was lost.
		feeOutstanding, prinOutstanding, err := s.outstandingSplit(ctx, tx, adv)
		if err != nil {
			return err
		}
		out = repo.WriteOff{
			WriteOffID: platform.NewID("wof"), TelcoID: telcoID, AdvanceID: advanceID,
			Principal: prinOutstanding, Fee: feeOutstanding,
			Reason: reason, RequestedBy: actor, State: "REQUESTED",
		}
		if err := s.writeoffs.Insert(ctx, tx, out); err != nil {
			return err
		}
		return s.auditRow(ctx, tx, telcoID, actor, "writeoff.requested", advanceID, reason)
	})
	return out, err
}

// ApproveWriteOff is the checker action: distinct approver (schema-enforced),
// and in the SAME transaction the loss crystallises — advance to
// WRITTEN_OFF, outstanding to zero, pool utilisation released, balanced
// WRITE_OFF journal posted, evidence stamped POSTED.
func (s *Service) ApproveWriteOff(ctx context.Context, telcoID, writeOffID, approver, correlationID string) error {
	if approver == "" || correlationID == "" {
		return fmt.Errorf("approver and correlation id are required")
	}
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.writeoffs.Decide(ctx, tx, writeOffID, approver, "APPROVED"); err != nil {
			return err
		}
		wo, err := s.writeoffs.Get(ctx, tx, writeOffID)
		if err != nil {
			return err
		}
		adv, err := s.advances.Get(ctx, tx, wo.AdvanceID)
		if err != nil {
			return err
		}
		if adv.State != entity.AdvActive && adv.State != entity.AdvPartiallyRecovered {
			return fmt.Errorf("%w: state %s changed since request", ErrNotWritable, adv.State)
		}

		// Loss crystallises: state + outstanding zero under the optimistic
		// guard, pool stops funding the dead receivable.
		zero, err := entity.ZeroMoney(adv.Outstanding.Currency())
		if err != nil {
			return err
		}
		if err := s.advances.ApplyRecovery(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvWrittenOff, zero); err != nil {
			return err
		}
		if err := s.pools.ReduceUtilisation(ctx, tx, adv.FundingPoolID, adv.Outstanding); err != nil {
			return err
		}

		// Balanced movement: the receivable dies, the loss is recognised
		// (template-rendered, CFG-012).
		if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
			BusinessEventKey: wo.WriteOffID + "/posted",
			EventType:        ledger.EventWriteOff,
			TelcoID:          telcoID,
			ProgrammeID:      adv.ProgrammeID,
			AdvanceID:        adv.AdvanceID,
			CorrelationID:    correlationID,
		}, ledger.Bindings{ledger.SymAmount: adv.Outstanding}); err != nil {
			return err
		}
		if err := s.writeoffs.MarkPosted(ctx, tx, writeOffID); err != nil {
			return err
		}
		s.Log.Warn("loss crystallised (maker-checker write-off)",
			"advance", adv.AdvanceID, "amount", adv.Outstanding.String(),
			"requested_by", wo.RequestedBy, "approved_by", approver)
		return s.auditRow(ctx, tx, telcoID, approver, "writeoff.approved", wo.AdvanceID, wo.Reason)
	})
}

// RejectWriteOff is the checker's refusal (distinct actor still enforced —
// a rejection is a decision too).
func (s *Service) RejectWriteOff(ctx context.Context, telcoID, writeOffID, approver, reason string) error {
	tctx := platform.WithTenant(ctx, telcoID)
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.writeoffs.Decide(ctx, tx, writeOffID, approver, "REJECTED"); err != nil {
			return err
		}
		wo, err := s.writeoffs.Get(ctx, tx, writeOffID)
		if err != nil {
			return err
		}
		return s.auditRow(ctx, tx, telcoID, approver, "writeoff.rejected", wo.AdvanceID, reason)
	})
}

// checkBucketEligibility: the advance's CURRENT bucket must sit at or past
// the policy minimum on the governed ladder.
func (s *Service) checkBucketEligibility(ctx context.Context, tx pgx.Tx, adv entity.Advance) error {
	woCv, err := s.Config.ActiveAt(ctx, "writeoff.policy", "programme:"+adv.ProgrammeID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("writeoff.policy config: %w", err)
	}
	var wc writeoffCfg
	if err := json.Unmarshal(woCv.Content, &wc); err != nil {
		return err
	}
	dbCv, err := s.Config.ActiveAt(ctx, "delinquency.buckets", "programme:"+adv.ProgrammeID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("delinquency.buckets config: %w", err)
	}
	var bc bucketsCfg
	if err := json.Unmarshal(dbCv.Content, &bc); err != nil {
		return err
	}
	rank := map[string]int{}
	for i, b := range bc.Buckets {
		rank[b.Code] = i
	}
	minRank, ok := rank[wc.MinBucket]
	if !ok {
		return fmt.Errorf("writeoff.policy min_bucket %q not on the delinquency ladder — config drift, refusing", wc.MinBucket)
	}
	var bucket string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(delinquency_bucket,'') FROM advances WHERE advance_id = $1`,
		adv.AdvanceID).Scan(&bucket); err != nil {
		return err
	}
	curRank, ok := rank[bucket]
	if !ok || curRank < minRank {
		return fmt.Errorf("%w: bucket %q, policy minimum %q", ErrNotEligible, bucket, wc.MinBucket)
	}
	return nil
}

// auditRow appends the append-only audit record for a write-off action.
func (s *Service) auditRow(ctx context.Context, tx pgx.Tx, telcoID, actor, action, advanceID, reason string) error {
	return s.audit.Insert(ctx, tx, entity.AuditEvent{
		ID: platform.NewID("aud"), TelcoID: telcoID, Actor: actor, Action: action,
		TargetType: "advance", TargetID: advanceID, Reason: reason,
	})
}

// outstandingSplit itemises the remaining obligation into fee/principal
// remainders using net recovered-so-far (reversal-aware by construction).
func (s *Service) outstandingSplit(ctx context.Context, tx pgx.Tx, adv entity.Advance) (fee, principal entity.Money, err error) {
	recovered, err := (repo.Allocations{}).SumByComponent(ctx, tx, adv.AdvanceID)
	if err != nil {
		return fee, principal, err
	}
	cur := adv.Outstanding.Currency()
	feeRec, ok := recovered[entity.ComponentFee]
	if !ok {
		if feeRec, err = entity.ZeroMoney(cur); err != nil {
			return fee, principal, err
		}
	}
	if fee, err = adv.Fee.Sub(feeRec); err != nil {
		return fee, principal, err
	}
	if fee.IsNegative() {
		if fee, err = entity.ZeroMoney(cur); err != nil {
			return fee, principal, err
		}
	}
	principal, err = adv.Outstanding.Sub(fee)
	return fee, principal, err
}
