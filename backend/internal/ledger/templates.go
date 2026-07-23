package ledger

// CFG-012 posting templates (M3e, carried from the M1 deferred register):
// every journal renders from the governed ledger.templates config. Balance
// is proven SYMBOLICALLY at config approval (validators_m3e.go); at posting
// time the rendered lines still pass the numeric per-currency check in Post
// — belt and braces, the same defense-in-depth stance as everywhere else.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// Symbol names an amount source a template line draws from.
type Symbol string

const (
	SymAmount      Symbol = "AMOUNT"      // the event's own amount
	SymDisbursed   Symbol = "DISBURSED"   // advance disbursed value
	SymFee         Symbol = "FEE"         // advance fee
	SymOutstanding Symbol = "OUTSTANDING" // repayment obligation (= DISBURSED + FEE)

	// Deferred fee recognition (bound to a real amount under fee_recognition=
	// DEFERRED, else to ZeroMoney so the paired legs omit — the journal stays
	// byte-identical under UPFRONT). Each is one DEBIT + one CREDIT leg in its
	// template, so it cancels on its own validator basis axis.
	SymFeeDeferAdj         Symbol = "FEE_DEFER_ADJ"         // origination: move fee income -> unearned liability
	SymFeeRecognized       Symbol = "FEE_RECOGNIZED"        // recovery: recognise (apply) / de-recognise (reverse) the allocated fee-portion
	SymFeeUnearnedReversed Symbol = "FEE_UNEARNED_REVERSED" // write-off: reverse remaining unearned (never income)
)

// TemplateLine is one governed posting line.
type TemplateLine struct {
	Account      string `json:"account"`
	Side         string `json:"side"` // DEBIT | CREDIT
	Amount       Symbol `json:"amount"`
	OmitWhenZero bool   `json:"omit_when_zero,omitempty"`
}

type templateSet struct {
	Templates map[string]struct {
		Lines []TemplateLine `json:"lines"`
	} `json:"templates"`
}

// Bindings supplies concrete Money per symbol for one posting.
type Bindings map[Symbol]entity.Money

// PostEvent renders the ACTIVE template for the event type and posts the
// journal. A missing template or unbound symbol REFUSES the posting — money
// movement without a governed template is not a fallback, it is a bug.
func (s *Service) PostEvent(ctx context.Context, tx pgx.Tx, j Journal, b Bindings) (posted bool, journalID string, err error) {
	cv, err := s.Config.ActiveAt(ctx, "ledger.templates", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return false, "", fmt.Errorf("ledger.templates config: %w", err)
	}
	var ts templateSet
	if err := json.Unmarshal(cv.Content, &ts); err != nil {
		return false, "", fmt.Errorf("ledger.templates parse: %w", err)
	}
	tpl, ok := ts.Templates[j.EventType]
	if !ok {
		return false, "", fmt.Errorf("no governed posting template for event type %q — refusing to post (CFG-012)", j.EventType)
	}

	lines := make([]Line, 0, len(tpl.Lines))
	for _, tl := range tpl.Lines {
		m, ok := b[tl.Amount]
		if !ok {
			return false, "", fmt.Errorf("template %s: symbol %s unbound — refusing to post", j.EventType, tl.Amount)
		}
		if tl.OmitWhenZero && m.IsZero() {
			continue
		}
		side := Debit
		if tl.Side == "CREDIT" {
			side = Credit
		}
		lines = append(lines, Line{Account: tl.Account, Side: side, Amount: m})
	}
	j.Lines = lines
	j.TemplateVersion = cv.ConfigVersionID
	return s.Post(ctx, tx, j)
}
