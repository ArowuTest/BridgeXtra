// Package configsvc implements governed configuration (V1-CFG-001..010,
// V2-CFG-001..014): draft -> submit -> approve (maker != checker) -> activate,
// with domain validators that run BEFORE any state can become APPROVED.
//
// SF-2 lives here: the product.concurrency validator checks the REAL database
// schema and refuses a value the schema would silently override — a config
// knob that cannot take effect must be impossible to set (armed-but-dead
// prevention; zero-config-floor discipline).
package configsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

type Service struct {
	Pool    *pgxpool.Pool // platform-role pool (config is global data)
	configs repo.ConfigVersions
	audit   repo.Audit
}

func New(pool *pgxpool.Pool) *Service { return &Service{Pool: pool} }

// Validator checks a domain's content before approval. It receives the tx so
// structural validators (SF-2) can interrogate the live schema.
type Validator func(ctx context.Context, tx pgx.Tx, content json.RawMessage) error

// validators is the domain registry. Adding a config domain without a
// validator is a review-visible decision (nil = structural checks only).
var validators = map[string]Validator{
	"product.concurrency":  validateConcurrency,
	"platform.idempotency": validateIdempotencyTTL,
	"platform.outbox":      validateOutbox,
}

// CreateDraft creates a new draft version for (domain, scope).
func (s *Service) CreateDraft(ctx context.Context, domain, scope, createdBy, reason string, content json.RawMessage) (entity.ConfigVersion, error) {
	if domain == "" || scope == "" || createdBy == "" || reason == "" {
		return entity.ConfigVersion{}, fmt.Errorf("domain, scope, created_by and reason are required")
	}
	if !json.Valid(content) {
		return entity.ConfigVersion{}, fmt.Errorf("content is not valid JSON")
	}
	sum := sha256.Sum256(content)
	c := entity.ConfigVersion{
		ConfigVersionID: platform.NewID("cfg"),
		Domain:          domain,
		Scope:           scope,
		State:           entity.ConfigDraft,
		Content:         content,
		ContentHash:     hex.EncodeToString(sum[:]),
		CreatedBy:       createdBy,
		Reason:          reason,
	}
	err := repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		n, err := s.configs.NextVersionNo(ctx, tx, domain, scope)
		if err != nil {
			return err
		}
		c.VersionNo = n
		return s.configs.Insert(ctx, tx, c)
	})
	return c, err
}

func (s *Service) Submit(ctx context.Context, id, actor string) error {
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.configs.TransitionState(ctx, tx, id, entity.ConfigDraft, entity.ConfigSubmitted, ""); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, entity.AuditConfigSubmitted, id, "")
	})
}

// Approve enforces maker != checker (V1-CFG-003 / V2-CFG-002) and runs the
// domain validator INSIDE the same transaction — validation and approval
// cannot be split by a concurrent schema or content change.
func (s *Service) Approve(ctx context.Context, id, approver string) error {
	if approver == "" {
		return fmt.Errorf("approver identity required")
	}
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.configs.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.CreatedBy == approver {
			// The DB CHECK constraint backs this, but reject with a clear error first.
			return fmt.Errorf("maker-checker violation: %q created this version and cannot approve it (V2-CFG-002)", approver)
		}
		if v, ok := validators[c.Domain]; ok && v != nil {
			if err := v(ctx, tx, c.Content); err != nil {
				return fmt.Errorf("validation failed for domain %s: %w", c.Domain, err)
			}
		}
		if err := s.configs.TransitionState(ctx, tx, id, entity.ConfigSubmitted, entity.ConfigApproved, approver); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, approver, entity.AuditConfigApproved, id, "")
	})
}

// Activate makes an APPROVED version effective (superseding the current ACTIVE
// one atomically). Re-runs the validator: the schema may have changed between
// approval and activation.
func (s *Service) Activate(ctx context.Context, id, actor string, at time.Time) error {
	return repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.configs.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if v, ok := validators[c.Domain]; ok && v != nil {
			if err := v(ctx, tx, c.Content); err != nil {
				return fmt.Errorf("activation validation failed for domain %s: %w", c.Domain, err)
			}
		}
		if err := s.configs.Activate(ctx, tx, id, at); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, entity.AuditConfigActivated, id, "")
	})
}

