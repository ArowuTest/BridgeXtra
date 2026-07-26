package configsvc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["origination.nin_gate"] = validateNINGate // Build 2: NIN eligibility gate
}

// validateNINGate (Build 2): origination.nin_gate governs whether the NIN
// identity-verification requirement is enforced. require_nin_verified must be
// present and a boolean — no default. (Origination reads it fail-closed: an ABSENT
// config means REQUIRE; this validator only constrains a config that IS set, so an
// operator cannot store a malformed/empty gate that would be ambiguous.)
func validateNINGate(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		RequireNINVerified *bool `json:"require_nin_verified"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.RequireNINVerified == nil {
		return fmt.Errorf("require_nin_verified is required (true = enforce NIN verification; a governed opt-out sets it false)")
	}
	return nil
}
