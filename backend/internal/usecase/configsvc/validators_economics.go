package configsvc

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/economics"
)

func init() {
	validators["programme.economics"] = validateProgrammeEconomics // Build 1: go-live economics gate
}

// validateProgrammeEconomics (Build 1): the programme.economics domain validator.
// It delegates to economics.Parse — the SAME parser origination reads through — so
// the maker-checker gate and the origination fail-closed read can never disagree on
// what a valid economics config is. Fail-closed: a missing field or an unknown enum
// value cannot become APPROVED/ACTIVE (no-hardcoding: absent/malformed → reject).
func validateProgrammeEconomics(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	_, err := economics.Parse(content)
	return err
}
