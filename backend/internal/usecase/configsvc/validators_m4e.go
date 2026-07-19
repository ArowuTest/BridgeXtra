package configsvc

// M4e ops-workspace config: the ambiguity-queue thresholds. Small domain,
// same discipline — strict decode, positive bounds, and a validator that
// exists so the zero-config floor (C3) is enforced at approval time as well
// as at read time: an ops.queues version that cannot bound the queues can
// never activate.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["ops.queues"] = validateOpsQueues
	validators["ops.status_actions"] = validateOpsStatusActions
	validators["ops.fault_demo"] = validateOpsFaultDemo
}

// validateOpsFaultDemo governs the demo catalogue. Armed-but-dead prevention
// in both directions: an ENABLED demo must name at least one telco (each with
// a programme) and at least one scenario with a non-empty token pool — an
// enabled-but-unrunnable demo cannot activate. A disabled demo may carry any
// (even empty) catalogue: OFF is always a valid posture.
func validateOpsFaultDemo(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		Enabled *bool `json:"enabled"`
		Telcos  map[string]struct {
			ProgrammeID *string `json:"programme_id"`
		} `json:"telcos"`
		Scenarios map[string]struct {
			Tokens      []string `json:"tokens"`
			Description *string  `json:"description"`
		} `json:"scenarios"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Enabled == nil {
		return fmt.Errorf("enabled is required (absent is not 'off')")
	}
	if !*v.Enabled {
		return nil
	}
	if len(v.Telcos) == 0 {
		return fmt.Errorf("an enabled demo must allowlist at least one telco — enabled-with-no-telcos is armed-but-dead")
	}
	for id, t := range v.Telcos {
		if t.ProgrammeID == nil || *t.ProgrammeID == "" {
			return fmt.Errorf("telco %q: programme_id is required", id)
		}
	}
	if len(v.Scenarios) == 0 {
		return fmt.Errorf("an enabled demo must define at least one scenario")
	}
	for name, sc := range v.Scenarios {
		if len(sc.Tokens) == 0 {
			return fmt.Errorf("scenario %q: token pool must be non-empty", name)
		}
		if sc.Description == nil || *sc.Description == "" {
			return fmt.Errorf("scenario %q: description is required (the demo narrates to non-engineers)", name)
		}
	}
	return nil
}

// validateOpsStatusActions governs the operator status-action transition set.
// C1 (VR-36): SELF_EXCLUDED is a NON-CONFIGURABLE boundary — it can never
// appear as a from- or to-status, because an ops override of self-exclusion
// is a conduct violation (EDG-030); the customer's own channel is the only
// producer (task #45). Configuration-first, but not configuration-unbounded:
// a governed config change cannot re-open what the validator refuses.
func validateOpsStatusActions(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		AllowedTransitions []struct {
			From *string `json:"from"`
			To   *string `json:"to"`
		} `json:"allowed_transitions"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.AllowedTransitions == nil {
		return fmt.Errorf("allowed_transitions is required (an empty list is a valid refuse-all posture; an absent key is not)")
	}
	operator := map[string]bool{"ACTIVE": true, "BARRED": true, "CLOSED": true}
	seen := map[string]bool{}
	for i, tr := range v.AllowedTransitions {
		if tr.From == nil || tr.To == nil {
			return fmt.Errorf("allowed_transitions[%d]: from and to are required", i)
		}
		from, to := *tr.From, *tr.To
		if from == "SELF_EXCLUDED" || to == "SELF_EXCLUDED" {
			return fmt.Errorf("allowed_transitions[%d]: SELF_EXCLUDED is not an operator-configurable status — self-exclusion belongs to the customer channel and cannot be set or overridden by ops (EDG-030)", i)
		}
		if !operator[from] || !operator[to] {
			return fmt.Errorf("allowed_transitions[%d]: statuses must be ACTIVE|BARRED|CLOSED, got %q -> %q", i, from, to)
		}
		if from == to {
			return fmt.Errorf("allowed_transitions[%d]: from and to must differ", i)
		}
		if from == "CLOSED" {
			return fmt.Errorf("allowed_transitions[%d]: CLOSED is terminal — no transition may leave it", i)
		}
		key := from + "->" + to
		if seen[key] {
			return fmt.Errorf("allowed_transitions[%d]: duplicate transition %s", i, key)
		}
		seen[key] = true
	}
	return nil
}

func validateOpsQueues(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		StaleSentAfterSeconds *int `json:"stale_sent_after_seconds"`
		MaxPageSize           *int `json:"max_page_size"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.StaleSentAfterSeconds == nil || *v.StaleSentAfterSeconds <= 0 {
		return fmt.Errorf("stale_sent_after_seconds must be a positive integer — the SENT-staleness threshold bounds the ambiguity queue")
	}
	if v.MaxPageSize == nil || *v.MaxPageSize <= 0 || *v.MaxPageSize > 500 {
		return fmt.Errorf("max_page_size must be in 1..500")
	}
	return nil
}
