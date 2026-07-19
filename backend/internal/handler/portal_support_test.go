package handler_test

// M4f pack. The load-bearing assertions: (1) UI-004 — no support response
// EVER carries a full subscriber token (proven by absence on the raw body,
// not by presence of a masked field); (2) V3-ORG-005 — SUPPORT's complete
// write surface, enumerated from the PRODUCTION RBAC map, is exactly the
// complaint workflow; (3) the complaint journey with its mandatory-resolution
// close and never-resurrect guard; (4) TelcoLevelBound scope on every read.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
)

func TestM4F_Timeline_MaskedByDefault_AndScope(t *testing.T) {
	f := newPortalFixture(t, "m4f_timeline")
	supSess := f.login(t, roleKeys["SUPPORT"])

	// A subscriber with one advance + attempt (the M4e seed chain), plus a
	// status action so every timeline section has content.
	const fullToken = "tok_m4e_1"
	seedAmbiguousChain(t, f, 1, "UNKNOWN", time.Hour)

	code, body := f.callBody(t, &supSess, "GET", "/v1/portal/support/subscriber?token="+fullToken, "")
	if code != http.StatusOK {
		t.Fatalf("timeline: %d %s", code, body)
	}
	// UI-004, the strong form: the FULL token appears NOWHERE in the
	// response — masking is enforced where data leaves the platform.
	if bytes.Contains(body, []byte(fullToken)) {
		t.Fatalf("timeline response must never carry the full token: %s", body)
	}
	var tl struct {
		Subscriber struct {
			Masked string `json:"msisdn_token_masked"`
			Status string `json:"status"`
		} `json:"subscriber"`
		Advances []struct {
			AdvanceID string `json:"advance_id"`
			State     string `json:"state"`
		} `json:"advances"`
	}
	if err := json.Unmarshal(body, &tl); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tl.Subscriber.Masked, "…") || !strings.HasSuffix(fullToken, strings.TrimPrefix(tl.Subscriber.Masked, "…")) {
		t.Fatalf("masked token must be …+suffix of the real token, got %q", tl.Subscriber.Masked)
	}
	if len(tl.Advances) != 1 || tl.Advances[0].State != "FULFILMENT_UNKNOWN" {
		t.Fatalf("timeline must carry the advance history: %s", body)
	}

	// Unknown token and out-of-scope both 404 — no oracle.
	if code, _ := f.callBody(t, &supSess, "GET", "/v1/portal/support/subscriber?token=tok_nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown token must 404, got %d", code)
	}
	ctx := context.Background()
	if err := (&repo.Admins{Pool: f.db.Admin}).CreateWithRole(ctx, "adm_m4f_p", "sup_prog", "portal-key-sup-prog-01", "SUPPORT", "programme:prg_sim_airtime01"); err != nil {
		t.Fatal(err)
	}
	progSess := f.login(t, "portal-key-sup-prog-01")
	if code, _ := f.callBody(t, &progSess, "GET", "/v1/portal/support/subscriber?token="+fullToken, ""); code != http.StatusNotFound {
		t.Fatalf("programme-scoped support must 404 on a telco-grained read (TelcoLevelBound), got %d", code)
	}
}

// TestM4F_SupportWriteSurface_IsComplaintsOnly proves V3-ORG-005 from the
// PRODUCTION authorization map: every mutating route SUPPORT can reach is
// the complaint workflow — no ledger, limits, config, guardrail, status, or
// demo mutation admits the role. Driven by RBACRoutes() so it can never
// drift from what the server enforces.
func TestM4F_SupportWriteSurface_IsComplaintsOnly(t *testing.T) {
	var writes []string
	for key, roles := range handler.RBACRoutes() {
		if strings.HasPrefix(key, "GET ") {
			continue
		}
		for _, role := range roles {
			if role == "SUPPORT" {
				writes = append(writes, key)
			}
		}
	}
	for _, key := range writes {
		if !strings.Contains(key, "/v1/portal/support/complaints") {
			t.Errorf("SUPPORT holds a non-complaint write route: %s (V3-ORG-005)", key)
		}
	}
	if len(writes) == 0 {
		t.Fatal("SUPPORT must hold the complaint workflow writes — a read-only-everything role is the wrong shape")
	}
}

func TestM4F_Complaint_JourneyAsSupport(t *testing.T) {
	f := newPortalFixture(t, "m4f_cmp")
	supSess := f.login(t, roleKeys["SUPPORT"])
	ctx := context.Background()
	const fullToken = "tok_m4f_cmp_1"
	seedSubscriber(t, f, "sub_m4f_cmp_1", fullToken)

	// Open (SUPPORT, '*' scope names the telco).
	code, body := f.callBody(t, &supSess, "POST", "/v1/portal/support/complaints",
		`{"telco_id":"SIM_NG","msisdn_token":"`+fullToken+`","channel":"CALL_CENTRE","category":"DISPUTED_ADVANCE","narrative":"subscriber disputes the June advance"}`)
	if code != http.StatusOK {
		t.Fatalf("open: %d %s", code, body)
	}
	var opened struct {
		ComplaintID string `json:"complaint_id"`
	}
	if err := json.Unmarshal(body, &opened); err != nil {
		t.Fatal(err)
	}

	// Closing without a resolution refuses (mandatory reason).
	if code, _ := f.callBody(t, &supSess, "POST", "/v1/portal/support/complaints/"+opened.ComplaintID+"/progress",
		`{"to":"RESOLVED"}`); code != http.StatusBadRequest {
		t.Fatalf("close without resolution must 400, got %d", code)
	}

	// OPEN -> IN_REVIEW -> RESOLVED, audited at each step.
	if code, _ := f.callBody(t, &supSess, "POST", "/v1/portal/support/complaints/"+opened.ComplaintID+"/progress",
		`{"to":"IN_REVIEW"}`); code != http.StatusOK {
		t.Fatalf("to IN_REVIEW: %d", code)
	}
	if code, _ := f.callBody(t, &supSess, "POST", "/v1/portal/support/complaints/"+opened.ComplaintID+"/progress",
		`{"to":"RESOLVED","resolution":"advance verified against telco evidence; goodwill airtime issued"}`); code != http.StatusOK {
		t.Fatalf("to RESOLVED: %d", code)
	}
	var audits int
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE target_id=$1 AND action LIKE 'complaint.%'`,
		opened.ComplaintID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Fatalf("open + both transitions must all audit (M3f audits the open too), got %d", audits)
	}

	// Closed is closed — the portal never resurrects worked evidence.
	if code, body := f.callBody(t, &supSess, "POST", "/v1/portal/support/complaints/"+opened.ComplaintID+"/progress",
		`{"to":"IN_REVIEW"}`); code != http.StatusConflict || !bytes.Contains(body, []byte("COMPLAINT_CLOSED")) {
		t.Fatalf("progressing a closed complaint must 409 COMPLAINT_CLOSED, got %d %s", code, body)
	}

	// The queue lists it, resolution attached, token masked (never full).
	code, body = f.callBody(t, &supSess, "GET", "/v1/portal/support/complaints", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	if bytes.Contains(body, []byte(fullToken)) {
		t.Fatalf("complaint list must never carry the full token: %s", body)
	}
	if !bytes.Contains(body, []byte("RESOLVED")) || !bytes.Contains(body, []byte("goodwill airtime")) {
		t.Fatalf("list must carry the worked resolution: %s", body)
	}
}
