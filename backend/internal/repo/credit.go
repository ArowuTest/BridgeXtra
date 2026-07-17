package repo

// Credit-core repositories (M1). All money crosses the DB boundary here and
// ONLY here (ADR-0002): BIGINT minor + CHAR(3) <-> entity.Money. Unique-
// violation constraint names map to typed domain errors so the saga never
// string-matches SQL errors (BC-7).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// Typed domain errors surfaced from schema constraints (BC-7).
var (
	// ErrConcurrentAdvanceBlocked: advances_one_active_uq — the subscriber
	// already has an open advance (V1-PRD-005, EDG-002).
	ErrConcurrentAdvanceBlocked = errors.New("repo: subscriber already has an open advance")
	// ErrOfferAlreadyUsed: advances_offer_uq (M1B-F4) — this offer has
	// already birthed an advance.
	ErrOfferAlreadyUsed = errors.New("repo: offer already used by another advance")
	// ErrNoFundingCapacity: no ACTIVE pool with headroom (V1-TRE-010 fail closed).
	ErrNoFundingCapacity = errors.New("repo: no funding capacity available")
	// ErrStaleVersion: optimistic FSM guard lost the race (V2-ADV-007).
	ErrStaleVersion = errors.New("repo: advance modified concurrently, reload and re-evaluate")
	// ErrIllegalTransition: transition not in the FSM table (V2-ADV-008).
	ErrIllegalTransition = errors.New("repo: illegal advance state transition")
)

func constraintErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "advances_one_active_uq":
			return fmt.Errorf("%w (%s)", ErrConcurrentAdvanceBlocked, pgErr.ConstraintName)
		case "advances_offer_uq":
			return fmt.Errorf("%w (%s)", ErrOfferAlreadyUsed, pgErr.ConstraintName)
		}
	}
	return err
}

func scanMoney(minor int64, cur string) (entity.Money, error) {
	return entity.NewMoney(minor, entity.Currency(cur))
}

// ---------------------------------------------------------------------------
// Subscribers
// ---------------------------------------------------------------------------

type Subscribers struct{}

// GetLiveByToken resolves the LIVE identity period for a token (tenant-scoped
// by RLS). Index: subscriber_live_identity_uq.
func (Subscribers) GetLiveByToken(ctx context.Context, tx pgx.Tx, msisdnToken string) (entity.SubscriberAccount, error) {
	var s entity.SubscriberAccount
	err := tx.QueryRow(ctx, `
		SELECT subscriber_account_id, telco_id, msisdn_token, status, effective_from, effective_to
		FROM subscriber_accounts
		WHERE msisdn_token = $1 AND effective_to IS NULL`, msisdnToken).
		Scan(&s.SubscriberAccountID, &s.TelcoID, &s.MSISDNToken, &s.Status, &s.EffectiveFrom, &s.EffectiveTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("subscriber token: %w", ErrNotFound)
	}
	return s, err
}

// ---------------------------------------------------------------------------
// Decision snapshots
// ---------------------------------------------------------------------------

type Decisions struct{}

