package handler_test

// Governed operator provisioning (v1), HTTP layer. Proves the four-eyes create,
// the single-actor revoke, and — the reviewer-required property — that revoke
// ends a live session on the operator's very next request (the M4A-F1 kill).

import (
	"encoding/json"
	"net/http"
	"testing"
)

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %q: %v (body=%s)", key, err, body)
	}
	s, _ := m[key].(string)
	return s
}

// Reviewer-required: after an operator is revoked, its live session is refused on
// the very next request (401) — not honoured until token expiry.
func TestOperators_RevokeEndsLiveSession401(t *testing.T) {
	f := newPortalFixture(t, "ops_revoke401")
	admin1 := f.login(t, roleKeys["ADMIN"]) // admin_actor
	admin2 := f.login(t, "portal-key-admin-000002")

	// four-eyes create of a fresh operator.
	code, body := f.callBody(t, &admin1, "POST", "/v1/portal/operators/requests",
		`{"actor":"op_target","role":"SUPPORT","scope":"*","reason":"pilot support"}`)
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%s)", code, body)
	}
	reqID := jsonField(t, body, "request_id")

	code, body = f.callBody(t, &admin2, "POST", "/v1/portal/operators/requests/"+reqID+"/approve", "")
	if code != http.StatusOK {
		t.Fatalf("approve: want 200, got %d (%s)", code, body)
	}
	key := jsonField(t, body, "access_key")
	if key == "" {
		t.Fatalf("approve must return a one-time access_key (body=%s)", body)
	}

	// the new operator can authenticate and use its session.
	target := f.login(t, key)
	if code := f.call(t, &target, "GET", "/v1/portal/me", ""); code != http.StatusOK {
		t.Fatalf("new operator /me: want 200, got %d", code)
	}

	// an admin revokes the operator (single-actor).
	if code := f.call(t, &admin1, "POST", "/v1/portal/operators/op_target/revoke", `{"reason":"offboarding"}`); code != http.StatusOK {
		t.Fatalf("revoke: want 200, got %d", code)
	}

	// the operator's SAME live session is now refused — the kill-switch fired.
	if code := f.call(t, &target, "GET", "/v1/portal/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked operator's next request must be 401, got %d", code)
	}
}

// Create is four-eyes over HTTP: the proposer cannot approve; a distinct admin
// can, and the approval returns the one-time key.
func TestOperators_FourEyesCreateHTTP(t *testing.T) {
	f := newPortalFixture(t, "ops_4eyes_http")
	admin1 := f.login(t, roleKeys["ADMIN"])
	admin2 := f.login(t, "portal-key-admin-000002")

	code, body := f.callBody(t, &admin1, "POST", "/v1/portal/operators/requests",
		`{"actor":"op_new2","role":"OPS","scope":"*","reason":"onboard"}`)
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%s)", code, body)
	}
	reqID := jsonField(t, body, "request_id")

	// self-approve refused.
	if code, body := f.callBody(t, &admin1, "POST", "/v1/portal/operators/requests/"+reqID+"/approve", ""); code != http.StatusConflict {
		t.Fatalf("self-approve: want 409, got %d (%s)", code, body)
	}
	// distinct approve succeeds with a key.
	code, body = f.callBody(t, &admin2, "POST", "/v1/portal/operators/requests/"+reqID+"/approve", "")
	if code != http.StatusOK || jsonField(t, body, "access_key") == "" {
		t.Fatalf("distinct approve: want 200 + key, got %d (%s)", code, body)
	}
}

// The whole surface is ADMIN-only: a non-ADMIN session is refused (403).
func TestOperators_NonAdminForbidden(t *testing.T) {
	f := newPortalFixture(t, "ops_rbac")
	risk := f.login(t, roleKeys["RISK"])
	if code := f.call(t, &risk, "GET", "/v1/portal/operators", ""); code != http.StatusForbidden {
		t.Fatalf("RISK GET /operators: want 403, got %d", code)
	}
	if code := f.call(t, &risk, "POST", "/v1/portal/operators/requests",
		`{"actor":"x","role":"OPS","scope":"*","reason":"y"}`); code != http.StatusForbidden {
		t.Fatalf("RISK propose: want 403, got %d", code)
	}
	if code := f.call(t, &risk, "POST", "/v1/portal/operators/someone/revoke", `{"reason":"y"}`); code != http.StatusForbidden {
		t.Fatalf("RISK revoke: want 403, got %d", code)
	}
}
