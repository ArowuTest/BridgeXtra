package configsvc_test

// M6a scoring.schedule validator + the two seeded globals (Phase 0 scheduler
// foundation). Proves: the validator is fail-closed (absent/out-of-range/
// fractional/unknown-field refused, valid accepted); the seeded scoring.schedule
// global default is DISABLED (zero-config floor OFF — nothing arms without an
// explicit enabled:true); and the new scoring.policy GLOBAL seed makes the
// programme->global scope fallback resolve for programmes without an override.

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestM6_ScoringScheduleValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m6_sched")
	scope := "programme:prg_sim_airtime01"

	for label, content := range map[string]string{
		"enabled absent":     `{"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`,
		"cadence absent":     `{"enabled":true,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`,
		"cadence zero":       `{"enabled":true,"cadence_hours":0,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`,
		"cadence over max":   `{"enabled":true,"cadence_hours":9000,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`,
		"cadence fractional": `{"enabled":true,"cadence_hours":24.5,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`,
		"headroom zero":      `{"enabled":true,"cadence_hours":24,"headroom_cycles":0,"lease_seconds":900,"max_attempts":6}`,
		"headroom over max":  `{"enabled":true,"cadence_hours":24,"headroom_cycles":25,"lease_seconds":900,"max_attempts":6}`,
		"lease too small":    `{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":30,"max_attempts":6}`,
		"lease too big":      `{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":90000,"max_attempts":6}`,
		"attempts zero":      `{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":0}`,
		"attempts over max":  `{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":200}`,
		"unknown field":      `{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6,"foo":1}`,
	} {
		mustReject(t, svc, "scoring.schedule", scope, label, content)
	}

	// A fully valid schedule activates cleanly.
	ctx := context.Background()
	c, err := svc.CreateDraft(ctx, "scoring.schedule", scope, "alice", "arm",
		[]byte(`{"enabled":true,"cadence_hours":24,"headroom_cycles":1,"lease_seconds":900,"max_attempts":6}`))
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("a valid scoring.schedule must approve: %v", err)
	}
}

func TestM6_SeededGlobals_FloorOff_And_ScopeFallback(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m6_seeds")
	ctx := context.Background()
	now := time.Now().UTC()

	// scoring.schedule global default resolves AND is DISABLED (zero-config floor
	// OFF): nothing arms until an operator sets enabled:true.
	sched, err := svc.ActiveAt(ctx, "scoring.schedule", "global", now)
	if err != nil {
		t.Fatalf("scoring.schedule global must be seeded ACTIVE: %v", err)
	}
	var s struct {
		Enabled        bool `json:"enabled"`
		CadenceHours   int  `json:"cadence_hours"`
		HeadroomCycles int  `json:"headroom_cycles"`
		LeaseSeconds   int  `json:"lease_seconds"`
		MaxAttempts    int  `json:"max_attempts"`
	}
	if err := json.Unmarshal(sched.Content, &s); err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("seeded scoring.schedule default must be DISABLED (arming is explicit)")
	}
	if s.CadenceHours <= 0 || s.HeadroomCycles < 1 || s.LeaseSeconds < 60 || s.MaxAttempts < 1 {
		t.Fatalf("seeded scoring.schedule default is out of range: %+v", s)
	}

	// A programme with NO scoring.policy override falls back to the new GLOBAL
	// seed — the fail-closed policy read resolves instead of erroring, so a new
	// programme is not silently un-scoreable.
	pol, err := svc.ActiveAt(ctx, "scoring.policy", "programme:prg_does_not_exist_yet", now)
	if err != nil {
		t.Fatalf("scoring.policy must fall back to the GLOBAL seed for a programme without an override: %v", err)
	}
	var p struct {
		DecisionValidHours int `json:"decision_valid_hours"`
	}
	if err := json.Unmarshal(pol.Content, &p); err != nil {
		t.Fatal(err)
	}
	if p.DecisionValidHours <= 0 {
		t.Fatalf("global scoring.policy must carry a positive decision_valid_hours, got %d", p.DecisionValidHours)
	}
}

// The GLOBAL scoring.policy seed (inserted directly by the migration, bypassing
// the validator) must PASS the scoring.policy validator against a fresh scope —
// proving the shipped global default is well-formed.
func TestM6_SeededScoringPolicyGlobal_PassesValidator(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m6_polreval")
	ctx := context.Background()

	global, err := svc.ActiveAt(ctx, "scoring.policy", "global", time.Now().UTC())
	if err != nil {
		t.Fatalf("scoring.policy global must be seeded: %v", err)
	}
	c, err := svc.CreateDraft(ctx, "scoring.policy", "programme:prg_reval_probe", "alice", "re-validate global", global.Content)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("the seeded global scoring.policy must pass validateScoringPolicy: %v", err)
	}
}
