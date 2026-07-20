// Package egress is the shared SSRF control for every outbound surface that
// dials a CONFIG-DRIVEN URL — the telco fulfilment adapter, the feature-file
// fetcher, the SMS sender, and the reconciliation telco-records fetcher
// (reviewer VR-32 closed three doors; the external audit found reconciliation
// as the FOURTH — R-P0-5). All four build an *http.Client through SafeClient
// so a single guard covers them.
//
// The guard runs at DIAL time on the RESOLVED IP and PINS the connection to
// that IP, so a hostname that resolves to a blocked address is refused and DNS
// rebinding cannot slip a second resolution past the check. TLS still verifies
// the URL's hostname because http.Transport layers TLS over the returned conn
// using the request's ServerName, not the dialed IP.
//
// What it blocks: the addresses that are NEVER a legitimate egress target —
// the link-local range (169.254.0.0/16 incl. the cloud metadata service
// 169.254.169.254, and fe80::/10), multicast, and the unspecified address.
// What it deliberately does NOT block: loopback and RFC1918 private ranges —
// this platform legitimately dials the simulator on localhost in dev and
// Render service-to-service over a private network. Blocking those is an
// environment-specific prod hardening tracked separately (egress private-range
// block); doing it here would break real, sanctioned traffic.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrBlocked is returned when a destination resolves to a forbidden address.
var ErrBlocked = errors.New("egress: destination is not a permitted target")

// SafeClient returns an *http.Client whose dialer enforces the egress guard.
// A zero timeout means "no client-level timeout" (the caller governs deadlines
// via the request context) — matching the adapter's per-call deadline model.
func SafeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           guardedDial,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	var lastErr error
	for _, ipa := range ips {
		if IsBlocked(ipa.IP) {
			lastErr = fmt.Errorf("%w: %q resolves to %s", ErrBlocked, host, ipa.IP)
			continue
		}
		// Pin the dial to the exact IP we checked (no re-resolution / rebinding).
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: %q has no address", ErrBlocked, host)
	}
	return nil, lastErr
}

// IsBlocked reports whether an IP is a forbidden egress target: link-local
// (cloud metadata) unicast/multicast, any multicast, or the unspecified
// address. Exported so the config validators can reject an endpoint that is an
// IP literal in a blocked range at approval time (defence in depth).
func IsBlocked(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
