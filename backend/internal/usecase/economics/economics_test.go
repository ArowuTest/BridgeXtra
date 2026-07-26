package economics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const validEconomics = `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"BridgeXtra Financial Services Ltd","loss_bearer":"BridgeXtra Financial Services Ltd","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_INCLUSIVE_FEE"}`

func TestParse_Valid(t *testing.T) {
	got, err := Parse(json.RawMessage(validEconomics))
	if err != nil {
		t.Fatalf("valid economics must parse: %v", err)
	}
	if got.FundingModel != FundingModelPlatformCapital || got.SettlementMethod != SettlementNetOffAgainstAirtime ||
		got.TaxTreatment != TaxVATInclusiveFee || got.LenderOfRecord == "" || got.LossBearer == "" {
		t.Fatalf("parsed terms wrong: %+v", got)
	}
}

// Every field is required and every enum closed — no default, absent/malformed
// rejected (no-hardcoding). Each case must fail with ErrInvalid.
func TestParse_FailClosed(t *testing.T) {
	cases := map[string]string{
		"empty object":            `{}`,
		"missing funding_model":   `{"lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`,
		"unknown funding_model":   `{"funding_model":"MYSTERY_FUND","lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`,
		"empty lender":            `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`,
		"missing loss_bearer":     `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`,
		"unknown settlement":      `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"X","loss_bearer":"X","settlement_method":"CASH_IN_HAND","tax_treatment":"VAT_EXEMPT"}`,
		"unknown tax":             `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"NONE"}`,
		"unknown field":           `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT","secret_backdoor":true}`,
		"lowercase enum rejected": `{"funding_model":"platform_capital","lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`,
		"not an object":           `"PLATFORM_CAPITAL"`,
		"empty":                   ``,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(json.RawMessage(raw))
			if err == nil {
				t.Fatalf("case %q must be rejected, got nil error", name)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("case %q must wrap ErrInvalid, got %v", name, err)
			}
		})
	}
}

func TestParse_OversizedPartyRejected(t *testing.T) {
	big := strings.Repeat("A", maxPartyLen+1)
	raw := `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"` + big + `","loss_bearer":"X","settlement_method":"DIRECT_RECOVERY","tax_treatment":"VAT_EXEMPT"}`
	if _, err := Parse(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an oversized lender_of_record must be rejected, got %v", err)
	}
}

// All three enum vocabularies accept every documented member (guards against a
// typo'd constant or a map that drifted from the doc comment).
func TestParse_AllEnumMembersAccepted(t *testing.T) {
	for _, fm := range []string{FundingModelPlatformCapital, FundingModelTelcoFronted, FundingModelWarehouseFunder} {
		for _, sm := range []string{SettlementNetOffAgainstAirtime, SettlementDirectRecovery, SettlementWarehouseSweep} {
			for _, tx := range []string{TaxVATInclusiveFee, TaxVATExclusiveFee, TaxVATExempt} {
				raw := `{"funding_model":"` + fm + `","lender_of_record":"L","loss_bearer":"B","settlement_method":"` + sm + `","tax_treatment":"` + tx + `"}`
				if _, err := Parse(json.RawMessage(raw)); err != nil {
					t.Fatalf("valid combo (%s,%s,%s) rejected: %v", fm, sm, tx, err)
				}
			}
		}
	}
}
