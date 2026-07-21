package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
)

func TestSecurityHeaders_SetOnEveryResponse(t *testing.T) {
	h := handler.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))

	want := map[string]string{
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"Cache-Control":             "no-store",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}
