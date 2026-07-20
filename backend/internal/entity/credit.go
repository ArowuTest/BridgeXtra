package entity

// M1 credit-core domain types. All money is Money (BC-1); state machines are
// explicit transition tables so the FSM test can be exhaustive (BC-8).

import "time"

// ---------------------------------------------------------------------------
// Advance FSM (V2 §13.1). Delinquency is an overlay classification, not a
// state (SRS v3 D-2 resolution).
// ---------------------------------------------------------------------------

type AdvanceState string

const (
	AdvRequested          AdvanceState = "REQUESTED"
	AdvValidated          AdvanceState = "VALIDATED"
	AdvExposureReserved   AdvanceState = "EXPOSURE_RESERVED"
	AdvPendingFulfilment  AdvanceState = "PENDING_FULFILMENT"
	AdvFulfilmentUnknown  AdvanceState = "FULFILMENT_UNKNOWN"
	AdvActive             AdvanceState = "ACTIVE"
	AdvPartiallyRecovered AdvanceState = "PARTIALLY_RECOVERED"
	AdvClosed             AdvanceState = "CLOSED"
	AdvFulfilmentFailed   AdvanceState = "FULFILMENT_FAILED"
	AdvDeclined           AdvanceState = "DECLINED"
	AdvWrittenOff         AdvanceState = "WRITTEN_OFF" // M3: loss crystallised (maker-checker)
)

// advanceTransitions is THE transition table (V2-ADV-008: only permitted
// transitions accepted; everything else rejected and audited).
var advanceTransitions = map[AdvanceState][]AdvanceState{
	AdvRequested:          {AdvValidated, AdvDeclined},
	AdvValidated:          {AdvExposureReserved, AdvDeclined},
	AdvExposureReserved:   {AdvPendingFulfilment, AdvDeclined},
	AdvPendingFulfilment:  {AdvActive, AdvFulfilmentFailed, AdvFulfilmentUnknown},
	AdvFulfilmentUnknown:  {AdvActive, AdvFulfilmentFailed},
	AdvActive:             {AdvPartiallyRecovered, AdvClosed, AdvWrittenOff},
	AdvPartiallyRecovered: {AdvPartiallyRecovered, AdvClosed, AdvWrittenOff},
	// CLOSED re-opens ONLY on recovery reversal (EDG-019 matrix): the
	// clawed-back amount is owed again, so the book must say so. This is the
	// controlled-reversal transition — it happens inside the reversal
	// transaction, never by hand.
	AdvClosed: {AdvPartiallyRecovered},
	// FULFILMENT_FAILED / DECLINED / WRITTEN_OFF are terminal. Post-write-off
	// recoveries are recovery INCOME (EDG-021) — they never re-open the
	// advance; the loss stays crystallised in the record.
}

