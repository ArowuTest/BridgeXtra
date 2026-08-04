package configsvc

import "testing"

// TestConfigFence_CriticalDomainsValidated (S3-C4 / I14): every money- or
// security-critical config domain the platform READS must have a registered,
// non-nil validator — a domain that governs money, arming, or eligibility cannot
// ship as "structural-only". A new such domain added without a validator fails
// HERE, not silently in production (Approve/Activate only run a validator when the
// domain is in the registry, so an unregistered money domain would pass with no
// range checks). And no domain may be registered with an explicit nil validator.
func TestConfigFence_CriticalDomainsValidated(t *testing.T) {
	critical := []string{
		// Phase 1 S3 — RECOVERY recon (arming + money-confirmation + feed auth).
		"recon.recovery", "telco.recovery_feed",
		// Phase 1 S2 — recharge webhook (money-in + occurred_at clamp).
		"telco.recharge_feed",
		// Reconciliation tolerance (the never-auto-resolve floor).
		"recon.tolerance",
		// Scoring — the lend decision.
		"scoring.policy", "scoring.schedule",
		// Money core.
		"product.airtime", "advance.reservation", "recovery.allocation",
		// Build 1 — the programme economic/legal go-live gate (origination refuses
		// to lend on a programme whose economics are unset/malformed).
		"programme.economics",
		// Build 2 — the NIN identity-verification eligibility gate.
		"origination.nin_gate",
		"treasury.guardrails", "writeoff.policy", "settlement.terms",
		// Wave B.3 Phase B — the write-off dispute gate.
		"collections.policy",
		"ledger.templates", "fee_recognition",
		// Safety / compliance.
		"disclosure.policy", "origination.self_exclusion",
	}
	for _, d := range critical {
		v, ok := validators[d]
		if !ok || v == nil {
			t.Errorf("critical config domain %q must have a registered non-nil validator (registered=%v)", d, ok)
		}
	}
	// A nil entry would pass Approve with only structural checks — a silent hole.
	for d, v := range validators {
		if v == nil {
			t.Errorf("config domain %q is registered with a nil validator", d)
		}
	}
}
