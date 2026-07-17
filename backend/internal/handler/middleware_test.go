package handler_test

// V2-TEN-002/003 + EDG-026: tenant resolved from the credential; a conflicting
// claimed tenant is rejected 403 AND security-audited; unknown credentials 401.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func setup(t *testing.T) (*testutil.DB, http.Handler, *string) {
	db := testutil.MustSetup(t, "mw")
	db.SeedTelco(t, "TELCO_A", "key-a")
	db.SeedTelco(t, "TELCO_B", "key-b")

	var seenTenant string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, err := platform.TenantFrom(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		seenTenant = tid
		w.WriteHeader(http.StatusOK)
	})
	mw := &handler.TenantAuth{
		Telcos: &repo.Telcos{Pool: db.App},
		Pool:   db.App,
		Log:    slog.Default(),
	}
	return db, mw.Wrap(inner), &seenTenant
}

func TestV2_TEN_002_CredentialResolvesTenant(t *testing.T) {
	_, h, seen := setup(t)
	req := httptest.NewRequest("GET", "/v1/programmes", nil)
	req.Header.Set("X-Api-Key", "key-a")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if *seen != "TELCO_A" {
		t.Fatalf("tenant context = %q, want TELCO_A", *seen)
	}
}

func TestV2_TEN_003_ConflictingClaimedTenant_RejectedAndAudited(t *testing.T) {
	db, h, seen := setup(t)
	req := httptest.NewRequest("GET", "/v1/programmes", nil)
	req.Header.Set("X-Api-Key", "key-a")    // credential belongs to TELCO_A
	req.Header.Set("X-Telco-Id", "TELCO_B") // claims to be TELCO_B
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	if *seen != "" {
		t.Fatal("handler must not run on tenant mismatch")
	}
	// Security audit row written (platform scope), EDG-026.
	var n int
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'TENANT_CONTEXT_MISMATCH'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 mismatch audit event, got %d", n)
	}
}

func TestAuth_UnknownOrMissingCredential_Unauthorized(t *testing.T) {
	_, h, _ := setup(t)
	for name, set := range map[string]func(*http.Request){
		"missing": func(r *http.Request) {},
		"unknown": func(r *http.Request) { r.Header.Set("X-Api-Key", "not-a-key") },
	} {
		req := httptest.NewRequest("GET", "/v1/programmes", nil)
		set(req)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s credential: want 401, got %d", name, rr.Code)
		}
	}
}
