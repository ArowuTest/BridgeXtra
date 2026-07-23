// Package feepolicy resolves the fee_recognition policy (UPFRONT | DEFERRED)
// that is PINNED on an advance at origination and replayed by recovery,
// reversal and write-off. Resolving here — once, at issuance — and pinning the
// result is what keeps apply/reverse symmetric even if an operator re-activates
// the policy mid-life: downstream postings read the pin, never fresh config.
package feepolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// Resolve reads the ACTIVE fee_recognition policy for a programme (ActiveAt
// falls back to the seeded global default). FAIL-CLOSED: a missing config or an
// unknown policy is an error that aborts the posting — never a silent default.
func Resolve(ctx context.Context, cfg *configsvc.Service, programmeID string) (string, error) {
	cv, err := cfg.ActiveAt(ctx, "fee_recognition", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("fee_recognition config: %w", err)
	}
	var v struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal(cv.Content, &v); err != nil {
		return "", fmt.Errorf("fee_recognition parse: %w", err)
	}
	if v.Policy != entity.FeeRecognitionUpfront && v.Policy != entity.FeeRecognitionDeferred {
		return "", fmt.Errorf("fee_recognition: unknown policy %q — refusing to post", v.Policy)
	}
	return v.Policy, nil
}
