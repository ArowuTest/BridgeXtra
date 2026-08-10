package handler

// Governed money formatting. toMoneyView renders the DISPLAY string from a currency's
// governed scale (decimals + symbol), loaded once at boot from the `currencies` table —
// so "5000 minor NGN" shows as "₦50.00", and the kobo->naira divisor is a governed value
// keyed by ISO code, never a hardcoded /100. An unknown currency (no governed format)
// falls back to the code + the raw grouped amount rather than guessing a divisor and
// misstating the value.

import (
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

// ratioBps renders part/whole as exact integer BASIS POINTS (BX-MED-008), so no ratio the portal
// displays is ever computed from money in JavaScript. Integer arithmetic throughout: no float ever
// touches a money value. Saturates at 0 for a non-positive denominator (nothing to be a part of),
// and the multiplication is done on int64 with an overflow guard — 10_000 * part can only overflow
// for values far beyond any real portfolio, and silently wrapping a money ratio is not acceptable.
func ratioBps(part, whole int64) int64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	const bpsScale = 10_000
	if part > (1<<62)/bpsScale {
		// Beyond plausible money; clamp rather than wrap. Reaching this means a bug upstream.
		return bpsScale
	}
	return (part * bpsScale) / whole
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}
