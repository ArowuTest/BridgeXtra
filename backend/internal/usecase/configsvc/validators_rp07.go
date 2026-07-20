package configsvc

// R-P0-7 disclosure.policy validator. The disclosure a customer is shown is
// governed config, not code: template id+version, locale set, allowed channels,
// and the rendered-body/total-cost templates. Fail-closed floors here prevent a
// disclosure that discloses nothing (the exact conduct gap R-P0-7 closed) from
// being approved: a body/total-cost template that omits the repayment total is
// rejected, an empty locale or channel set is rejected, and the default locale
// must be one the policy actually supports.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["disclosure.policy"] = validateDisclosurePolicy
}

func validateDisclosurePolicy(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		TemplateID        *string  `json:"template_id"`
		TemplateVersion   *string  `json:"template_version"`
		DefaultLocale     *string  `json:"default_locale"`
		SupportedLocales  []string `json:"supported_locales"`
		AllowedChannels   []string `json:"allowed_channels"`
		BodyTemplate      *string  `json:"body_template"`
		TotalCostTemplate *string  `json:"total_cost_template"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.TemplateID == nil || strings.TrimSpace(*v.TemplateID) == "" {
		return fmt.Errorf("template_id is required")
	}
	if v.TemplateVersion == nil || strings.TrimSpace(*v.TemplateVersion) == "" {
		return fmt.Errorf("template_version is required")
	}
	if v.DefaultLocale == nil || strings.TrimSpace(*v.DefaultLocale) == "" {
		return fmt.Errorf("default_locale is required")
	}
	if len(v.SupportedLocales) == 0 {
		return fmt.Errorf("supported_locales must list at least one locale")
	}
	supported := false
	for _, l := range v.SupportedLocales {
		if strings.TrimSpace(l) == "" {
			return fmt.Errorf("supported_locales entries must be non-empty")
		}
		if l == *v.DefaultLocale {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("default_locale %q must be one of supported_locales", *v.DefaultLocale)
	}
	if len(v.AllowedChannels) == 0 {
		// Zero-config floor: an empty channel set would accept no confirm at
		// all (fail-closed), or — worse if a future default were permissive —
		// accept any. Require an explicit, non-empty channel allow-list.
		return fmt.Errorf("allowed_channels must list at least one channel")
	}
	for _, c := range v.AllowedChannels {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("allowed_channels entries must be non-empty")
		}
	}
	// A disclosure that omits the repayment total is not a disclosure — the
	// single most material term (what the customer will owe) MUST appear.
	if v.BodyTemplate == nil || !strings.Contains(*v.BodyTemplate, "{{repayment}}") {
		return fmt.Errorf("body_template must disclose the repayment total via {{repayment}}")
	}
	if v.TotalCostTemplate == nil || !strings.Contains(*v.TotalCostTemplate, "{{repayment}}") {
		return fmt.Errorf("total_cost_template must disclose the repayment total via {{repayment}}")
	}
	return nil
}
