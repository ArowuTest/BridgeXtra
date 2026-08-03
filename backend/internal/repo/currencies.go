package repo

// Money-format governance (currencies reference table). LoadCurrencyFormats reads the
// governed display scale (decimals + symbol) once — the caller loads it at boot and
// hands it to the handler layer, so toMoneyView renders major units from a governed
// source keyed by ISO code rather than a hardcoded /100.

import (
	"context"
	"fmt"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// LoadCurrencyFormats returns the display format for every currency in the reference
// table, keyed by ISO code. It is global reference data (no tenant scope).
func LoadCurrencyFormats(ctx context.Context, q Querier) (map[string]entity.CurrencyFormat, error) {
	rows, err := q.Query(ctx, `SELECT code, decimals, symbol FROM currencies`)
	if err != nil {
		return nil, fmt.Errorf("load currency formats: %w", err)
	}
	defer rows.Close()
	out := make(map[string]entity.CurrencyFormat)
	for rows.Next() {
		var code, symbol string
		var decimals int
		if err := rows.Scan(&code, &decimals, &symbol); err != nil {
			return nil, fmt.Errorf("scan currency format: %w", err)
		}
		out[code] = entity.CurrencyFormat{Decimals: decimals, Symbol: symbol}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
