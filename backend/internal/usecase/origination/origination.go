// Package origination is the advance origination saga (V2 §13, BUILD_PLAN
// M1b-3): offer retrieval from governed config, idempotent confirmation, FSM
// with optimistic locking, atomic funding reservation, fulfilment OUTSIDE any
// transaction (V2-ADV-006), and ledger posting only on confirmed fulfilment
// (V2-LED-006).
//
// Transaction shape — the no-txn-across-network-call rule, structurally:
//
//	tx1: accept offer -> create advance -> reserve funding -> record attempt
//	     -> PENDING_FULFILMENT                                    [commit]
//	    ---- network: adapter.SubmitFulfilment (NO transaction) ----
//	tx2: resolve outcome -> ACTIVE+journal / FAILED+release / UNKNOWN+enquiry
//	     schedule                                                 [commit]
//
// A crash between tx1 and tx2 leaves a SENT attempt on a PENDING_FULFILMENT
// advance — the resolver worker (M1b-4) treats stale SENT as UNKNOWN and
// resolves via status enquiry (EDG-007: recover exactly once, never re-lend).
package origination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

// Typed errors (BC-7) mapped once at the HTTP boundary.
var (
	ErrOfferNotFound        = errors.New("origination: offer not found")
	ErrOfferExpired         = errors.New("origination: offer expired") // EDG-011
	ErrOfferNotAcceptable   = errors.New("origination: offer no longer acceptable")
	ErrSubscriberIneligible = errors.New("origination: subscriber not eligible") // barred/self-excluded/closed
	// ErrDecisionUnavailable (M2e): the credit decision is expired, ineligible
	// or absent — customer-safe NO_OFFER, never a stale lend (EDG-014).
	ErrDecisionUnavailable = errors.New("origination: no valid credit decision")
	// ErrOverlayBlocked (M2e, V2-SCR-015): a real-time risk overlay blocks the
	// subscriber at this moment. Which flag fired is logged, never disclosed.
	ErrOverlayBlocked = errors.New("origination: risk overlay blocks this action")
)

type Service struct {
	Pool    *pgxpool.Pool // tcp_app
	Config  *configsvc.Service
	Ledger  *ledger.Service
	Adapter mno.Client
	Log     *slog.Logger
	// treasury guards the confirm path (M3d). UNEXPORTED and set only by New
	// (M3D-F1): no struct-literal construction can produce a Service with a
	// disarmed guardrail, and there is no nil-skip anywhere — an absent
	// guardrail FAILS, it never silently waves money through (BC-5
	// arm-or-refuse; reachability invariant).
	treasury *treasury.Service

	subscribers repo.Subscribers
	decisions   repo.Decisions
	offers      repo.Offers
	pools       repo.FundingPools
	advances    repo.Advances
	attempts    repo.Attempts
	outbox      repo.Outbox
	flags       repo.SubscriberFlags
	consents    repo.Consents
	programmes  repo.Programmes
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, led *ledger.Service, adapter mno.Client, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Ledger: led, Adapter: adapter, Log: log,
		treasury: treasury.New(pool, cfg, log)}
}

type productCfg struct {
	Currency           entity.Currency `json:"currency"`
	DenominationsMinor []int64         `json:"denominations_minor"`
	FeeBps             int64           `json:"fee_bps"`
	FeeModel           entity.FeeModel `json:"fee_model"`
	OfferExpiryMinutes int             `json:"offer_expiry_minutes"`
}

type fulfilmentCfg struct {
	StatusEnquiryDelaysSeconds []int `json:"status_enquiry_delays_seconds"`
	UnknownEscalationMinutes   int   `json:"unknown_escalation_minutes"`
}

type overlaysCfg struct {
	BlockingFlags       []string `json:"blocking_flags"`
	SimSwapCooloffHours int      `json:"sim_swap_cooloff_hours"`
	CheckAt             []string `json:"check_at"`
}

