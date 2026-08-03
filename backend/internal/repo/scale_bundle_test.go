package repo_test

// Loan-scale MTN bundle invariants. The config validators only check >0 / ascending,
// NOT scale or the underwriting ratio — so a future scale change could silently move the
// tier limits without the affordability gate and under-collateralise the whole book.
// These pin, against the SEEDED production config, the two properties that make the
// bundle safe: (1) min_recharge_90d is exactly 10× max_face across the whole tier ladder
// (the advance-to-90d-recharge underwriting ratio), and (2) the smallest offer
// denomination is at/under the lowest tier limit (else a starter subscriber gets an
// EMPTY offer ladder — a silent total-lending outage). They also assert the corrected
// ₦-scale numbers, so the migration's values can't regress.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
)

func activeContent(t *testing.T, db *testutil.DB, domain, scope string) []byte {
	t.Helper()
	var content []byte
	err := db.Admin.QueryRow(context.Background(), `
SELECT content FROM config_versions
WHERE domain=$1 AND scope=$2 AND state='ACTIVE'
ORDER BY version_no DESC LIMIT 1`, domain, scope).Scan(&content)
	if err != nil {
		t.Fatalf("read active %s/%s: %v", domain, scope, err)
	}
	return content
}

func TestScoringPolicyBundle_RatioInvariant(t *testing.T) {
	db := testutil.MustSetup(t, "scale_ratio")
	var pol struct {
		Tiers []struct {
			Code                string `json:"code"`
			MaxFaceMinor        int64  `json:"max_face_minor"`
			MinRecharge90dMinor int64  `json:"min_recharge_90d_minor"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(activeContent(t, db, "scoring.policy", "programme:prg_sim_airtime01"), &pol); err != nil {
		t.Fatalf("unmarshal scoring.policy: %v", err)
	}
	// Corrected ₦-scale tier ladder: ₦500 / ₦1k / ₦5k / ₦10k.
	wantFace := []int64{50000, 100000, 500000, 1000000}
	if len(pol.Tiers) != len(wantFace) {
		t.Fatalf("expected %d tiers, got %d", len(wantFace), len(pol.Tiers))
	}
	var prev int64
	for i, tr := range pol.Tiers {
		if tr.MaxFaceMinor != wantFace[i] {
			t.Fatalf("tier %s max_face = %d, want %d (₦-scale ladder)", tr.Code, tr.MaxFaceMinor, wantFace[i])
		}
		// THE underwriting invariant: 90-day affordability gate is exactly 10× the loan.
		if tr.MinRecharge90dMinor != tr.MaxFaceMinor*10 {
			t.Fatalf("tier %s: min_recharge_90d (%d) must be 10× max_face (%d) — the advance-to-recharge underwriting ratio is broken",
				tr.Code, tr.MinRecharge90dMinor, tr.MaxFaceMinor)
		}
		if tr.MaxFaceMinor <= prev {
			t.Fatalf("tier %s max_face not ascending", tr.Code)
		}
		prev = tr.MaxFaceMinor
	}
}

func TestProductBundle_DenomFloorAndScale(t *testing.T) {
	db := testutil.MustSetup(t, "scale_denom")
	var pc struct {
		Denominations []int64 `json:"denominations_minor"`
		FeeBps        int     `json:"fee_bps"`
	}
	if err := json.Unmarshal(activeContent(t, db, "product.airtime", "programme:prg_sim_airtime01"), &pc); err != nil {
		t.Fatalf("unmarshal product.airtime: %v", err)
	}
	var sp struct {
		Tiers []struct {
			MaxFaceMinor int64 `json:"max_face_minor"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(activeContent(t, db, "scoring.policy", "programme:prg_sim_airtime01"), &sp); err != nil {
		t.Fatalf("unmarshal scoring.policy: %v", err)
	}

	// Corrected ₦-scale denomination ladder ₦100..₦10,000.
	want := []int64{10000, 50000, 100000, 200000, 500000, 1000000}
	if len(pc.Denominations) != len(want) {
		t.Fatalf("denominations = %v, want %v", pc.Denominations, want)
	}
	for i := range want {
		if pc.Denominations[i] != want[i] {
			t.Fatalf("denom[%d] = %d, want %d", i, pc.Denominations[i], want[i])
		}
	}
	if pc.FeeBps != 1000 {
		t.Fatalf("fee rate must stay 10%% (1000 bps), got %d", pc.FeeBps)
	}

	// Empty-offer-ladder guard: the smallest denomination must be <= the LOWEST tier
	// limit, or buildLadder filters everything out and a starter subscriber gets no offers.
	minDenom := pc.Denominations[0]
	for _, d := range pc.Denominations {
		if d < minDenom {
			minDenom = d
		}
	}
	lowestTier := sp.Tiers[0].MaxFaceMinor
	if minDenom > lowestTier {
		t.Fatalf("smallest denomination (%d) exceeds the lowest tier limit (%d) — a starter subscriber would get an EMPTY offer ladder", minDenom, lowestTier)
	}
}

func TestTreasuryBundle_DailyCapRaised(t *testing.T) {
	db := testutil.MustSetup(t, "scale_treasury")
	var g struct {
		MaxDailyDisbursedMinor int64 `json:"max_daily_disbursed_minor"`
		MaxOpenExposureBps     int   `json:"max_open_exposure_bps_of_committed"`
	}
	if err := json.Unmarshal(activeContent(t, db, "treasury.guardrails", "programme:prg_sim_airtime01"), &g); err != nil {
		t.Fatalf("unmarshal treasury.guardrails: %v", err)
	}
	if g.MaxDailyDisbursedMinor != 500000000 {
		t.Fatalf("DAILY_DISBURSED cap must be the raised ₦5M pilot default (500000000), got %d", g.MaxDailyDisbursedMinor)
	}
	// Exposure stays a bps ratio (self-scales off committed capital) — invariant.
	if g.MaxOpenExposureBps != 8000 {
		t.Fatalf("open-exposure cap must stay 80%% (8000 bps), got %d", g.MaxOpenExposureBps)
	}
}
