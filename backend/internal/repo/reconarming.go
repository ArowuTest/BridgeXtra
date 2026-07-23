package repo

// ReconArming reads/writes the recon-layer arming marker — the structural
// "no webhook money without reconciliation" gate. The recharge webhook refuses
// to ingest for a telco unless that telco's RECOVERY recon layer is live here;
// S3 sets it live when it arms the layer. Read by telco_id before tenant context
// (a control lookup), so it is not RLS-scoped.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The recon layer the recharge webhook gates on.
const ReconLayerRecovery = "RECOVERY"

type ReconArming struct{ Pool *pgxpool.Pool }

// IsLayerLive reports whether the recon layer is armed (live) for the telco.
func (r *ReconArming) IsLayerLive(ctx context.Context, telcoID, layer string) (bool, error) {
	var n int
	if err := r.Pool.QueryRow(ctx,
		`SELECT count(*) FROM recon_layer_arming WHERE telco_id = $1 AND layer = $2`,
		telcoID, layer).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetLive arms a layer for a telco (S3 / ops). Idempotent.
func (r *ReconArming) SetLive(ctx context.Context, telcoID, layer string) error {
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO recon_layer_arming (telco_id, layer) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		telcoID, layer)
	return err
}

// SetDown disarms a layer (ops), which immediately stops webhook ingestion.
func (r *ReconArming) SetDown(ctx context.Context, telcoID, layer string) error {
	_, err := r.Pool.Exec(ctx,
		`DELETE FROM recon_layer_arming WHERE telco_id = $1 AND layer = $2`, telcoID, layer)
	return err
}