// checkOverlays applies the real-time risk overlays (V2-SCR-015) at the given
// checkpoint (OFFER or CONFIRM). Config-driven; the validator guarantees
// CONFIRM can never be configured off. SIM_SWAP blocks only inside its
// cool-off window; every other blocking flag blocks while open.
func (s *Service) checkOverlays(ctx context.Context, tx pgx.Tx, telcoID, subscriberAccountID, checkpoint string, now time.Time) error {
	cv, err := s.Config.ActiveAt(ctx, "overlays.policy", "telco:"+telcoID, now)
	if err != nil {
		return fmt.Errorf("overlays.policy config: %w", err)
	}
	var oc overlaysCfg
	if err := json.Unmarshal(cv.Content, &oc); err != nil {
		return err
	}
	applies := false
	for _, c := range oc.CheckAt {
		if c == checkpoint {
			applies = true
		}
	}
	if !applies {
		return nil
	}
	blocking := map[string]bool{}
	for _, f := range oc.BlockingFlags {
		blocking[f] = true
	}
	open, err := s.flags.ListOpen(ctx, tx, subscriberAccountID)
	if err != nil {
		return err
	}
	for _, f := range open {
		if !blocking[f.Flag] {
			continue
		}
		if f.Flag == "SIM_SWAP" &&
			now.After(f.EffectiveFrom.Add(time.Duration(oc.SimSwapCooloffHours)*time.Hour)) {
			continue // cool-off elapsed: a settled SIM swap no longer blocks
		}
		s.Log.Warn("overlay blocked", "subscriber", subscriberAccountID,
			"flag", f.Flag, "checkpoint", checkpoint)
		return fmt.Errorf("%w (%s)", ErrOverlayBlocked, checkpoint)
	}
	return nil
}

// requireValidDecision enforces decision validity at the lending boundary
// (EDG-014 / V2-SCR-015): a scored decision past valid_until or ineligible
// never serves an offer; seeds (no expiry) pass — they exist only in
// pre-scoring environments.
func requireValidDecision(dec entity.DecisionSnapshot, now time.Time) error {
	if dec.ValidUntil != nil && !dec.ValidUntil.After(now) {
		return fmt.Errorf("%w: decision expired %s", ErrDecisionUnavailable, dec.ValidUntil.UTC().Format(time.RFC3339))
	}
	if !dec.MaxFaceValue.IsPositive() {
		return fmt.Errorf("%w: not eligible", ErrDecisionUnavailable)
	}
	return nil
}

// GetOffers returns the subscriber's valid offers, generating the ladder from
// the governed product config when none exist (V2-OFR-009 reuse). Every value
// on an offer derives from config + the pinned decision — nothing hardcoded.
func (s *Service) GetOffers(ctx context.Context, programmeID, msisdnToken string) ([]entity.Offer, error) {
	now := time.Now().UTC()
	cfgV, err := s.Config.ActiveAt(ctx, "product.airtime", "programme:"+programmeID, now)
	if err != nil {
		return nil, fmt.Errorf("product config: %w", err)
	}
	var pc productCfg
	if err := json.Unmarshal(cfgV.Content, &pc); err != nil {
		return nil, fmt.Errorf("product config parse: %w", err)
	}

	var out []entity.Offer
	err = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// M3d: a suspended programme serves NOTHING (guardrail tripped or
		// operator action) — fail closed at the first touch.
		if status, err := s.programmes.GetStatus(ctx, tx, programmeID); err != nil {
			return err
		} else if status != entity.ProgrammeActive {
			return fmt.Errorf("%w (programme %s)", treasury.ErrProgrammeSuspended, status)
		}
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, msisdnToken)
		if err != nil {
			return err
		}
		if sub.Status != "ACTIVE" {
			return fmt.Errorf("%w: status %s", ErrSubscriberIneligible, sub.Status)
		}
		if err := s.checkOverlays(ctx, tx, sub.TelcoID, sub.SubscriberAccountID, "OFFER", now); err != nil {
			return err
		}
		// VR-7a: serialize ladder generation per (subscriber, programme) so
		// concurrent first-time enquiries cannot mint duplicate ladders
		// (double USSD menu entries). The second enquirer waits here, then
		// sees the winner's offers in ListValid below.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || '/' || $2))`,
			sub.SubscriberAccountID, programmeID); err != nil {
			return err
		}
		existing, err := s.offers.ListValid(ctx, tx, sub.SubscriberAccountID, programmeID, now)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			out = existing
			return nil
		}
		dec, err := s.decisions.GetCurrent(ctx, tx, sub.SubscriberAccountID)
		if errors.Is(err, repo.ErrNotFound) {
			return fmt.Errorf("%w: no decision on file", ErrDecisionUnavailable)
		}
		if err != nil {
			return err
		}
		if err := requireValidDecision(dec, now); err != nil {
			return err
		}
		built, err := buildLadder(sub, dec, programmeID, cfgV.ConfigVersionID, pc, now)
		if err != nil {
			return err
		}
		for _, o := range built {
			if err := s.offers.Insert(ctx, tx, o); err != nil {
				return err
			}
		}
		out = built
		return nil
	})
	return out, err
}

