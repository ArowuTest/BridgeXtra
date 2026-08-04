package configsvc

// collections.policy (Wave B.3 Phase B) governs the write-off DISPUTE GATE — the owner
// decision: soft-block a write-off when the subscriber has an OPEN debt dispute, allowing a
// deliberate, audited override. Both the gating categories AND whether an override is
// permitted are governed here (not hardcoded), so compliance can tune them without a deploy;
// and the gate is scoped to DEBT disputes — an unrelated service complaint must not block a
// legitimate write-off.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["collections.policy"] = validateCollectionsPolicy
}

// complaintCategoryCodes mirrors the complaints.category CHECK (0011). The gating set must be
// a subset — a typo can't arm a gate on a category that can never exist.
var complaintCategoryCodes = map[string]bool{
	"DISPUTED_ADVANCE":  true,
	"DISPUTED_RECOVERY": true,
	"DISCLOSURE":        true,
	"SERVICE":           true,
	"OTHER":             true,
}

func validateCollectionsPolicy(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		DebtDisputeCategories  []string `json:"debt_dispute_categories"`
		AllowOverrideOnDispute *bool    `json:"allow_override_on_dispute"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if len(v.DebtDisputeCategories) == 0 {
		return fmt.Errorf("collections.policy: debt_dispute_categories is required and must be non-empty (which complaints gate a write-off)")
	}
	seen := map[string]bool{}
	for _, c := range v.DebtDisputeCategories {
		if !complaintCategoryCodes[c] {
			return fmt.Errorf("collections.policy: debt_dispute_categories has unknown complaint category %q", c)
		}
		if seen[c] {
			return fmt.Errorf("collections.policy: debt_dispute_categories has duplicate %q", c)
		}
		seen[c] = true
	}
	if v.AllowOverrideOnDispute == nil {
		return fmt.Errorf("collections.policy: allow_override_on_dispute is required (bool) — false means an open dispute HARD-blocks the write-off")
	}
	return nil
}
