package configsvc

// CFG-012 template validator (M3e): a posting-template version CANNOT
// activate unless every template provably balances under ALL permitted
// branches. The proof is symbolic, at approval time:
//
//   - each amount symbol is a vector in the basis (DISBURSED, FEE, AMOUNT),
//     with the money identity OUTSTANDING = DISBURSED + FEE expanded;
//   - a template balances iff the summed debit vector equals the summed
//     credit vector PER SYMBOL COMPONENT;
//   - omit_when_zero branches need no extra proof: a line omitted only when
//     its symbol is zero removes a numeric zero, so component equality of
//     the full line set covers every branch.
//
// Accounts are cross-checked against the ACTIVE chart (a template naming an
// account the chart lacks would fail at posting — armed-but-dead).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func init() {
	validators["ledger.templates"] = validateLedgerTemplates
}

// symbol basis vectors: (disbursed, fee, amount)
var symbolBasis = map[string][3]int{
	"DISBURSED":   {1, 0, 0},
	"FEE":         {0, 1, 0},
	"OUTSTANDING": {1, 1, 0}, // identity: outstanding = disbursed + fee (both fee models)
	"AMOUNT":      {0, 0, 1},
}

func validateLedgerTemplates(ctx context.Context, tx pgx.Tx, content json.RawMessage) error {
	var v struct {
		Templates map[string]struct {
			Lines []struct {
				Account      *string `json:"account"`
				Side         *string `json:"side"`
				Amount       *string `json:"amount"`
				OmitWhenZero bool    `json:"omit_when_zero"`
			} `json:"lines"`
		} `json:"templates"`
	}
	if err := strictUnmarshal(content, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if len(v.Templates) == 0 {
		return fmt.Errorf("templates must be non-empty — an empty template set halts all posting")
	}

	// Cross-domain: every referenced account must exist in the ACTIVE chart.
	chartAccounts, err := activeChartAccounts(ctx, tx)
	if err != nil {
		return err
	}

	for event, tpl := range v.Templates {
		if event == "" {
			return fmt.Errorf("template with empty event type")
		}
		if len(tpl.Lines) < 2 {
			return fmt.Errorf("template %s: at least two lines required (double entry)", event)
		}
		var debit, credit [3]int
		for i, l := range tpl.Lines {
			if l.Account == nil || !chartAccounts[*l.Account] {
				return fmt.Errorf("template %s line %d: account not on the ACTIVE chart (armed-but-dead)", event, i)
			}
			if l.Side == nil || (*l.Side != "DEBIT" && *l.Side != "CREDIT") {
				return fmt.Errorf("template %s line %d: side must be DEBIT or CREDIT", event, i)
			}
			if l.Amount == nil {
				return fmt.Errorf("template %s line %d: amount symbol required", event, i)
			}
			vec, ok := symbolBasis[*l.Amount]
			if !ok {
				return fmt.Errorf("template %s line %d: unknown amount symbol %q (known: DISBURSED, FEE, OUTSTANDING, AMOUNT)", event, i, *l.Amount)
			}
			for k := 0; k < 3; k++ {
				if *l.Side == "DEBIT" {
					debit[k] += vec[k]
				} else {
					credit[k] += vec[k]
				}
			}
		}
		// The symbolic balance proof (CFG-012): per-component equality means
		// debits equal credits for EVERY possible binding of the symbols —
		// including every omit_when_zero branch, which only ever removes
		// numeric zeros.
		if debit != credit {
			return fmt.Errorf("template %s could post unbalanced: debit basis %v != credit basis %v (CFG-012 refuses activation)", event, debit, credit)
		}
	}
	return nil
}

// activeChartAccounts reads the ACTIVE ledger.accounts chart inside the
// approval transaction.
func activeChartAccounts(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	var content []byte
	err := tx.QueryRow(ctx, `
		SELECT content FROM config_versions
		WHERE domain = 'ledger.accounts' AND scope = $1 AND state = 'ACTIVE'
		ORDER BY version_no DESC LIMIT 1`, entity.ScopeGlobal).Scan(&content)
	if err != nil {
		return nil, fmt.Errorf("no ACTIVE chart of accounts to validate templates against: %w", err)
	}
	var c struct {
		Accounts []struct {
			Code string `json:"code"`
		} `json:"accounts"`
	}
	// Lenient decode by design: this is a PROJECTION read of an already-active,
	// already-strict-validated chart (we only need the codes to cross-check).
	// strictUnmarshal (EXT-4) guards VALIDATION of new drafts — the chart's
	// other fields (kind, …) were fully modelled and checked at its own
	// approval by validateLedgerAccounts.
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(c.Accounts))
	for _, a := range c.Accounts {
		out[a.Code] = true
	}
	return out, nil
}
