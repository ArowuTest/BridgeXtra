// Package recovery ingests telco recharge-recovery events and allocates them
// to advances (V2-COL-001..008, M1b-4): DB-arbitered dedup (EDG-018),
// config-driven waterfall, over-recovery quarantined to suspense — never
// silently retained (EDG-020), unmatched events preserved — never discarded
// (V2-REP-004), recovery posting and balance update atomic (V2-COL-005).
package recovery

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

type Service struct {
	Pool   *pgxpool.Pool // tcp_app
	Config *configsvc.Service
	Ledger *ledger.Service
	Log    *slog.Logger

	events      repo.RecoveryEvents
	allocations repo.Allocations
	suspense    repo.Suspense
	subscribers repo.Subscribers
	advances    repo.Advances
	pools       repo.FundingPools
	outbox      repo.Outbox
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, led *ledger.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Ledger: led, Log: log}
}

// IngestCmd is one canonical telco recovery event.
type IngestCmd struct {
	SourceEventID string
	MSISDNToken   string
	Amount        entity.Money
	OccurredAt    time.Time
	CorrelationID string
}

// IngestResult reports what happened to the event.
type IngestResult struct {
	RecoveryEventID string
	State           entity.RecoveryEventState
	Applied         entity.Money // portion allocated to the advance
	Excess          entity.Money // portion quarantined to suspense
	AdvanceClosed   bool
	Replayed        bool // duplicate source event (EDG-018)
}

type allocationCfg struct {
	Waterfall    []string `json:"waterfall"`
	OverRecovery string   `json:"over_recovery"`
}

