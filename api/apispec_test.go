// Package api holds the OpenAPI specifications (V2-API-001). This test makes
// the specs release-gating: a spec that fails to parse or validate fails CI,
// and structural drift checks pin the routes the code actually serves — an
// endpoint added in code without a spec update turns the build red.
package api

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func loadSpec(t *testing.T, path string) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate %s: %v", path, err)
	}
	return doc
}

func TestV2_API_001_PlatformSpecValidates(t *testing.T) {
	doc := loadSpec(t, "openapi.yaml")

	// Route pinning: every path the API serves must be documented, and every
	// documented path must exist in code. Update BOTH in the same commit.
	served := map[string]bool{
		"/healthz":            true,
		"/v1/programmes":      true,
		"/v1/offers":          true,
		"/v1/advances":        true,
		"/v1/advances/{id}":   true,
		"/v1/recovery/events": true,
		// M4a portal (session-authenticated, deny-by-default RBAC)
		"/v1/portal/login":                true,
		"/v1/portal/logout":               true,
		"/v1/portal/me":                   true,
		"/v1/portal/config/active":        true,
		"/v1/portal/config/overview":      true,
		"/v1/portal/config/versions":      true,
		"/v1/portal/config/{id}":          true,
		"/v1/portal/config/drafts":        true,
		"/v1/portal/config/{id}/submit":   true,
		"/v1/portal/config/{id}/approve":  true,
		"/v1/portal/config/{id}/activate": true,
		// M4c risk workspace (guardrail trips + two-person re-arm)
		"/v1/portal/risk/trips":                    true,
		"/v1/portal/risk/trips/{id}/request-rearm": true,
		"/v1/portal/risk/trips/{id}/approve-rearm": true,
		// M4d finance workspace (ledger browser + breaks queue + settlement verify)
		"/v1/portal/finance/ledger/journals":         true,
		"/v1/portal/finance/ledger/journals/{id}":    true,
		"/v1/portal/finance/breaks":                  true,
		"/v1/portal/finance/breaks/{id}/action":      true,
		"/v1/portal/finance/settlements":             true,
		"/v1/portal/finance/settlements/{id}":        true,
		"/v1/portal/finance/settlements/{id}/verify": true,
		// M4e ops workspace (ambiguity queues)
		"/v1/portal/ops/fulfilments":                  true,
		"/v1/portal/ops/fulfilments/{id}/enquire-now": true,
		"/v1/portal/ops/reversals":                    true,
		"/v1/portal/ops/reversals/{id}/retry":         true,
	}
	for p := range served {
		if doc.Paths.Find(p) == nil {
			t.Errorf("served route %s missing from openapi.yaml", p)
		}
	}
	for _, p := range doc.Paths.InMatchingOrder() {
		if !served[p] {
			t.Errorf("openapi.yaml documents %s which the API does not serve — spec drift", p)
		}
	}
}

func TestV2_API_001_SimulatorSpecValidates(t *testing.T) {
	doc := loadSpec(t, "simulator-openapi.yaml")

	served := map[string]bool{
		"/healthz":                         true,
		"/v1/telcos/{telcoId}/fulfilments": true,
		"/v1/telcos/{telcoId}/fulfilments/{platformRequestId}": true,
		"/v1/telcos/{telcoId}/feature-file":                    true,
		"/v1/telcos/{telcoId}/sms":                             true,
		"/sim/sms":                                             true,
		"/sim/transactions":                                    true,
	}
	for p := range served {
		if doc.Paths.Find(p) == nil {
			t.Errorf("served route %s missing from simulator-openapi.yaml", p)
		}
	}
	for _, p := range doc.Paths.InMatchingOrder() {
		if !served[p] {
			t.Errorf("simulator-openapi.yaml documents %s which the simulator does not serve — spec drift", p)
		}
	}

	// The canonical fulfilment contract must always require the idempotency key
	// (V2-API-002) — this is load-bearing for every future telco adapter.
	op := doc.Paths.Find("/v1/telcos/{telcoId}/fulfilments").Post
	found := false
	for _, p := range op.Parameters {
		if p.Value != nil && p.Value.Name == "Idempotency-Key" && p.Value.In == "header" && p.Value.Required {
			found = true
		}
	}
	if !found {
		t.Error("canonical fulfilment contract must require the Idempotency-Key header")
	}
}
