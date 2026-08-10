package handler

// Governed money formatting. toMoneyView renders the DISPLAY string from a currency's
// governed scale (decimals + symbol), loaded once at boot from the `currencies` table —
// so "5000 minor NGN" shows as "₦50.00", and the kobo->naira divisor is a governed value
// keyed by ISO code, never a hardcoded /100. An unknown currency (no governed format)
// falls back to the code + the raw grouped amount rather than guessing a divisor and
// misstating the value.

import (
	"math"
	"math/bits"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// currencyFormats holds the governed display formats, set once at boot. atomic.Pointer
// gives lock-free reads on the hot toMoneyView path with no data race against the
// single boot-time (or per-test-fixture) Store.
var currencyFormats atomic.Pointer[map[string]entity.CurrencyFormat]

// SetCurrencyFormats installs the governed currency display formats (loaded from the
// `currencies` table). Call once at boot before serving; the map is copied defensively.
func SetCurrencyFormats(m map[string]entity.CurrencyFormat) {
	cp := make(map[string]entity.CurrencyFormat, len(m))
	for k, v := range m {
		cp[k] = v
	}
	currencyFormats.Store(&cp)
}

func currencyFormatFor(code string) (entity.CurrencyFormat, bool) {
	p := currencyFormats.Load()
	if p == nil {
		return entity.CurrencyFormat{}, false
	}
	f, ok := (*p)[code]
	return f, ok
}

// formatMoney renders a minor-unit amount as a display string using the governed scale:
// major units with grouped thousands + the currency symbol (e.g. ₦1,234.56, -₦50.00,
// ₦0.00). No governed format => code + raw grouped minor amount (never a guessed divisor).
func formatMoney(minor int64, code string) string {
	f, ok := currencyFormatFor(code)
	if !ok {
		return code + " " + groupMinor(minor)
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	div := pow10(f.Decimals)
	major := minor / div
	frac := minor % div

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(f.Symbol)
	b.WriteString(groupMinor(major))
	if f.Decimals > 0 {
		b.WriteByte('.')
		fs := strconv.FormatInt(frac, 10)
		for i := len(fs); i < f.Decimals; i++ { // zero-pad the fraction to width Decimals
			b.WriteByte('0')
		}
		b.WriteString(fs)
	}
	return b.String()
}

// bpsScale is the basis-point denominator: 10_000 bps == 100%.
const bpsScale = 10_000

// ratioBps renders part/whole as EXACT integer BASIS POINTS (BX-MED-008), so no ratio the portal
// displays is ever computed from money in JavaScript, and no float ever touches a money value.
//
// The arithmetic is exact across the WHOLE int64 domain. An earlier version guarded overflow by
// clamping on the numerator alone — `if part > (1<<62)/bpsScale { return bpsScale }` — which is not
// an overflow strategy at all: it reported 100% for ANY numerator above ~4.6e14 regardless of the
// denominator. part=2^53+1 with whole=2*part returned 10000 bps instead of 5000 — and 2^53 is the
// precise boundary this finding exists to protect. Reviewer-caught; do not reintroduce a
// magnitude-based clamp.
//
// Instead: compute the full 128-bit product part*bpsScale (bits.Mul64 cannot overflow), then do a
// 128-by-64 division. For every int64 pair with whole > 0 the true quotient fits in int64 —
// part*10000/whole <= MaxInt64*10000, and the quotient only exceeds MaxInt64 when whole is small
// enough that part/whole > ~9.2e14, i.e. a ratio above ~9.2e18 bps. That is not a ratio any real
// figure produces, so it saturates at MaxInt64 rather than panicking or wrapping — an absurd-but-
// honest maximum, never a silent 100%.
//
// Truncating (not rounding) is deliberate: a utilisation or paydown figure must never round UP and
// overstate. Ratios above 100% are returned honestly (over-limit utilisation is real and must show).
func ratioBps(part, whole int64) int64 {
	if whole <= 0 || part <= 0 {
		// No denominator (or nothing to be a part of) => no ratio. Negative money in either position
		// is not a proportion this function is willing to invent an answer for.
		return 0
	}
	hi, lo := bits.Mul64(uint64(part), bpsScale) // exact 128-bit product
	if hi >= uint64(whole) {
		// The quotient would not fit in 64 bits (bits.Div64 would panic). Unreachable for any real
		// pair of money figures; saturate loudly-large rather than wrap.
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, uint64(whole))
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}
