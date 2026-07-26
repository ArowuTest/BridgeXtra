// Package economics is the programme economic/legal identity (Build 1): who funds
// a programme's loans, who is the lender of record, who bears losses, and how the
// book settles and is taxed. It is a fail-closed go-live gate — a programme may be
// ACTIVE but cannot originate a loan until its economics are configured
// (programme.economics, effective-dated + maker-checker through configsvc).
//
// This package is a PURE leaf (no configsvc/origination import) so both the
// configsvc validator AND origination's fail-closed read share ONE parser with no
// import cycle — the "floor lives in code too" discipline (a raw-seeded config that
// bypasses the validator is still rejected at the origination read).
//
// No hardcoded defaults: every field is required, every enum is closed. An absent
// or malformed config is an error, never a silent default (V1 no-hardcoding).
package economics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalid is returned when the economics content is missing a field, carries an
// unknown enum value, or has an unexpected shape. Callers map an absent config
// (no version at all) separately (origination.ErrProgrammeEconomicsNotSet).
var ErrInvalid = errors.New("programme economics config invalid")

// Funding model — who provides the capital the loan is disbursed from.
const (
	FundingModelPlatformCapital = "PLATFORM_CAPITAL" // the platform lends its own capital
	FundingModelTelcoFronted    = "TELCO_FRONTED"    // the telco fronts the airtime/credit
	FundingModelWarehouseFunder = "WAREHOUSE_FUNDER" // a third-party warehouse facility funds it
)

// Settlement method — how repayments settle between platform and telco.
const (
	SettlementNetOffAgainstAirtime = "NET_OFF_AGAINST_AIRTIME" // netted against airtime the telco fronted
	SettlementDirectRecovery       = "DIRECT_RECOVERY"         // recovered directly from the subscriber balance
	SettlementWarehouseSweep       = "WAREHOUSE_SWEEP"         // swept to the warehouse funder
)

// Tax treatment — how the fee/interest is treated for VAT.
const (
	TaxVATInclusiveFee = "VAT_INCLUSIVE_FEE" // the fee is VAT-inclusive
	TaxVATExclusiveFee = "VAT_EXCLUSIVE_FEE" // VAT is added on top of the fee
	TaxVATExempt       = "VAT_EXEMPT"        // the fee is VAT-exempt
)

// These closed vocabularies are the v1 economic/legal set. Extending one is a
// deliberate code+config change (a new value must be added HERE, and every reader
// re-validates), never an operator free-text field — so an unrecognised value can
// never silently authorise lending.
var (
	validFundingModels     = map[string]bool{FundingModelPlatformCapital: true, FundingModelTelcoFronted: true, FundingModelWarehouseFunder: true}
	validSettlementMethods = map[string]bool{SettlementNetOffAgainstAirtime: true, SettlementDirectRecovery: true, SettlementWarehouseSweep: true}
	validTaxTreatments     = map[string]bool{TaxVATInclusiveFee: true, TaxVATExclusiveFee: true, TaxVATExempt: true}
)

// maxPartyLen bounds the free-text legal-party fields (lender_of_record,
// loss_bearer) — required + non-empty + not absurd.
const maxPartyLen = 200

// Terms is a validated programme economics record — every field present, every
// enum recognised. It is recorded on the origination audit trail.
type Terms struct {
	FundingModel     string `json:"funding_model"`
	LenderOfRecord   string `json:"lender_of_record"`
	LossBearer       string `json:"loss_bearer"`
	SettlementMethod string `json:"settlement_method"`
	TaxTreatment     string `json:"tax_treatment"`
}

// Parse strictly decodes and fully validates a programme.economics content blob.
// Unknown fields, a missing/empty required field, or an out-of-vocabulary enum all
// fail closed with ErrInvalid — there is no partial or defaulted result.
func Parse(content json.RawMessage) (Terms, error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	var t Terms
	if err := dec.Decode(&t); err != nil {
		return Terms{}, fmt.Errorf("%w: parse: %v", ErrInvalid, err)
	}
	if dec.More() {
		return Terms{}, fmt.Errorf("%w: trailing content after object", ErrInvalid)
	}
	if !validFundingModels[t.FundingModel] {
		return Terms{}, fmt.Errorf("%w: funding_model %q is not one of PLATFORM_CAPITAL|TELCO_FRONTED|WAREHOUSE_FUNDER", ErrInvalid, t.FundingModel)
	}
	if err := requireParty("lender_of_record", t.LenderOfRecord); err != nil {
		return Terms{}, err
	}
	if err := requireParty("loss_bearer", t.LossBearer); err != nil {
		return Terms{}, err
	}
	if !validSettlementMethods[t.SettlementMethod] {
		return Terms{}, fmt.Errorf("%w: settlement_method %q is not one of NET_OFF_AGAINST_AIRTIME|DIRECT_RECOVERY|WAREHOUSE_SWEEP", ErrInvalid, t.SettlementMethod)
	}
	if !validTaxTreatments[t.TaxTreatment] {
		return Terms{}, fmt.Errorf("%w: tax_treatment %q is not one of VAT_INCLUSIVE_FEE|VAT_EXCLUSIVE_FEE|VAT_EXEMPT", ErrInvalid, t.TaxTreatment)
	}
	return t, nil
}

func requireParty(field, v string) error {
	if v == "" {
		return fmt.Errorf("%w: %s is required (who owns the loan / bears losses — no default)", ErrInvalid, field)
	}
	if len(v) > maxPartyLen {
		return fmt.Errorf("%w: %s exceeds %d chars", ErrInvalid, field, maxPartyLen)
	}
	return nil
}
