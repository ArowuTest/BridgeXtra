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
	"github.com/jackc/pgx/v5/pgconn"
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
	reversals   repo.PendingReversals
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
			// A reversal parked against an UNMATCHED original stays PARKED:
			// no money was booked, so releasing it is an operator decision in
			// the breaks workflow, never automatic.
			return nil
		}

		adv, err := s.advances.FindOpenBySubscriber(ctx, tx, subscriberID)
		if errors.Is(err, repo.ErrNotFound) {
			// EDG-021: a WRITTEN_OFF advance takes recoveries as INCOME —
			// the loss stays crystallised, the money is honestly booked.
			wo, woErr := s.advances.FindWrittenOffBySubscriber(ctx, tx, subscriberID)
			if woErr == nil {
				return s.writeoffIncome(ctx, tx, &out, evt, wo, cmd)
			}
			if !errors.Is(woErr, repo.ErrNotFound) {
				return woErr
			}
			// No recoverable advance at all: full quarantine (EDG-020 flavor).
			return s.quarantine(ctx, tx, &out, evt, cmd.Amount, "NO_OPEN_ADVANCE", cmd.CorrelationID)
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

		// Balanced journal for the applied portion (idempotent by event id;
		// template-rendered, CFG-012).
		if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
			BusinessEventKey: evt.RecoveryEventID + "/applied",
			EventType:        ledger.EventRecoveryApplied,
			TelcoID:          telcoID,
			ProgrammeID:      adv.ProgrammeID,
			AdvanceID:        adv.AdvanceID,
			CorrelationID:    cmd.CorrelationID,
		}, ledger.Bindings{ledger.SymAmount: applied}); err != nil {
			return err
		}

		// Excess -> suspense with its own balanced journal (EDG-020: held as
		// an explicit liability, never silently retained).
		if excess.IsSet() && excess.IsPositive() {
			if err := s.suspense.Insert(ctx, tx, telcoID, evt.RecoveryEventID, excess, "OVER_RECOVERY"); err != nil {
				return err
			}
			if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
				BusinessEventKey: evt.RecoveryEventID + "/suspense",
				EventType:        ledger.EventRecoverySuspense,
				TelcoID:          telcoID,
				ProgrammeID:      adv.ProgrammeID,
				AdvanceID:        adv.AdvanceID,
				CorrelationID:    cmd.CorrelationID,
			}, ledger.Bindings{ledger.SymAmount: excess}); err != nil {
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

		// EDG-019: a reversal that arrived BEFORE this original was parked —
		// apply it now, in the same transaction, so the pair nets exactly.
		return s.applyParkedIfAny(ctx, tx, &out, cmd)
	})
	return out, err
}

// ReverseCmd is a telco reversal of a prior recovery event.
type ReverseCmd struct {
	ReversalSourceEventID string
	OriginalSourceEventID string
	Amount                entity.Money
	CorrelationID         string
}

// ReverseResult reports what happened to the reversal.
type ReverseResult struct {
	Parked            bool         // original unseen — parked (EDG-019)
	Applied           entity.Money // amount clawed back from the advance book
	AdvanceReopened   bool         // CLOSED -> PARTIALLY_RECOVERED
	Replayed          bool
	PendingReversalID string
}

