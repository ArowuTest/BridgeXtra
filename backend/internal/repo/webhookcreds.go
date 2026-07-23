package repo

// Phase 1 S2 — webhook credential lookup: the public key_id -> telco + HMAC
// secret-env-name map. Resolved BEFORE tenant context (an identity lookup, like
// Telcos.ResolveCredential), so it reads via the pool directly and the table is
// not RLS-scoped. Only ACTIVE credentials resolve — a REVOKED or unknown key_id
// both yield ErrWebhookCredentialNotFound so the handler can treat them
// identically (uniform reject, no ACTIVE/REVOKED timing oracle).

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrWebhookCredentialNotFound is returned for an unknown or revoked key_id.
var ErrWebhookCredentialNotFound = errors.New("repo: webhook credential not found or revoked")

type WebhookCredentials struct{ Pool *pgxpool.Pool }

// WebhookCredential is the resolved (public key_id -> telco + secret env name).
type WebhookCredential struct {
	KeyID     string
	TelcoID   string
	SecretEnv string
}

// ResolveByKeyID maps a public key_id to its telco and the NAME of the env var
// holding the HMAC secret. Unknown or revoked -> ErrWebhookCredentialNotFound.
func (r *WebhookCredentials) ResolveByKeyID(ctx context.Context, keyID string) (WebhookCredential, error) {
	var c WebhookCredential
	err := r.Pool.QueryRow(ctx, `
		SELECT key_id, telco_id, secret_env
		FROM telco_webhook_credentials
		WHERE key_id = $1 AND status = 'ACTIVE'`, keyID).Scan(&c.KeyID, &c.TelcoID, &c.SecretEnv)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookCredential{}, ErrWebhookCredentialNotFound
	}
	if err != nil {
		return WebhookCredential{}, err
	}
	return c, nil
}

// Create registers a webhook credential (bootstrap/admin, out-of-band like the
// seed-operators harness). The secret itself is provisioned separately as the
// named env var — never stored here.
func (r *WebhookCredentials) Create(ctx context.Context, keyID, telcoID, secretEnv, label string) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO telco_webhook_credentials (key_id, telco_id, secret_env, label)
		VALUES ($1, $2, $3, $4)`, keyID, telcoID, secretEnv, label)
	return err
}

// Revoke marks a credential REVOKED (it stops resolving immediately).
func (r *WebhookCredentials) Revoke(ctx context.Context, keyID string) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE telco_webhook_credentials SET status = 'REVOKED' WHERE key_id = $1`, keyID)
	return err
}
