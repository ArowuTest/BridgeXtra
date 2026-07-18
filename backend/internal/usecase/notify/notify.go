// Package notify delivers subscriber notifications with evidence (M2e,
// V2 §10.2). Delivery is best-effort; EVIDENCE of the attempt is not:
//
//   - the evidence row (template version + rendered-content hash) is ensured
//     BEFORE any send, idempotent per (advance, kind);
//   - the send is idempotent at the telco (Idempotency-Key = notification id),
//     so a retry after a crash can never double-send;
//   - templates, sender id and quiet hours come from governed
//     notify.templates config — nothing rendered is hardcoded.
//
// Amount formatting is integer-only (BC-1): minor units to a 2-exponent
// display string via integer division, never floating point.
package notify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/egress"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

type Service struct {
	Pool       *pgxpool.Pool // tcp_app
	Config     *configsvc.Service
	Log        *slog.Logger
	HTTPClient *http.Client
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Log: log,
		HTTPClient: egress.SafeClient(10 * time.Second)} // SSRF egress guard (VR-32)
}

type templatesCfg struct {
	SenderID  string `json:"sender_id"`
	Templates map[string]struct {
		Version string `json:"version"`
		Body    string `json:"body"`
	} `json:"templates"`
}

// AdvanceConfirmed sends the ADVANCE_CONFIRMED notification for an advance.
// Called by the worker's outbox consumer — replays are safe end to end.
func (s *Service) AdvanceConfirmed(ctx context.Context, telcoID, advanceID string) error {
	return s.send(ctx, telcoID, advanceID, "ADVANCE_CONFIRMED")
}

func (s *Service) send(ctx context.Context, telcoID, advanceID, kind string) error {
	now := time.Now().UTC()
	cv, err := s.Config.ActiveAt(ctx, "notify.templates", "telco:"+telcoID, now)
	if err != nil {
		return fmt.Errorf("notify.templates config: %w", err)
	}
	var tc templatesCfg
	if err := json.Unmarshal(cv.Content, &tc); err != nil {
		return err
	}
	tpl, ok := tc.Templates[kind]
	if !ok {
		// No template for this kind is a CONFIG decision (e.g. a telco that
		// only notifies on confirmation) — recorded, not an error.
		s.Log.Info("no template configured for notification kind — skipping", "kind", kind, "telco", telcoID)
		return nil
	}

	// Phase 1 (evidence, tenant tx): load the advance + subscriber, render,
	// ensure the evidence row.
	tctx := platform.WithTenant(ctx, telcoID)
	var n repo.Notification
	var token, body string
	err = repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		adv, err := (repo.Advances{}).Get(ctx, tx, advanceID)
		if err != nil {
			return err
		}
		sub, err := (repo.Subscribers{}).GetByID(ctx, tx, adv.SubscriberAccountID)
		if err != nil {
			return err
		}
		token = sub.MSISDNToken
		body = render(tpl.Body, adv)
		bodyHash := sha256.Sum256([]byte(body))
		n, err = (repo.Notifications{}).Ensure(ctx, tx, repo.Notification{
			NotificationID: platform.NewID("ntf"), TelcoID: telcoID,
			SubscriberAccountID: adv.SubscriberAccountID, AdvanceID: advanceID,
			Kind: kind, TemplateVersion: tpl.Version,
			RenderedHash: hex.EncodeToString(bodyHash[:]),
		})
		return err
	})
	if err != nil {
		return err
	}
	if n.State == "SENT" {
		return nil // already delivered (replay)
	}

	// Phase 2 (network, NO transaction): idempotent submit — the
	// notification id is the idempotency key, so retries replay.
	providerRef, sendErr := s.submit(ctx, telcoID, n.NotificationID, token, tc.SenderID, body)

	// Phase 3 (outcome, tenant tx).
	return repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		if sendErr != nil {
			s.Log.Error("notification send failed — evidence retained as FAILED",
				"notification", n.NotificationID, "err", sendErr)
			return (repo.Notifications{}).MarkFailed(ctx, tx, n.NotificationID)
		}
		return (repo.Notifications{}).MarkSent(ctx, tx, n.NotificationID, providerRef)
	})
}

func (s *Service) submit(ctx context.Context, telcoID, idemKey, token, sender, body string) (string, error) {
	cv, err := s.Config.ActiveAt(ctx, "telco.adapter", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("telco.adapter config: %w", err)
	}
	var ac struct {
		FulfilmentURL string `json:"fulfilment_url"`
	}
	if err := json.Unmarshal(cv.Content, &ac); err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{
		"msisdn_token": token, "sender_id": sender, "body": body,
	})
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/v1/telcos/%s/sms", ac.FulfilmentURL, telcoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sms endpoint returned %d", resp.StatusCode)
	}
	var out struct {
		ProviderRef string `json:"provider_ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ProviderRef, nil
}

// render substitutes template placeholders with display amounts.
func render(tpl string, adv entity.Advance) string {
	r := strings.NewReplacer(
		"{{face}}", display(adv.FaceValue),
		"{{fee}}", display(adv.Fee),
		"{{disbursed}}", display(adv.Disbursed),
		"{{repayment}}", display(adv.Outstanding),
		"{{outstanding}}", display(adv.Outstanding),
	)
	return r.Replace(tpl)
}

// display renders Money as "NGN 100.00" using integer math only (BC-1).
// Exponent-2 covers every launch currency; a zero-exponent currency arrives
// with its own config in the settlement milestone.
func display(m entity.Money) string {
	minor := m.Amount()
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s %s%d.%02d", m.Currency(), sign, minor/100, minor%100)
}