// Reverse processes a recovery reversal. Original seen and allocated ->
// applied now; original unseen -> parked until it arrives (EDG-019). A
// reversal exceeding the event's net applied amount is REFUSED loudly — the
// discrepancy surfaces in reconciliation, it is never partially guessed.
func (s *Service) Reverse(ctx context.Context, cmd ReverseCmd) (ReverseResult, error) {
	if cmd.ReversalSourceEventID == "" || cmd.OriginalSourceEventID == "" || cmd.CorrelationID == "" {
		return ReverseResult{}, fmt.Errorf("reversal source, original source and correlation id are required")
	}
	if !cmd.Amount.IsPositive() {
		return ReverseResult{}, fmt.Errorf("reversal amount must be positive")
	}

	var out ReverseResult
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		telcoID, err := platform.TenantFrom(ctx)
		if err != nil {
			return err
		}
		original, err := s.events.GetBySource(ctx, tx, cmd.OriginalSourceEventID)
		if errors.Is(err, repo.ErrNotFound) {
			// EDG-019: reversal BEFORE original — park it (idempotent).
			created, err := s.reversals.Park(ctx, tx, repo.PendingReversal{
				PendingReversalID:     platform.NewID("prv"),
				TelcoID:               telcoID,
				OriginalSourceEventID: cmd.OriginalSourceEventID,
				ReversalSourceEventID: cmd.ReversalSourceEventID,
				Amount:                cmd.Amount,
			})
			if err != nil {
				return err
			}
			parked, err := s.reversals.FindParkedForOriginal(ctx, tx, cmd.OriginalSourceEventID)
			if err != nil {
				return err
			}
			out = ReverseResult{Parked: true, Replayed: !created, PendingReversalID: parked.PendingReversalID}
			s.Log.Warn("reversal parked before original (EDG-019)",
				"original", cmd.OriginalSourceEventID, "reversal", cmd.ReversalSourceEventID)
			return nil
		}
		if err != nil {
			return err
		}
		if original.State != entity.RecoveryAllocated {
			// Money never reached the book (UNMATCHED/QUARANTINED): operator
			// territory — park for the breaks workflow, never guess.
			created, err := s.reversals.Park(ctx, tx, repo.PendingReversal{
				PendingReversalID:     platform.NewID("prv"),
				TelcoID:               telcoID,
				OriginalSourceEventID: cmd.OriginalSourceEventID,
				ReversalSourceEventID: cmd.ReversalSourceEventID,
				Amount:                cmd.Amount,
				ParkReason:            "ORIGINAL_NOT_ALLOCATED",
			})
			if err != nil {
				return err
			}
			out = ReverseResult{Parked: true, Replayed: !created}
			return nil
		}

		// M3B-F1: application runs under a SAVEPOINT — an invariant collision
		// (subscriber's new open advance blocks the reopen; pool lacks
		// headroom) rolls back the attempt WITHOUT aborting this transaction,
		// and the reversal PARKS with the collision as its reason. It lands
		// in the operator queue, never nowhere.
		collision, err := s.applyReversalGuarded(ctx, tx, &out, original, cmd.Amount, cmd.CorrelationID, cmd.ReversalSourceEventID)
		if err != nil {
			return err
		}
		if collision != "" {
			created, err := s.reversals.Park(ctx, tx, repo.PendingReversal{
				PendingReversalID:     platform.NewID("prv"),
				TelcoID:               telcoID,
				OriginalSourceEventID: cmd.OriginalSourceEventID,
				ReversalSourceEventID: cmd.ReversalSourceEventID,
				Amount:                cmd.Amount,
				ParkReason:            collision,
			})
			if err != nil {
				return err
			}
			if !created {
				// Already parked (telco retry): keep the freshest reason.
				parked, err := s.reversals.FindParkedForOriginal(ctx, tx, cmd.OriginalSourceEventID)
				if err != nil {
					return err
				}
				if err := s.reversals.SetParkReason(ctx, tx, parked.PendingReversalID, collision); err != nil {
					return err
				}
			}
			out = ReverseResult{Parked: true, Replayed: !created}
			s.Log.Warn("reversal parked on invariant collision (M3B-F1)",
				"original", cmd.OriginalSourceEventID, "reason", collision)
			return nil
		}
		// Applied: if a prior attempt had parked this reversal (collision
		// since cleared), close the parked row so the queue drains.
		if parked, err := s.reversals.FindParkedForOriginal(ctx, tx, cmd.OriginalSourceEventID); err == nil {
			if err := s.reversals.MarkApplied(ctx, tx, parked.PendingReversalID); err != nil {
				return err
			}
		} else if !errors.Is(err, repo.ErrNotFound) {
			return err
		}
		return nil
	})
	return out, err
}

// applyReversalGuarded runs applyReversal inside a savepoint. A collision
// with the one-active index or the pool-headroom CHECK returns a non-empty
// park reason with the outer transaction still healthy; every other error is
// returned as-is.
func (s *Service) applyReversalGuarded(ctx context.Context, tx pgx.Tx, out *ReverseResult,
	original entity.RecoveryEvent, amount entity.Money, correlationID, reversalSourceID string) (string, error) {

	sp, err := tx.Begin(ctx) // pgx nested Begin = SAVEPOINT
	if err != nil {
		return "", err
	}
	err = s.applyReversal(ctx, sp, out, original, amount, correlationID, reversalSourceID)
	if err == nil {
		return "", sp.Commit(ctx)
	}
	_ = sp.Rollback(ctx)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "advances_one_active_uq":
			return "SUBSCRIBER_HAS_OPEN_ADVANCE", nil
		case pgErr.Code == "23514" && pgErr.ConstraintName == "funding_no_overallocation":
			return "POOL_HEADROOM", nil
		}
	}
	return "", err
}

