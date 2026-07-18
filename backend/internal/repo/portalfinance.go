package repo

// M4d finance operator reads: the ledger browser. Journals carry telco_id AND
// programme_id, so every read is bounded by the operator's OperatorScope in
// SQL — the same non-bypassable pattern as the risk trips (M4C-F1). These run
// on the worker (BYPASSRLS) operator-read pool; a scoped operator sees only
// their tenant's journals, a '*' admin sees all, a no-authority operator sees
// none. No money arithmetic here — amounts are carried as entity.Money and
// rendered server-side.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

type JournalHeader struct {
	JournalID      string
	EventType      string
	TelcoID        string
	ProgrammeID    string
	AdvanceID      string // '' when the journal is not advance-scoped (e.g. telco-level)
	CorrelationID  string
	AccountingDate string // DATE, rendered YYYY-MM-DD
	PostedAt       string // RFC3339
}

type JournalEntryRow struct {
	EntryID     string
	AccountCode string
	Debit       entity.Money
	Credit      entity.Money
}

type JournalDetail struct {
	JournalHeader
	Entries []JournalEntryRow
}

const journalCols = `journal_id, event_type, telco_id, programme_id, COALESCE(advance_id,''),
	correlation_id, to_char(accounting_date,'YYYY-MM-DD'), to_char(posted_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')`

func scanJournalHeader(row pgx.Row) (JournalHeader, error) {
	var h JournalHeader
	err := row.Scan(&h.JournalID, &h.EventType, &h.TelcoID, &h.ProgrammeID, &h.AdvanceID,
		&h.CorrelationID, &h.AccountingDate, &h.PostedAt)
	return h, err
}

// ListJournals returns journals newest-first within the operator's scope,
// optionally filtered by advance or correlation id. A no-authority operator
// gets an empty set without a query (structural, M4C-F1).
func ListJournals(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, advanceID, correlationID string, limit int) ([]JournalHeader, error) {
	if !scope.authority {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT `+journalCols+`
		FROM journals
		WHERE ($1 = '' OR telco_id = $1)
		  AND ($2 = '' OR programme_id = $2)
		  AND ($3 = '' OR advance_id = $3)
		  AND ($4 = '' OR correlation_id = $4)
		ORDER BY posted_at DESC, journal_id
		LIMIT $5`, scope.telco, scope.programme, advanceID, correlationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalHeader
	for rows.Next() {
		h, err := scanJournalHeader(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetJournalWithEntries loads one journal and its balanced entries WITHIN the
// operator's scope — an out-of-scope or absent id both return ErrNotFound, so
// the no-oracle 404 is structural (tap-to-journal lineage from the browser).
func GetJournalWithEntries(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, journalID string) (JournalDetail, error) {
	var d JournalDetail
	if !scope.authority {
		return d, fmt.Errorf("journal %q: %w", journalID, ErrNotFound)
	}
	h, err := scanJournalHeader(pool.QueryRow(ctx, `
		SELECT `+journalCols+` FROM journals
		WHERE journal_id = $1
		  AND ($2 = '' OR telco_id = $2)
		  AND ($3 = '' OR programme_id = $3)`, journalID, scope.telco, scope.programme))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("journal %q: %w", journalID, ErrNotFound)
	}
	if err != nil {
		return d, err
	}
	d.JournalHeader = h

	rows, err := pool.Query(ctx, `
		SELECT entry_id, account_code, debit_minor, credit_minor, currency
		FROM journal_entries WHERE journal_id = $1
		ORDER BY entry_id`, journalID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var e JournalEntryRow
		var debit, credit int64
		var cur string
		if err := rows.Scan(&e.EntryID, &e.AccountCode, &debit, &credit, &cur); err != nil {
			return d, err
		}
		if e.Debit, err = scanMoney(debit, cur); err != nil {
			return d, err
		}
		if e.Credit, err = scanMoney(credit, cur); err != nil {
			return d, err
		}
		d.Entries = append(d.Entries, e)
	}
	return d, rows.Err()
}

// --- reconciliation breaks queue (telco-grained; M4d part 2) ----------------

type BreakItem struct {
	ReconItemID string
	RunID       string
	TelcoID     string
	ItemType    string
	Status      string
	PlatformRef string
	TelcoRef    string
	AssignedTo  string
	CreatedAt   string // RFC3339
}

const breakCols = `recon_item_id, run_id, telco_id, item_type, status,
	COALESCE(platform_ref,''), COALESCE(telco_ref,''), COALESCE(assigned_to,''),
	to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SS.USOF')`

func scanBreak(row pgx.Row) (BreakItem, error) {
	var b BreakItem
	err := row.Scan(&b.ReconItemID, &b.RunID, &b.TelcoID, &b.ItemType, &b.Status,
		&b.PlatformRef, &b.TelcoRef, &b.AssignedTo, &b.CreatedAt)
	return b, err
}

// ListOpenBreaks returns unresolved reconciliation breaks in the operator's
// telco-level scope (M4C-F1 via TelcoLevelBound — a programme/global operator
// reads none, never "all telcos").
func ListOpenBreaks(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope) ([]BreakItem, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT `+breakCols+`
		FROM recon_items
		WHERE status LIKE 'BREAK_%' AND resolved_at IS NULL
		  AND ($1 = '' OR telco_id = $1)
		ORDER BY created_at`, telco)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BreakItem
	for rows.Next() {
		b, err := scanBreak(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetOpenBreak loads one open break within the operator's telco-level scope —
// out-of-scope or absent both return ErrNotFound (no oracle). Used to resolve
// the telco for an action after authorization.
func GetOpenBreak(ctx context.Context, pool *pgxpool.Pool, scope OperatorScope, reconItemID string) (BreakItem, error) {
	telco, ok := scope.TelcoLevelBound()
	if !ok {
		return BreakItem{}, fmt.Errorf("recon break %q: %w", reconItemID, ErrNotFound)
	}
	b, err := scanBreak(pool.QueryRow(ctx, `
		SELECT `+breakCols+` FROM recon_items
		WHERE recon_item_id = $1 AND status LIKE 'BREAK_%' AND resolved_at IS NULL
		  AND ($2 = '' OR telco_id = $2)`, reconItemID, telco))
	if errors.Is(err, pgx.ErrNoRows) {
		return BreakItem{}, fmt.Errorf("recon break %q: %w", reconItemID, ErrNotFound)
	}
	return b, err
}
