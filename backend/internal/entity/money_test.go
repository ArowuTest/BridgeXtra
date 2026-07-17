package entity

// BC-1/BC-8 test pack (ADR-0002): exhaustive tables for mismatch/overflow/
// unset misuse, half-up boundary cases including negatives, and a 10k-case
// randomized property test for allocation (deterministic seed — reproducible).

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"testing"
)

func TestMoney_ZeroValueIsUnusable(t *testing.T) {
	var unset Money
	valid := MustMoney(100, NGN)

	if unset.IsSet() || unset.IsZero() || unset.IsPositive() || unset.IsNegative() {
		t.Fatal("zero-value Money must not report any valid state")
	}
	if _, err := unset.Add(valid); !errors.Is(err, ErrMoneyUnset) {
		t.Fatalf("Add on unset: want ErrMoneyUnset, got %v", err)
	}
	if _, err := valid.Add(unset); !errors.Is(err, ErrMoneyUnset) {
		t.Fatalf("Add with unset arg: want ErrMoneyUnset, got %v", err)
	}
	if _, err := unset.Neg(); !errors.Is(err, ErrMoneyUnset) {
		t.Fatal("Neg on unset must error")
	}
	if _, err := unset.PercentBps(1000); !errors.Is(err, ErrMoneyUnset) {
		t.Fatal("PercentBps on unset must error")
	}
	if _, err := unset.AllocateByRatio(1, 1); !errors.Is(err, ErrMoneyUnset) {
		t.Fatal("AllocateByRatio on unset must error")
	}
	if _, err := unset.MarshalJSON(); !errors.Is(err, ErrMoneyUnset) {
		t.Fatal("marshalling unset Money must error")
	}
}

func TestMoney_CurrencyValidationAndMismatch(t *testing.T) {
	for _, bad := range []Currency{"", "NG", "NGNX", "ngn", "N1N"} {
		if _, err := NewMoney(1, bad); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("currency %q must be rejected", bad)
		}
	}
	ngn := MustMoney(100, NGN)
	usd := MustMoney(100, "USD")
	if _, err := ngn.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("Add across currencies must be ErrCurrencyMismatch")
	}
	if _, err := ngn.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("Sub across currencies must be ErrCurrencyMismatch")
	}
	if _, err := ngn.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal("Cmp across currencies must be ErrCurrencyMismatch")
	}
}

func TestMoney_ArithmeticAndOverflow(t *testing.T) {
	a := MustMoney(70, NGN)
	b := MustMoney(30, NGN)
	if sum, _ := a.Add(b); sum.Amount() != 100 {
		t.Fatalf("70+30=%d", sum.Amount())
	}
	if d, _ := a.Sub(b); d.Amount() != 40 {
		t.Fatalf("70-30=%d", d.Amount())
	}
	if n, _ := b.Sub(a); n.Amount() != -40 {
		t.Fatalf("30-70=%d", n.Amount())
	}
	if p, _ := a.MulInt(3); p.Amount() != 210 {
		t.Fatalf("70*3=%d", p.Amount())
	}

	maxM := MustMoney(math.MaxInt64, NGN)
	minM := MustMoney(math.MinInt64, NGN)
	one := MustMoney(1, NGN)
	negOne := MustMoney(-1, NGN)

	if _, err := maxM.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("MaxInt64+1 must overflow")
	}
	if _, err := minM.Add(negOne); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("MinInt64-1 must overflow")
	}
	if _, err := minM.Neg(); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("negating MinInt64 must overflow")
	}
	if _, err := maxM.MulInt(2); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("MaxInt64*2 must overflow")
	}
	if _, err := minM.MulInt(-1); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("MinInt64*-1 must overflow")
	}
	// Sub that would overflow via negation path.
	if _, err := one.Sub(minM); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("1 - MinInt64 must overflow")
	}
}