// applyParkedIfAny applies a parked reversal right after its original
// allocated (same transaction — the pair commits or rolls back together).
func (s *Service) applyParkedIfAny(ctx context.Context, tx pgx.Tx, ingest *IngestResult, cmd IngestCmd) error {
	parked, err := s.reversals.FindParkedForOriginal(ctx, tx, cmd.SourceEventID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	original, err := s.events.GetBySource(ctx, tx, cmd.SourceEventID)
	if err != nil {
		return err
	}
	// M3B-F1: guarded — a collision must NOT sink the original's ingest.
	// The reversal stays PARKED with the collision recorded for operators.
	var rev ReverseResult
	collision, err := s.applyReversalGuarded(ctx, tx, &rev, original, parked.Amount, cmd.CorrelationID, parked.ReversalSourceEventID)
	if err != nil {
		return err
	}
	if collision != "" {
		s.Log.Warn("parked reversal still blocked after original arrived (M3B-F1)",
			"original", cmd.SourceEventID, "reason", collision)
		return s.reversals.SetParkReason(ctx, tx, parked.PendingReversalID, collision)
	}
	if err := s.reversals.MarkApplied(ctx, tx, parked.PendingReversalID); err != nil {
		return err
	}
	s.Log.Info("parked reversal applied with its original (EDG-019)",
		"original", cmd.SourceEventID, "reversed", parked.Amount.String())
	return nil
}

// applyReversal claws back an applied recovery: reverse-waterfall negative
// allocations, outstanding restored, pool utilisation re-added (headroom
// CHECK guards), CLOSED re-opens, mirrored balanced journal.
func (s *Service) applyReversal(ctx context.Context, tx pgx.Tx, out *ReverseResult,
	original entity.RecoveryEvent, amount entity.Money, correlationID, reversalSourceID string) error {

	netApplied, advanceID, err := s.allocations.NetAppliedByEvent(ctx, tx, original.RecoveryEventID)
	if err != nil {
		return err
	}
	if c, err := amount.Cmp(netApplied); err != nil {
		return err
	} else if c > 0 {
		return fmt.Errorf("reversal %s exceeds the event's net applied %s — refused; resolve via the breaks workflow, never guessed",
			amount, netApplied)
	}

	adv, err := s.advances.Get(ctx, tx, advanceID)
	if err != nil {
		return err
	}
	switch adv.State {
	case entity.AdvActive, entity.AdvPartiallyRecovered, entity.AdvClosed:
		// reversible book states
	default:
		// WRITTEN_OFF etc.: the receivable no longer exists — park for the
		// breaks workflow (income adjustment is an operator decision).
		return fmt.Errorf("reversal against advance in state %s requires operator resolution", adv.State)
	}

	// Reverse-waterfall un-allocation: negative rows against the components
	// in REVERSE of the allocation order (principal back first under the
	// seeded fee-first waterfall).
	perComp, err := s.allocations.ListNetByEventComponent(ctx, tx, original.RecoveryEventID)
	if err != nil {
		return err
	}
	cfgV, err := s.Config.ActiveAt(ctx, "recovery.allocation", "programme:"+adv.ProgrammeID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("recovery.allocation config: %w", err)
	}
	var ac allocationCfg
	if err := json.Unmarshal(cfgV.Content, &ac); err != nil {
		return err
	}
	remaining := amount
	for i := len(ac.Waterfall) - 1; i >= 0 && remaining.IsPositive(); i-- {
		comp := entity.AllocationComponent(ac.Waterfall[i])
		have, ok := perComp[comp]
		if !ok || !have.IsPositive() {
			continue
		}
		take := remaining
		if c, err := take.Cmp(have); err != nil {
			return err
		} else if c > 0 {
			take = have
		}
		neg, err := take.Neg()
		if err != nil {
			return err
		}
		if err := s.allocations.Insert(ctx, tx, entity.RecoveryAllocation{
			AllocationID:    platform.NewID("alc"),
			RecoveryEventID: original.RecoveryEventID,
			AdvanceID:       adv.AdvanceID,
			Component:       comp,
			Amount:          neg,
		}); err != nil {
			return err
		}
		if remaining, err = remaining.Sub(take); err != nil {
			return err
		}
	}
	if remaining.IsPositive() {
		return fmt.Errorf("reversal un-allocation did not consume %s", remaining)
	}

	// Book restoration: outstanding grows back, pool funds it again.
	newOutstanding, err := adv.Outstanding.Add(amount)
	if err != nil {
		return err
	}
	toState := adv.State
	if adv.State == entity.AdvClosed {
		toState = entity.AdvPartiallyRecovered
		out.AdvanceReopened = true
	}
	if err := s.advances.ApplyReversal(ctx, tx, adv.AdvanceID, adv.Version, adv.State, toState, newOutstanding); err != nil {
		return err
	}
	if err := s.pools.ReAddUtilisation(ctx, tx, adv.FundingPoolID, amount); err != nil {
		return err
	}

	// Mirrored journal: receivable rebuilds, telco claws back.
	if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
		BusinessEventKey: original.RecoveryEventID + "/reversed/" + reversalSourceID,
		EventType:        ledger.EventRecoveryReversed,
		TelcoID:          original.TelcoID,
		ProgrammeID:      adv.ProgrammeID,
		AdvanceID:        adv.AdvanceID,
		CorrelationID:    correlationID,
	}, ledger.Bindings{ledger.SymAmount: amount}); err != nil {
		return err
	}

	// Fully-reversed events become visible as such.
	if c, err := amount.Cmp(netApplied); err == nil && c == 0 {
		if err := s.events.SetState(ctx, tx, original.RecoveryEventID, entity.RecoveryAllocated, entity.RecoveryReversed); err != nil {
			return err
		}
	}
	out.Applied = amount
	return nil
}

