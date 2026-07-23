package configsvc

// M3f fee_recognition validator: the policy that decides whether a programme's
// fee income is recognised UPFRONT (at issuance) or DEFERRED (as recovered).
// A version cannot activate unless policy is exactly UPFRONT or DEFERRED — an
// absent or unknown policy is refused so the fail-closed read at origination
// can never resolve to something the posting engine does not understand.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["fee_recognition"] = validateFeeRecognition
}

func validateFeeRecognition(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		Policy *string `json:"policy"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Policy == nil {
		return fmt.Errorf("fee_recognition: policy required (UPFRONT|DEFERRED)")
	}
	if *v.Policy != "UPFRONT" && *v.Policy != "DEFERRED" {
		return fmt.Errorf("fee_recognition: policy must be UPFRONT or DEFERRED, got %q", *v.Policy)
	}
	return nil
}