// buildLadder computes the offer set: every config denomination within the
// decision's max, priced per the config fee model — all Money arithmetic,
// PercentBps as the single rounding site (ADR-0002).
func buildLadder(sub entity.SubscriberAccount, dec entity.DecisionSnapshot, programmeID, productCfgVersion string, pc productCfg, now time.Time) ([]entity.Offer, error) {
	expiry := now.Add(time.Duration(pc.OfferExpiryMinutes) * time.Minute)
	var out []entity.Offer
	for _, denom := range pc.DenominationsMinor {
		face, err := entity.NewMoney(denom, pc.Currency)
		if err != nil {
			return nil, err
		}
		if cmp, err := face.Cmp(dec.MaxFaceValue); err != nil {
			return nil, err
		} else if cmp > 0 {
			continue // above the subscriber's limit
		}
		fee, err := face.PercentBps(pc.FeeBps)
		if err != nil {
			return nil, err
		}
		var disbursed, repayment entity.Money
		switch pc.FeeModel {
		case entity.FeeDeductedUpfront:
			if disbursed, err = face.Sub(fee); err != nil {
				return nil, err
			}
			repayment = face
		case entity.FeeAddedToRepayment:
			disbursed = face
			if repayment, err = face.Add(fee); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported fee model %q", pc.FeeModel)
		}
		if !disbursed.IsPositive() {
			// fee consumes the whole denomination — unofferable, skip loudly
			// in caller logs rather than mint a zero-value credit.
			continue
		}
		out = append(out, entity.Offer{
			OfferID:                platform.NewID("off"),
			TelcoID:                sub.TelcoID,
			ProgrammeID:            programmeID,
			SubscriberAccountID:    sub.SubscriberAccountID,
			DecisionSnapshotID:     dec.DecisionSnapshotID,
			FaceValue:              face,
			Fee:                    fee,
			Disbursed:              disbursed,
			Repayment:              repayment,
			FeeModel:               pc.FeeModel,
			ProductConfigVersionID: productCfgVersion,
			State:                  entity.OfferGenerated,
			ExpiresAt:              expiry,
			CreatedAt:              now,
		})
	}
	return out, nil
}

// ConfirmCmd is one customer confirmation (channel-idempotent via IdemKey).
type ConfirmCmd struct {
	ProgrammeID   string
	OfferID       string
	MSISDNToken   string
	IdemKey       string
	CorrelationID string
}

// ConfirmResult reports the (possibly replayed) advance.
type ConfirmResult struct {
	Advance  entity.Advance
	Replayed bool
}

