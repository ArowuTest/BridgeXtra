package repo_test

// BX-HIGH-003: telco suspension is the tenant kill-switch. Authentication previously
// checked only the CREDENTIAL status, never the TELCO status — so setting a telco to
// SUSPENDED did nothing to its money path. The fix joins telcos at both authenticated
// money entry points (channel auth Telcos.ResolveCredential + webhook auth
// WebhookCredentials.ResolveByKeyID) and requires telcos.status='ACTIVE', so suspension
// denies every authenticated channel and inbound-recovery operation immediately, at the
// DB, with no per-usecase check to forget.

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func TestBXHIGH003_SuspendedTelcoDeniedAtBothMoneyDoors(t *testing.T) {
	db := testutil.MustSetup(t, "high003_killswitch")
	ctx := context.Background()
	db.SeedTelco(t, "KILL_NG", "kill-apikey") // ACTIVE telco + api credential
	if _, err := db.Admin.Exec(ctx,
		`INSERT INTO telco_webhook_credentials (key_id, telco_id, secret_env, label)
		 VALUES ('kid-kill','KILL_NG','TCP_KILL_HMAC','k')`); err != nil {
		t.Fatal(err)
	}
	telcos := &repo.Telcos{Pool: db.App}
	wh := &repo.WebhookCredentials{Pool: db.App}

	// While ACTIVE, both money doors authenticate.
	if _, _, err := telcos.ResolveCredential(ctx, "kill-apikey"); err != nil {
		t.Fatalf("active telco channel auth must resolve: %v", err)
	}
	if _, err := wh.ResolveByKeyID(ctx, "kid-kill"); err != nil {
		t.Fatalf("active telco webhook auth must resolve: %v", err)
	}

	// Flip the kill-switch.
	if _, err := db.Admin.Exec(ctx, `UPDATE telcos SET status='SUSPENDED' WHERE telco_id='KILL_NG'`); err != nil {
		t.Fatal(err)
	}

	// Both money doors now deny immediately. Mutation proof: drop the telcos join / t.status
	// filter in either resolver and that door resolves a suspended telco again.
	if _, _, err := telcos.ResolveCredential(ctx, "kill-apikey"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("suspended telco channel auth must be denied (ErrNotFound), got %v", err)
	}
	if _, err := wh.ResolveByKeyID(ctx, "kid-kill"); !errors.Is(err, repo.ErrWebhookCredentialNotFound) {
		t.Fatalf("suspended telco webhook auth must be denied (ErrWebhookCredentialNotFound), got %v", err)
	}
}