// writeoffIncome books a post-write-off recovery as income (EDG-021): the
// advance stays WRITTEN_OFF, outstanding stays zero, the money is honestly
// recognised against the crystallised loss.
func (s *Service) writeoffIncome(ctx context.Context, tx pgx.Tx, out *IngestResult,
	evt entity.RecoveryEvent, wo entity.Advance, cmd IngestCmd) error {

	if err := s.allocations.Insert(ctx, tx, entity.RecoveryAllocation{
		AllocationID:    platform.NewID("alc"),
		RecoveryEventID: evt.RecoveryEventID,
		AdvanceID:       wo.AdvanceID,
		Component:       entity.ComponentWriteoffIncome,
		Amount:          cmd.Amount,
	}); err != nil {
		return err
	}
	if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
		BusinessEventKey: evt.RecoveryEventID + "/writeoff-income",
		EventType:        ledger.EventWriteoffRecovery,
		TelcoID:          evt.TelcoID,
		ProgrammeID:      wo.ProgrammeID,
		AdvanceID:        wo.AdvanceID,
		CorrelationID:    cmd.CorrelationID,
	}, ledger.Bindings{ledger.SymAmount: cmd.Amount}); err != nil {
		return err
	}
	if err := s.events.SetState(ctx, tx, evt.RecoveryEventID, entity.RecoveryPending, entity.RecoveryAllocated); err != nil {
		return err
	}
	out.RecoveryEventID = evt.RecoveryEventID
	out.State = entity.RecoveryAllocated
	out.Applied = cmd.Amount
	s.Log.Info("post-write-off recovery booked as income (EDG-021)",
		"advance", wo.AdvanceID, "amount", cmd.Amount.String())
	return nil
}

// quarantine handles events with no allocatable advance: an explicit
// suspense record AND — DD-19, resolved at M3b — a TELCO-LEVEL ledger
// attribution. A programme-less event has no programme to book against, so
// the liability posts without one (the only event type the 0012 schema
// permits that for): the books now say exactly what the operations table
// says — money held, owed onward, attributable to the telco relationship.
func (s *Service) quarantine(ctx context.Context, tx pgx.Tx, out *IngestResult, evt entity.RecoveryEvent, amount entity.Money, reason, correlationID string) error {
	if err := s.suspense.Insert(ctx, tx, evt.TelcoID, evt.RecoveryEventID, amount, reason); err != nil {
		return err
	}
	if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
		BusinessEventKey: evt.RecoveryEventID + "/quarantined",
		EventType:        ledger.EventRecoveryQuarantined,
		TelcoID:          evt.TelcoID,
		// NO ProgrammeID: telco-level by nature (DD-19).
		CorrelationID: correlationID,
	}, ledger.Bindings{ledger.SymAmount: amount}); err != nil {
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
	// recoveredOf: Money accessor with an explicit zero for absent components.
	cur := adv.Outstanding.Currency()
	recoveredOf := func(c entity.AllocationComponent) (entity.Money, error) {
		if m, ok := recovered[c]; ok {
			return m, nil
		}
		return entity.ZeroMoney(cur)
	}

	// Component totals (invariant: outstanding == Σ component remainders):
	// gross repayment = outstanding + everything recovered so far;
	// FEE total = adv.Fee; PRINCIPAL total = gross repayment - fee.
	feeRec, err := recoveredOf(entity.ComponentFee)
	if err != nil {
		return err
	}
	prinRec, err := recoveredOf(entity.ComponentPrincipal)
	if err != nil {
		return err
	}
	totalRecovered, err := feeRec.Add(prinRec)
	if err != nil {
		return err
	}
	grossRepayment, err := adv.Outstanding.Add(totalRecovered)
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
			if compTotal, err = grossRepayment.Sub(adv.Fee); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown waterfall component %q", comp)
		}
		compRecovered, err := recoveredOf(entity.AllocationComponent(comp))
		if err != nil {
			return err
		}
		compRemaining, err := compTotal.Sub(compRecovered)
		if err != nil {
			return err
		}
		if !compRemaining.IsPositive() {
			continue
		}
		take := remaining
		if c, err := take.Cmp(compRemaining); err != nil {
			return err
		} else if c > 0 {
			take = compRemaining
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
