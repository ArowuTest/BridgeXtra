package handler

// Phase 1 S2.2b — the inbound MNO recharge webhook: the money-auth spine.
//
// It mounts on the SAME onion as the internal recovery ingress
// (IP-rate-limit -> WebhookAuth -> per-telco-rate-limit -> Correlation), but
// authenticates with HMAC instead of an api key. The middleware chain is
// ordered and fail-closed; see build/PHASE1_S2_ASSUMED_CONTRACT.md and the S2
// design memory for the adversarial rationale behind each step:
//
//	body cap (413) -> Step 0 KILL-SWITCH (config enabled at telco AND global,
//	transport/auth match, STRUCTURAL recon-live gate) -> credential->telco
//	(dummy-HMAC uniform 401 on unknown/revoked) -> path cross-check
//	(TENANT_CONTEXT_MISMATCH) -> secret from env (dummy-HMAC 401 if absent) ->
//	constant-time HMAC verify -> asymmetric freshness -> set tenant -> [handler]
//	nonce (idempotent-on-seen, never 409) -> map + money-validate -> per-event /
//	daily blast-radius clamp (HELD, never ingested) -> recovery.Ingest ("wh:").
//
// The telco is derived ONLY from the authenticated credential; the path/body
// telco is cross-checked, never trusted (TEN-002/003).

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform/ratelimit"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recovery"
)

const (
	domainRechargeFeed  = "telco.recharge_feed"
	opRechargeWebhook   = "recharge.webhook"
	auditRechargeDenied = "RECHARGE_FEED_DENIED"
	auditRechargeHeld   = "RECHARGE_EVENT_HELD"
)

// RechargeWebhook is the HTTP boundary for the inbound recharge feed.
type RechargeWebhook struct {
	Recovery          *recovery.Service
	Config            *configsvc.Service
	Creds             *repo.WebhookCredentials
	Recon             *repo.ReconArming
	Pool              *pgxpool.Pool // app-role pool (tenant tx for nonce + held)
	Auth              rechargewebhook.InboundAuthAdapter
	Mapper            rechargewebhook.Mapper
	Audit             repo.Audit
	Limiter           *ratelimit.Limiter
	TrustedProxyCount int
	Log               *slog.Logger
}

type feedCfg struct {
	Enabled                   bool   `json:"enabled"`
	Transport                 string `json:"transport"`
	Auth                      string `json:"auth"`
	KeyIDHeader               string `json:"key_id_header"`
	SignatureHeader           string `json:"signature_header"`
	TimestampHeader           string `json:"timestamp_header"`
	ReplayWindowSeconds       int    `json:"replay_window_seconds"`
	FutureSkewSeconds         int    `json:"future_skew_seconds"`
	MaxBodyBytes              int64  `json:"max_body_bytes"`
	ExpectedCurrency          string `json:"expected_currency"`
	PerEventAmountMaxMinor    int64  `json:"per_event_amount_max_minor"`
	PerTelcoDailyCeilingMinor int64  `json:"per_telco_daily_ceiling_minor"`
}

// Mount wires the webhook route through the standard onion.
func (h *RechargeWebhook) Mount(mux *http.ServeMux) {
	if h.Limiter == nil {
		panic("recharge webhook: rate limiter is required (fail-closed)")
	}
	ipKey := func(r *http.Request) string { return clientIP(r, h.TrustedProxyCount) }
	inner := perTelcoRateLimit(h.Limiter, Correlation(http.HandlerFunc(h.ingest)))
	mux.Handle("POST /v1/telcos/{telco}/recharge-webhook",
		rateLimited(h.Limiter, "channel_ip", ipKey, h.webhookAuth(inner)))
}

type verifiedKey struct{}

type verifiedWebhook struct {
	decodedMAC []byte
	rawBody    []byte
	cfg        feedCfg
}