// GetCurrent is the hot-path point read (V2-TAR-004). Index: decision_current_uq.
func (Decisions) GetCurrent(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (entity.DecisionSnapshot, error) {
	var d entity.DecisionSnapshot
	var minor int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor, currency,
		       is_current, config_version_id, created_at
		FROM decision_snapshots
		WHERE subscriber_account_id = $1 AND is_current`, subscriberAccountID).
		Scan(&d.DecisionSnapshotID, &d.TelcoID, &d.SubscriberAccountID, &minor, &cur,
			&d.IsCurrent, &d.ConfigVersionID, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("current decision: %w", ErrNotFound)
	}
	if err != nil {
		return d, err
	}
	d.MaxFaceValue, err = scanMoney(minor, cur)
	return d, err
}

// ---------------------------------------------------------------------------
// Offers
// ---------------------------------------------------------------------------

type Offers struct{}

func (Offers) Insert(ctx context.Context, tx pgx.Tx, o entity.Offer) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO offers (offer_id, telco_id, programme_id, subscriber_account_id, decision_snapshot_id,
		  face_value_minor, fee_minor, disbursed_minor, repayment_minor, currency, fee_model,
		  product_config_version_id, state, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		o.OfferID, o.TelcoID, o.ProgrammeID, o.SubscriberAccountID, o.DecisionSnapshotID,
		o.FaceValue.Amount(), o.Fee.Amount(), o.Disbursed.Amount(), o.Repayment.Amount(),
		string(o.FaceValue.Currency()), o.FeeModel, o.ProductConfigVersionID, o.State, o.ExpiresAt)
	return err
}

func offerScan(row pgx.Row) (entity.Offer, error) {
	var o entity.Offer
	var face, fee, disb, rep int64
	var cur string
	err := row.Scan(&o.OfferID, &o.TelcoID, &o.ProgrammeID, &o.SubscriberAccountID, &o.DecisionSnapshotID,
		&face, &fee, &disb, &rep, &cur, &o.FeeModel, &o.ProductConfigVersionID, &o.State, &o.ExpiresAt, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	c := entity.Currency(cur)
	if o.FaceValue, err = entity.NewMoney(face, c); err != nil {
		return o, err
	}
	if o.Fee, err = entity.NewMoney(fee, c); err != nil {
		return o, err
	}
	if o.Disbursed, err = entity.NewMoney(disb, c); err != nil {
		return o, err
	}
	o.Repayment, err = entity.NewMoney(rep, c)
	return o, err
}

const offerCols = `offer_id, telco_id, programme_id, subscriber_account_id, decision_snapshot_id,
	face_value_minor, fee_minor, disbursed_minor, repayment_minor, currency, fee_model,
	product_config_version_id, state, expires_at, created_at`

// GetForUpdate row-locks the offer for the acceptance race (V2-OFR-003).
func (Offers) GetForUpdate(ctx context.Context, tx pgx.Tx, offerID string) (entity.Offer, error) {
	return offerScan(tx.QueryRow(ctx,
		`SELECT `+offerCols+` FROM offers WHERE offer_id = $1 FOR UPDATE`, offerID))
}

// ListValid returns non-expired GENERATED offers (V2-OFR-009 reuse).
// Index: offers_active_ix.
func (Offers) ListValid(ctx context.Context, tx pgx.Tx, subscriberAccountID, programmeID string, now time.Time) ([]entity.Offer, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+offerCols+` FROM offers
		WHERE subscriber_account_id = $1 AND programme_id = $2
		  AND state = 'GENERATED' AND expires_at > $3
		ORDER BY face_value_minor`, subscriberAccountID, programmeID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.Offer
	for rows.Next() {
		o, err := offerScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetState transitions an offer with a from-state guard.
func (Offers) SetState(ctx context.Context, tx pgx.Tx, offerID string, from, to entity.OfferState) error {
	ct, err := tx.Exec(ctx,
		`UPDATE offers SET state = $3 WHERE offer_id = $1 AND state = $2`, offerID, from, to)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("offer %s not in state %s: %w", offerID, from, ErrNotFound)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Funding pools (V2-TRE-002: atomic conditional reservation — never
// read-then-write)
// ---------------------------------------------------------------------------

type FundingPools struct{}

// Reserve atomically reserves headroom in one ACTIVE pool of the programme.
// Zero rows = no capacity: fail closed (V1-TRE-010, EDG-023).
//
// Deliberately plain FOR UPDATE, NOT SKIP LOCKED: with a single pool per
// programme, SKIP LOCKED makes concurrent reservations skip the briefly-
// locked row and misreport "no capacity" (spurious declines under load —
// caught by the EDG-002 concurrency test). Contenders wait the few ms for
// the row lock; the headroom predicate is re-evaluated under the lock, so
// over-allocation remains impossible (plus the table CHECK as backstop).
// Lock ordering (VR-7c, post-0006): offer row -> advance INSERT (one-active
// decided) -> pool row. Only the one-active winner ever reaches the pool,
// which is what makes the ordering deadlock-safe.
func (FundingPools) Reserve(ctx context.Context, tx pgx.Tx, programmeID string, amount entity.Money) (poolID string, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE funding_pools SET reserved_minor = reserved_minor + $1
		WHERE pool_id = (
			SELECT pool_id FROM funding_pools
			WHERE programme_id = $2 AND status = 'ACTIVE' AND currency = $3
			  AND committed_minor - reserved_minor - utilised_minor >= $1
			ORDER BY pool_id
			LIMIT 1
			FOR UPDATE)
		RETURNING pool_id`,
		amount.Amount(), programmeID, string(amount.Currency())).Scan(&poolID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoFundingCapacity
	}
	return poolID, err
}

