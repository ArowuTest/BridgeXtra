package handler

// R-P2-7: client-IP derivation through the trusted proxy chain. White-box
// (in-package) so it can call the unexported clientIP directly.

import (
	"net/http"
	"testing"
)

func req(remote, xff string) *http.Request {
	r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestClientIP_TrustedProxy(t *testing.T) {
	cases := []struct {
		name        string
		remote      string
		xff         string
		trustedHops int
		want        string
	}{
		// Direct (no proxy trusted): always the peer, XFF ignored even if a
		// client tries to spoof it.
		{"direct spoof ignored", "9.9.9.9:5555", "1.2.3.4", 0, "9.9.9.9"},
		// One trusted proxy (Render): the real client is the rightmost XFF.
		{"render one hop", "10.0.0.1:443", "203.0.113.7", 1, "203.0.113.7"},
		// One trusted proxy but a spoofed extra hop prepended: we only trust
		// one, so we take the rightmost XFF (the client the proxy saw).
		{"one hop, spoofed prefix", "10.0.0.1:443", "evil, 203.0.113.7", 1, "203.0.113.7"},
		// Two trusted proxies: strip one from the right, take the next.
		{"two hops", "10.0.0.1:443", "203.0.113.7, 10.0.0.9", 2, "203.0.113.7"},
		// Trust one hop but XFF is empty (direct dev call): fall back to peer.
		{"one hop no xff", "127.0.0.1:1234", "", 1, "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clientIP(req(c.remote, c.xff), c.trustedHops); got != c.want {
				t.Fatalf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}