// CanTransition reports whether from → to is a legal advance transition.
func CanTransition(from, to AdvanceState) bool {
	for _, t := range advanceTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// AdvanceStates lists every state (for exhaustive FSM tests).
func AdvanceStates() []AdvanceState {
	return []AdvanceState{
		AdvRequested, AdvValidated, AdvExposureReserved, AdvPendingFulfilment,
		AdvFulfilmentUnknown, AdvActive, AdvPartiallyRecovered, AdvClosed,
		AdvFulfilmentFailed, AdvDeclined, AdvWrittenOff,
	}
}

// OpenAdvanceStates are the states covered by the one-active partial unique
// index — MUST stay in sync with advances_one_active_uq (0004).
func OpenAdvanceStates() []AdvanceState {
	return []AdvanceState{
		AdvRequested, AdvValidated, AdvExposureReserved, AdvPendingFulfilment,
		AdvFulfilmentUnknown, AdvActive, AdvPartiallyRecovered,
	}
}

// ---------------------------------------------------------------------------
// Aggregates
// ---------------------------------------------------------------------------

type SubscriberAccount struct {
	SubscriberAccountID string
	TelcoID             string
	MSISDNToken         string
	Status              string
	EffectiveFrom       time.Time
	EffectiveTo         *time.Time
}

type DecisionSnapshot struct {
	DecisionSnapshotID  string
	TelcoID             string
	SubscriberAccountID string
	MaxFaceValue        Money
	IsCurrent           bool
	ConfigVersionID     string
	CreatedAt           time.Time

	// M2 canonical-result fields (§11.2). TierCode 'SEED' marks pre-M2 seeds;
	// scored decisions carry full provenance + replay pins (0007/0009 CHECKs).
	TierCode          string
	ReasonCodes       []byte // JSON array
	FeatureSnapshotID string
	ScoringRunID      string
	ValidUntil        *time.Time
	DecisionHash      string
	DecisionDoc       []byte // canonical engine output (bit-exact replay target)
	PriorTierCode     string
	ScoredAt          *time.Time
}

// ScoringRun is one batch decisioning run, pinned to a policy version
// (V2-SCR-018 control totals).
type ScoringRun struct {
	ScoringRunID    string
	TelcoID         string
	ProgrammeID     string
	FeatureFileID   string
	PolicyVersionID string
	Status          string // RUNNING | COMPLETED | FAILED
	SubjectsTotal   int
	SubjectsScored  int
	SubjectsSkipped int
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// ---------------------------------------------------------------------------
// Feature store (M2b, V2-SCR-001/002)
// ---------------------------------------------------------------------------

type FeatureFile struct {
	FeatureFileID   string
	TelcoID         string
	Source          string
	AsOf            time.Time
	ContentHash     string
	RowCount        int
	QuarantinedRows int
	Status          string // INGESTED | QUARANTINED
	ReceivedAt      time.Time
}

// FeatureSnapshot is one subscriber's features at one as-of cut. Features and
// Quality are canonical JSON (integer quantities only — BC-1 float ban covers
// the scoring perimeter); ContentHash pins the exact bytes for BC-4 replay.
type FeatureSnapshot struct {
	FeatureSnapshotID   string
	TelcoID             string
	SubscriberAccountID string
	FeatureFileID       string
	AsOf                time.Time
	Features            []byte
	Quality             []byte
	ContentHash         string
	CreatedAt           time.Time
}

type FeeModel string

const (
	FeeDeductedUpfront  FeeModel = "DEDUCTED_UPFRONT"
	FeeAddedToRepayment FeeModel = "ADDED_TO_REPAYMENT"
)

type OfferState string

const (
	OfferGenerated  OfferState = "GENERATED"
	OfferAccepted   OfferState = "ACCEPTED"
	OfferExpired    OfferState = "EXPIRED"
	OfferWithdrawn  OfferState = "WITHDRAWN"
	OfferSuperseded OfferState = "SUPERSEDED"
)

type Offer struct {
	OfferID                string
	TelcoID                string
	ProgrammeID            string
	SubscriberAccountID    string
	DecisionSnapshotID     string
	FaceValue              Money
	Fee                    Money
	Disbursed              Money
	Repayment              Money
	FeeModel               FeeModel
	ProductConfigVersionID string
	State                  OfferState
	ExpiresAt              time.Time
	CreatedAt              time.Time
}

// DisclosureSnapshot is the exact disclosure presented for one offer (R-P0-7),
// minted at menu generation and referenced back at confirm. Append-only; its
// integrity is the server-computed ContentHash. 1:1 with Offer.
type DisclosureSnapshot struct {
	DisclosureSnapshotID      string
	TelcoID                   string
	ProgrammeID               string
	OfferID                   string
	TemplateID                string
	TemplateVersion           string
	Locale                    string
	DisclosureConfigVersionID string
	Currency                  Currency
	FaceValue                 Money
	Fee                       Money
	Disbursed                 Money
	Repayment                 Money
	RenderedBody              string
	TotalCostText             string
	ContentHash               string
	IssuedAt                  time.Time
	ExpiresAt                 time.Time
}

type Advance struct {
	AdvanceID           string
	TelcoID             string
	ProgrammeID         string
	SubscriberAccountID string
	OfferID             string
	FundingPoolID       string
	IdempotencyKey      string
	CorrelationID       string
	State               AdvanceState
	Version             int
	FaceValue           Money
	Fee                 Money
	Disbursed           Money
	Outstanding         Money
	AcceptedAt          time.Time
	ActivatedAt         *time.Time
	ClosedAt            *time.Time
	UpdatedAt           time.Time
}

type FulfilmentAttemptState string

const (
	AttemptSent      FulfilmentAttemptState = "SENT"
	AttemptConfirmed FulfilmentAttemptState = "CONFIRMED"
	AttemptFailed    FulfilmentAttemptState = "FAILED"
	AttemptUnknown   FulfilmentAttemptState = "UNKNOWN"
)

type FulfilmentAttempt struct {
	AttemptID           string
	AdvanceID           string
	AttemptNo           int
	TelcoIdempotencyKey string
	State               FulfilmentAttemptState
	TelcoReference      string
	RequestEvidence     []byte
	ResponseEvidence    []byte
	SubmittedAt         time.Time
	NextEnquiryAt       *time.Time
	EnquiryCount        int
	ResolvedAt          *time.Time
}

type RecoveryEventState string

const (
	RecoveryPending     RecoveryEventState = "PENDING"
	RecoveryAllocated   RecoveryEventState = "ALLOCATED"
	RecoveryQuarantined RecoveryEventState = "QUARANTINED"
	RecoveryUnmatched   RecoveryEventState = "UNMATCHED"
	RecoveryReversed    RecoveryEventState = "REVERSED" // M3b: fully clawed back (EDG-019)
)

type RecoveryEvent struct {
	RecoveryEventID     string
	TelcoID             string
	SourceEventID       string
	SubscriberAccountID string
	Amount              Money
	State               RecoveryEventState
	OccurredAt          time.Time
	ReceivedAt          time.Time
}

type AllocationComponent string

const (
	ComponentFee       AllocationComponent = "FEE"
	ComponentPrincipal AllocationComponent = "PRINCIPAL"
	// ComponentWriteoffIncome marks post-write-off recoveries (EDG-021):
	// income against a crystallised loss, never receivable repayment.
	ComponentWriteoffIncome AllocationComponent = "WRITEOFF_INCOME"
)

type RecoveryAllocation struct {
	AllocationID    string
	RecoveryEventID string
	AdvanceID       string
	Component       AllocationComponent
	Amount          Money
	CreatedAt       time.Time
}

type FundingPool struct {
	PoolID      string
	ProgrammeID string
	TelcoID     string
	Currency    Currency
	Committed   Money
	Reserved    Money
	Utilised    Money
	Status      string
}
