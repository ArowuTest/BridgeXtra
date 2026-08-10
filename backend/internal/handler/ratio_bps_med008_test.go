package handler

// BX-MED-008 (reviewer follow-up): ratioBps must be EXACT across the whole int64 domain.
//
// The first implementation guarded overflow by clamping on the numerator alone:
//
//	if part > (1<<62)/bpsScale { return bpsScale }
//
// which reported 100% for ANY numerator above ~4.6e14 regardless of the denominator. The reviewer's
// case: part = 2^53+1, whole = 2*part must be 5000 bps and returned 10000. 2^53 is precisely the
// boundary this finding exists to protect, so the guard failed exactly where it mattered.
//
// These tests use math/big as an INDEPENDENT ORACLE: the implementation derives the answer with
// 128-bit math/bits arithmetic, the expectation derives it with arbitrary-precision integers. Two
// separate derivations agreeing is the proof; a shared helper would only prove self-consistency.

import (
	"math"
	"math/big"
	"testing"
)

// oracleBps computes floor(part*10000/whole) in arbitrary precision — no float, no int64 limits.
func oracleBps(part, whole int64) int64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	p := new(big.Int).Mul(big.NewInt(part), big.NewInt(bpsScale))
	q := new(big.Int).Quo(p, big.NewInt(whole)) // Quo truncates toward zero, like the impl
	if !q.IsInt64() {
		return math.MaxInt64
	}
	return q.Int64()
}

func TestBXMED008_RatioBps_ExactAcrossTheInt64Domain(t *testing.T) {
	const twoPow53Plus1 = int64(9_007_199_254_740_993) // 2^53 + 1

	for _, tc := range []struct {
		name        string
		part, whole int64
		want        int64
	}{
		// Ordinary cases.
		{"half", 5_000, 10_000, 5_000},
		{"exactly 100%", 10_000, 10_000, 10_000},
		{"over 100% is reported honestly", 15_000, 10_000, 15_000},
		{"small fraction truncates, never rounds up", 1, 3, 3_333},
		{"truncates rather than overstating", 1_000, 6_000, 1_666},

		// THE REVIEWER'S CASE: the exact boundary MED-008 exists to protect.
		{"2^53+1 over twice itself is exactly 50%", twoPow53Plus1, 2 * twoPow53Plus1, 5_000},
		{"2^53+1 over itself is exactly 100%", twoPow53Plus1, twoPow53Plus1, 10_000},
		{"2^53+1 over four times itself is 25%", twoPow53Plus1, 4 * twoPow53Plus1, 2_500},

		// Near MaxInt64 — the product part*10000 is far beyond int64 and MUST NOT wrap.
		{"MaxInt64 over itself is 100%", math.MaxInt64, math.MaxInt64, 10_000},
		{"MaxInt64 over half of itself is 200%", math.MaxInt64, math.MaxInt64 / 2, 20_000},
		{"MaxInt64 over a tenth of itself is 1000%", math.MaxInt64, math.MaxInt64 / 10, 100_000},
		{"large numerator, larger denominator", math.MaxInt64 / 3, math.MaxInt64, 3_333},

		// Degenerate denominators / numerators.
		{"zero denominator", 1_000, 0, 0},
		{"negative denominator", 1_000, -5, 0},
		{"zero numerator", 0, 1_000, 0},
		{"negative numerator", -1_000, 5_000, 0},
		{"both zero", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ratioBps(tc.part, tc.whole)
			if got != tc.want {
				t.Errorf("ratioBps(%d, %d) = %d, want %d", tc.part, tc.whole, got, tc.want)
			}
			// Cross-check the hand-written expectation against the arbitrary-precision oracle, so a
			// wrong expectation cannot make a wrong implementation look right.
			if o := oracleBps(tc.part, tc.whole); o != tc.want {
				t.Errorf("the stated expectation disagrees with the big.Int oracle: want %d, oracle %d", tc.want, o)
			}
		})
	}
}

// Property sweep: across a spread of magnitudes spanning the 2^53 boundary and up to MaxInt64, the
// 128-bit implementation must agree with arbitrary precision on every pair. This is what makes the
// claim "exact across the int64 domain" a tested property rather than a comment.
func TestBXMED008_RatioBps_AgreesWithArbitraryPrecision(t *testing.T) {
	values := []int64{
		1, 2, 3, 7, 99, 10_000, 999_983,
		1 << 20, 1 << 40,
		(1 << 53) - 1, 1 << 53, (1 << 53) + 1,
		1 << 60, 1 << 62,
		math.MaxInt64 / 3, math.MaxInt64 / 2, math.MaxInt64 - 1, math.MaxInt64,
	}
	checked := 0
	for _, part := range values {
		for _, whole := range values {
			got, want := ratioBps(part, whole), oracleBps(part, whole)
			if got != want {
				t.Fatalf("ratioBps(%d, %d) = %d, arbitrary-precision oracle = %d", part, whole, got, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("swept no pairs — the property proves nothing")
	}
	t.Logf("BX-MED-008: %d (part, whole) pairs agree with arbitrary precision", checked)
}

// The float-free invariant, stated where it is actually observable: at 2^53+1 a float64 CANNOT
// represent the numerator, and a float-derived bps disagrees with the exact one. (Deliberately a
// case where the error does NOT cancel — for whole = 2*part it happens to round back to the same
// 5000, which would make a weaker assertion look like proof that float is fine.)
func TestBXMED008_RatioBps_FloatWouldDisagree(t *testing.T) {
	// Vars, not consts: as constants Go would fold the float expression at compile time and reject
	// the overflow, hiding the very runtime behaviour under test.
	p := int64(9_007_199_254_740_993) // 2^53 + 1 — not representable in float64
	whole := int64(10)                // chosen so the EXACT result still fits in int64
	if int64(float64(p)) == p {
		t.Skip("float64 represents 2^53+1 exactly here; the premise does not hold")
	}
	exact := ratioBps(p, whole)
	if o := oracleBps(p, whole); exact != o {
		t.Fatalf("exact path disagrees with arbitrary precision: %d vs %d", exact, o)
	}
	viaFloat := int64(float64(p) * float64(bpsScale) / float64(whole))
	if viaFloat == exact {
		t.Errorf("expected the float path to LOSE precision here (that is the whole point); float=%d exact=%d",
			viaFloat, exact)
	} else {
		t.Logf("float path yields %d, exact integer path yields %d — off by %d bps", viaFloat, exact, exact-viaFloat)
	}
}