// Confirm executes the origination saga.
func (s *Service) Confirm(ctx context.Context, cmd ConfirmCmd) (ConfirmResult, error) {
	if cmd.IdemKey == "" || cmd.CorrelationID == "" {
		return ConfirmResult{}, fmt.Errorf("idempotency key and correlation id are required")
	}

	// ---- tx1: accept + reserve + record attempt ---------------------------
	var adv entity.Advance
	var attempt entity.FulfilmentAttempt
	var offer entity.Offer
	replayed := false
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// M3d: suspended programme = lending stopped, fail closed.
		if status, err := s.programmes.GetStatus(ctx, tx, cmd.ProgrammeID); err != nil {
			return err
		} else if status != entity.ProgrammeActive {
			return fmt.Errorf("%w (programme %s)", treasury.ErrProgrammeSuspended, status)
		}
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, cmd.MSISDNToken)
		if err != nil {
			return err
		}
		if sub.Status != "ACTIVE" {
			return fmt.Errorf("%w: status %s", ErrSubscriberIneligible, sub.Status)
		}
		// Real-time overlays at the money-moving moment (V2-SCR-015). The
		// validator guarantees CONFIRM cannot be configured off.
		if err := s.checkOverlays(ctx, tx, sub.TelcoID, sub.SubscriberAccountID, "CONFIRM", time.Now().UTC()); err != nil {
			return err
		}

		offer, err = s.offers.GetForUpdate(ctx, tx, cmd.OfferID)
		if errors.Is(err, repo.ErrNotFound) {
			return ErrOfferNotFound
		}
		if err != nil {
			return err
		}
		if offer.SubscriberAccountID != sub.SubscriberAccountID {
			return ErrOfferNotFound // someone else's offer is invisible, not forbidden
		}

		now := time.Now().UTC()
		switch {
		case offer.State == entity.OfferAccepted:
			// EDG-001 replay path: the advance for this offer already exists.
			existing, err := s.advances.GetByIdemKey(ctx, tx, cmd.IdemKey)
			if err == nil {
				adv, replayed = existing, true
				return nil
			}
			return ErrOfferNotAcceptable
		case offer.State != entity.OfferGenerated:
			return ErrOfferNotAcceptable
		case !offer.ExpiresAt.After(now):
			// EDG-011: expired between menu and confirm — fail safely.
			_ = s.offers.SetState(ctx, tx, offer.OfferID, entity.OfferGenerated, entity.OfferExpired)
			return ErrOfferExpired
		}

		// The offer's pinned decision must still be valid AT CONFIRM: an
		// offer whose decision expired between menu and confirm is a stale
		// lend (EDG-014) — refuse, never honour.
		dec, err := s.decisions.Get(ctx, tx, offer.DecisionSnapshotID)
		if err != nil {
			return err
		}
		if err := requireValidDecision(dec, now); err != nil {
			return err
		}

		// Create the advance FIRST, pool-less (0006): the one-active contest
		// is decided at this INSERT, before any pool lock exists — a losing
		// contender therefore never holds the pool row, which is what broke
		// the tx1/tx2 deadlock cycle found by the EDG-002 test.
		adv = entity.Advance{
			AdvanceID:           platform.NewID("adv"),
			TelcoID:             sub.TelcoID,
			ProgrammeID:         offer.ProgrammeID,
			SubscriberAccountID: sub.SubscriberAccountID,
			OfferID:             offer.OfferID,
			IdempotencyKey:      cmd.IdemKey,
			CorrelationID:       cmd.CorrelationID,
			State:               entity.AdvRequested,
			Version:             1,
			FaceValue:           offer.FaceValue,
			Fee:                 offer.Fee,
			Disbursed:           offer.Disbursed,
			Outstanding:         offer.Repayment, // obligation = repayment amount
		}
		created, err := s.advances.Insert(ctx, tx, adv)
		if err != nil {
			return err
		}
		if !created {
			// Same idem key raced us in another request: replay outside.
			return errReplayRace
		}
		if err := s.advances.Transition(ctx, tx, adv.AdvanceID, 1, entity.AdvRequested, entity.AdvValidated); err != nil {
			return err
		}

		// Only the one-active winner reaches the pool (exposure = repayment
		// obligation). Reservation + EXPOSURE_RESERVED are one atomic step.
		poolID, err := s.pools.Reserve(ctx, tx, offer.ProgrammeID, offer.Repayment)
		if err != nil {
			return err
		}

		// M3d: guardrails measure HERE, with the pool row locked — the
		// serialization point concurrent confirms queue behind (EDG-024). A
		// breach aborts this confirm; the trip records out-of-band below.
		// NO nil-skip (M3D-F1): the guardrail is structurally always armed.
		if err := s.treasury.EvaluateInTx(ctx, tx, offer.ProgrammeID, poolID, offer.Disbursed); err != nil {
			return err
		}
		adv.FundingPoolID = poolID
		if err := s.advances.ReserveTransition(ctx, tx, adv.AdvanceID, 2, poolID); err != nil {
			return err
		}
		if err := s.offers.SetState(ctx, tx, offer.OfferID, entity.OfferGenerated, entity.OfferAccepted); err != nil {
			return err
		}

		// Consent/disclosure evidence IN the confirm transaction (V2-REG-001):
		// an advance cannot exist without the exact terms the customer
		// accepted. The record is what was DISCLOSED — offer-pinned amounts,
		// fee model and decision provenance — hashed for tamper evidence.
		terms, err := json.Marshal(map[string]any{
			"offer_id":             offer.OfferID,
			"face_value_minor":     offer.FaceValue.Amount(),
			"fee_minor":            offer.Fee.Amount(),
			"disbursed_minor":      offer.Disbursed.Amount(),
			"repayment_minor":      offer.Repayment.Amount(),
			"currency":             string(offer.FaceValue.Currency()),
			"fee_model":            string(offer.FeeModel),
			"decision_snapshot_id": offer.DecisionSnapshotID,
			"product_config":       offer.ProductConfigVersionID,
		})
		if err != nil {
			return err
		}
		termsHash := sha256.Sum256(terms)
		if err := s.consents.Insert(ctx, tx, repo.Consent{
			ConsentID: platform.NewID("cns"), TelcoID: sub.TelcoID,
			AdvanceID: adv.AdvanceID, SubscriberAccountID: sub.SubscriberAccountID,
			DisclosedTerms: terms, ContentHash: hex.EncodeToString(termsHash[:]),
			Channel: "USSD",
		}); err != nil {
			return err
		}

		// Record the attempt BEFORE the network call: a crash after commit
		// leaves durable evidence the resolver can act on (EDG-007/008).
		wire, err := json.Marshal(map[string]any{
			"platform_request_id": adv.AdvanceID,
			"face_value_minor":    offer.FaceValue.Amount(),
			"currency":            string(offer.FaceValue.Currency()),
			"offer_snapshot_id":   offer.OfferID,
		})
		if err != nil {
			return err
		}
		attempt = entity.FulfilmentAttempt{
			AttemptID:           platform.NewID("att"),
			AdvanceID:           adv.AdvanceID,
			AttemptNo:           1,
			TelcoIdempotencyKey: platform.NewID("tik"),
			State:               entity.AttemptSent,
			RequestEvidence:     wire,
		}
		if err := s.attempts.Insert(ctx, tx, attempt); err != nil {
			return err
		}
		if err := s.advances.Transition(ctx, tx, adv.AdvanceID, 3, entity.AdvExposureReserved, entity.AdvPendingFulfilment); err != nil {
			return err
		}
		adv.State = entity.AdvPendingFulfilment
		adv.Version = 4
		return nil
	})
	var breach *treasury.BreachError
	switch {
	case errors.As(err, &breach):
		// M3d: the confirm aborted on a guardrail breach. Record the trip +
		// suspend the programme in a SEPARATE transaction (it must survive
		// the abort), then decline customer-safe. Crash between the two:
		// the next confirm re-detects and converges.
		if telcoID, terr := platform.TenantFrom(ctx); terr == nil {
			if terr := s.treasury.RecordTrip(ctx, telcoID, cmd.ProgrammeID, breach); terr != nil {
				s.Log.Error("guardrail trip recording failed — next confirm will re-detect", "err", terr)
			}
		}
		return ConfirmResult{}, fmt.Errorf("%w: %s", treasury.ErrProgrammeSuspended, breach.Guardrail)
	case errors.Is(err, errReplayRace):
		// Our idempotency key already has an advance (EDG-001): replay it.
		return s.replayByIdemKey(ctx, cmd.IdemKey)
	case errors.Is(err, repo.ErrConcurrentAdvanceBlocked):
		// One-active backstop fired. If OUR key created the open advance, a
		// concurrent duplicate of this very request won — replay. Otherwise
		// it is a genuine concurrency block (EDG-002): deterministic decline.
		if cmdHasExistingAdvance(ctx, s, cmd) {
			return s.replayByIdemKey(ctx, cmd.IdemKey)
		}
		return ConfirmResult{}, err
	case err != nil:
		return ConfirmResult{}, err
	}
	if replayed {
		return ConfirmResult{Advance: adv, Replayed: true}, nil
	}

	// ---- network: NO transaction open (V2-ADV-006) ------------------------
	res, err := s.Adapter.SubmitFulfilment(ctx, adv.TelcoID, attempt.TelcoIdempotencyKey, mno.FulfilmentRequest{
		PlatformRequestID:   adv.AdvanceID,
		SubscriberAccountID: adv.SubscriberAccountID,
		MSISDNToken:         cmd.MSISDNToken,
		ProductType:         "AIRTIME_ADVANCE",
		FaceValue:           adv.FaceValue,
		OfferSnapshotID:     adv.OfferID,
	})
	if err != nil {
		// Adapter returns errors only for config/programming faults; the
		// outcome for the advance is still unknowable — resolve as Unknown.
		s.Log.Error("adapter fault during submit; classifying unknown", "advance", adv.AdvanceID, "err", err)
		res = mno.Result{Outcome: mno.OutcomeUnknown, ResponseEvidence: []byte(fmt.Sprintf(`{"adapter_fault":%q}`, err.Error()))}
	}

	// ---- tx2: resolve outcome --------------------------------------------
	final, err := s.ResolveOutcome(ctx, adv.AdvanceID, attempt.AttemptID, res)
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Advance: final}, nil
}

