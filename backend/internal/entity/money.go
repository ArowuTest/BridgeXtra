package entity

// Money is the ONLY representation of money in entity/ and above (BC-1,
// ADR-0002, V2-API-005). Unexported fields: an unvalidated or currency-less
// amount cannot be constructed, and the zero value Money{} errors on every
// operation — it can never silently act as ₦0.
//
// Rounding policy (ADR-0002): HALF-UP, and ONLY inside PercentBps — the single
// rounding site in the codebase. AllocateByRatio never rounds: largest-
// remainder allocation guarantees Σ parts == total exactly.
// No floats exist in this file or anywhere in the money path (CI-enforced).

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
)

// Currency is an ISO 4217 alpha-3 code.
type Currency string

// NGN is the Release 1 operating currency (ASSUMPTIONS A-13).
const NGN Currency = "NGN"

var (
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrInvalidCurrency  = errors.New("money: invalid currency code")
	ErrMoneyOverflow    = errors.New("money: int64 overflow")
	ErrMoneyUnset       = errors.New("money: zero-value Money is unusable; construct via NewMoney")
	ErrBadRatio         = errors.New("money: allocation ratios must be positive with a positive sum")
)

// Valid reports whether c is a plausible ISO 4217 alpha-3 code.
func (c Currency) Valid() bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Money is an amount in minor units of a single currency.
type Money struct {
	amount   int64
	currency Currency
}

// NewMoney constructs a validated Money. minor may be negative (reversals,
// corrections are first-class citizens of a ledgered system).
func NewMoney(minor int64, cur Currency) (Money, error) {
	if !cur.Valid() {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, cur)
	}
	return Money{amount: minor, currency: cur}, nil
}

// MustMoney is for constants, seeds and tests only.
func MustMoney(minor int64, cur Currency) Money {
	m, err := NewMoney(minor, cur)
	if err != nil {
		panic(err)
	}
	return m
}

// ZeroMoney is an explicit, valid zero in cur — distinct from the unusable
// zero value Money{}.
func ZeroMoney(cur Currency) (Money, error) { return NewMoney(0, cur) }

// IsSet reports whether m was properly constructed.
func (m Money) IsSet() bool { return m.currency != "" }

// Amount returns the minor-unit amount. The DB/repo boundary is the only
// place this should cross into a bare integer (ADR-0002).
func (m Money) Amount() int64 { return m.amount }

func (m Money) Currency() Currency { return m.currency }

func (m Money) IsZero() bool     { return m.IsSet() && m.amount == 0 }
func (m Money) IsPositive() bool { return m.IsSet() && m.amount > 0 }
func (m Money) IsNegative() bool { return m.IsSet() && m.amount < 0 }

func (m Money) String() string {
	if !m.IsSet() {
		return "Money(unset)"
	}
	return fmt.Sprintf("%s %d(minor)", m.currency, m.amount)
}

func (m Money) sameCurrency(o Money) error {
	if !m.IsSet() || !o.IsSet() {
		return ErrMoneyUnset
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Add returns m+o, overflow-checked.
func (m Money) Add(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	sum := m.amount + o.amount
	// Overflow iff operands share a sign and the result flipped it.
	if (m.amount > 0 && o.amount > 0 && sum < 0) || (m.amount < 0 && o.amount < 0 && sum >= 0) {
		return Money{}, fmt.Errorf("%w: %d + %d", ErrMoneyOverflow, m.amount, o.amount)
	}
	return Money{amount: sum, currency: m.currency}, nil
}

// Sub returns m-o, overflow-checked.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	neg, err := o.Neg()
	if err != nil {
		return Money{}, err
	}
	return m.Add(neg)
}

// Neg returns -m (math.MinInt64 cannot be negated).
func (m Money) Neg() (Money, error) {
	if !m.IsSet() {
		return Money{}, ErrMoneyUnset
	}
	if m.amount == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: negate MinInt64", ErrMoneyOverflow)
	}
	return Money{amount: -m.amount, currency: m.currency}, nil
}

// MulInt returns m*n, overflow-checked. No rounding occurs (integer factor).
func (m Money) MulInt(n int64) (Money, error) {
	if !m.IsSet() {
		return Money{}, ErrMoneyUnset
	}
	if m.amount == 0 || n == 0 {
		return Money{amount: 0, currency: m.currency}, nil
	}
	// Checked BEFORE the p/n verification: MinInt64 / -1 panics in Go.
	if m.amount == math.MinInt64 && n == -1 {
		return Money{}, fmt.Errorf("%w: %d * %d", ErrMoneyOverflow, m.amount, n)
	}
	p := m.amount * n
	if p/n != m.amount {
		return Money{}, fmt.Errorf("%w: %d * %d", ErrMoneyOverflow, m.amount, n)
	}
	return Money{amount: p, currency: m.currency}, nil
}