// Ingest processes one recovery event end-to-end in ONE transaction:
// dedup -> match subscriber -> lock advance -> waterfall-allocate ->
// outstanding update + state -> pool utilisation release -> balanced
// journal(s) -> outbox. All-or-nothing (V2-COL-005).
func (s *Service) Ingest(ctx context.Context, cmd IngestCmd) (IngestResult, error) {
	if cmd.SourceEventID == "" || cmd.CorrelationID == "" {
		return IngestResult{}, fmt.Errorf("source_event_id and correlation id are required")
	}
	if !cmd.Amount.IsPositive() {
		return IngestResult{}, fmt.Errorf("recovery amount must be positive")
	}

	var out IngestResult
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		telcoID, err := platform.TenantFrom(ctx)
		if err != nil {
			return err
		}

		// Subscriber match BEFORE insert so the event row carries the link.
		var subscriberID string
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, cmd.MSISDNToken)
		switch {
		case err == nil:
			subscriberID = sub.SubscriberAccountID
		case errors.Is(err, repo.ErrNotFound):
			subscriberID = "" // unmatched path below
		default:
			return err
		}

		evt := entity.RecoveryEvent{
			RecoveryEventID:     platform.NewID("rcv"),
			TelcoID:             telcoID,
			SourceEventID:       cmd.SourceEventID,
			SubscriberAccountID: subscriberID,
			Amount:              cmd.Amount,
			State:               entity.RecoveryPending,
			OccurredAt:          cmd.OccurredAt,
		}
		created, err := s.events.Insert(ctx, tx, evt)
		if err != nil {
			return err
		}
		if !created {
			// EDG-018: telco replay — return the original outcome, touch nothing.
			existing, err := s.events.GetBySource(ctx, tx, cmd.SourceEventID)
			if err != nil {
				return err
			}
			out = IngestResult{RecoveryEventID: existing.RecoveryEventID, State: existing.State, Replayed: true}
			return nil
		}

		if subscriberID == "" {
			// V2-REP-004: never discarded — preserved as UNMATCHED for ops.
			if err := s.events.SetState(ctx, tx, evt.RecoveryEventID, entity.RecoveryPending, entity.RecoveryUnmatched); err != nil {
				return err
			}
			out = IngestResult{RecoveryEventID: evt.RecoveryEventID, State: entity.RecoveryUnmatched}
			s.Log.Warn("recovery event unmatched", "source_event_id", cmd.SourceEventID)
			return nil
		}

		adv, err := s.advances.FindOpenBySubscriber(ctx, tx, subscriberID)
		if errors.Is(err, repo.ErrNotFound) {
			// No recoverable advance: full quarantine (EDG-020 flavor).
			return s.quarantine(ctx, tx, &out, evt, cmd.Amount, "NO_OPEN_ADVANCE")
		}
		if err != nil {
			return err
		}

		// Split applied vs excess against outstanding.
		applied := cmd.Amount
		var excess entity.Money
		if cmpRes, err := cmd.Amount.Cmp(adv.Outstanding); err != nil {
			return err
		} else if cmpRes > 0 {
			applied = adv.Outstanding
			if excess, err = cmd.Amount.Sub(adv.Outstanding); err != nil {
				return err
			}
		}

		// Waterfall split from governed config (V2-COL-002/004).
		if err := s.allocate(ctx, tx, evt, adv, applied); err != nil {
			return err
		}

		// Outstanding + state under the optimistic guard.
		newOutstanding, err := adv.Outstanding.Sub(applied)
		if err != nil {
			return err
		}
		toState := entity.AdvPartiallyRecovered
		if newOutstanding.IsZero() {
			toState = entity.AdvClosed
			out.AdvanceClosed = true
		}
		if err := s.advances.ApplyRecovery(ctx, tx, adv.AdvanceID, adv.Version, adv.State, toState, newOutstanding); err != nil {
			return err
		}
		if err := s.pools.ReduceUtilisation(ctx, tx, adv.FundingPoolID, applied); err != nil {
			return err
		}

		// Balanced journal for the applied portion (idempotent by event id).
		if _, _, err := s.Ledger.Post(ctx, tx, ledger.Journal{
			BusinessEventKey: evt.RecoveryEventID + "/applied",
			EventType:        ledger.EventRecoveryApplied,
			TelcoID:          telcoID,
			ProgrammeID:      adv.ProgrammeID,
			AdvanceID:        adv.AdvanceID,
			CorrelationID:    cmd.CorrelationID,
			Lines: []ledger.Line{
				{Account: "TELCO_SETTLEMENT_RECEIVABLE", Side: ledger.Debit, Amount: applied},
				{Account: "SUBSCRIBER_RECEIVABLE", Side: ledger.Credit, Amount: applied},
			},
		}); err != nil {
			return err
		}

		// Excess -> suspense with its own balanced journal (EDG-020: held as
		// an explicit liability, never silently retained).
		if excess.IsSet() && excess.IsPositive() {
			if err := s.suspense.Insert(ctx, tx, telcoID, evt.RecoveryEventID, excess, "OVER_RECOVERY"); err != nil {
				return err
			}
			if _, _, err := s.Ledger.Post(ctx, tx, ledger.Journal{
				BusinessEventKey: evt.RecoveryEventID + "/suspense",
				EventType:        ledger.EventRecoverySuspense,
				TelcoID:          telcoID,
				ProgrammeID:      adv.ProgrammeID,
				AdvanceID:        adv.AdvanceID,
				CorrelationID:    cmd.CorrelationID,
				Lines: []ledger.Line{
					{Account: "TELCO_SETTLEMENT_RECEIVABLE", Side: ledger.Debit, Amount: excess},
					{Account: "RECOVERY_SUSPENSE", Side: ledger.Credit, Amount: excess},
				},
			}); err != nil {
				return err
			}
		}

		if err := s.events.SetState(ctx, tx, evt.RecoveryEventID, entity.RecoveryPending, entity.RecoveryAllocated); err != nil {
			return err
		}

		payload, err := json.Marshal(map[string]string{
			"recovery_event_id": evt.RecoveryEventID,
			"advance_id":        adv.AdvanceID,
			"correlation_id":    cmd.CorrelationID,
		})
		if err != nil {
			return err
		}
		if err := s.outbox.Append(ctx, tx, entity.OutboxEvent{
			ID: platform.NewID("evt"), TelcoID: telcoID, AggregateType: "Advance",
			AggregateID: adv.AdvanceID, EventType: "advance.RecoveryApplied", SchemaVersion: 1,
			Payload: payload, OccurredAt: time.Now().UTC(),
		}); err != nil {
			return err
		}

		out.RecoveryEventID = evt.RecoveryEventID
		out.State = entity.RecoveryAllocated
		out.Applied = applied
		out.Excess = excess
		return nil
	})
	return out, err
}

