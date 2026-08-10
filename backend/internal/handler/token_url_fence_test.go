package handler_test

// BX-MED-007: a subscriber token is a sensitive identifier and must NEVER be read from a URL
// query string (query strings leak into access logs, browser history, proxies and telemetry).
// This fence fails the build if any handler ever reads "token" or "msisdn_token" from the query
// string again — the "automated URL scanning" the audit asks for. Token-bearing reads take the
// token in a POST body instead (see /v1/offers and /v1/portal/support/subscriber).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBXMED007_NoSubscriberTokenInQueryString(t *testing.T) {
	forbidden := regexp.MustCompile(`Query\(\)\.Get\("(msisdn_)?token"\)`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if loc := forbidden.FindString(string(b)); loc != "" {
			t.Errorf("%s reads a subscriber token from the query string (%s) — a token must come from the POST body, never a URL (BX-MED-007)", name, loc)
		}
	}
	if scanned == 0 {
		t.Fatal("token-URL fence scanned no handler source (layout changed?) — the fence would be vacuous")
	}
}
