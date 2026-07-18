package repo_test

// Self-audit: settlement statements and guardrail trips are contractual / audit
// evidence. 0020 makes a FINAL statement frozen (trigger) and locks the breach
// evidence + statement identity out of the tcp_app UPDATE grant. These tests
// attack the forbidden mutations and confirm the legitimate ones still work.

import (
	"context"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func seedFinalStatement(t *testing.T, db *testutil.DB, id, state, periodStart, periodEnd string) {
	t.Helper()
	if _, err := db.Admin.Exec(context.Background(), `
		INSERT INTO settlement_statements
		  (statement_id, telco_id, programme_id, period_start, period_end, state, currency, content_hash, terms_version_id)
		SELECT $1, telco_id, 'prg_sim_airtime01', $3, $4, $2, 'NGN',
		       CASE WHEN $2='FINAL' THEN 'abc123' ELSE NULL END, 'terms_v1'
		FROM programmes WHERE programme_id='prg_sim_airtime01'`, id, state, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
}

func TestSelfAudit_SettlementFinalIsImmutable(t *testing.T) {
	db := testutil.MustSetup(t, "settle_immut")
	ctx := context.Background()

	// A FINAL statement cannot have its content_hash rewritten — even by the
	// table owner (the trigger backstop).
	seedFinalStatement(t, db, "stm_final_1", "FINAL", "2026-01-01", "2026-01-08")
	_, err := db.Admin.Exec(ctx,
		`UPDATE settlement_statements SET content_hash='tampered' WHERE statement_id='stm_final_1'`)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("FINAL content_hash must be immutable via trigger, got: %v", err)
	}
	// Nor re-opened to DRAFT.
	_, err = db.Admin.Exec(ctx,
		`UPDATE settlement_statements SET state='DRAFT' WHERE statement_id='stm_final_1'`)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("FINAL statement must not be re-opened, got: %v", err)
	}

	// The legitimate DRAFT -> FINAL finalise still works.
	seedFinalStatement(t, db, "stm_draft_1", "DRAFT", "2026-02-01", "2026-02-08")
	if _, err := db.Admin.Exec(ctx, `
		UPDATE settlement_statements SET state='FINAL', content_hash='def456', finalised_at=now()
		WHERE statement_id='stm_draft_1' AND state='DRAFT'`); err != nil {
		t.Fatalf("DRAFT -> FINAL finalise must still work: %v", err)
	}
}

// The tcp_app UPDATE grant is column-scoped: it may touch lifecycle columns but
// NOT the recorded evidence / identity.
func TestSelfAudit_GrantScopes(t *testing.T) {
	db := testutil.MustSetup(t, "grant_scopes")
	ctx := context.Background()

	type probe struct {
		table, col string
		wantUpdate bool
	}
	for _, p := range []probe{
		// guardrail_trips: re-arm lifecycle yes, breach evidence no.
		{"guardrail_trips", "state", true},
		{"guardrail_trips", "rearm_approved_by", true},
		{"guardrail_trips", "measured_minor", false},
		{"guardrail_trips", "limit_minor", false},
		// settlement_statements: finalise columns yes, identity no.
		{"settlement_statements", "content_hash", true},
		{"settlement_statements", "state", true},
		{"settlement_statements", "period_start", false},
		{"settlement_statements", "terms_version_id", false},
	} {
		var can bool
		if err := db.Admin.QueryRow(ctx,
			`SELECT has_column_privilege('tcp_app', $1, $2, 'UPDATE')`, p.table, p.col).Scan(&can); err != nil {
			t.Fatal(err)
		}
		if can != p.wantUpdate {
			t.Errorf("tcp_app UPDATE on %s.%s = %v, want %v", p.table, p.col, can, p.wantUpdate)
		}
	}
}
