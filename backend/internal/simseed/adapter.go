//go:build simseed_loop

// Package simseed loop-mode: the synthetic telco adapter. FENCED THREE WAYS
// (design §3, the CRITICAL prod-safety finding): (a) this runtime telco-id guard
// refuses any telco but SIM_NG; (b) the //go:build simseed_loop tag keeps it out of
// every normal binary (cmd/api, cmd/worker); (c) a CI import-fence forbids cmd/api
// and cmd/worker from importing internal/simseed at all. An always-CONFIRMED,
// no-network adapter wired into a production binary would mark advances ACTIVE and
// post the issuance journal for real subscribers with no airtime delivered — so it
// must be structurally impossible to reach from production.
package simseed

import (
	"context"
	"fmt"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
)

// SyntheticAdapter implements mno.Client with a deterministic, no-network outcome
// per subscriber token. It fabricates nothing beyond what the loop explicitly maps.
type SyntheticAdapter struct {
	Seed    string
	ByToken map[string]mno.Outcome // per-member outcome; an UNMAPPED token => FAILED (never CONFIRMED)
	ByAdv   map[string]mno.Outcome // advance-id => outcome for EnquireStatus (Slice 4 UNKNOWN->resolver)
}

var _ mno.Client = (*SyntheticAdapter)(nil)

func (a *SyntheticAdapter) SubmitFulfilment(_ context.Context, telcoID, _ string, req mno.FulfilmentRequest) (mno.Result, error) {
	if telcoID != SyntheticTelco {
		return mno.Result{}, fmt.Errorf("SyntheticAdapter refuses non-synthetic telco %q (fence a)", telcoID)
	}
	return a.decide(req.PlatformRequestID, a.outcomeFor(req.MSISDNToken)), nil
}

func (a *SyntheticAdapter) EnquireStatus(_ context.Context, telcoID, platformRequestID string) (mno.Result, error) {
	if telcoID != SyntheticTelco {
		return mno.Result{}, fmt.Errorf("SyntheticAdapter refuses non-synthetic telco %q (fence a)", telcoID)
	}
	// Keyed on the advance id (not a one-way-hashed token). An unmapped advance =>
	// the default branch of decide => FAILED (never a fabricated CONFIRMED).
	return a.decide(platformRequestID, a.ByAdv[platformRequestID]), nil
}

// outcomeFor defaults an unmapped token to FAILED (design P2: never permissively
// CONFIRMED). The cmd path maps every borrower explicitly.
func (a *SyntheticAdapter) outcomeFor(token string) mno.Outcome {
	if o, ok := a.ByToken[token]; ok {
		return o
	}
	return mno.OutcomeFailed
}

// decide builds a deterministic Result. Only an EXPLICIT CONFIRMED confirms; FAILED,
// UNKNOWN and any unrecognised/empty outcome are non-confirming (fail-safe).
func (a *SyntheticAdapter) decide(advanceID string, o mno.Outcome) mno.Result {
	ref := fmt.Sprintf("SIM-%016x", stableHash64(a.Seed+"/telcoref/"+advanceID)) // deterministic, no time.Now/NewID
	switch o {
	case mno.OutcomeConfirmed:
		return mno.Result{Outcome: mno.OutcomeConfirmed, TelcoReference: ref, ResponseEvidence: []byte(`{"status":"SUCCESS"}`)}
	case mno.OutcomeUnknown:
		return mno.Result{Outcome: mno.OutcomeUnknown, ResponseEvidence: []byte(`{"status":"PENDING"}`)}
	default:
		return mno.Result{Outcome: mno.OutcomeFailed, TelcoReference: ref, ResponseEvidence: []byte(`{"status":"FAILED"}`)}
	}
}