// ActiveAt resolves the effective version for (domain, scope) at t, falling
// back scope -> global so tenants inherit platform defaults (V1 no-hardcoding:
// a seeded global default always exists for every domain the code reads).
func (s *Service) ActiveAt(ctx context.Context, domain, scope string, t time.Time) (entity.ConfigVersion, error) {
	var out entity.ConfigVersion
	err := repo.WithPlatformTx(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.configs.GetActiveAt(ctx, tx, domain, scope, t)
		if err == nil {
			out = c
			return nil
		}
		if scope != entity.ScopeGlobal {
			c, err = s.configs.GetActiveAt(ctx, tx, domain, entity.ScopeGlobal, t)
			if err == nil {
				out = c
				return nil
			}
		}
		return fmt.Errorf("no active config for domain %q scope %q (missing seeded default?): %w", domain, scope, err)
	})
	return out, err
}

func (s *Service) auditTx(ctx context.Context, tx pgx.Tx, actor, action, targetID, reason string) error {
	return s.audit.Insert(ctx, tx, entity.AuditEvent{
		ID: platform.NewID("aud"), Actor: actor, Action: action,
		TargetType: "config_version", TargetID: targetID, Reason: reason,
	})
}

// ---------------------------------------------------------------------------
// Domain validators
// ---------------------------------------------------------------------------

// validateConcurrency: SF-2. While the one-active-advance schema backstop
// exists (or before the advances table exists at all — fail-safe floor), any
// value above 1 is rejected with an instruction, not stored-and-ignored.
func validateConcurrency(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		MaxConcurrentAdvances *int `json:"max_concurrent_advances"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.MaxConcurrentAdvances == nil {
		return fmt.Errorf("max_concurrent_advances is required")
	}
	n := *v.MaxConcurrentAdvances
	if n < 1 {
		return fmt.Errorf("max_concurrent_advances must be >= 1, got %d", n)
	}
	if n == 1 {
		return nil
	}
	tableExists, indexExists, err := repo.AdvancesOneActiveIndexExists(ctx, tx)
	if err != nil {
		return fmt.Errorf("schema introspection: %w", err)
	}
	if !tableExists || indexExists {
		return fmt.Errorf(
			"max_concurrent_advances=%d rejected: the one-active-advance schema backstop is in force "+
				"(advances_one_active_uq). Enabling N>1 is a schema-level change gated by architecture "+
				"review — see ADR-0001 SF-2 amendment (V1-PRD-005)", n)
	}
	return nil
}

// validateIdempotencyTTL: SF-5 — TTL must respect the floor; both configurable,
// but ttl below floor is never activatable.
func validateIdempotencyTTL(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		TTLHours      *int `json:"ttl_hours"`
		MinFloorHours *int `json:"min_floor_hours"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.TTLHours == nil || v.MinFloorHours == nil {
		return fmt.Errorf("ttl_hours and min_floor_hours are required")
	}
	if *v.MinFloorHours < 72 {
		return fmt.Errorf("min_floor_hours %d below the hard floor of 72h (SF-5: must cover the longest legitimate retry window)", *v.MinFloorHours)
	}
	if *v.TTLHours < *v.MinFloorHours {
		return fmt.Errorf("ttl_hours %d < min_floor_hours %d (SF-5)", *v.TTLHours, *v.MinFloorHours)
	}
	return nil
}

func validateOutbox(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		ClaimBatchSize      *int `json:"claim_batch_size"`
		MaxAttempts         *int `json:"max_attempts"`
		RetryBackoffSeconds *int `json:"retry_backoff_seconds"`
	}
	if err := json.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	switch {
	case v.ClaimBatchSize == nil || *v.ClaimBatchSize < 1 || *v.ClaimBatchSize > 1000:
		return fmt.Errorf("claim_batch_size must be 1..1000")
	case v.MaxAttempts == nil || *v.MaxAttempts < 1:
		return fmt.Errorf("max_attempts must be >= 1 (V2-EVT-007 bounded retry)")
	case v.RetryBackoffSeconds == nil || *v.RetryBackoffSeconds < 1:
		return fmt.Errorf("retry_backoff_seconds must be >= 1")
	}
	return nil
}