var errReplayRace = errors.New("origination: idempotency replay race")

func cmdHasExistingAdvance(ctx context.Context, s *Service, cmd ConfirmCmd) bool {
	found := false
	_ = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := s.advances.GetByIdemKey(ctx, tx, cmd.IdemKey)
		found = err == nil
		return nil
	})
	return found
}

// GetAdvance is the status-route accessor (EDG-004: the durable status
// enquiry a customer uses after a dropped session). Tenant-scoped by RLS.
func (s *Service) GetAdvance(ctx context.Context, advanceID string) (entity.Advance, error) {
	var adv entity.Advance
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		adv, e = s.advances.Get(ctx, tx, advanceID)
		return e
	})
	return adv, err
}

func (s *Service) replayByIdemKey(ctx context.Context, idemKey string) (ConfirmResult, error) {
	var adv entity.Advance
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		adv, e = s.advances.GetByIdemKey(ctx, tx, idemKey)
		return e
	})
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Advance: adv, Replayed: true}, nil
}

// ResolveOutcome applies a fulfilment result to the advance — shared by the
// saga (tx2) and the M1b-4 resolver worker, so both paths have IDENTICAL
// semantics: ACTIVE+journal / FAILED+release / UNKNOWN+enquiry schedule.
func (s *Service) ResolveOutcome(ctx context.Context, advanceID, attemptID string, res mno.Result) (entity.Advance, error) {
	var out entity.Advance
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		adv, err := s.advances.Get(ctx, tx, advanceID)
		if err != nil {
			return err
		}
		// Already terminal/active (resolver raced us): idempotent no-op.
		if adv.State != entity.AdvPendingFulfilment && adv.State != entity.AdvFulfilmentUnknown {
			out = adv
			return nil
		}

		switch res.Outcome {
		case mno.OutcomeConfirmed:
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptConfirmed, res.TelcoReference, res.ResponseEvidence, nil); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvActive); err != nil {
				return err
			}
			if err := s.pools.ConfirmUtilisation(ctx, tx, adv.FundingPoolID, adv.Outstanding); err != nil {
				return err
			}
			// Ledger: recognition at confirmed fulfilment (A-10/V2-LED-006).
			lines := []ledger.Line{
				{Account: "SUBSCRIBER_RECEIVABLE", Side: ledger.Debit, Amount: adv.Outstanding},
				{Account: "AIRTIME_FUNDING_CLEARING", Side: ledger.Credit, Amount: adv.Disbursed},
			}
			if adv.Fee.IsPositive() {
				lines = append(lines, ledger.Line{Account: "FEE_INCOME", Side: ledger.Credit, Amount: adv.Fee})
			}
			if _, _, err := s.Ledger.Post(ctx, tx, ledger.Journal{
				BusinessEventKey: adv.AdvanceID + "/issued",
				EventType:        ledger.EventAdvanceIssued,
				TelcoID:          adv.TelcoID,
				ProgrammeID:      adv.ProgrammeID,
				AdvanceID:        adv.AdvanceID,
				CorrelationID:    adv.CorrelationID,
				Lines:            lines,
			}); err != nil {
				return err
			}
			if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentConfirmed"); err != nil {
				return err
			}

		case mno.OutcomeFailed, mno.OutcomeNotFound:
			// NotFound (from enquiry) = provably never landed = safe to fail.
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptFailed, res.TelcoReference, res.ResponseEvidence, nil); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvFulfilmentFailed); err != nil {
				return err
			}
			// Release exactly once — guarded by the FSM transition above
			// succeeding (V2-ADV-010).
			if err := s.pools.Release(ctx, tx, adv.FundingPoolID, adv.Outstanding); err != nil {
				return err
			}
			if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentFailed"); err != nil {
				return err
			}

		case mno.OutcomeUnknown:
			// Schedule the first enquiry from governed config (V2-ADV-009).
			next, err := s.firstEnquiryAt(ctx, adv.TelcoID)
			if err != nil {
				return err
			}
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptUnknown, "", res.ResponseEvidence, &next); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			// VR-7b: the FulfilmentUnknown event is emitted on STATE ENTRY
			// only — repeated still-unknown enquiry cycles reschedule quietly
			// instead of flooding the outbox with duplicates.
			if adv.State == entity.AdvPendingFulfilment {
				if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvFulfilmentUnknown); err != nil {
					return err
				}
				if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentUnknown"); err != nil {
					return err
				}
			}
			// NO ledger entry, NO utilisation, reservation stays (V2-LED-006).

		default:
			return fmt.Errorf("unrecognised adapter outcome %q", res.Outcome)
		}

		out, err = s.advances.Get(ctx, tx, adv.AdvanceID)
		return err
	})
	return out, err
}

