package repo

// ReconArming reads/writes the recon-layer arming marker — the structural
// "no webhook money without reconciliation" gate. The recharge webhook refuses
// to ingest for a telco unless that telco's RECOVERY recon layer is live here;
// S3 sets it live when it arms the layer. Read by telco_id before tenant context
// (a control lookup), so it is not RLS-scoped.

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The recon layer the recharge webhook gates on.
const ReconLayerRecovery = "RECOVERY"

type ReconArming struct{ Pool *pgxpool.Pool }

// IsLayerLive reports whether the recon layer is armed AND FRESH for the telco
// (S3-B). "Live" is true only if the layer is armed, has been confirmed by at
// least one recon (last_recon_at NOT NULL — a freshly-armed layer is not live
// until its first confirmed recon), and that confirmation is within the governed
// freshness window. make_interval(secs => 0) never satisfies `>= now()`, so a
// mis-shrunk window fails CLOSED; the column CHECK bounds the upper side. Single
// pre-tenant, PK-served query; server clock on both sides (skew-immune).
func (r *ReconArming) IsLayerLive(ctx context.Context, telcoID, layer string) (bool, error) {
	var live bool
	if err := r.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM recon_layer_arming
			WHERE telco_id = $1 AND layer = $2
			  AND last_recon_at IS NOT NULL
			  AND last_recon_at >= now() - make_interval(secs => arm_freshness_max_seconds))`,
		telcoID, layer).Scan(&live); err != nil {
		return false, err
	}
	return live, nil
}

// IsLayerArmed reports whether the layer is armed for the telco (row present),
// independent of freshness — the driver checks this so it never reconciles-then-
// arms an unarmed telco, and the arming path checks it too.
func (r *ReconArming) IsLayerArmed(ctx context.Context, telcoID, layer string) (bool, error) {
	var n int
	if err := r.Pool.QueryRow(ctx,
		`SELECT count(*) FROM recon_layer_arming WHERE telco_id = $1 AND layer = $2`,
		telcoID, layer).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// AdvanceFreshness stamps last_recon_at = SERVER now() and denormalises the
// governed freshness window onto the row — called by the recon driver ONLY after a
// day whose booked recovery money the feed confirmed (S3-B positive-confirmation
// gate). UPDATE-only with a mandatory composite predicate: it NEVER inserts, so 0
// rows affected on a disarmed / never-armed layer (arming rows are created only by
// the four-eyes SetLive path). It therefore cannot resurrect a row a concurrent
// SetDown just deleted. Returns whether a row was advanced.
func (r *ReconArming) AdvanceFreshness(ctx context.Context, telcoID, layer string, freshnessMaxSeconds int) (bool, error) {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE recon_layer_arming
		   SET last_recon_at = now(), arm_freshness_max_seconds = $3
		 WHERE telco_id = $1 AND layer = $2`, telcoID, layer, freshnessMaxSeconds)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// SetLive arms a layer for a telco (S3 / ops). Idempotent. last_recon_at defaults
// NULL, so an armed layer is NOT live until its first confirmed recon.
func (r *ReconArming) SetLive(ctx context.Context, telcoID, layer string) error {
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO recon_layer_arming (telco_id, layer) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		telcoID, layer)
	return err
}

// SetLiveArmed arms a layer inside a caller tx (the four-eyes approve), writing the
// governed freshness window from the arm request. tx-based so arming is atomic with
// the request-state flip. last_recon_at stays NULL — armed but NOT live until the
// first confirmed recon. Idempotent.
func (r *ReconArming) SetLiveArmed(ctx context.Context, tx pgx.Tx, telcoID, layer string, freshnessMaxSeconds int) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO recon_layer_arming (telco_id, layer, arm_freshness_max_seconds)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		telcoID, layer, freshnessMaxSeconds)
	return err
}

// SetDown disarms a layer (ops), which immediately stops webhook ingestion.
func (r *ReconArming) SetDown(ctx context.Context, telcoID, layer string) error {
	_, err := r.Pool.Exec(ctx,
		`DELETE FROM recon_layer_arming WHERE telco_id = $1 AND layer = $2`, telcoID, layer)
	return err
}

// SetDownTx disarms a layer inside a caller's transaction, so the disarm and its durable audit
// record commit atomically (BX-MED-004).
func (ReconArming) SetDownTx(ctx context.Context, tx pgx.Tx, telcoID, layer string) error {
	_, err := tx.Exec(ctx,
		`DELETE FROM recon_layer_arming WHERE telco_id = $1 AND layer = $2`, telcoID, layer)
	return err
}
