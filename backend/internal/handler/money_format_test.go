package handler

// Governed money formatting — the load-bearing property is that the minor->major divisor
// is a GOVERNED value keyed by ISO code, not a hardcoded /100, and that rendering is
// correct across the awkward cases (sub-major amounts, grouping, negatives, zero-decimal
// currencies, and an unknown currency where guessing a divisor would misstate the value).

import (
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestFormatMoney_GovernedScale(t *testing.T) {
	SetCurrencyFormats(map[string]entity.CurrencyFormat{
		"NGN": {Decimals: 2, Symbol: "₦"},
		"JPY": {Decimals: 0, Symbol: "¥"},
	})
	cases := []struct {
		minor int64
		code  string
		want  string
	}{
		{5000, "NGN", "₦50.00"},
		{123456, "NGN", "₦1,234.56"},
		{0, "NGN", "₦0.00"},
		{5, "NGN", "₦0.05"},  // sub-major: fraction zero-padded
		{50, "NGN", "₦0.50"}, // trailing zero preserved
		{-5000, "NGN", "-₦50.00"},
		{100000000, "NGN", "₦1,000,000.00"}, // grouping on the major part
		{5000, "JPY", "¥5,000"},             // zero-decimal currency: no fractional part
		{5000, "XYZ", "XYZ 5,000"},          // unknown currency: raw grouped, no guessed divisor
	}
	for _, c := range cases {
		if got := formatMoney(c.minor, c.code); got != c.want {
			t.Fatalf("formatMoney(%d,%q) = %q, want %q", c.minor, c.code, got, c.want)
		}
	}
}

// The divisor is GOVERNED: change NGN's decimals and the render changes — proving there
// is no hardcoded /100 anywhere in the path.
func TestFormatMoney_DivisorIsGoverned(t *testing.T) {
	SetCurrencyFormats(map[string]entity.CurrencyFormat{"NGN": {Decimals: 3, Symbol: "₦"}})
	if got := formatMoney(5000, "NGN"); got != "₦5.000" {
		t.Fatalf("with governed decimals=3, 5000 minor must render ₦5.000, got %q — a hardcoded /100 would still show ₦50.00", got)
	}
}
