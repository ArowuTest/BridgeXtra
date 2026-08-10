package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// Idempotency implements V2-API-002/003: the outcome of a material command is
// persisted before the response is returned, and a valid retry receives the
// original result. The database (PK) is the arbiter, not application memory.
type Idempotency struct{}

// PutIfAbsent inserts the outcome; if the key already exists it returns the
// ORIGINAL record and stored=false. Runs inside the same tenant transaction
// that committed the business effect (crash-after-commit safe: either both the
// effect and the record exist, or neither does).
func (Idempotency) PutIfAbsent(ctx context.Context, tx pgx.Tx, rec entity.IdempotencyRecord) (entity.IdempotencyRecord, bool, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records
		  (telco_id, operation, idem_key, request_hash, response_status, response_body, terminal)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (telco_id, operation, idem_key) DO NOTHING`,
		rec.TelcoID, rec.Operation, rec.IdemKey, rec.RequestHash,
		rec.ResponseStatus, rec.ResponseBody, rec.Terminal)
	if err != nil {
		return entity.IdempotencyRecord{}, false, err
	}
	if ct.RowsAffected() == 1 {
		return rec, true, nil
	}
	existing, err := Idempotency{}.Get(ctx, tx, rec.TelcoID, rec.Operation, rec.IdemKey)
	return existing, false, err
}

func (Idempotency) Get(ctx context.Context, tx pgx.Tx, telcoID, operation, key string) (entity.IdempotencyRecord, error) {
	var r entity.IdempotencyRecord
	err := tx.QueryRow(ctx, `
		SELECT telco_id, operation, idem_key, request_hash, response_status, response_body, terminal, created_at
		FROM idempotency_records
		WHERE telco_id=$1 AND operation=$2 AND idem_key=$3`,
		telcoID, operation, key).
		Scan(&r.TelcoID, &r.Operation, &r.IdemKey, &r.RequestHash,
			&r.ResponseStatus, &r.ResponseBody, &r.Terminal, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SetResponse persists the exact outcome of a first-time command onto its
// idempotency record, in the SAME tx that committed the business effect
// (R-P0-2). A later valid replay returns this byte-for-byte, so the original
// outcome is reproduced rather than re-derived. Never touches request_hash.
func (Idempotency) SetResponse(ctx context.Context, tx pgx.Tx, telcoID, operation, key string, status int, body []byte) error {
	ct, err := tx.Exec(ctx, `
		UPDATE idempotency_records SET response_status=$4, response_body=$5
		WHERE telco_id=$1 AND operation=$2 AND idem_key=$3`,
		telcoID, operation, key, status, body)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("idempotency record %s/%s/%s: %w", telcoID, operation, key, ErrNotFound)
	}
	return nil
}

// SetResponseIfAbsent persists the outcome onto the idempotency record ONLY if no response has
// been recorded yet (response_status = 0), reporting whether it wrote. BX-MED-002: the confirm
// response is now written in the SAME transaction that commits the fulfilment outcome, and BOTH
// the saga (tx2) and the resolver worker can be the one to settle an outcome. The conditional
// predicate makes the loser of that race a clean no-op (wrote=false) instead of tripping the
// migration-0029 write-once trigger, and under READ COMMITTED the loser re-evaluates the predicate
// after the winner commits — so the FIRST recorded response stays authoritative and immutable.
// A missing record is an error, never a silent create: the record is seeded in tx1.
func (Idempotency) SetResponseIfAbsent(ctx context.Context, tx pgx.Tx, telcoID, operation, key string, status int, body []byte) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE idempotency_records SET response_status=$4, response_body=$5
		WHERE telco_id=$1 AND operation=$2 AND idem_key=$3 AND response_status = 0`,
		telcoID, operation, key, status, body)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 1 {
		return true, nil
	}
	// Nothing updated: either a response is already on record (fine — it wins), or the record does
	// not exist at all (a real fault: tx1 seeds it). Disambiguate, mirroring PutIfAbsent's shape.
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM idempotency_records WHERE telco_id=$1 AND operation=$2 AND idem_key=$3)`,
		telcoID, operation, key).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("idempotency record %s/%s/%s: %w", telcoID, operation, key, ErrNotFound)
	}
	return false, nil
}

// MarkTerminal flags the record as eligible for TTL sweep (SF-5): only flows
// that reached a terminal business state may ever be swept.
func (Idempotency) MarkTerminal(ctx context.Context, tx pgx.Tx, telcoID, operation, key string) error {
	_, err := tx.Exec(ctx, `
		UPDATE idempotency_records SET terminal = true
		WHERE telco_id=$1 AND operation=$2 AND idem_key=$3`,
		telcoID, operation, key)
	return err
}
