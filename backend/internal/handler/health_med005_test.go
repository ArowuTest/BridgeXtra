package handler_test

// BX-MED-005: liveness must be DB-free. handler.Live takes no pool — it structurally CANNOT depend
// on the database — and answers 200 for "the process is up". Readiness (handler.Health) is proven
// DB-dependent elsewhere (it 503s when the pool is unreachable); this pins the liveness contract.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
)

func TestBXMED005_Liveness_IsDBFreeAnd200(t *testing.T) {
	rec := httptest.NewRecorder()
	handler.Live().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must be 200 whenever the process is up, got %d", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("liveness body must be JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Status != "ok" {
		t.Fatalf("liveness status = %q, want ok", body.Status)
	}
}