// ConfirmUtilisation moves a reservation to utilised on confirmed fulfilment.
func (FundingPools) ConfirmUtilisation(ctx context.Context, tx pgx.Tx, poolID string, amount entity.Money) error {
	ct, err := tx.Exec(ctx, `
		UPDATE funding_pools
		SET reserved_minor = reserved_minor - $1, utilised_minor = utilised_minor + $1
		WHERE pool_id = $2 AND reserved_minor >= $1`, amount.Amount(), poolID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pool %s: reservation underflow confirming %s", poolID, amount)
	}
	return nil
}

// Release returns a reservation on failed fulfilment — exactly once by the
// caller's FSM guard (V2-ADV-010).
func (FundingPools) Release(ctx context.Context, tx pgx.Tx, poolID string, amount entity.Money) error {
	ct, err := tx.Exec(ctx, `
		UPDATE funding_pools SET reserved_minor = reserved_minor - $1
		WHERE pool_id = $2 AND reserved_minor >= $1`, amount.Amount(), poolID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pool %s: reservation underflow releasing %s", poolID, amount)
	}
	return nil
}

// ReduceUtilisation reflects recovered value (M1b-4).
func (FundingPools) ReduceUtilisation(ctx context.Context, tx pgx.Tx, poolID string, amount entity.Money) error {
	ct, err := tx.Exec(ctx, `
		UPDATE funding_pools SET utilised_minor = utilised_minor - $1
		WHERE pool_id = $2 AND utilised_minor >= $1`, amount.Amount(), poolID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pool %s: utilisation underflow reducing %s", poolID, amount)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Advances
// ---------------------------------------------------------------------------

type Advances struct{}

const advanceCols = `advance_id, telco_id, programme_id, subscriber_account_id, offer_id, funding_pool_id,
	idempotency_key, correlation_id, state, version, face_value_minor, fee_minor, disbursed_minor,
	outstanding_minor, currency, accepted_at, activated_at, closed_at, updated_at`

func advanceScan(row pgx.Row) (entity.Advance, error) {
	var a entity.Advance
	var face, fee, disb, out int64
	var cur string
	var poolID *string // NULL until EXPOSURE_RESERVED (0006)
	err := row.Scan(&a.AdvanceID, &a.TelcoID, &a.ProgrammeID, &a.SubscriberAccountID, &a.OfferID,
		&poolID, &a.IdempotencyKey, &a.CorrelationID, &a.State, &a.Version,
		&face, &fee, &disb, &out, &cur, &a.AcceptedAt, &a.ActivatedAt, &a.ClosedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if poolID != nil {
		a.FundingPoolID = *poolID
	}
	c := entity.Currency(cur)
	if a.FaceValue, err = entity.NewMoney(face, c); err != nil {
		return a, err
	}
	if a.Fee, err = entity.NewMoney(fee, c); err != nil {
		return a, err
	}
	if a.Disbursed, err = entity.NewMoney(disb, c); err != nil {
		return a, err
	}
	a.Outstanding, err = entity.NewMoney(out, c)
	return a, err
}

// Insert creates the advance in REQUESTED, pool-less (0006: the one-active
// contest is decided HERE, before any pool lock is taken). Returns:
//   - (created=true)  on success;
//   - (created=false) when the idempotency key already exists (EDG-001 replay);
//   - typed errors for one-active / offer-reuse violations.
func (Advances) Insert(ctx context.Context, tx pgx.Tx, a entity.Advance) (created bool, err error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO advances (advance_id, telco_id, programme_id, subscriber_account_id, offer_id,
		  funding_pool_id, idempotency_key, correlation_id, state, version,
		  face_value_minor, fee_minor, disbursed_minor, outstanding_minor, currency)
		VALUES ($1,$2,$3,$4,$5,NULL,$6,$7,$8,1,$9,$10,$11,$12,$13)
		ON CONFLICT (telco_id, idempotency_key) DO NOTHING`,
		a.AdvanceID, a.TelcoID, a.ProgrammeID, a.SubscriberAccountID, a.OfferID,
		a.IdempotencyKey, a.CorrelationID, a.State,
		a.FaceValue.Amount(), a.Fee.Amount(), a.Disbursed.Amount(),
		a.Outstanding.Amount(), string(a.FaceValue.Currency()))
	if err != nil {
		return false, constraintErr(err)
	}
	return ct.RowsAffected() == 1, nil
}

// ReserveTransition atomically assigns the funding pool and moves
// VALIDATED -> EXPOSURE_RESERVED (the 0006 CHECK requires the pool from this
// state onward; doing both in one statement makes the constraint unviolable).
func (Advances) ReserveTransition(ctx context.Context, tx pgx.Tx, advanceID string, fromVersion int, poolID string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE advances SET state = 'EXPOSURE_RESERVED', funding_pool_id = $3,
		  version = version + 1, updated_at = now()
		WHERE advance_id = $1 AND version = $2 AND state = 'VALIDATED'`,
		advanceID, fromVersion, poolID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("advance %s v%d VALIDATED->EXPOSURE_RESERVED: %w", advanceID, fromVersion, ErrStaleVersion)
	}
	return nil
}

func (Advances) Get(ctx context.Context, tx pgx.Tx, advanceID string) (entity.Advance, error) {
	return advanceScan(tx.QueryRow(ctx,
		`SELECT `+advanceCols+` FROM advances WHERE advance_id = $1`, advanceID))
}

func (Advances) GetByIdemKey(ctx context.Context, tx pgx.Tx, idemKey string) (entity.Advance, error) {
	return advanceScan(tx.QueryRow(ctx,
		`SELECT `+advanceCols+` FROM advances WHERE idempotency_key = $1`, idemKey))
}

// Transition moves the FSM under BOTH guards: the entity transition table
// (V2-ADV-008) and the optimistic version check (V2-ADV-007). Zero rows with
// a legal transition = concurrent modification (ErrStaleVersion).
func (Advances) Transition(ctx context.Context, tx pgx.Tx, advanceID string, fromVersion int, from, to entity.AdvanceState) error {
	if !entity.CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE advances SET state = $4, version = version + 1, updated_at = now(),
		  activated_at = CASE WHEN $4 = 'ACTIVE' THEN now() ELSE activated_at END,
		  closed_at    = CASE WHEN $4 IN ('CLOSED','FULFILMENT_FAILED','DECLINED') THEN now() ELSE closed_at END
		WHERE advance_id = $1 AND version = $2 AND state = $3`,
		advanceID, fromVersion, from, to)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("advance %s v%d %s->%s: %w", advanceID, fromVersion, from, to, ErrStaleVersion)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Fulfilment attempts
// ---------------------------------------------------------------------------

type Attempts struct{}

func (Attempts) Insert(ctx context.Context, tx pgx.Tx, at entity.FulfilmentAttempt) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO fulfilment_attempts (attempt_id, advance_id, attempt_no, telco_idempotency_key,
		  state, request_evidence)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		at.AttemptID, at.AdvanceID, at.AttemptNo, at.TelcoIdempotencyKey, at.State, at.RequestEvidence)
	return err
}

// Resolve finalises an attempt outcome with a from-state guard.
func (Attempts) Resolve(ctx context.Context, tx pgx.Tx, attemptID string, from, to entity.FulfilmentAttemptState, telcoRef string, responseEvidence []byte, nextEnquiryAt *time.Time) error {
	ct, err := tx.Exec(ctx, `
		UPDATE fulfilment_attempts
		SET state = $3, telco_reference = NULLIF($4,''), response_evidence = $5,
		    next_enquiry_at = $6,
		    resolved_at = CASE WHEN $3 IN ('CONFIRMED','FAILED') THEN now() ELSE resolved_at END
		WHERE attempt_id = $1 AND state = $2`,
		attemptID, from, to, telcoRef, responseEvidence, nextEnquiryAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("attempt %s not in state %s: %w", attemptID, from, ErrNotFound)
	}
	return nil
}

// GetByAdvance returns the latest attempt for an advance.
func (Attempts) GetByAdvance(ctx context.Context, tx pgx.Tx, advanceID string) (entity.FulfilmentAttempt, error) {
	var at entity.FulfilmentAttempt
	var ref *string
	err := tx.QueryRow(ctx, `
		SELECT attempt_id, advance_id, attempt_no, telco_idempotency_key, state, telco_reference,
		       request_evidence, response_evidence, submitted_at, next_enquiry_at, enquiry_count, resolved_at
		FROM fulfilment_attempts WHERE advance_id = $1
		ORDER BY attempt_no DESC LIMIT 1`, advanceID).
		Scan(&at.AttemptID, &at.AdvanceID, &at.AttemptNo, &at.TelcoIdempotencyKey, &at.State, &ref,
			&at.RequestEvidence, &at.ResponseEvidence, &at.SubmittedAt, &at.NextEnquiryAt, &at.EnquiryCount, &at.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, ErrNotFound
	}
	if ref != nil {
		at.TelcoReference = *ref
	}
	return at, err
}
