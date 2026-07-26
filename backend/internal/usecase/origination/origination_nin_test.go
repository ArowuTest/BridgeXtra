package origination_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

// Build 2 — the NIN identity-verification eligibility gate. origination.nin_gate is
// seeded require_nin_verified=true (migration 0059); seedSubscriber marks subscribers
// verified by default, so the happy path exercises the verified case. These tests
// make a subscriber unverified (NULL/false) and assert refusal, then check the
// governed opt-out and the fail-closed default.

// setNIN sets a subscriber's nin_verified in place (nil => NULL/unknown).
func (f *fixture) setNIN(t *testing.T, token string, val interface{}) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE subscriber_accounts SET nin_verified = $2 WHERE msisdn_token = $1 AND effective_to IS NULL`,
		token, val); err != nil {
		t.Fatal(err)
	}
}

// setNINGate replaces the origination.nin_gate config (content is immutable, so
// delete+insert). require=false is the governed opt-out.
func (f *fixture) setNINGate(t *testing.T, require bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Admin.Exec(ctx, `DELETE FROM config_versions WHERE domain='origination.nin_gate'`); err != nil {
		t.Fatal(err)
	}
	content := `{"require_nin_verified":true}`
	if !require {
		content = `{"require_nin_verified":false}`
	}
	if _, err := f.db.Admin.Exec(ctx, `
		WITH t AS (SELECT $1::text AS c)
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		SELECT 'cfg_nin_gate_test', 'origination.nin_gate', 'global', 1, 'ACTIVE',
		  t.c::jsonb, encode(sha256(t.c::bytea),'hex'), now(), 'seed:builder', 'seed:reviewer', 'nin gate test'
		FROM t`, content); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) deleteNINGate(t *testing.T) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `DELETE FROM config_versions WHERE domain='origination.nin_gate'`); err != nil {
		t.Fatal(err)
	}
}

// NULL (unknown) => not verified => GetOffers refuses.
func TestNINGate_GetOffersRefusesWhenUnknown(t *testing.T) {
	f := newFixture(t, "nin_getoffers_null", 0, 2_000)
	f.seedSubscriber(t, "sub_n1", "tok_n1", 50_000)
	f.setNIN(t, "tok_n1", nil)
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_n1"); !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("an unknown (NULL) nin_verified must refuse; want ErrSubscriberIneligible, got %v", err)
	}
}

// false (explicitly unverified) => GetOffers refuses.
func TestNINGate_GetOffersRefusesWhenFalse(t *testing.T) {
	f := newFixture(t, "nin_getoffers_false", 0, 2_000)
	f.seedSubscriber(t, "sub_n2", "tok_n2", 50_000)
	f.setNIN(t, "tok_n2", false)
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_n2"); !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("a false nin_verified must refuse; want ErrSubscriberIneligible, got %v", err)
	}
}

// Confirm refuses too: an offer minted while verified cannot be booked once the
// subscriber's verification is revoked — no advance.
func TestNINGate_ConfirmRefusesWhenUnverified(t *testing.T) {
	f := newFixture(t, "nin_confirm", 0, 2_000)
	f.seedSubscriber(t, "sub_n3", "tok_n3", 50_000) // verified
	ov := f.offersFor(t, "tok_n3")[0]
	f.setNIN(t, "tok_n3", nil) // verification lost before confirm
	_, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_n3", "nin-confirm-1", "cor-nin-1"))
	if !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("Confirm must refuse an unverified subscriber; want ErrSubscriberIneligible, got %v", err)
	}
	if n := f.advanceCount(t); n != 0 {
		t.Fatalf("no advance may exist after a NIN-gated Confirm, got %d", n)
	}
}

// Positive control: a verified subscriber originates normally.
func TestNINGate_VerifiedOriginates(t *testing.T) {
	f := newFixture(t, "nin_verified_ok", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0] // migration-backfilled nin_verified=true
	if _, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_sim_0001", "nin-ok-1", "cor-nin-ok-1")); err != nil {
		t.Fatalf("a verified subscriber must originate: %v", err)
	}
}

// Governed opt-out: require_nin_verified=false lets an unverified subscriber borrow.
func TestNINGate_OptOutAllowsUnverified(t *testing.T) {
	f := newFixture(t, "nin_optout", 0, 2_000)
	f.seedSubscriber(t, "sub_n5", "tok_n5", 50_000)
	f.setNIN(t, "tok_n5", nil)
	f.setNINGate(t, false) // opt out
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_n5"); err != nil {
		t.Fatalf("with require_nin_verified=false, an unverified subscriber must be served: %v", err)
	}
}

// Fail-closed default: if the nin_gate config is ABSENT, the gate is REQUIRED
// (absent must never mean "off") — an unverified subscriber is still refused.
func TestNINGate_FailClosedWhenConfigAbsent(t *testing.T) {
	f := newFixture(t, "nin_failclosed", 0, 2_000)
	f.seedSubscriber(t, "sub_n6", "tok_n6", 50_000)
	f.setNIN(t, "tok_n6", nil)
	f.deleteNINGate(t) // no config at all
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_n6"); !errors.Is(err, origination.ErrSubscriberIneligible) {
		t.Fatalf("with no nin_gate config, the gate must fail closed (require); want ErrSubscriberIneligible, got %v", err)
	}
}
