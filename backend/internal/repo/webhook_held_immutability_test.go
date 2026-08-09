package repo_test

// BX-HIGH-013 / HIGH-014 (0074): the recharge-webhook root-of-trust map and the HELD
// maker-checker release queue carried table-wide tcp_app UPDATE grants. A live
// credential could be re-bound in place, and a single UPDATE could self-approve a HELD
// release (the approved_by<>requested_by CHECK stops equal values, not setting both in
// one statement). 0074 locks both with column-scoped grants + immutability triggers.
// The triggers are attacked here even via the table OWNER (db.Admin) — the managed-PG
// worker=owner case where column grants do not bind and only the trigger holds.

import (
	"context"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func colUpdatable(t *testing.T, db *testutil.DB, table, col string) bool {
	t.Helper()
	var can bool
	if err := db.Admin.QueryRow(context.Background(),
		`SELECT has_column_privilege('tcp_app', $1, $2, 'UPDATE')`, table, col).Scan(&can); err != nil {
		t.Fatal(err)
	}
	return can
}

func TestBXHIGH013_WebhookCredentialImmutable(t *testing.T) {
	db := testutil.MustSetup(t, "wh_cred_immut")
	ctx := context.Background()
	if _, err := db.Admin.Exec(ctx, `
		INSERT INTO telco_webhook_credentials (key_id, telco_id, secret_env, label)
		VALUES ('kid_immut', 'SIM_NG', 'ENV_SECRET_1', 'test')`); err != nil {
		t.Fatal(err)
	}

	// The trust binding is write-once — refused even for the table owner (trigger).
	for _, set := range []string{"telco_id = 'OTHER_NG'", "secret_env = 'ENV_SECRET_2'", "key_id = 'kid_other'", "label = 'relabelled'"} {
		_, err := db.Admin.Exec(ctx, `UPDATE telco_webhook_credentials SET `+set+` WHERE key_id='kid_immut'`)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("rebinding [%s] must be refused by the trigger, got: %v", set, err)
		}
	}

	// The legitimate revoke (status) still works.
	if _, err := db.Admin.Exec(ctx, `UPDATE telco_webhook_credentials SET status='REVOKED' WHERE key_id='kid_immut'`); err != nil {
		t.Fatalf("revoke (status) must still work: %v", err)
	}
	// A REVOKED credential is terminal — cannot be un-revoked.
	if _, err := db.Admin.Exec(ctx, `UPDATE telco_webhook_credentials SET status='ACTIVE' WHERE key_id='kid_immut'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a REVOKED credential must be immutable, got: %v", err)
	}

	// The tcp_app UPDATE grant is column-scoped: status yes, trust binding no.
	if !colUpdatable(t, db, "telco_webhook_credentials", "status") {
		t.Error("tcp_app must retain UPDATE(status) on telco_webhook_credentials")
	}
	for _, col := range []string{"secret_env", "telco_id", "key_id"} {
		if colUpdatable(t, db, "telco_webhook_credentials", col) {
			t.Errorf("tcp_app must NOT have UPDATE(%s) on telco_webhook_credentials", col)
		}
	}
}

func TestBXHIGH014_HeldRechargeMakerCheckerAtDB(t *testing.T) {
	db := testutil.MustSetup(t, "held_immut")
	ctx := context.Background()
	seed := func(id string) {
		t.Helper()
		if _, err := db.Admin.Exec(ctx, `
			INSERT INTO held_recharge_events
			  (held_id, telco_id, source_event_id, msisdn_token, amount_minor, currency, occurred_at, reason)
			VALUES ($1, 'SIM_NG', 'wh:'||$1, 'tok_h', 100000, 'NGN', now(), 'PER_EVENT_CLAMP')`, id); err != nil {
			t.Fatal(err)
		}
	}

	// Identity / amount / event evidence is write-once (trigger, even for the owner).
	seed("h1")
	for _, set := range []string{"amount_minor = 1", "source_event_id = 'wh:other'", "msisdn_token = 'x'", "currency = 'USD'"} {
		_, err := db.Admin.Exec(ctx, `UPDATE held_recharge_events SET `+set+` WHERE held_id='h1'`)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("mutating [%s] must be refused, got: %v", set, err)
		}
	}

	// THE maker-checker DB-bypass: one statement setting requester + approver + RELEASED.
	seed("h2")
	if _, err := db.Admin.Exec(ctx, `
		UPDATE held_recharge_events SET requested_by='mallory', approved_by='mallory2', status='RELEASED', resolved_at=now()
		WHERE held_id='h2'`); err == nil || !strings.Contains(err.Error(), "four-eyes") {
		t.Fatalf("a single UPDATE self-approving a release must be refused (four-eyes at the DB), got: %v", err)
	}

	// The legitimate two-step flow works: maker requests, DISTINCT checker releases.
	seed("h3")
	if _, err := db.Admin.Exec(ctx, `UPDATE held_recharge_events SET requested_by='alice' WHERE held_id='h3' AND status='HELD' AND requested_by IS NULL`); err != nil {
		t.Fatalf("maker request must work: %v", err)
	}
	if _, err := db.Admin.Exec(ctx, `
		UPDATE held_recharge_events SET approved_by='bob', status='RELEASED', resolved_at=now()
		WHERE held_id='h3' AND status='HELD' AND requested_by IS NOT NULL AND requested_by <> 'bob'`); err != nil {
		t.Fatalf("distinct-checker release must work: %v", err)
	}
	// A RELEASED hold is terminal.
	if _, err := db.Admin.Exec(ctx, `UPDATE held_recharge_events SET status='HELD' WHERE held_id='h3'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a RELEASED hold must be immutable, got: %v", err)
	}

	// Column-scoped grant: lifecycle yes, identity/money no.
	for _, col := range []string{"status", "requested_by", "approved_by", "resolved_at"} {
		if !colUpdatable(t, db, "held_recharge_events", col) {
			t.Errorf("tcp_app must retain UPDATE(%s) on held_recharge_events", col)
		}
	}
	for _, col := range []string{"amount_minor", "source_event_id", "msisdn_token", "occurred_at"} {
		if colUpdatable(t, db, "held_recharge_events", col) {
			t.Errorf("tcp_app must NOT have UPDATE(%s) on held_recharge_events", col)
		}
	}
}
