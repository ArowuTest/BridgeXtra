//go:build simseed_loop

package simseed

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

type decisionRow struct {
	tier     string
	maxFace  int64
	reasons  string
	eligible bool
	found    bool
}

func decisionByToken(t *testing.T, db *testutil.DB, token string) decisionRow {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var d decisionRow
	err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `
			SELECT d.tier_code, d.max_face_value_minor, d.reason_codes::text,
			       COALESCE(d.decision_doc->>'eligible','true') = 'true'
			FROM decision_snapshots d
			JOIN subscriber_accounts s ON s.subscriber_account_id = d.subscriber_account_id
			WHERE s.msisdn_token = $1 AND d.is_current`, token).Scan(&d.tier, &d.maxFace, &d.reasons, &d.eligible)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e == nil {
			d.found = true
		}
		return e
	})
	if err != nil {
		t.Fatalf("decision for %s: %v", token, err)
	}
	return d
}

func advanceFaceByToken(t *testing.T, db *testutil.DB, token string) (int64, bool) {
	t.Helper()
	ctx := platform.WithTenant(context.Background(), SyntheticTelco)
	var face int64
	var found bool
	err := repo.WithTenantTx(ctx, db.App, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `
			SELECT a.face_value_minor FROM advances a
			JOIN subscriber_accounts s ON s.subscriber_account_id = a.subscriber_account_id
			WHERE s.msisdn_token = $1`, token).Scan(&face)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e == nil {
			found = true
		}
		return e
	})
	if err != nil {
		t.Fatalf("advance for %s: %v", token, err)
	}
	return face, found
}

// Slice 2: the varied population scores + originates as designed — exact per-profile
// limits + reason codes, the winsorisation caps the defaulter's spike, the declined
// profile is rejected and books no advance, and the treasury floor is NOT tripped.
func TestLoop_Slice2_VariedPopulation(t *testing.T) {
	db := testutil.MustSetup(t, "simseed_loop_s2")
	ctx := context.Background()
	plan := LoopPlan{Seed: "loop-s2", BusinessDay: recentBusinessDay(), ProgrammeID: "prg_sim_airtime01", Repeat: 1}

	res, err := RunLoop(ctx, db.App, plan)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if res.Subscribers != 5 || res.Advances != 4 || res.Declined != 1 {
		t.Fatalf("want 5 subscribers / 4 advances / 1 declined, got %+v", res)
	}

	loopSeed := plan.Seed + "|loop"
	for i, p := range profiles {
		token := msisdnToken(loopSeed, i)
		d := decisionByToken(t, db, token)

		// Token-join (U8): every cohort token has an is_current decision.
		if !d.found {
			t.Fatalf("%s (%s): no is_current decision", p.name, token)
		}
		if d.eligible != p.expectEligible {
			t.Errorf("%s: eligible=%v, want %v (reasons=%s)", p.name, d.eligible, p.expectEligible, d.reasons)
		}
		if d.maxFace != p.expectMaxFace {
			t.Errorf("%s: max_face=%d, want %d (tier=%s reasons=%s)", p.name, d.maxFace, p.expectMaxFace, d.tier, d.reasons)
		}
		if p.expectReason != "" && !strings.Contains(d.reasons, p.expectReason) {
			t.Errorf("%s: reason_codes %s must contain %q", p.name, d.reasons, p.expectReason)
		}

		face, hasAdv := advanceFaceByToken(t, db, token)
		if p.originates {
			if !hasAdv {
				t.Errorf("%s: expected an advance, none booked", p.name)
			} else if face != p.expectMaxFace {
				t.Errorf("%s: advance face=%d, want top rung %d", p.name, face, p.expectMaxFace)
			}
		} else if hasAdv {
			t.Errorf("%s: declined profile must NOT book an advance, got face=%d", p.name, face)
		}
	}

	// Winsorisation falsification (design §2): the defaulter's raw 13-week sum is
	// 3000000 + 12*45000 = 3540000 (>=1000000 -> TIER_02 -> 100000). The winsorised total
	// caps the spike week, so the ACTUAL limit is the starter 50000. The raw-sum
	// expectation MUST NOT hold; the winsorised one MUST.
	dft := decisionByToken(t, db, msisdnToken(loopSeed, 3)) // index 3 = defaulter
	if dft.maxFace == 100000 {
		t.Fatalf("defaulter scored the RAW-sum tier (100000) — winsorisation did not cap the spike")
	}
	if dft.maxFace != 50000 {
		t.Fatalf("defaulter must score the winsorised starter limit 50000, got %d (tier=%s)", dft.maxFace, dft.tier)
	}

	// Treasury floor is real but NOT tripped by the small default cohort: the
	// programme stays ACTIVE (a guardrail trip would SUSPEND it).
	var status string
	if err := repo.WithTenantTx(platform.WithTenant(ctx, SyntheticTelco), db.App, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM programmes WHERE programme_id=$1`, plan.ProgrammeID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" {
		t.Fatalf("the small default cohort must not trip the treasury guardrail; programme status=%s", status)
	}
}
