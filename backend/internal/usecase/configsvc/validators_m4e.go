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
