// Package rechargewebhook implements the inbound MNO recharge-stream webhook
// (Phase 1 S2): a config-selected, authenticated ingress that maps a vendor
// recharge event to the canonical recovery command and feeds the existing
// recovery money-core. Everything MNO-specific (the exact signed bytes, the
// signature/timestamp encoding, the field mapping) lives behind adapter
// interfaces so the real MTN specifics are an adapter swap, not a rebuild.
package rechargewebhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"time"
)

// ErrBadSignature is the single, uniform verification failure — callers must not
// distinguish its causes to a client (no oracle).
var ErrBadSignature = errors.New("rechargewebhook: signature verification failed")

// InboundAuthAdapter isolates the vendor-specific wire details of authenticating
// an inbound webhook. The mock hmac_sha256 adapter below is UNVERIFIED against a
// real MTN sample (see build/PHASE1_S2_ASSUMED_CONTRACT.md); a real-MTN adapter
// is a future config-selected implementation of this SAME interface.
type InboundAuthAdapter interface {
	// Scheme is the config `auth` value this adapter implements.
	Scheme() string
	// CanonicalString returns the EXACT bytes the MAC is computed over. rawBody is
	// the exact received body (buffered once) and comes last so embedded newlines
	// are unambiguous.
	CanonicalString(keyID, timestamp string, rawBody []byte) []byte
	// DecodeSig decodes the signature header to raw MAC bytes; any malformed input
	// is an error (fail-closed), never a silent empty/partial value.
	DecodeSig(header string) ([]byte, error)
	// ParseTimestamp parses the timestamp header to an absolute time; malformed or
	// empty is an error (never a now()/zero-value fallback).
	ParseTimestamp(header string) (time.Time, error)
}

// canonicalDomain is the domain-separation prefix: it binds every signature to
// this exact purpose+version, so a MAC captured for another use can never be
// replayed here and a format change is a clean version bump.
const canonicalDomain = "bridgextra.recharge_webhook.v1"

type hmacSHA256Adapter struct{}

// NewHMACSHA256Adapter is the mock-first inbound auth adapter.
func NewHMACSHA256Adapter() InboundAuthAdapter { return hmacSHA256Adapter{} }

func (hmacSHA256Adapter) Scheme() string { return "hmac_sha256" }

func (hmacSHA256Adapter) CanonicalString(keyID, timestamp string, rawBody []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(canonicalDomain) + len(keyID) + len(timestamp) + len(rawBody) + 3)
	b.WriteString(canonicalDomain)
	b.WriteByte('\n')
	b.WriteString(keyID)
	b.WriteByte('\n')
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.Write(rawBody)
	return b.Bytes()
}

// hexSig pins ONE canonical signature encoding: exactly 64 lowercase hex chars
// (= 32 bytes). Any case/length/charset variation is rejected, so there is no
// second, malleable representation of the same MAC.
var hexSig = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (hmacSHA256Adapter) DecodeSig(header string) ([]byte, error) {
	if !hexSig.MatchString(header) {
		return nil, errors.New("signature must be 64 lowercase hex characters")
	}
	return hex.DecodeString(header) // cannot fail after the regexp; yields 32 bytes
}

// tsSeconds pins the timestamp to a plain epoch-seconds integer (optionally
// negative for pre-1970, which the freshness window rejects anyway).
var tsSeconds = regexp.MustCompile(`^-?[0-9]{1,19}$`)

func (hmacSHA256Adapter) ParseTimestamp(header string) (time.Time, error) {
	if !tsSeconds.MatchString(header) {
		return time.Time{}, errors.New("timestamp must be integer epoch seconds")
	}
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0).UTC(), nil
}

// Verify recomputes the MAC over the adapter's canonical string with secret and
// constant-time compares it against the decoded signature header. Every failure
// collapses to ErrBadSignature.
func Verify(a InboundAuthAdapter, secret []byte, keyID, timestamp string, rawBody []byte, sigHeader string) error {
	decoded, err := a.DecodeSig(sigHeader)
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(a.CanonicalString(keyID, timestamp, rawBody))
	if !hmac.Equal(mac.Sum(nil), decoded) {
		return ErrBadSignature
	}
	return nil
}

// Sign produces the canonical signature header for (keyID, timestamp, rawBody)
// under secret — used by tests and by any first-party sender/simulator. It is
// the exact inverse of Verify.
func Sign(a InboundAuthAdapter, secret []byte, keyID, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(a.CanonicalString(keyID, timestamp, rawBody))
	return hex.EncodeToString(mac.Sum(nil))
}

// DummyVerify performs an equivalent-cost HMAC against a fixed throwaway secret.
// The handler runs it on unknown/revoked key_id or empty secret so all auth
// failures share one code and latency profile (timing-oracle guard).
func DummyVerify(a InboundAuthAdapter, keyID, timestamp string, rawBody []byte, sigHeader string) {
	_ = Verify(a, dummySecret, keyID, timestamp, rawBody, sigHeader)
}

var dummySecret = []byte("bridgextra.recharge_webhook.dummy-timing-secret")