// quarantine handles events with no allocatable advance: an explicit
// suspense record — never silently retained, never discarded (EDG-020,
// V2-REP-004). NO ledger posting here: a programme-less event has no honest
// journal attribution yet (which programme's settlement position it affects
// is a DD-19 settlement-design decision); booking it against an arbitrary
// programme would be a hardcode masquerading as accounting. The suspense
// item IS the operational record; ledger attribution lands with the M3
// settlement engine (BUILD_PLAN §9 deferred register).
func (s *Service) quarantine(ctx context.Context, tx pgx.Tx, out *IngestResult, evt entity.RecoveryEvent, amount entity.Money, reason string) error {
	if err := s.suspense.Insert(ctx, tx, evt.TelcoID, evt.RecoveryEventID, amount, reason); err != nil {
		return err
	}
	if err := s.events.SetState(ctx, tx, evt.RecoveryEventID, entity.RecoveryPending, entity.RecoveryQuarantined); err != nil {
		return err
	}
	out.RecoveryEventID = evt.RecoveryEventID
	out.State = entity.RecoveryQuarantined
	out.Excess = amount
	return nil
}

// allocate splits the applied amount across waterfall components using
// recovered-so-far state (fee first by seeded default).
func (s *Service) allocate(ctx context.Context, tx pgx.Tx, evt entity.RecoveryEvent, adv entity.Advance, applied entity.Money) error {
	cfgV, err := s.Config.ActiveAt(ctx, "recovery.allocation", "programme:"+adv.ProgrammeID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("recovery.allocation config: %w", err)
	}
	var ac allocationCfg
	if err := json.Unmarshal(cfgV.Content, &ac); err != nil {
		return err
	}

	recovered, err := s.allocations.SumByComponent(ctx, tx, adv.AdvanceID)
	if err != nil {
		return err
	}
	// Component totals: FEE = adv.Fee; PRINCIPAL = repayment - fee
	// (outstanding always equals Σ component remainders — invariant).
	principalTotal, err := adv.Outstanding.Add(entity.MustMoney(recovered[entity.ComponentFee]+recovered[entity.ComponentPrincipal], adv.Outstanding.Currency()))
	if err != nil {
		return err
	}
	remaining := applied
	for _, comp := range ac.Waterfall {
		if !remaining.IsPositive() {
			break
		}
		var compTotal entity.Money
		switch entity.AllocationComponent(comp) {
		case entity.ComponentFee:
			compTotal = adv.Fee
		case entity.ComponentPrincipal:
			compTotal, err = principalTotal.Sub(adv.Fee)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown waterfall component %q", comp)
		}
		compRecovered := recovered[entity.AllocationComponent(comp)]
		compRemainingMinor := compTotal.Amount() - compRecovered
		if compRemainingMinor <= 0 {
			continue
		}
		take := remaining
		if take.Amount() > compRemainingMinor {
			take, err = entity.NewMoney(compRemainingMinor, remaining.Currency())
			if err != nil {
				return err
			}
		}
		if err := s.allocations.Insert(ctx, tx, entity.RecoveryAllocation{
			AllocationID:    platform.NewID("alc"),
			RecoveryEventID: evt.RecoveryEventID,
			AdvanceID:       adv.AdvanceID,
			Component:       entity.AllocationComponent(comp),
			Amount:          take,
		}); err != nil {
			return err
		}
		if remaining, err = remaining.Sub(take); err != nil {
			return err
		}
	}
	if remaining.IsPositive() {
		return fmt.Errorf("allocation waterfall did not consume applied amount: %s left of %s", remaining, applied)
	}
	return nil
}
