package configsvc

// M6a scoring.schedule validator (Phase 0 — arm the scoring pipeline). Governs
// the durable scheduler that keeps fresh decisions on file:
//   enabled          — arming is explicit; a version cannot omit it.
//   cadence_hours    — nominal re-score period.
//   headroom_cycles  — spare cadences of freshness margin (>=1, never zero, so a
//                      missed/crashed cycle cannot open a NO_OFFER gap).
//   lease_seconds    — a CLAIMED cycle is re-claimable only after this; must
//                      exceed the longest scoring run.
//   max_attempts     — reclaim ceiling before a cycle is parked FAILED + alerted.
// The check is structural/fail-closed: absent or out-of-range knobs are refused,
// and fractional values fail strict unmarshalling into the integer fields. The
// cadence<=decision_valid_hours relationship is cross-domain, so it is enforced
// at runtime by the scheduler (clamp-and-run), not here.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func init() {
	validators["scoring.schedule"] = validateScoringSchedule
}

func validateScoringSchedule(_ context.Context, _ pgx.Tx, content json.RawMessage) error {
	var v struct {
		Enabled        *bool `json:"enabled"`
		CadenceHours   *int  `json:"cadence_hours"`
		HeadroomCycles *int  `json:"headroom_cycles"`
		LeaseSeconds   *int  `json:"lease_seconds"`
		MaxAttempts    *int  `json:"max_attempts"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if v.Enabled == nil {
		return fmt.Errorf("scoring.schedule: enabled required (bool) — arming must be explicit, never a silent default")
	}
	if v.CadenceHours == nil || *v.CadenceHours < 1 || *v.CadenceHours > 8760 {
		return fmt.Errorf("scoring.schedule: cadence_hours must be an integer in [1,8760]")
	}
	if v.HeadroomCycles == nil || *v.HeadroomCycles < 1 || *v.HeadroomCycles > 24 {
		return fmt.Errorf("scoring.schedule: headroom_cycles must be an integer in [1,24] — zero headroom would let a single missed cycle expire decisions to NO_OFFER")
	}
	if v.LeaseSeconds == nil || *v.LeaseSeconds < 60 || *v.LeaseSeconds > 86400 {
		return fmt.Errorf("scoring.schedule: lease_seconds must be an integer in [60,86400] and must exceed the longest scoring run so a live run is never reclaimed under it")
	}
	if v.MaxAttempts == nil || *v.MaxAttempts < 1 || *v.MaxAttempts > 100 {
		return fmt.Errorf("scoring.schedule: max_attempts must be an integer in [1,100]")
	}
	return nil
}
