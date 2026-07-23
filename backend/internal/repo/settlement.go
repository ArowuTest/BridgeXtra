package repo

// M3e settlement repositories: period aggregates come FROM THE LEDGER (one
// set-based query over journal entries), statements persist with append-only
// lines and a FINAL content hash.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

type SettlementQueries struct{}

type SettlementStatement struct {
	StatementID    string
	TelcoID        string
	ProgrammeID    string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	State          string
	Currency       string
	ContentHash    string
	TermsVersionID string
	Lines          []SettlementLine
}

type SettlementLine struct {
	Code   string
	Amount entity.Money
}

// PeriodAggregates sums the programme's ledger activity for the period in
// ONE query, keyed by settlement concept. Sources are (event_type, account,
// side) triples — the same governed vocabulary the posting templates use.
func (SettlementQueries) PeriodAggregates(ctx context.Context, tx pgx.Tx, programmeID string, from, to time.Time) (map[string]entity.Money, entity.Currency, error) {
	rows, err := tx.Query(ctx, `
		SELECT concept, SUM(amt), MIN(currency) FROM (
			SELECT CASE
				WHEN j.event_type = 'ADVANCE_ISSUED' AND e.account_code = 'AIRTIME_FUNDING_CLEARING' AND e.credit_minor > 0 THEN 'DISBURSED'
				-- Fee income follows RECOGNITION, not issuance (deferred fee): the
				-- NET movement of FEE_INCOME across ALL event types in the window.
				-- FEE_INCOME is touched only by ADVANCE_ISSUED (issuance credit +
				-- deferral debit), RECOVERY_APPLIED (recognise) and RECOVERY_REVERSED
				-- (de-recognise), so signed net = recognised revenue exactly. UPFRONT:
				-- only the issuance credit exists (recovery legs zero-bound/omitted) so
				-- net == gross == fee — byte-identical to before. DEFERRED: 0 at
				-- issuance, recognised fee in the recovery period, 0 for a full default.
				WHEN e.account_code = 'FEE_INCOME' THEN 'FEE_INCOME'
				WHEN j.event_type = 'RECOVERY_APPLIED' AND e.account_code = 'TELCO_SETTLEMENT_RECEIVABLE' AND e.debit_minor > 0 THEN 'RECOVERED'
				WHEN j.event_type = 'RECOVERY_REVERSED' AND e.account_code = 'TELCO_SETTLEMENT_RECEIVABLE' AND e.credit_minor > 0 THEN 'REVERSED'
				WHEN j.event_type = 'WRITE_OFF' AND e.account_code = 'WRITE_OFF_EXPENSE' AND e.debit_minor > 0 THEN 'WRITTEN_OFF'
				WHEN j.event_type = 'WRITEOFF_RECOVERY_INC' AND e.account_code = 'WRITEOFF_RECOVERY_INCOME' AND e.credit_minor > 0 THEN 'WO_INCOME'
			END AS concept,
			-- FEE_INCOME is a signed net (credit - debit); every other concept is a
			-- single-sided movement (GREATEST picks the populated side).
			CASE WHEN e.account_code = 'FEE_INCOME'
			     THEN e.credit_minor - e.debit_minor
			     ELSE GREATEST(e.debit_minor, e.credit_minor) END AS amt,
			e.currency
			FROM journal_entries e
			JOIN journals j ON j.journal_id = e.journal_id
			WHERE j.programme_id = $1 AND j.posted_at >= $2 AND j.posted_at < $3
		) x WHERE concept IS NOT NULL
		GROUP BY concept`, programmeID, from, to)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := map[string]entity.Money{}
	cur := entity.NGN // default currency when the period is empty
	for rows.Next() {
		var concept, c string
		var minor int64
		if err := rows.Scan(&concept, &minor, &c); err != nil {
			return nil, "", err
		}
		m, err := scanMoney(minor, c)
		if err != nil {
			return nil, "", err
		}
		out[concept] = m
		cur = m.Currency()
	}
	return out, cur, rows.Err()
}

// InsertStatement persists a DRAFT statement with its lines. Duplicate
// period for the same programme is refused by the schema.
func (SettlementQueries) InsertStatement(ctx context.Context, tx pgx.Tx, st SettlementStatement) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO settlement_statements
		  (statement_id, telco_id, programme_id, period_start, period_end, currency, terms_version_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		st.StatementID, st.TelcoID, st.ProgrammeID, st.PeriodStart, st.PeriodEnd,
		st.Currency, st.TermsVersionID)
	if err != nil {
		return fmt.Errorf("insert statement: %w", err)
	}
	for _, l := range st.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO settlement_lines (line_id, statement_id, telco_id, line_code, amount_minor, currency)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			newLineID(st.StatementID, l.Code), st.StatementID, st.TelcoID,
			l.Code, l.Amount.Amount(), string(l.Amount.Currency())); err != nil {
			return fmt.Errorf("insert statement line %s: %w", l.Code, err)
		}
	}
	return nil
}

// newLineID is deterministic per (statement, code): re-inserting the same
// line collides on the PK instead of duplicating.
func newLineID(statementID, code string) string {
	return "sln_" + statementID + "_" + code
}

func (SettlementQueries) GetStatement(ctx context.Context, tx pgx.Tx, statementID string) (SettlementStatement, error) {
	var st SettlementStatement
	var hash *string
	err := tx.QueryRow(ctx, `
		SELECT statement_id, telco_id, programme_id, period_start, period_end, state,
		       currency, content_hash, terms_version_id
		FROM settlement_statements WHERE statement_id = $1`, statementID).
		Scan(&st.StatementID, &st.TelcoID, &st.ProgrammeID, &st.PeriodStart, &st.PeriodEnd,
			&st.State, &st.Currency, &hash, &st.TermsVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("statement %q: %w", statementID, ErrNotFound)
	}
	if err != nil {
		return st, err
	}
	if hash != nil {
		st.ContentHash = *hash
	}
	rows, err := tx.Query(ctx, `
		SELECT line_code, amount_minor, currency FROM settlement_lines
		WHERE statement_id = $1 ORDER BY line_id`, statementID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, cur string
		var minor int64
		if err := rows.Scan(&code, &minor, &cur); err != nil {
			return st, err
		}
		m, err := scanMoney(minor, cur)
		if err != nil {
			return st, err
		}
		st.Lines = append(st.Lines, SettlementLine{Code: code, Amount: m})
	}
	return st, rows.Err()
}

// FinaliseStatement stamps DRAFT -> FINAL with the content hash.
func (SettlementQueries) FinaliseStatement(ctx context.Context, tx pgx.Tx, statementID, contentHash string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE settlement_statements
		SET state = 'FINAL', content_hash = $2, finalised_at = now()
		WHERE statement_id = $1 AND state = 'DRAFT'`, statementID, contentHash)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("statement %q not DRAFT: %w", statementID, ErrNotFound)
	}
	return nil
}