// Cmp returns -1/0/+1 comparing m to o.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.sameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.amount < o.amount:
		return -1, nil
	case m.amount > o.amount:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports value equality (same currency AND amount). Unset == unset.
func (m Money) Equal(o Money) bool { return m == o }

// PercentBps computes (m * bps / 10_000) — fee and revenue-share math in
// basis points. THE single rounding site in the codebase (ADR-0002):
// HALF-UP — round half away from zero. Deterministic; big.Int intermediate,
// so no overflow before the final range check.
func (m Money) PercentBps(bps int64) (Money, error) {
	if !m.IsSet() {
		return Money{}, ErrMoneyUnset
	}
	num := new(big.Int).Mul(big.NewInt(m.amount), big.NewInt(bps))
	den := big.NewInt(10_000)
	q, r := new(big.Int).QuoRem(num, den, new(big.Int)) // truncated toward zero
	// half-up: |r|*2 >= den → move away from zero.
	r.Abs(r)
	if r.Mul(r, big.NewInt(2)).Cmp(den) >= 0 {
		if num.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return Money{}, fmt.Errorf("%w: %d bps of %s", ErrMoneyOverflow, bps, m)
	}
	return Money{amount: q.Int64(), currency: m.currency}, nil
}

// AllocateByRatio splits m into len(ratios) parts proportional to ratios,
// with NO rounding loss: largest-remainder method guarantees Σ parts == m
// exactly; every part differs from its exact proportional share by < 1 minor
// unit. Ratios must be positive. Ties break toward the lowest index
// (deterministic). Used for settlement splits and recovery waterfalls.
func (m Money) AllocateByRatio(ratios ...int64) ([]Money, error) {
	if !m.IsSet() {
		return nil, ErrMoneyUnset
	}
	if len(ratios) == 0 {
		return nil, ErrBadRatio
	}
	total := new(big.Int)
	for _, r := range ratios {
		if r <= 0 {
			return nil, fmt.Errorf("%w: ratio %d", ErrBadRatio, r)
		}
		total.Add(total, big.NewInt(r))
	}

	// Work on |amount|; re-sign at the end so remainder logic is uniform.
	negative := m.amount < 0
	absAmt := new(big.Int).Abs(big.NewInt(m.amount))

	parts := make([]Money, len(ratios))
	remainders := make([]*big.Int, len(ratios))
	floorSum := new(big.Int)
	for i, r := range ratios {
		num := new(big.Int).Mul(absAmt, big.NewInt(r))
		q, rem := new(big.Int).QuoRem(num, total, new(big.Int))
		remainders[i] = rem
		floorSum.Add(floorSum, q)
		parts[i] = Money{amount: q.Int64(), currency: m.currency} // q <= |amount| fits int64
	}
	// Distribute the leftover minor units to the largest remainders, lowest
	// index first on ties. Since Σratios == total, Σ(|m|·rᵢ/total) == |m|
	// exactly, and each floor drops a fractional part < 1 minor unit, so
	// 0 <= leftover < len(ratios) — the loop is tightly bounded.
	leftover := new(big.Int).Sub(absAmt, floorSum).Int64()
	for leftover > 0 {
		best := -1
		for i := range remainders {
			if best == -1 || remainders[i].Cmp(remainders[best]) > 0 {
				best = i
			}
		}
		parts[best].amount++
		remainders[best].SetInt64(-1) // consumed
		leftover--
	}
	if negative {
		for i := range parts {
			parts[i].amount = -parts[i].amount
		}
	}
	return parts, nil
}

// moneyJSON is the wire shape (matches the OpenAPI money convention).
type moneyJSON struct {
	AmountMinor int64    `json:"amount_minor"`
	Currency    Currency `json:"currency"`
}

// MarshalJSON emits {"amount_minor":..., "currency":"NGN"}. Marshalling an
// unset Money is a programming error surfaced as one.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsSet() {
		return nil, ErrMoneyUnset
	}
	return json.Marshal(moneyJSON{AmountMinor: m.amount, Currency: m.currency})
}

// UnmarshalJSON validates through the constructor — invalid JSON money can
// never enter the domain.
func (m *Money) UnmarshalJSON(b []byte) error {
	var w moneyJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	mm, err := NewMoney(w.AmountMinor, w.Currency)
	if err != nil {
		return err
	}
	*m = mm
	return nil
}
