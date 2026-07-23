package rechargewebhook_test

// Phase 1 S2.1 — inbound auth adapter BOUNDARY CONTRACT tests. These pin the
// exact signed bytes, the one canonical signature/timestamp encoding, and that
// key_id + timestamp + body are ALL covered by the MAC (so a captured signature
// cannot be replayed under a swapped key_id, an altered timestamp, or a tampered
// body). When the real MTN adapter lands, its own contract test either matches
// these bytes or fails loudly.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	rw "github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
)

func TestS21_CanonicalString_ExactContract(t *testing.T) {
	a := rw.NewHMACSHA256Adapter()
	got := a.CanonicalString("kid-1", "1700000000", []byte(`{"x":1}`))
	want := []byte("bridgextra.recharge_webhook.v1\nkid-1\n1700000000\n{\"x\":1}")
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical string drift:\n got %q\nwant %q", got, want)
	}
}

func TestS21_DecodeSig_OnlyLowercaseHex32(t *testing.T) {
	a := rw.NewHMACSHA256Adapter()
	b, err := a.DecodeSig(strings.Repeat("a", 64))
	if err != nil || len(b) != 32 {
		t.Fatalf("valid 64-hex must decode to 32 bytes, got len=%d err=%v", len(b), err)
	}
	for _, bad := range []string{
		"",                             // empty
		strings.Repeat("A", 64),        // uppercase
		strings.Repeat("a", 63),        // too short
		strings.Repeat("a", 65),        // too long
		strings.Repeat("g", 64),        // non-hex
		"0x" + strings.Repeat("a", 62), // 0x prefix
	} {
		if _, err := a.DecodeSig(bad); err == nil {
			t.Errorf("DecodeSig(%q) must fail", bad)
		}
	}
}

func TestS21_ParseTimestamp_EpochSecondsOnly(t *testing.T) {
	a := rw.NewHMACSHA256Adapter()
	tm, err := a.ParseTimestamp("1700000000")
	if err != nil || tm.Unix() != 1700000000 {
		t.Fatalf("valid epoch must parse, got %v err=%v", tm, err)
	}
	for _, bad := range []string{"", "abc", "17.5", "1e9", " 100", "100 ", "0x10"} {
		if _, err := a.ParseTimestamp(bad); err == nil {
			t.Errorf("ParseTimestamp(%q) must fail (no now()/zero fallback)", bad)
		}
	}
}

func TestS21_Verify_RoundTripAndTamperAllFields(t *testing.T) {
	a := rw.NewHMACSHA256Adapter()
	secret := []byte("s3cr3t-hmac-key")
	keyID, ts, body := "kid-1", "1700000000", []byte(`{"event_id":"e1","amount_minor":500}`)

	sig := rw.Sign(a, secret, keyID, ts, body)
	if err := rw.Verify(a, secret, keyID, ts, body, sig); err != nil {
		t.Fatalf("round-trip Sign->Verify must succeed: %v", err)
	}

	// Every one of these must break the MAC — proving all are signed.
	cases := map[string]func() error{
		"wrong secret": func() error { return rw.Verify(a, []byte("other"), keyID, ts, body, sig) },
		"tampered body": func() error {
			return rw.Verify(a, secret, keyID, ts, []byte(`{"event_id":"e1","amount_minor":999}`), sig)
		},
		"tampered ts":    func() error { return rw.Verify(a, secret, keyID, "1700000001", body, sig) },
		"swapped key_id": func() error { return rw.Verify(a, secret, "kid-2", ts, body, sig) },
		"malformed sig":  func() error { return rw.Verify(a, secret, keyID, ts, body, "ZZZ") },
		"empty sig":      func() error { return rw.Verify(a, secret, keyID, ts, body, "") },
	}
	for name, fn := range cases {
		if err := fn(); !errors.Is(err, rw.ErrBadSignature) {
			t.Errorf("%s: want ErrBadSignature, got %v", name, err)
		}
	}
}
