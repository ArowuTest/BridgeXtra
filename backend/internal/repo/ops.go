package repo

// M3f repositories: breaks workflow (lifecycle columns + append-only action
// log on recon_items), complaints register, bureau export staging.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Breaks workflow (V2-REC-008..012)
// ---------------------------------------------------------------------------

type Breaks struct{}

// Action appends one break action (append-only by grants) and applies its
// lifecycle effect. Resolution REQUIRES a reason and stamps the item;
// assignment stamps the assignee; NOTE/ESCALATE only log.
func (Breaks) Action(ctx context.Context, tx pgx.Tx, telcoID, reconItemID, action, actor, reason string) error {
	if actor == "" || reason == "" {
		return fmt.Errorf("actor and reason are required for break actions")
	}
	switch action {
	case "ASSIGN":
		ct, err := tx.Exec(ctx, `
			UPDATE recon_items SET assigned_to = $2
			WHERE recon_item_id = $1 AND status LIKE 'BREAK_%' AND resolved_at IS NULL`,
			reconItemID, actor)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("recon item %q is not an open break: %w", reconItemID, ErrNotFound)
		}
	case "RESOLVE":
		ct, err := tx.Exec(ctx, `
			UPDATE recon_items SET resolved_at = now(), resolution = $2
			WHERE recon_item_id = $1 AND status LIKE 'BREAK_%' AND resolved_at IS NULL`,
			reconItemID, reason)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("recon item %q is not an open break: %w", reconItemID, ErrNotFound)
		}
	case "ESCALATE", "NOTE":
		// log-only actions; the item row is untouched
	default:
		return fmt.Errorf("unknown break action %q", action)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO recon_break_actions (action_id, telco_id, recon_item_id, action, actor, reason)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		"rba_"+reconItemID+"_"+action+"_"+fmt.Sprint(time.Now().UnixNano()),
		telcoID, reconItemID, action, actor, reason)
	return err
}

// AgedBreak is one unresolved break older than the alert threshold.
type AgedBreak struct {
	ReconItemID string
	Status      string
	AssignedTo  string
	AgeHours    int
}

