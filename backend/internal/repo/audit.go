package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// Audit is append-only: the schema grants INSERT (+SELECT) only — no UPDATE or
// DELETE exists for any application role (V2-OBS-006).
type Audit struct{}

func (Audit) Insert(ctx context.Context, tx pgx.Tx, e entity.AuditEvent) error {
	detail := e.Detail
	if len(detail) == 0 {
		detail = []byte(`{}`)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events (id, telco_id, actor, action, target_type, target_id, reason, detail, source_ip)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, NULLIF($7,''), $8, NULLIF($9,''))`,
		e.ID, e.TelcoID, e.Actor, e.Action, e.TargetType, e.TargetID, e.Reason, detail, e.SourceIP)
	return err
}

// InsertPlatform writes a platform-scope audit event outside any tenant
// transaction (e.g. rejected authentication where no tenant context exists yet).
func (Audit) InsertPlatform(ctx context.Context, pool *pgxpool.Pool, e entity.AuditEvent) error {
	detail := e.Detail
	if len(detail) == 0 {
		detail = []byte(`{}`)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO audit_events (id, telco_id, actor, action, target_type, target_id, reason, detail, source_ip)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, NULLIF($7,''), $8, NULLIF($9,''))`,
		e.ID, e.TelcoID, e.Actor, e.Action, e.TargetType, e.TargetID, e.Reason, detail, e.SourceIP)
	return err
}