// currentAttemptState infers the guard state for attempt resolution from the
// advance state (SENT before first resolution, UNKNOWN thereafter).
func currentAttemptState(adv entity.Advance) entity.FulfilmentAttemptState {
	if adv.State == entity.AdvFulfilmentUnknown {
		return entity.AttemptUnknown
	}
	return entity.AttemptSent
}

func (s *Service) firstEnquiryAt(ctx context.Context, telcoID string) (time.Time, error) {
	cv, err := s.Config.ActiveAt(ctx, "advance.fulfilment", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return time.Time{}, fmt.Errorf("advance.fulfilment config: %w", err)
	}
	var fc fulfilmentCfg
	if err := json.Unmarshal(cv.Content, &fc); err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC().Add(time.Duration(fc.StatusEnquiryDelaysSeconds[0]) * time.Second), nil
}

func (s *Service) emitOutbox(ctx context.Context, tx pgx.Tx, adv entity.Advance, eventType string) error {
	payload, err := json.Marshal(map[string]string{
		"advance_id":     adv.AdvanceID,
		"programme_id":   adv.ProgrammeID,
		"correlation_id": adv.CorrelationID, // BC-6 lineage; no PII (V2-EVT-010)
	})
	if err != nil {
		return err
	}
	return s.outbox.Append(ctx, tx, entity.OutboxEvent{
		ID: platform.NewID("evt"), TelcoID: adv.TelcoID, AggregateType: "Advance",
		AggregateID: adv.AdvanceID, EventType: eventType, SchemaVersion: 1,
		Payload: payload, OccurredAt: time.Now().UTC(),
	})
}