// ListAged returns unresolved breaks older than the threshold (V2-REC-012:
// breaks demand attention; aging makes silence impossible).
func (Breaks) ListAged(ctx context.Context, tx pgx.Tx, olderThan time.Duration) ([]AgedBreak, error) {
	rows, err := tx.Query(ctx, `
		SELECT recon_item_id, status, COALESCE(assigned_to,''),
		       (EXTRACT(EPOCH FROM (now() - created_at)) / 3600)::int
		FROM recon_items
		WHERE status LIKE 'BREAK_%' AND resolved_at IS NULL
		  AND created_at < now() - $1::interval
		ORDER BY created_at`, fmt.Sprintf("%d hours", int(olderThan.Hours())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgedBreak
	for rows.Next() {
		var b AgedBreak
		if err := rows.Scan(&b.ReconItemID, &b.Status, &b.AssignedTo, &b.AgeHours); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Complaints register (V1-CUS)
// ---------------------------------------------------------------------------

type Complaints struct{}

type Complaint struct {
	ComplaintID         string
	TelcoID             string
	SubscriberAccountID string
	AdvanceID           string
	Channel             string
	Category            string
	Narrative           string
	State               string
	Resolution          string
	OpenedAt            time.Time
	ResolvedAt          *time.Time
}

func (Complaints) Insert(ctx context.Context, tx pgx.Tx, c Complaint) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO complaints
		  (complaint_id, telco_id, subscriber_account_id, advance_id, channel, category, narrative)
		VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7)`,
		c.ComplaintID, c.TelcoID, c.SubscriberAccountID, c.AdvanceID,
		c.Channel, c.Category, c.Narrative)
	if err != nil {
		return fmt.Errorf("insert complaint: %w", err)
	}
	return nil
}

// Transition moves the complaint lifecycle; closing states REQUIRE a
// resolution (the schema CHECK is the arbiter).
func (Complaints) Transition(ctx context.Context, tx pgx.Tx, complaintID, from, to, resolution string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE complaints
		SET state = $3, resolution = NULLIF($4,''),
		    resolved_at = CASE WHEN $3 IN ('RESOLVED','REJECTED') THEN now() ELSE resolved_at END
		WHERE complaint_id = $1 AND state = $2`, complaintID, from, to, resolution)
	if err != nil {
		return fmt.Errorf("complaint transition: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("complaint %q not in %s: %w", complaintID, from, ErrNotFound)
	}
	return nil
}

func (Complaints) Get(ctx context.Context, tx pgx.Tx, complaintID string) (Complaint, error) {
	var c Complaint
	var sub, adv, res *string
	err := tx.QueryRow(ctx, `
		SELECT complaint_id, telco_id, subscriber_account_id, advance_id, channel, category,
		       narrative, state, resolution, opened_at, resolved_at
		FROM complaints WHERE complaint_id = $1`, complaintID).
		Scan(&c.ComplaintID, &c.TelcoID, &sub, &adv, &c.Channel, &c.Category,
			&c.Narrative, &c.State, &res, &c.OpenedAt, &c.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("complaint %q: %w", complaintID, ErrNotFound)
	}
	if sub != nil {
		c.SubscriberAccountID = *sub
	}
	if adv != nil {
		c.AdvanceID = *adv
	}
	if res != nil {
		c.Resolution = *res
	}
	return c, err
}

// ---------------------------------------------------------------------------
// Bureau export staging (V1-REG, DORMANT)
// ---------------------------------------------------------------------------

type BureauBatches struct{}

type BureauBatch struct {
	BatchID     string
	TelcoID     string
	PeriodStart time.Time
	PeriodEnd   time.Time
	RowCount    int
	FileHash    string
	State       string
	CreatedAt   time.Time
}

func (BureauBatches) Insert(ctx context.Context, tx pgx.Tx, b BureauBatch) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO bureau_export_batches
		  (batch_id, telco_id, period_start, period_end, row_count, file_hash)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		b.BatchID, b.TelcoID, b.PeriodStart, b.PeriodEnd, b.RowCount, b.FileHash)
	if err != nil {
		return fmt.Errorf("insert bureau batch: %w", err)
	}
	return nil
}

func (BureauBatches) Get(ctx context.Context, tx pgx.Tx, batchID string) (BureauBatch, error) {
	var b BureauBatch
	err := tx.QueryRow(ctx, `
		SELECT batch_id, telco_id, period_start, period_end, row_count, COALESCE(file_hash,''), state, created_at
		FROM bureau_export_batches WHERE batch_id = $1`, batchID).
		Scan(&b.BatchID, &b.TelcoID, &b.PeriodStart, &b.PeriodEnd, &b.RowCount, &b.FileHash, &b.State, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, fmt.Errorf("bureau batch %q: %w", batchID, ErrNotFound)
	}
	return b, err
}

// BureauRow is one advance's performance record (tokenised — PII-lean).
type BureauRow struct {
	MSISDNToken       string    `json:"msisdn_token"`
	AdvanceID         string    `json:"advance_id"`
	FaceValueMinor    int64     `json:"face_value_minor"`
	OutstandingMinor  int64     `json:"outstanding_minor"`
	State             string    `json:"state"`
	DelinquencyBucket string    `json:"delinquency_bucket"`
	AcceptedAt        time.Time `json:"accepted_at"`
}

// PerformanceRows derives the period's bureau rows deterministically from
// the book, ordered by advance id (stable file bytes).
func (BureauBatches) PerformanceRows(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]BureauRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.msisdn_token, a.advance_id, a.face_value_minor, a.outstanding_minor,
		       a.state, COALESCE(a.delinquency_bucket,''), a.accepted_at
		FROM advances a
		JOIN subscriber_accounts s USING (subscriber_account_id)
		WHERE a.accepted_at >= $1 AND a.accepted_at < $2
		  AND a.state NOT IN ('DECLINED','FULFILMENT_FAILED')
		ORDER BY a.advance_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BureauRow
	for rows.Next() {
		var r BureauRow
		if err := rows.Scan(&r.MSISDNToken, &r.AdvanceID, &r.FaceValueMinor, &r.OutstandingMinor,
			&r.State, &r.DelinquencyBucket, &r.AcceptedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
