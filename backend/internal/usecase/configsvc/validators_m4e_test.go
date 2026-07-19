package configsvc_test

// M4e validator pack. The load-bearing case is C1 (VR-36): SELF_EXCLUDED is
// a NON-CONFIGURABLE boundary — the ops.status_actions validator must
// structurally refuse it as a from- OR to-status, so no governed config
// change can ever let an operator set or override a customer's
// self-exclusion (EDG-030). Plus the ops.queues bounds (C3 at approval).

import (
	"context"
	"testing"
)

func mustAccept(t *testing.T, label, domain, scope, content string) {
	t.Helper()
	svc, _ := newSvc(t, "cfg_m4e_ok_"+label)
	ctx := context.Background()
	c, err := svc.CreateDraft(ctx, domain, scope, "alice", label, []byte(content))
	if err != nil {
		t.Fatalf("%s: draft: %v", label, err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatalf("%s: submit: %v", label, err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Errorf("%s: must be accepted, got %v", label, err)
	}
}

func TestM4E_StatusActionsValidator_SelfExcludedIsStructurallyRefused(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m4e_ssa")

	cases := map[string]string{
		// C1: the conduct floor — SELF_EXCLUDED refused in either position.
		"self_excluded_as_to":   `{"allowed_transitions":[{"from":"ACTIVE","to":"SELF_EXCLUDED"}]}`,
		"self_excluded_as_from": `{"allowed_transitions":[{"from":"SELF_EXCLUDED","to":"ACTIVE"}]}`,
		// CLOSED is terminal — nothing may leave it.
		"reopen_closed": `{"allowed_transitions":[{"from":"CLOSED","to":"ACTIVE"}]}`,
		// Structure floors.
		"unknown_status":  `{"allowed_transitions":[{"from":"ACTIVE","to":"SUSPENDED"}]}`,
		"self_transition": `{"allowed_transitions":[{"from":"ACTIVE","to":"ACTIVE"}]}`,
		"duplicate":       `{"allowed_transitions":[{"from":"ACTIVE","to":"BARRED"},{"from":"ACTIVE","to":"BARRED"}]}`,
		"absent_key":      `{}`,
		"unknown_field":   `{"allowed_transitions":[],"extra":true}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "ops.status_actions", "global", label, content)
	}

	// The seeded shape passes; an EMPTY list is a valid refuse-all posture.
	mustAccept(t, "seed_shape", "ops.status_actions", "global",
		`{"allowed_transitions":[{"from":"ACTIVE","to":"BARRED"},{"from":"BARRED","to":"ACTIVE"}]}`)
	mustAccept(t, "refuse_all", "ops.status_actions", "global",
		`{"allowed_transitions":[]}`)
}

func TestM4E_QueuesValidator_BoundsEnforced(t *testing.T) {
	svc, _ := newSvc(t, "cfg_m4e_q")
	cases := map[string]string{
		"zero_staleness":     `{"stale_sent_after_seconds":0,"max_page_size":100}`,
		"negative_staleness": `{"stale_sent_after_seconds":-1,"max_page_size":100}`,
		"absent_staleness":   `{"max_page_size":100}`,
		"page_size_zero":     `{"stale_sent_after_seconds":600,"max_page_size":0}`,
		"page_size_huge":     `{"stale_sent_after_seconds":600,"max_page_size":10000}`,
		"unknown_field":      `{"stale_sent_after_seconds":600,"max_page_size":100,"x":1}`,
	}
	for label, content := range cases {
		mustReject(t, svc, "ops.queues", "global", label, content)
	}
	mustAccept(t, "seed_shape", "ops.queues", "global",
		`{"stale_sent_after_seconds":600,"max_page_size":200}`)
}
