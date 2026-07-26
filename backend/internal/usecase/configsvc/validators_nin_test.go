package configsvc

import (
	"context"
	"encoding/json"
	"testing"
)

// Build 2: origination.nin_gate must carry a boolean require_nin_verified — no
// default, no other shape. (Origination still reads it fail-closed; this validator
// only prevents storing an ambiguous/malformed gate through maker-checker.)
func TestValidateNINGate(t *testing.T) {
	ok := []string{`{"require_nin_verified":true}`, `{"require_nin_verified":false}`}
	for _, c := range ok {
		if err := validateNINGate(context.Background(), nil, json.RawMessage(c)); err != nil {
			t.Fatalf("valid nin_gate %s rejected: %v", c, err)
		}
	}
	bad := map[string]string{
		"missing field": `{}`,
		"wrong type":    `{"require_nin_verified":"yes"}`,
		"null value":    `{"require_nin_verified":null}`,
		"unknown field": `{"require_nin_verified":true,"extra":1}`,
		"not an object": `true`,
	}
	for name, c := range bad {
		if err := validateNINGate(context.Background(), nil, json.RawMessage(c)); err == nil {
			t.Fatalf("nin_gate %q (%s) must be rejected", name, c)
		}
	}
}
