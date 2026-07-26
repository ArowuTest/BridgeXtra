package origination_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

// Build 1 — the programme economic/legal go-live gate. A programme may be ACTIVE
// but cannot originate until programme.economics is configured. prg_sim_airtime01
// is seeded valid by migration 0058 (so the happy path in origination_test.go
// exercises the present case); these tests remove/corrupt it and assert refusal.

const validEconJSON = `{"funding_model":"PLATFORM_CAPITAL","lender_of_record":"BridgeXtra Financial Services Ltd","loss_bearer":"BridgeXtra Financial Services Ltd","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_INCLUSIVE_FEE"}`

func (f *fixture) deleteEconomics(t *testing.T) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(),
		`DELETE FROM config_versions WHERE domain='programme.economics'`); err != nil {
		t.Fatal(err)
	}
}

// insertEconomics seeds a raw ACTIVE programme.economics version at an arbitrary
// scope, bypassing the maker-checker validator (as a migration seed does). The CTE
// gives the JSON text a definite type so it can be cast to both jsonb (content) and
// bytea (for the hash) in one statement.
func (f *fixture) insertEconomics(t *testing.T, id, scope, contentJSON string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		WITH t AS (SELECT $3::text AS c)
		INSERT INTO config_versions (config_version_id, domain, scope, version_no, state, content, content_hash,
		  effective_from, created_by, approved_by, reason)
		SELECT $1, 'programme.economics', $2, 1, 'ACTIVE',
		  t.c::jsonb, encode(sha256(t.c::bytea),'hex'), now(), 'seed:builder', 'seed:reviewer', 'economics gate test'
		FROM t`, id, scope, contentJSON); err != nil {
		t.Fatal(err)
	}
}

// GetOffers refuses on a programme with no economics configured.
func TestEconomicsGate_GetOffersRefusesWhenUnset(t *testing.T) {
	f := newFixture(t, "econ_getoffers_unset", 0, 2_000)
	f.seedSubscriber(t, "sub_e1", "tok_e1", 50_000)
	f.deleteEconomics(t)
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_e1"); !errors.Is(err, origination.ErrProgrammeEconomicsNotSet) {
		t.Fatalf("GetOffers must refuse with ErrProgrammeEconomicsNotSet when economics unset, got %v", err)
	}
}

// Confirm refuses too: an offer minted while economics were present cannot be
// booked once the programme loses its economics — no advance is created.
func TestEconomicsGate_ConfirmRefusesWhenUnset(t *testing.T) {
	f := newFixture(t, "econ_confirm_unset", 0, 2_000)
	f.seedSubscriber(t, "sub_e2", "tok_e2", 50_000)
	ov := f.offersFor(t, "tok_e2")[0] // economics present (seed) -> real offer
	f.deleteEconomics(t)
	_, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_e2", "econ-confirm-1", "cor-econ-1"))
	if !errors.Is(err, origination.ErrProgrammeEconomicsNotSet) {
		t.Fatalf("Confirm must refuse with ErrProgrammeEconomicsNotSet when economics unset, got %v", err)
	}
	if n := f.advanceCount(t); n != 0 {
		t.Fatalf("no advance may exist after an economics-gated Confirm, got %d", n)
	}
}

// Scope-leak guard: a GLOBAL economics config must NOT authorise a specific
// programme (ActiveAt falls back scope->global, but economics are per-programme).
func TestEconomicsGate_GlobalScopeDoesNotAuthorise(t *testing.T) {
	f := newFixture(t, "econ_global_leak", 0, 2_000)
	f.seedSubscriber(t, "sub_e3", "tok_e3", 50_000)
	f.deleteEconomics(t)
	f.insertEconomics(t, "cfg_econ_global", "global", validEconJSON)
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_e3"); !errors.Is(err, origination.ErrProgrammeEconomicsNotSet) {
		t.Fatalf("a GLOBAL economics config must not authorise a programme; want ErrProgrammeEconomicsNotSet, got %v", err)
	}
}

// Floor-in-code: a raw-seeded MALFORMED economics (bypassing the maker-checker
// validator) is still refused at the origination read.
func TestEconomicsGate_MalformedConfigRefused(t *testing.T) {
	f := newFixture(t, "econ_malformed", 0, 2_000)
	f.seedSubscriber(t, "sub_e4", "tok_e4", 50_000)
	// config_versions content is immutable after creation (EXT-3 trigger), so a
	// raw bad config is seeded by replacing the row: delete the valid seed, insert
	// a JSON-valid but out-of-vocabulary one (bypassing the maker-checker validator).
	f.deleteEconomics(t)
	f.insertEconomics(t, "cfg_econ_bad", "programme:prg_sim_airtime01",
		`{"funding_model":"MYSTERY","lender_of_record":"X","loss_bearer":"X","settlement_method":"NET_OFF_AGAINST_AIRTIME","tax_treatment":"VAT_EXEMPT"}`)
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_e4"); !errors.Is(err, origination.ErrProgrammeEconomicsInvalid) {
		t.Fatalf("a malformed economics config must be refused at read; want ErrProgrammeEconomicsInvalid, got %v", err)
	}
}

// Positive control: with the seeded economics present, origination still books a
// loan and records the applied economic terms on the consent (origination audit).
func TestEconomicsGate_PresentOriginatesAndRecordsTerms(t *testing.T) {
	f := newFixture(t, "econ_present", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	if _, err := f.svc.Confirm(tenantCtx(), acceptFor(ov, "tok_sim_0001", "econ-ok-1", "cor-econ-ok-1")); err != nil {
		t.Fatalf("origination must succeed with economics present: %v", err)
	}
	var disclosed string
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT disclosed_terms::text FROM consents ORDER BY accepted_at DESC LIMIT 1`).Scan(&disclosed); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"economic_terms", "PLATFORM_CAPITAL", "economics_config_version_id", "cfg_seed_prog_economics_sim_v1"} {
		if !strings.Contains(disclosed, want) {
			t.Fatalf("origination audit (consent disclosed_terms) must record %q; got %s", want, disclosed)
		}
	}
}
