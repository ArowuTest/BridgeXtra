package handler

// BX-MED-008: the portal's amount_minor is an int64 (kobo). A portfolio-level aggregate can exceed
// JavaScript's 2^53 safe-integer limit, where a JSON number silently loses precision in the
// browser. moneyView serialises amount_minor as a STRING so the exact integer reaches the client.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestBXMED008_MoneyView_AmountMinorSerialisedAsExactString(t *testing.T) {
	// 2^53 + 1 — the smallest integer a float64 (a JS number) cannot represent exactly.
	const big = int64(9_007_199_254_740_993)
	b, err := json.Marshal(toMoneyView(entity.MustMoney(big, entity.NGN)))
	if err != nil {
		t.Fatal(err)
	}
	// amount_minor must be a STRING carrying the exact integer. Mutation proof: drop `,string` on
	// moneyView.AmountMinor and this is emitted as the bare number 9007199254740993 — RED here.
	if !strings.Contains(string(b), `"amount_minor":"9007199254740993"`) {
		t.Fatalf("amount_minor must serialise as an exact string, got %s", b)
	}
	// And it decodes back to the exact int64 — no float64 rounding anywhere in the round-trip.
	var back struct {
		AmountMinor int64 `json:"amount_minor,string"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.AmountMinor != big {
		t.Fatalf("amount_minor round-trip lost precision: got %d want %d", back.AmountMinor, big)
	}

	// Negative movements serialise the same way (string), and decode exactly.
	nb, err := json.Marshal(toMoneyView(entity.MustMoney(-big, entity.NGN)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nb), `"amount_minor":"-9007199254740993"`) {
		t.Fatalf("a negative amount_minor must serialise as an exact string, got %s", nb)
	}
}
