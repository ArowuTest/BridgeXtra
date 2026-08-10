package repo_test

// Phase 1 S2 — webhook credential resolution: key_id -> telco + secret env name;
// unknown and REVOKED both yield ErrWebhookCredentialNotFound (uniform reject,
// no ACTIVE/REVOKED oracle); and secret_env is unique across credentials (a
// shared secret would enable cross-telco forgery under another's public key_id).
//
// BX-HIGH-013: creation is an owner/admin BOOTSTRAP operation. The request-serving
// app role (tcp_app) may resolve (SELECT) and revoke (UPDATE status) a credential
// but must NOT be able to INSERT one — otherwise a compromised app could forge the
// key_id -> telco -> secret_env trust map that authenticates inbound recharge
// webhooks into the money core.

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestS2_WebhookCredentials_ResolveRevokeUnique(t *testing.T) {
	db := testutil.MustSetup(t, "repo_whcred")
	ctx := context.Background()
	admin := &repo.WebhookCredentials{Pool: db.Admin} // creation: owner/admin bootstrap
	app := &repo.WebhookCredentials{Pool: db.App}     // runtime: resolve + revoke

	if err := admin.Create(ctx, "kid-1", "SIM_NG", "TCP_MTN_HMAC_1", "mtn primary"); err != nil {
		t.Fatalf("create (admin): %v", err)
	}

	c, err := app.ResolveByKeyID(ctx, "kid-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if c.TelcoID != "SIM_NG" || c.SecretEnv != "TCP_MTN_HMAC_1" {
		t.Fatalf("resolved credential wrong: %+v", c)
	}

	// Unknown key_id -> not found.
	if _, err := app.ResolveByKeyID(ctx, "nope"); !errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		t.Fatalf("unknown key_id must be ErrWebhookCredentialNotFound, got %v", err)
	}

	// Revoked -> no longer resolves (uniform with unknown). The app performs the revoke
	// (UPDATE status), which it retains.
	if err := app.Revoke(ctx, "kid-1"); err != nil {
		t.Fatalf("revoke (app): %v", err)
	}
	if _, err := app.ResolveByKeyID(ctx, "kid-1"); !errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		t.Fatalf("revoked key_id must not resolve, got %v", err)
	}

	// secret_env is unique: a second credential reusing the same env var is refused.
	if err := admin.Create(ctx, "kid-2", "SIM_NG", "TCP_MTN_HMAC_1", "dup secret"); err == nil {
		t.Fatal("two credentials must not share one secret_env (cross-telco forgery guard)")
	}
}

// BX-HIGH-013: the request-serving app role must NOT be able to forge a webhook
// credential. Migration 0077 revokes INSERT from tcp_app; this pins it. Mutation
// proof: drop the REVOKE and this INSERT succeeds.
func TestBXHIGH013_AppCannotInsertWebhookCredential(t *testing.T) {
	db := testutil.MustSetup(t, "repo_whcred_noinsert")
	ctx := context.Background()
	app := &repo.WebhookCredentials{Pool: db.App}

	if err := app.Create(ctx, "kid-forge", "SIM_NG", "TCP_FORGE_1", "forged"); err == nil {
		t.Fatal("tcp_app must NOT be able to INSERT a webhook credential — creation is owner/admin only (BX-HIGH-013)")
	}
}
