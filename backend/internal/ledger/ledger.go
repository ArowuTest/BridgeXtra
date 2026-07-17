// Package ledger is the SOLE writer of journals and journal_entries
// (V2-SRV-002; ADR-0001 layering contract). Every posting is:
//   - idempotent: (business_event_key, event_type) unique — the DB is the
//     arbiter (INV-003);
//   - balanced per currency at posting time (V2-LED-001) — an unbalanced
//     journal is rejected before any row is written, and the schema's
//     append-only grants mean a posted journal can never be edited;
//   - account-validated against the governed chart of accounts config
//     (ledger.accounts) — posting to an unknown account fails closed;
//   - correlation-linked (BC-6): every journal carries the correlation_id of
//     the customer action that caused it.
//
// No other package may INSERT into journals/journal_entries; reviews treat a
// violation as an architecture defect (V2-ARC-007).
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

// Typed errors (BC-7).
var (
	ErrUnbalanced     = errors.New("ledger: journal does not balance per currency")
	ErrUnknownAccount = errors.New("ledger: account not in the governed chart of accounts")
	ErrEmptyJournal   = errors.New("ledger: journal must have at least two entries")
	ErrBadLine        = errors.New("ledger: line must have exactly one positive side")
)

// Side of an entry.
type Side string

const (
	Debit  Side = "DEBIT"
	Credit Side = "CREDIT"
)

// Line is one journal entry to post.
type Line struct {
	Account string
	Side    Side
	Amount  entity.Money // must be positive
}

// Journal is a balanced set of lines for one economic event.
type Journal struct {
	BusinessEventKey string // e.g. "advance:ADV-.../issued"
	EventType        string // e.g. "ADVANCE_ISSUED"
	TelcoID          string
	ProgrammeID      string
	AdvanceID        string
	CorrelationID    string
	Lines            []Line
}

// Well-known M1 event types.
const (
	EventAdvanceIssued   = "ADVANCE_ISSUED"
	EventRecoveryApplied = "RECOVERY_APPLIED"
)

// Service posts journals. Chart-of-accounts comes from governed config —
// admin-managed, never hardcoded.
type Service struct {
	Config *configsvc.Service
}

func New(cfg *configsvc.Service) *Service { return &Service{Config: cfg} }

type chart struct {
	Accounts []struct {
		Code string `json:"code"`
		Kind string `json:"kind"`
	} `json:"accounts"`
}

func (s *Service) allowedAccounts(ctx context.Context, at time.Time) (map[string]bool, error) {
	cv, err := s.Config.ActiveAt(ctx, "ledger.accounts", entity.ScopeGlobal, at)
	if err != nil {
		return nil, fmt.Errorf("chart of accounts config: %w", err)
	}
	var c chart
	if err := json.Unmarshal(cv.Content, &c); err != nil {
		return nil, fmt.Errorf("chart of accounts parse: %w", err)
	}
	out := make(map[string]bool, len(c.Accounts))
	for _, a := range c.Accounts {
		out[a.Code] = true
	}
	return out, nil
}

// Post writes one balanced journal inside the caller's transaction (the same
// transaction as the state change it records — V2-COL-005 atomicity).
// Duplicate (business_event_key, event_type) returns posted=false with no
// error and NO new rows: at-most-once economic posting (INV-003).
func (s *Service) Post(ctx context.Context, tx pgx.Tx, j Journal) (posted bool, journalID string, err error) {
	if len(j.Lines) < 2 {
		return false, "", ErrEmptyJournal
	}
	if j.BusinessEventKey == "" || j.EventType == "" || j.TelcoID == "" || j.ProgrammeID == "" || j.CorrelationID == "" {
		return false, "", fmt.Errorf("ledger: business_event_key, event_type, telco, programme and correlation_id are required")
	}

	allowed, err := s.allowedAccounts(ctx, time.Now().UTC())
	if err != nil {
		return false, "", err
	}

	// Balance check per currency BEFORE any write (V2-LED-001).
	sums := map[entity.Currency]int64{} // debit-positive, credit-negative; must end zero
	for _, l := range j.Lines {
		if !l.Amount.IsPositive() {
			return false, "", fmt.Errorf("%w: account %s", ErrBadLine, l.Account)
		}
		if !allowed[l.Account] {
			return false, "", fmt.Errorf("%w: %q", ErrUnknownAccount, l.Account)
		}
		switch l.Side {
		case Debit:
			sums[l.Amount.Currency()] += l.Amount.Amount()
		case Credit:
			sums[l.Amount.Currency()] -= l.Amount.Amount()
		default:
			return false, "", fmt.Errorf("%w: side %q", ErrBadLine, l.Side)
		}
	}
	for cur, s := range sums {
		if s != 0 {
			return false, "", fmt.Errorf("%w: %s off by %d minor", ErrUnbalanced, cur, s)
		}
	}

	journalID = platform.NewID("jrn")
	ct, err := tx.Exec(ctx, `
		INSERT INTO journals (journal_id, business_event_key, event_type, telco_id, programme_id, advance_id, correlation_id)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7)
		ON CONFLICT (business_event_key, event_type) DO NOTHING`,
		journalID, j.BusinessEventKey, j.EventType, j.TelcoID, j.ProgrammeID, j.AdvanceID, j.CorrelationID)
	if err != nil {
		return false, "", err
	}
	if ct.RowsAffected() == 0 {
		// Already posted — return the existing journal id for traceability.
		var existing string
		if err := tx.QueryRow(ctx,
			`SELECT journal_id FROM journals WHERE business_event_key=$1 AND event_type=$2`,
			j.BusinessEventKey, j.EventType).Scan(&existing); err != nil {
			return false, "", err
		}
		return false, existing, nil
	}

	for _, l := range j.Lines {
		debit, credit := int64(0), int64(0)
		if l.Side == Debit {
			debit = l.Amount.Amount()
		} else {
			credit = l.Amount.Amount()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_entries (entry_id, journal_id, account_code, debit_minor, credit_minor, currency)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			platform.NewID("je"), journalID, l.Account, debit, credit, string(l.Amount.Currency())); err != nil {
			return false, "", err
		}
	}
	return true, journalID, nil
}

// AccountBalance reconstructs an account balance (debits - credits) from
// entries — the V2-LED-008 rebuild primitive the BC-3 invariant checker and
// daily close use. Set-based SQL in the sole-writer package (repo rule:
// ledger owns its own queries the way a specialized repo would).
func (s *Service) AccountBalance(ctx context.Context, pool *pgxpool.Pool, account string, cur entity.Currency) (entity.Money, error) {
	var minor int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit_minor - credit_minor), 0)
		FROM journal_entries WHERE account_code = $1 AND currency = $2`,
		account, string(cur)).Scan(&minor)
	if err != nil {
		return entity.Money{}, err
	}
	return entity.NewMoney(minor, cur)
}

// CheckAllJournalsBalanced is invariant INV-004 as one set-based query:
// returns the ids of any journal whose entries do not sum to zero per
// currency. Empty result = healthy.
func (s *Service) CheckAllJournalsBalanced(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT journal_id FROM journal_entries
		GROUP BY journal_id, currency
		HAVING SUM(debit_minor) <> SUM(credit_minor)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		bad = append(bad, id)
	}
	return bad, rows.Err()
}
