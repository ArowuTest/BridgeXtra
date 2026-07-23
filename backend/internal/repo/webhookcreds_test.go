package repo_test

// Phase 1 S2 — webhook credential resolution: key_id -> telco + secret env name;
// unknown and REVOKED both yield ErrWebhookCredentialNotFound (uniform reject,
// no ACTIVE/REVOKED oracle); and secret_env is unique across credentials (a
// shared secret would enable cross-telco forgery under another's public key_id).

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestS2_WebhookCredentials_ResolveRevokeUnique(t *testing.T) {
	db := testutil.MustSetup(t, "repo_whcred")
	r := &repo.WebhookCredentials{Pool: db.App}
	ctx := context.Background()

	if err := r.Create(ctx, "kid-1", "SIM_NG", "TCP_MTN_HMAC_1", "mtn primary"); err != nil {
		t.Fatalf("create: %v", err)
	}

	c, err := r.ResolveByKeyID(ctx, "kid-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if c.TelcoID != "SIM_NG" || c.SecretEnv != "TCP_MTN_HMAC_1" {
		t.Fatalf("resolved credential wrong: %+v", c)
	}

	// Unknown key_id -> not found.
	if _, err := r.ResolveByKeyID(ctx, "nope"); !errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		t.Fatalf("unknown key_id must be ErrWebhookCredentialNotFound, got %v", err)
	}

	// Revoked -> no longer resolves (uniform with unknown).
	if err := r.Revoke(ctx, "kid-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := r.ResolveByKeyID(ctx, "kid-1"); !errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		t.Fatalf("revoked key_id must not resolve, got %v", err)
	}

	// secret_env is unique: a second credential reusing the same env var is refused.
	if err := r.Create(ctx, "kid-2", "SIM_NG", "TCP_MTN_HMAC_1", "dup secret"); err == nil {
		t.Fatal("two credentials must not share one secret_env (cross-telco forgery guard)")
	}
}