func TestMoney_PercentBps_HalfUp_SingleRoundingSite(t *testing.T) {
	// ADR-0002: HALF-UP — round half away from zero. Table covers exact,
	// below-half, exactly-half, above-half, negatives, zero bps, >100%.
	cases := []struct {
		amount int64
		bps    int64
		want   int64
	}{
		{10_000, 1_500, 1_500},                 // 15.00% of 100.00 = 15.00 exact
		{10_000, 1, 1},                         // 0.01% of 100.00 = 1 exactly
		{333, 1_000, 33},                       // 33.3 → 33 (below half)
		{335, 1_000, 34},                       // 33.5 → 34 (exactly half, up)
		{337, 1_000, 34},                       // 33.7 → 34 (above half)
		{-333, 1_000, -33},                     // -33.3 → -33 (toward zero)
		{-335, 1_000, -34},                     // -33.5 → -34 (half AWAY from zero)
		{-337, 1_000, -34},                     // -33.7 → -34
		{1, 4_999, 0},                          // 0.4999 → 0
		{1, 5_000, 1},                          // 0.5 → 1
		{10_000, 0, 0},                         // 0 bps
		{10_000, 20_000, 20_000},               // 200%
		{math.MaxInt64, 10_000, math.MaxInt64}, // 100% of max: big.Int path, no overflow
		{math.MaxInt64 - 1, 10_000, math.MaxInt64 - 1},
	}
	for _, c := range cases {
		m := MustMoney(c.amount, NGN)
		got, err := m.PercentBps(c.bps)
		if err != nil {
			t.Fatalf("PercentBps(%d, %d): %v", c.amount, c.bps, err)
		}
		if got.Amount() != c.want {
			t.Errorf("PercentBps(%d, %d) = %d, want %d", c.amount, c.bps, got.Amount(), c.want)
		}
	}
	// Overflow: 200% of MaxInt64 exceeds int64 — must error, not wrap.
	if _, err := MustMoney(math.MaxInt64, NGN).PercentBps(20_000); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatal("200% of MaxInt64 must be ErrMoneyOverflow")
	}
}

func TestMoney_AllocateByRatio_ExactAndDeterministic(t *testing.T) {
	// Known case: 100 into 1:1:1 → 34,33,33 (largest remainder, lowest index ties).
	parts, err := MustMoney(100, NGN).AllocateByRatio(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Amount() != 34 || parts[1].Amount() != 33 || parts[2].Amount() != 33 {
		t.Fatalf("100/1:1:1 = %v", parts)
	}
	// Negative total mirrors exactly.
	nparts, err := MustMoney(-100, NGN).AllocateByRatio(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if nparts[0].Amount() != -34 || nparts[1].Amount() != -33 || nparts[2].Amount() != -33 {
		t.Fatalf("-100/1:1:1 = %v", nparts)
	}
	// Bad ratios rejected.
	if _, err := MustMoney(100, NGN).AllocateByRatio(); !errors.Is(err, ErrBadRatio) {
		t.Fatal("empty ratios must be rejected")
	}
	if _, err := MustMoney(100, NGN).AllocateByRatio(3, 0); !errors.Is(err, ErrBadRatio) {
		t.Fatal("zero ratio must be rejected")
	}
	if _, err := MustMoney(100, NGN).AllocateByRatio(3, -1); !errors.Is(err, ErrBadRatio) {
		t.Fatal("negative ratio must be rejected")
	}
}

func TestMoney_AllocateByRatio_Property_SumExact_DeviationBounded(t *testing.T) {
	// BC-8 property test: 10k randomized cases (fixed seed — reproducible).
	// Invariants: Σ parts == total EXACTLY (no rounding loss, V2-LED-013),
	// every part within 1 minor unit of its exact proportional share.
	rng := rand.New(rand.NewSource(20260717)) // deterministic
	for i := 0; i < 10_000; i++ {
		amount := rng.Int63n(2_000_000_000) - 1_000_000_000 // ±₦10m in kobo
		n := 2 + rng.Intn(6)
		ratios := make([]int64, n)
		var totalRatio int64
		for j := range ratios {
			ratios[j] = 1 + rng.Int63n(999)
			totalRatio += ratios[j]
		}
		m := MustMoney(amount, NGN)
		parts, err := m.AllocateByRatio(ratios...)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		var sum int64
		for j, p := range parts {
			sum += p.Amount()
			// |part*totalRatio - amount*ratio| < totalRatio  ⇔ deviation < 1 unit.
			lhs := p.Amount()*totalRatio - amount*ratios[j]
			if lhs < 0 {
				lhs = -lhs
			}
			if lhs >= totalRatio {
				t.Fatalf("case %d part %d deviates ≥1 minor unit: part=%d amount=%d ratio=%d/%d",
					i, j, p.Amount(), amount, ratios[j], totalRatio)
			}
		}
		if sum != amount {
			t.Fatalf("case %d: Σparts=%d != total=%d (money created or destroyed)", i, sum, amount)
		}
	}
}

func TestMoney_JSONRoundtripAndValidation(t *testing.T) {
	m := MustMoney(12_345, NGN)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount_minor":12345,"currency":"NGN"}` {
		t.Fatalf("wire shape: %s", b)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(m) {
		t.Fatalf("roundtrip mismatch: %v != %v", back, m)
	}
	// Invalid currency can never enter the domain via JSON.
	if err := json.Unmarshal([]byte(`{"amount_minor":1,"currency":"naira"}`), &back); err == nil {
		t.Fatal("invalid currency must fail unmarshal")
	}
}