// webhookAuth performs authentication, the kill-switch + recon gate, and
// freshness — everything that must hold before a byte reaches the money path.
func (h *RechargeWebhook) webhookAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		now := time.Now().UTC()

		// Protocol config (global): header names + the enabled FLOOR. Always seeded.
		gcfg, ok := h.readFeed(ctx, entity.ScopeGlobal, w)
		if !ok {
			return
		}

		keyID := r.Header.Get(gcfg.KeyIDHeader)
		cred, err := h.Creds.ResolveByKeyID(ctx, keyID)
		if err != nil {
			// Unknown or revoked: equal-cost dummy HMAC, then one uniform 401.
			rechargewebhook.DummyVerify(h.Auth, keyID, r.Header.Get(gcfg.TimestampHeader), nil, r.Header.Get(gcfg.SignatureHeader))
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unauthenticated")
			return
		}
		telco := cred.TelcoID

		// Path telco is untrusted — cross-check against the credential (TEN-003).
		if pt := r.PathValue("telco"); pt != "" && pt != telco {
			h.auditPlatform(ctx, entity.AuditTenantContextMismatch, "credential:"+cred.KeyID, pt,
				"path telco does not match credential", r)
			writeErr(w, http.StatusForbidden, "TENANT_CONTEXT_MISMATCH", "tenant context mismatch")
			return
		}

		// Telco-scope config MUST exist: a fall-back to global (Scope != telco)
		// means no telco row => DENY (never inherit a global enable).
		tcv, err := h.Config.ActiveAt(ctx, domainRechargeFeed, "telco:"+telco, now)
		if err != nil || tcv.Scope != "telco:"+telco {
			h.auditPlatform(ctx, auditRechargeDenied, "telco:"+telco, telco, "no telco-scope feed config", r)
			writeErr(w, http.StatusForbidden, "RECHARGE_FEED_DISABLED", "feed not enabled")
			return
		}
		var tcfg feedCfg
		if err := json.Unmarshal(tcv.Content, &tcfg); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "feed unavailable")
			return
		}

		// KILL-SWITCH (before body/HMAC): enabled at BOTH scopes; the compiled
		// adapter must match the configured transport/auth (defence over the
		// write-time validator).
		if !gcfg.Enabled || !tcfg.Enabled {
			h.auditPlatform(ctx, auditRechargeDenied, "telco:"+telco, telco, "feed disabled", r)
			writeErr(w, http.StatusForbidden, "RECHARGE_FEED_DISABLED", "feed not enabled")
			return
		}
		if tcfg.Transport != "webhook_push" || tcfg.Auth != h.Auth.Scheme() {
			writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "feed transport/auth mismatch")
			return
		}

		// STRUCTURAL recon gate: no webhook money without reconciliation. Refuse
		// on !live OR on error (fail-closed).
		live, err := h.Recon.IsLayerLive(ctx, telco, repo.ReconLayerRecovery)
		if err != nil || !live {
			h.auditPlatform(ctx, auditRechargeDenied, "telco:"+telco, telco, "recovery recon layer not live", r)
			writeErr(w, http.StatusForbidden, "RECHARGE_RECON_NOT_LIVE", "recovery reconciliation is not live for this telco")
			return
		}

		// Secret from env (never stored). Absent => equal-cost dummy HMAC + 401.
		secret := os.Getenv(cred.SecretEnv)
		if secret == "" {
			rechargewebhook.DummyVerify(h.Auth, keyID, r.Header.Get(tcfg.TimestampHeader), nil, r.Header.Get(tcfg.SignatureHeader))
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unauthenticated")
			return
		}

		// Body cap BEFORE the body is read. Require a known, in-range Content-Length.
		if r.ContentLength < 0 || r.ContentLength > tcfg.MaxBodyBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, "RECHARGE_BODY_TOO_LARGE", "missing or oversized body")
			return
		}
		rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tcfg.MaxBodyBytes))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "RECHARGE_BODY_TOO_LARGE", "body too large")
			return
		}

		tsHeader := r.Header.Get(tcfg.TimestampHeader)
		sigHeader := r.Header.Get(tcfg.SignatureHeader)

		// Constant-time HMAC verify (uniform failure). Runs for every resolved
		// credential so the timing matches the dummy-HMAC failure paths.
		if err := rechargewebhook.Verify(h.Auth, []byte(secret), keyID, tsHeader, rawBody, sigHeader); err != nil {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unauthenticated")
			return
		}
		// The timestamp is inside the MAC (tamper-evident) but must still PARSE
		// (a legit sender's garbage timestamp is a sender bug, not authenticated).
		ts, err := h.Auth.ParseTimestamp(tsHeader)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unauthenticated")
			return
		}
		// Freshness, asymmetric: reject stale (older than the window) and
		// far-future (beyond the skew) — no symmetric abs() future-dating.
		delta := now.Sub(ts).Seconds()
		if delta > float64(tcfg.ReplayWindowSeconds) || delta < -float64(tcfg.FutureSkewSeconds) {
			writeErr(w, http.StatusUnauthorized, "RECHARGE_STALE", "timestamp outside the accepted window")
			return
		}

		decoded, err := h.Auth.DecodeSig(sigHeader) // already validated by Verify
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "AUTH_INVALID_CLIENT", "unauthenticated")
			return
		}

		v := &verifiedWebhook{decodedMAC: decoded, rawBody: rawBody, cfg: tcfg}
		ctx = platform.WithTenant(ctx, telco)
		ctx = context.WithValue(ctx, verifiedKey{}, v)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ingest maps + money-validates the verified event, applies the blast-radius
