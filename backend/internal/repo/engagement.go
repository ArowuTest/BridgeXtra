package repo

// M2e repositories: overlay flags, consent evidence, notification evidence.
// All tenant-scoped by RLS; consent is INSERT-only by grants (evidence is
// never edited).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

// ---------------------------------------------------------------------------
// Subscriber overlay flags (V2-SCR-008/015)
// ---------------------------------------------------------------------------

type SubscriberFlags struct{}

// OpenFlag is one active risk flag with the time it was raised (the SIM-swap
// cool-off is computed from EffectiveFrom).
type OpenFlag struct {
	Flag          string
	EffectiveFrom time.Time
}

// ListOpen returns the subscriber's open flags. Index: subscriber_flags_sub_ix.
func (SubscriberFlags) ListOpen(ctx context.Context, tx pgx.Tx, subscriberAccountID string) ([]OpenFlag, error) {
	rows, err := tx.Query(ctx, `
		SELECT flag, effective_from FROM subscriber_flags
		WHERE subscriber_account_id = $1 AND effective_to IS NULL`, subscriberAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenFlag
	for rows.Next() {
		var f OpenFlag
		if err := rows.Scan(&f.Flag, &f.EffectiveFrom); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Raise opens a flag (idempotent: the partial unique index makes a re-raise
// a no-op). Returns whether a new flag row was created.
func (SubscriberFlags) Raise(ctx context.Context, tx pgx.Tx, flagID, telcoID, subscriberAccountID, flag, source string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO subscriber_flags (flag_id, telco_id, subscriber_account_id, flag, source)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (subscriber_account_id, flag) WHERE effective_to IS NULL DO NOTHING`,
		flagID, telcoID, subscriberAccountID, flag, source)
	if err != nil {
		return false, fmt.Errorf("raise flag: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// Clear closes an open flag (evidence retained, never deleted).
func (SubscriberFlags) Clear(ctx context.Context, tx pgx.Tx, subscriberAccountID, flag string) error {
	_, err := tx.Exec(ctx, `
		UPDATE subscriber_flags SET effective_to = now()
		WHERE subscriber_account_id = $1 AND flag = $2 AND effective_to IS NULL`,
		subscriberAccountID, flag)
	return err
}

// ---------------------------------------------------------------------------
// Consent / disclosure evidence (V2-REG-001)
// ---------------------------------------------------------------------------

type Consents struct{}

type Consent struct {
	ConsentID           string
	TelcoID             string
	AdvanceID           string
	SubscriberAccountID string
	DisclosedTerms      []byte
	ContentHash         string
	Channel             string
	CapturedAt          time.Time
}

// Insert writes the consent record — called INSIDE the confirm transaction,
// so an advance without consent evidence cannot exist (UNIQUE(advance_id)
// also makes the replay path safe).
func (Consents) Insert(ctx context.Context, tx pgx.Tx, c Consent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO consents
		  (consent_id, telco_id, advance_id, subscriber_account_id, disclosed_terms, content_hash, channel)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.ConsentID, c.TelcoID, c.AdvanceID, c.SubscriberAccountID,
		c.DisclosedTerms, c.ContentHash, c.Channel)
	if err != nil {
		return fmt.Errorf("insert consent: %w", err)
	}
	return nil
}

// GetByAdvance reads the consent evidence for an advance.
func (Consents) GetByAdvance(ctx context.Context, tx pgx.Tx, advanceID string) (Consent, error) {
	var c Consent
	err := tx.QueryRow(ctx, `
		SELECT consent_id, telco_id, advance_id, subscriber_account_id,
		       disclosed_terms, content_hash, channel, captured_at
		FROM consents WHERE advance_id = $1`, advanceID).
		Scan(&c.ConsentID, &c.TelcoID, &c.AdvanceID, &c.SubscriberAccountID,
			&c.DisclosedTerms, &c.ContentHash, &c.Channel, &c.CapturedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("consent for advance %q: %w", advanceID, ErrNotFound)
	}
	return c, err
}

// ---------------------------------------------------------------------------
// Notification evidence (V2 §10.2)
// ---------------------------------------------------------------------------

type Notifications struct{}

type Notification struct {
	NotificationID      string
	TelcoID             string
	SubscriberAccountID string
	AdvanceID           string
	Kind                string
	TemplateVersion     string
	RenderedHash        string
	State               string // PENDING | SENT | FAILED
	ProviderRef         string
	CreatedAt           time.Time
	SentAt              *time.Time
}

// Ensure creates the evidence row if absent (idempotent per (advance, kind));
// returns the row either way.
func (Notifications) Ensure(ctx context.Context, tx pgx.Tx, n Notification) (Notification, error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO notifications
		  (notification_id, telco_id, subscriber_account_id, advance_id, kind,
		   template_version, rendered_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (advance_id, kind) DO NOTHING`,
		n.NotificationID, n.TelcoID, n.SubscriberAccountID, n.AdvanceID, n.Kind,
		n.TemplateVersion, n.RenderedHash)
	if err != nil {
		return n, fmt.Errorf("ensure notification: %w", err)
	}
	return (Notifications{}).GetByAdvanceKind(ctx, tx, n.AdvanceID, n.Kind)
}

func (Notifications) GetByAdvanceKind(ctx context.Context, tx pgx.Tx, advanceID, kind string) (Notification, error) {
	var n Notification
	err := tx.QueryRow(ctx, `
		SELECT notification_id, telco_id, subscriber_account_id, COALESCE(advance_id,''), kind,
		       template_version, rendered_hash, state, COALESCE(provider_ref,''), created_at, sent_at
		FROM notifications WHERE advance_id = $1 AND kind = $2`, advanceID, kind).
		Scan(&n.NotificationID, &n.TelcoID, &n.SubscriberAccountID, &n.AdvanceID, &n.Kind,
			&n.TemplateVersion, &n.RenderedHash, &n.State, &n.ProviderRef, &n.CreatedAt, &n.SentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, fmt.Errorf("notification %s/%s: %w", advanceID, kind, ErrNotFound)
	}
	return n, err
}

// MarkSent/MarkFailed record the delivery outcome (worker role).
func (Notifications) MarkSent(ctx context.Context, tx pgx.Tx, notificationID, providerRef string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE notifications SET state = 'SENT', provider_ref = $2, sent_at = now()
		WHERE notification_id = $1 AND state = 'PENDING'`, notificationID, providerRef)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("notification %q not PENDING: %w", notificationID, ErrNotFound)
	}
	return nil
}

func (Notifications) MarkFailed(ctx context.Context, tx pgx.Tx, notificationID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notifications SET state = 'FAILED'
		WHERE notification_id = $1 AND state = 'PENDING'`, notificationID)
	return err
}

// GetByID resolves a subscriber account by primary key (notifier path).
func (Subscribers) GetByID(ctx context.Context, tx pgx.Tx, subscriberAccountID string) (entity.SubscriberAccount, error) {
	var s entity.SubscriberAccount
	err := tx.QueryRow(ctx, `
		SELECT subscriber_account_id, telco_id, msisdn_token, status, effective_from, effective_to
		FROM subscriber_accounts WHERE subscriber_account_id = $1`, subscriberAccountID).
		Scan(&s.SubscriberAccountID, &s.TelcoID, &s.MSISDNToken, &s.Status, &s.EffectiveFrom, &s.EffectiveTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("subscriber %q: %w", subscriberAccountID, ErrNotFound)
	}
	return s, err
}

// ---------------------------------------------------------------------------
// Decision point-read (M2e confirm-time validity check)
// ---------------------------------------------------------------------------

// Get reads one decision snapshot by id (validity fields included).
func (Decisions) Get(ctx context.Context, tx pgx.Tx, decisionSnapshotID string) (entity.DecisionSnapshot, error) {
	var d entity.DecisionSnapshot
	var minor int64
	var cur string
	err := tx.QueryRow(ctx, `
		SELECT decision_snapshot_id, telco_id, subscriber_account_id, max_face_value_minor, currency,
		       is_current, config_version_id, created_at, tier_code, COALESCE(scoring_run_id,''), valid_until
		FROM decision_snapshots WHERE decision_snapshot_id = $1`, decisionSnapshotID).
		Scan(&d.DecisionSnapshotID, &d.TelcoID, &d.SubscriberAccountID, &minor, &cur,
			&d.IsCurrent, &d.ConfigVersionID, &d.CreatedAt, &d.TierCode, &d.ScoringRunID, &d.ValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("decision %q: %w", decisionSnapshotID, ErrNotFound)
	}
	if err != nil {
		return d, err
	}
	d.MaxFaceValue, err = scanMoney(minor, cur)
	return d, err
}
