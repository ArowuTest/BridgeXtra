// Package entity holds pure domain types and enum constants. No DB tags, no
// JSON coupling, no SQL. Money is always int64 minor units plus an ISO
// currency code (V2-API-005).
package entity

import "time"

// ---------------------------------------------------------------------------
// Telco / Programme (V1 §5 core enterprise objects)
// ---------------------------------------------------------------------------

type TelcoStatus string

const (
	TelcoInactive      TelcoStatus = "INACTIVE"
	TelcoCertification TelcoStatus = "CERTIFICATION"
	TelcoActive        TelcoStatus = "ACTIVE"
	TelcoSuspended     TelcoStatus = "SUSPENDED"
)

type Telco struct {
	TelcoID   string
	Name      string
	Country   string
	Status    TelcoStatus
	CreatedAt time.Time
}

type ProgrammeStatus string

const (
	ProgrammeDraft         ProgrammeStatus = "DRAFT"
	ProgrammeCertification ProgrammeStatus = "CERTIFICATION"
	ProgrammeActive        ProgrammeStatus = "ACTIVE"
	ProgrammeSuspended     ProgrammeStatus = "SUSPENDED"
	ProgrammeClosed        ProgrammeStatus = "CLOSED"
)

type Programme struct {
	ProgrammeID string
	TelcoID     string
	Code        string
	Name        string
	Status      ProgrammeStatus
	CreatedAt   time.Time
}

// ---------------------------------------------------------------------------
// Governed configuration (V2-CFG-001 lifecycle states)
// ---------------------------------------------------------------------------

type ConfigState string

const (
	ConfigDraft      ConfigState = "DRAFT"
	ConfigSubmitted  ConfigState = "SUBMITTED"
	ConfigApproved   ConfigState = "APPROVED"
	ConfigScheduled  ConfigState = "SCHEDULED"
	ConfigActive     ConfigState = "ACTIVE"
	ConfigSuperseded ConfigState = "SUPERSEDED"
	ConfigRolledBack ConfigState = "ROLLED_BACK"
	ConfigRejected   ConfigState = "REJECTED"
)

// ScopeGlobal is the scope for platform-wide configuration records.
const ScopeGlobal = "global"

type ConfigVersion struct {
	ConfigVersionID string
	Domain          string
	Scope           string
	VersionNo       int
	State           ConfigState
	Content         []byte // canonical JSON
	ContentHash     string
	EffectiveFrom   *time.Time
	EffectiveTo     *time.Time
	CreatedBy       string
	ApprovedBy      string
	Reason          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ConfigSummary is the per-(domain,scope) governance overview: the version
// currently in force plus how many versions sit in the pre-active pipeline.
type ConfigSummary struct {
	Domain          string
	Scope           string
	ActiveVersionNo int // 0 = nothing active
	ActiveSince     *time.Time
	PendingCount    int // DRAFT + SUBMITTED + APPROVED
}

// ---------------------------------------------------------------------------
// Idempotency (V2-API-003)
// ---------------------------------------------------------------------------

type IdempotencyRecord struct {
	TelcoID        string
	Operation      string
	IdemKey        string
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
	Terminal       bool
	CreatedAt      time.Time
}

// ---------------------------------------------------------------------------
// Outbox (V2-EVT-001 canonical envelope; SF-4 ordering via DB seq)
// ---------------------------------------------------------------------------

type OutboxEvent struct {
	Seq           int64
	ID            string
	TelcoID       string
	AggregateType string
	AggregateID   string
	EventType     string
	SchemaVersion int
	Payload       []byte
	OccurredAt    time.Time
	PublishedAt   *time.Time
	Attempts      int
	LastError     string
}

// ---------------------------------------------------------------------------
// Audit (V2-OBS-005)
// ---------------------------------------------------------------------------

type AuditEvent struct {
	ID         string
	TelcoID    string // empty = platform scope
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Reason     string
	Detail     []byte
	SourceIP   string
	OccurredAt time.Time
}

// Well-known audit actions.
const (
	AuditTenantContextMismatch = "TENANT_CONTEXT_MISMATCH" // V2-TEN-003 / EDG-026
	AuditConfigSubmitted       = "CONFIG_SUBMITTED"
	AuditConfigApproved        = "CONFIG_APPROVED"
	AuditConfigActivated       = "CONFIG_ACTIVATED"
	AuditConfigRejected        = "CONFIG_REJECTED"
)
