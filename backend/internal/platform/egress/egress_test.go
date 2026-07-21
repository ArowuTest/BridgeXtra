package egress_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
)

// The guard must refuse the cloud-metadata / link-local range and the
// unspecified address — the addresses that are never a legitimate egress
// target — while allowing an ordinary reachable server.
func TestSafeClient_BlocksMetadataAndUnspecified(t *testing.T) {
	c := egress.SafeClient(3 * time.Second)
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/", // AWS/GCP/Azure metadata
		"http://169.254.169.254:80/",
		"http://[fe80::1]/", // IPv6 link-local
		"http://0.0.0.0/",   // unspecified
	} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		_, err := c.Do(req)
		if err == nil {
			t.Errorf("egress to %s must be blocked", target)
			continue
		}
		if !errors.Is(err, egress.ErrBlocked) {
			t.Errorf("egress to %s: want ErrBlocked, got %v", target, err)
		}
	}
}

// A reachable server (httptest binds loopback, which is deliberately allowed
// for the dev simulator) must still work through the guarded client.
func TestSafeClient_AllowsReachableServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := egress.SafeClient(3 * time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("reachable server must be allowed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestIsBlocked(t *testing.T) {
	cases := map[string]bool{
		"169.254.169.254": true,  // link-local (metadata)
		"224.0.0.1":       true,  // multicast
		"0.0.0.0":         true,  // unspecified
		"8.8.8.8":         false, // public
		"127.0.0.1":       false, // loopback — deliberately allowed here
		"10.0.0.5":        false, // private — deliberately allowed here
	}
	for ipStr, want := range cases {
		if got := egress.IsBlocked(net.ParseIP(ipStr)); got != want {
			t.Errorf("IsBlocked(%s) = %v, want %v", ipStr, got, want)
		}
	}
}

// #44 (VR-32 prod hardening): with the private-range block ON, loopback and
// RFC1918/ULA are ALSO blocked; with it OFF (default), they are allowed. The
// always-blocked ranges (metadata/multicast/unspecified) are unaffected.
func TestBlockPrivate_Toggle(t *testing.T) {
	t.Cleanup(func() { egress.SetBlockPrivate(false) })

	private := []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.5.5", "172.16.0.1", "fc00::1"}

	// Default OFF: private/loopback allowed.
	egress.SetBlockPrivate(false)
	for _, ip := range private {
		if egress.IsBlocked(net.ParseIP(ip)) {
			t.Errorf("with block OFF, %s must be allowed", ip)
		}
	}
	// ON: private/loopback blocked.
	egress.SetBlockPrivate(true)
	for _, ip := range private {
		if !egress.IsBlocked(net.ParseIP(ip)) {
			t.Errorf("with block ON, %s must be blocked", ip)
		}
	}
	// Always-blocked ranges stay blocked; an ordinary public IP stays allowed.
	if !egress.IsBlocked(net.ParseIP("169.254.169.254")) {
		t.Error("metadata address must always be blocked")
	}
	if egress.IsBlocked(net.ParseIP("8.8.8.8")) {
		t.Error("a public address must remain allowed even in strict mode")
	}
}