// clamps (HELD, never ingested), and otherwise feeds the recovery money-core.
func (h *RechargeWebhook) ingest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, _ := ctx.Value(verifiedKey{}).(*verifiedWebhook)
	if v == nil {
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}
	telco, err := platform.TenantFrom(ctx)
	if err != nil || telco == "" {
		writeErr(w, http.StatusInternalServerError, "SYSTEM_TEMPORARILY_UNAVAILABLE", "internal error")
		return
	}

	ev, err := h.Mapper.Map(v.rawBody)
	if err != nil {
		h.Log.Warn("recharge webhook: invalid event", "telco", telco, "err", err)
		writeErr(w, http.StatusUnprocessableEntity, "RECHARGE_INVALID_EVENT", "invalid recharge event")
		return
	}
	if ev.Currency != v.cfg.ExpectedCurrency {
		writeErr(w, http.StatusUnprocessableEntity, "RECHARGE_INVALID_EVENT", "unexpected currency")
		return
	}
	amount, err := entity.NewMoney(ev.AmountMinor, entity.Currency(ev.Currency))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "RECHARGE_INVALID_EVENT", "unsupported currency")
		return
	}
	src := "wh:" + ev.EventID

	// Nonce (defence-in-depth) + blast-radius clamps, in one tenant transaction.
	// A seen nonce is idempotent success — it must NOT reject a legit byte-for-
	// byte retry; recovery idempotency by source_event_id is the durable backstop.
	var heldReason string
	err = repo.WithTenantTx(ctx, h.Pool, func(tx pgx.Tx) error {
		_, stored, e := (repo.Idempotency{}).PutIfAbsent(ctx, tx, entity.IdempotencyRecord{
			TelcoID: telco, Operation: opRechargeWebhook,
			IdemKey: "wh:" + hex.EncodeToString(v.decodedMAC), RequestHash: src,
			// The store's response_body is NOT NULL; this row is used purely as a
			// seen-MAC nonce (never read back, recovery idempotency is the durable
			// backstop), so a constant placeholder body is fine.
			ResponseBody: []byte("{}"),
		})
		if e != nil {
			return e
		}
		if !stored {
			h.Log.Warn("recharge webhook: replay observed (idempotent)", "telco", telco, "src", src)
		}
		if ev.AmountMinor > v.cfg.PerEventAmountMaxMinor {
			heldReason = repo.HeldReasonPerEventClamp
		} else {
			daily, e := (repo.HeldRecharge{}).DailyIngestedMinor(ctx, tx, telco)
			if e != nil {
				return e
			}
			if daily+ev.AmountMinor > v.cfg.PerTelcoDailyCeilingMinor {
				heldReason = repo.HeldReasonDailyCeiling
			}
		}
		if heldReason != "" {
			_, e = (repo.HeldRecharge{}).Hold(ctx, tx, repo.HeldEvent{
				TelcoID: telco, SourceEventID: src, MSISDNToken: ev.MSISDNToken,
				AmountMinor: ev.AmountMinor, Currency: ev.Currency, OccurredAt: ev.OccurredAt, Reason: heldReason,
			})
			return e
		}
		return nil
	})
	if err != nil {
		h.Log.Error("recharge webhook: nonce/clamp tx failed", "telco", telco, "src", src, "err", err)
		writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "temporary error")
		return
	}
	if heldReason != "" {
		h.Log.Error("RECHARGE_HELD over blast-radius clamp — parked for maker-checker release",
			"telco", telco, "src", src, "reason", heldReason, "amount_minor", ev.AmountMinor)
		h.auditPlatform(ctx, auditRechargeHeld, "telco:"+telco, telco, heldReason, r)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "HELD", "reason": heldReason})
		return
	}

	out, err := h.Recovery.Ingest(ctx, recovery.IngestCmd{
		SourceEventID: src, MSISDNToken: ev.MSISDNToken, Amount: amount,
		OccurredAt: ev.OccurredAt, CorrelationID: platform.CorrelationFrom(ctx),
	})
	if err != nil {
		if errors.Is(err, recovery.ErrDivergentRecovery) {
			writeErr(w, http.StatusConflict, "DIVERGENT_DUPLICATE", "source event id reused with a different payload")
			return
		}
		h.Log.Error("recharge webhook: ingest failed", "telco", telco, "src", src, "err", err)
		writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "temporary error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recovery_event_id": out.RecoveryEventID, "state": string(out.State),
		"advance_closed": out.AdvanceClosed, "replayed": out.Replayed,
	})
}

// readFeed loads and parses a telco.recharge_feed config at an exact scope.
func (h *RechargeWebhook) readFeed(ctx context.Context, scope string, w http.ResponseWriter) (feedCfg, bool) {
	cv, err := h.Config.ActiveAt(ctx, domainRechargeFeed, scope, time.Now().UTC())
	if err != nil {
		h.Log.Error("recharge webhook: feed config missing", "scope", scope, "err", err)
		writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "feed unavailable")
		return feedCfg{}, false
	}
	var c feedCfg
	if err := json.Unmarshal(cv.Content, &c); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "SYSTEM_TEMPORARILY_UNAVAILABLE", "feed unavailable")
		return feedCfg{}, false
	}
	return c, true
}

func (h *RechargeWebhook) auditPlatform(ctx context.Context, action, actor, targetID, reason string, r *http.Request) {
	_ = h.Audit.InsertPlatform(ctx, h.Pool, entity.AuditEvent{
		ID:         platform.NewID("aud"),
		Actor:      actor,
		Action:     action,
		TargetType: "recharge_webhook",
		TargetID:   targetID,
		Reason:     reason,
		SourceIP:   r.RemoteAddr,
	})
}
