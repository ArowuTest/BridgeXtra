package recoveryfeed

// S3-A feed-adapter unit tests: the content hash is order-independent and
// change-sensitive, the total refuses overflow, and (I13) the https envelope MAC +
// row-integrity are enforced before a day is trusted.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonicalHash_OrderIndependentChangeSensitive(t *testing.T) {
	a := []FeedRow{{MSISDNToken: "t1", RecoveryDeductedMinor: 100, Currency: "NGN"}, {MSISDNToken: "t2", RecoveryDeductedMinor: 200, Currency: "NGN"}}
	b := []FeedRow{{MSISDNToken: "t2", RecoveryDeductedMinor: 200, Currency: "NGN"}, {MSISDNToken: "t1", RecoveryDeductedMinor: 100, Currency: "NGN"}}
	if CanonicalHash(a) != CanonicalHash(b) {
		t.Fatal("hash must be order-independent")
	}
	c := []FeedRow{{MSISDNToken: "t1", RecoveryDeductedMinor: 101, Currency: "NGN"}, {MSISDNToken: "t2", RecoveryDeductedMinor: 200, Currency: "NGN"}}
	if CanonicalHash(a) == CanonicalHash(c) {
		t.Fatal("hash must change when an amount changes")
	}
}

func TestTotalMinor_OverflowRefused(t *testing.T) {
	if _, err := TotalMinor([]FeedRow{{MSISDNToken: "a", RecoveryDeductedMinor: math.MaxInt64, Currency: "NGN"}, {MSISDNToken: "b", RecoveryDeductedMinor: 1, Currency: "NGN"}}); err == nil {
		t.Fatal("an overflowing feed total must be refused, not wrapped")
	}
}

// I13: the https adapter authenticates the envelope MAC and re-checks the manifest
// against the delivered rows BEFORE returning a day. A bad MAC or a hash that no
// longer matches the rows REJECTS the day (fail-closed).
func TestHTTPAdapter_EnvelopeMAC(t *testing.T) {
	const secret = "s3cr3t-eod"
	t.Setenv("REC_FEED_HMAC", secret)
	rows := []FeedRow{{MSISDNToken: "tokA", RecoveryDeductedMinor: 500, Currency: "NGN"}}
	realHash := CanonicalHash(rows)
	total, _ := TotalMinor(rows)

	var sig, declaredHash string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := map[string]any{
			"business_date":                 "2026-06-15",
			"record_count":                  1,
			"recovery_deducted_total_minor": total,
			"content_hash":                  declaredHash,
			"envelope_signature":            sig,
			"rows": []map[string]any{
				{"msisdn_token": "tokA", "recovery_deducted_minor": 500, "currency": "NGN"},
			},
		}
		_ = json.NewEncoder(w).Encode(env)
	}))
	defer srv.Close()
	a := &HTTPAdapter{Client: srv.Client(), URL: srv.URL, SecretEnv: "REC_FEED_HMAC"}

	// Valid: correct hash + a MAC over it.
	declaredHash = realHash
	sig = Sign(secret, "2026-06-15", 1, total, realHash)
	if env, err := a.FetchDay(context.Background(), "REAL_NG", "2026-06-15"); err != nil || len(env.Rows) != 1 {
		t.Fatalf("a valid envelope must be accepted: %v", err)
	}

	// Bad MAC (signed with the wrong secret) → reject.
	declaredHash = realHash
	sig = Sign("wrong-secret", "2026-06-15", 1, total, realHash)
	if _, err := a.FetchDay(context.Background(), "REAL_NG", "2026-06-15"); !errors.Is(err, ErrFeedUnauthenticated) {
		t.Fatalf("a bad envelope MAC must reject the day, got %v", err)
	}

	// Tampered rows: a validly-signed but wrong content_hash (hash no longer matches
	// the delivered rows) → reject at the integrity check.
	declaredHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	sig = Sign(secret, "2026-06-15", 1, total, declaredHash)
	if _, err := a.FetchDay(context.Background(), "REAL_NG", "2026-06-15"); !errors.Is(err, ErrFeedUnauthenticated) {
		t.Fatalf("a hash that does not match the rows must reject, got %v", err)
	}
}
