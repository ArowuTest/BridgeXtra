// Package origination is the advance origination saga (V2 §13, BUILD_PLAN
// M1b-3): offer retrieval from governed config, idempotent confirmation, FSM
// with optimistic locking, atomic funding reservation, fulfilment OUTSIDE any
// transaction (V2-ADV-006), and ledger posting only on confirmed fulfilment
// (V2-LED-006).
//
// Transaction shape — the no-txn-across-network-call rule, structurally:
//
//	tx1: accept offer -> create advance -> reserve funding -> record attempt
//	     -> PENDING_FULFILMENT                                    [commit]
//	    ---- network: adapter.SubmitFulfilment (NO transaction) ----
//	tx2: resolve outcome -> ACTIVE+journal / FAILED+release / UNKNOWN+enquiry
//	     schedule                                                 [commit]
//
// A crash between tx1 and tx2 leaves a SENT attempt on a PENDING_FULFILMENT
// advance — the resolver worker (M1b-4) treats stale SENT as UNKNOWN and
// resolves via status enquiry (EDG-007: recover exactly once, never re-lend).
package origination

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/ledger"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/mno"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/economics"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/feepolicy"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/treasury"
)

// Typed errors (BC-7) mapped once at the HTTP boundary.
var (
	ErrOfferNotFound        = errors.New("origination: offer not found")
	ErrOfferExpired         = errors.New("origination: offer expired") // EDG-011
	ErrOfferNotAcceptable   = errors.New("origination: offer no longer acceptable")
	ErrProgrammeMismatch    = errors.New("origination: offer programme does not match the confirm request") // BX-P0-001
	ErrSubscriberIneligible = errors.New("origination: subscriber not eligible")                            // barred/self-excluded/closed
	// Build 1 — programme economic/legal go-live gate. A programme may be ACTIVE
	// but cannot originate until its economics are configured (fail-closed).
	ErrProgrammeEconomicsNotSet  = errors.New("origination: programme economics not configured — cannot originate")
	ErrProgrammeEconomicsInvalid = errors.New("origination: programme economics config invalid — cannot originate")
	// ErrRecoveryUnconfirmedHold (S3-C2): the subscriber has a debt CLOSED by a
	// webhook recovery the EOD recon has not yet confirmed — re-origination is held
	// so a phantom close cannot free the advance slot before recon catches it.
	ErrRecoveryUnconfirmedHold = errors.New("origination: subscriber has an unconfirmed recovery hold")
	// Self-exclusion (R1-MUST) errors.
	ErrSelfExclusionChannelNotAllowed = errors.New("origination: self-exclusion channel not permitted")
	ErrNotSelfExcluded                = errors.New("origination: subscriber has no active self-exclusion")
	ErrCoolOffNotElapsed              = errors.New("origination: self-exclusion cool-off has not elapsed")
	// ErrOperatorReinstatementRequired: policy requires an operator to lift a
	// self-exclusion (require_operator_reinstatement=true) — self-service is
	// refused (fail-closed until the operator maker-checker path exists).
	ErrOperatorReinstatementRequired = errors.New("origination: self-exclusion reinstatement requires operator approval")
	// ErrDecisionUnavailable (M2e): the credit decision is expired, ineligible
	// or absent — customer-safe NO_OFFER, never a stale lend (EDG-014).
	ErrDecisionUnavailable = errors.New("origination: no valid credit decision")
	// ErrOverlayBlocked (M2e, V2-SCR-015): a real-time risk overlay blocks the
	// subscriber at this moment. Which flag fired is logged, never disclosed.
	ErrOverlayBlocked = errors.New("origination: risk overlay blocks this action")
	// ErrDivergentDuplicate (R-P0-1, API-002/003): a confirm reused an
	// idempotency key with a DIFFERENT request payload. An idempotent retry
	// must be the SAME request; a different body under the same key is a
	// client/security error, never a silent replay of the original advance.
	ErrDivergentDuplicate = errors.New("origination: idempotency key reused with a divergent request")

	// R-P0-7 consent/channel disclosure-evidence errors.
	ErrDisclosureUnavailable     = errors.New("origination: no active disclosure policy") // fail-closed: cannot disclose -> cannot offer
	ErrDisclosureRequired        = errors.New("origination: disclosure reference is required at confirm")
	ErrDisclosureMismatch        = errors.New("origination: disclosure reference does not match the offer presented")
	ErrDisclosureExpired         = errors.New("origination: acceptance falls outside the disclosure validity window")
	ErrChannelNotAllowed         = errors.New("origination: channel is not permitted for this disclosure policy")
	ErrAcceptanceEvidenceMissing = errors.New("origination: channel/session acceptance evidence is required at confirm")
)

// acceptanceSkew tolerates modest clock skew between the telco channel (which
// stamps accepted_at) and us when checking that acceptance happened inside the
// disclosure's validity window.
const acceptanceSkew = 2 * time.Minute

// confirmRequestHash is the canonical equivalence fingerprint of a confirm command
// (R-P0-1, BX-MED-001). It hashes every field that DEFINES the command AND its
// legal/consent evidence — programme, offer, token, disclosure_ref, channel, session_id,
// accepted_at and telco_evidence — so a retry that changed ANY of them (materially different
// acceptance evidence under the same idempotency key) is caught as a divergent duplicate
// rather than silently treated as the same command. Only per-attempt TRACING is excluded:
// correlation_id (a legitimate retry may carry a fresh one) and the idem_key itself (the key,
// not the body). Time and JSON evidence are NORMALISED so a byte-different-but-equivalent
// retry does not falsely diverge: accepted_at to whole UTC seconds, telco_evidence to
// canonical JSON (sorted keys, number-precision-preserving, whitespace-independent).
func confirmRequestHash(cmd ConfirmCmd) string {
	// Struct field order is fixed, so json.Marshal is deterministic.
	b, _ := json.Marshal(struct {
		Programme     string `json:"programme_id"`
		Offer         string `json:"offer_id"`
		Token         string `json:"msisdn_token"`
		DisclosureRef string `json:"disclosure_ref"`
		Channel       string `json:"channel"`
		SessionID     string `json:"session_id"`
		AcceptedAt    int64  `json:"accepted_at_unix"`
		TelcoEvidence string `json:"telco_evidence"`
	}{
		cmd.ProgrammeID, cmd.OfferID, cmd.MSISDNToken,
		cmd.DisclosureRef, cmd.Channel, cmd.SessionID,
		normalizeAcceptedAt(cmd.AcceptedAt),
		canonicalJSON(cmd.TelcoEvidence),
	})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// normalizeAcceptedAt reduces accepted_at to whole UTC seconds: sub-second precision, or a
// timezone-offset representation of the SAME instant, must not make a legitimate retry diverge.
// A zero time hashes to 0.
func normalizeAcceptedAt(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Truncate(time.Second).Unix()
}

// canonicalJSON re-marshals arbitrary JSON evidence to a canonical form (sorted object keys,
// no insignificant whitespace, original number precision via UseNumber) so an equivalent retry
// whose evidence differs only in key order or formatting does not diverge. Empty stays empty;
// bytes that are not valid JSON are hashed verbatim rather than dropped.
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v) // json.Marshal sorts map keys; json.Number keeps the exact literal
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// confirmResponseDTO is the exact confirm response snapshot persisted onto the idempotency
// record after the outcome resolves (BX-MED-002), so a later replay returns THIS — the response
// the original confirm produced — rather than the advance's since-progressed current state
// (e.g. UNKNOWN later resolved to ACTIVE, or outstanding reduced by recovery). It holds exactly
// the fields the channel confirm response renders.
type confirmResponseDTO struct {
	AdvanceID string `json:"advance_id"`
	State     string `json:"state"`
	FaceMinor int64  `json:"face_minor"`
	FaceCur   string `json:"face_cur"`
	DisbMinor int64  `json:"disb_minor"`
	DisbCur   string `json:"disb_cur"`
	OutMinor  int64  `json:"out_minor"`
	OutCur    string `json:"out_cur"`
}

func encodeConfirmResponse(a entity.Advance) ([]byte, error) {
	d := confirmResponseDTO{AdvanceID: a.AdvanceID, State: string(a.State)}
	if a.FaceValue.IsSet() {
		d.FaceMinor, d.FaceCur = a.FaceValue.Amount(), string(a.FaceValue.Currency())
	}
	if a.Disbursed.IsSet() {
		d.DisbMinor, d.DisbCur = a.Disbursed.Amount(), string(a.Disbursed.Currency())
	}
	if a.Outstanding.IsSet() {
		d.OutMinor, d.OutCur = a.Outstanding.Amount(), string(a.Outstanding.Currency())
	}
	return json.Marshal(d)
}

func decodeConfirmResponse(b []byte) (entity.Advance, bool) {
	var d confirmResponseDTO
	if err := json.Unmarshal(b, &d); err != nil || d.AdvanceID == "" {
		return entity.Advance{}, false // placeholder / not-yet-finalised body
	}
	a := entity.Advance{AdvanceID: d.AdvanceID, State: entity.AdvanceState(d.State)}
	if d.FaceCur != "" {
		a.FaceValue = entity.MustMoney(d.FaceMinor, entity.Currency(d.FaceCur))
	}
	if d.DisbCur != "" {
		a.Disbursed = entity.MustMoney(d.DisbMinor, entity.Currency(d.DisbCur))
	}
	if d.OutCur != "" {
		a.Outstanding = entity.MustMoney(d.OutMinor, entity.Currency(d.OutCur))
	}
	return a, true
}

// replayConfirmAdvance returns the advance to hand back on a confirm REPLAY (BX-MED-002): the
// EXACT persisted original response when it was finalised (ResponseStatus 200), else the current
// advance as a fallback for a duplicate racing the winner's tx2/SetResponse window.
func (s *Service) replayConfirmAdvance(ctx context.Context, tx pgx.Tx, telcoID, idemKey string) (entity.Advance, error) {
	if rec, err := s.idem.Get(ctx, tx, telcoID, "advance.confirm", idemKey); err == nil && rec.ResponseStatus == 200 {
		if a, ok := decodeConfirmResponse(rec.ResponseBody); ok {
			return a, nil
		}
	}
	return s.advances.GetByIdemKey(ctx, tx, idemKey)
}

type Service struct {
	Pool    *pgxpool.Pool // tcp_app
	Config  *configsvc.Service
	Ledger  *ledger.Service
	Adapter mno.Client
	Log     *slog.Logger
	// treasury guards the confirm path (M3d). UNEXPORTED and set only by New
	// (M3D-F1): no struct-literal construction can produce a Service with a
	// disarmed guardrail, and there is no nil-skip anywhere — an absent
	// guardrail FAILS, it never silently waves money through (BC-5
	// arm-or-refuse; reachability invariant).
	treasury *treasury.Service

	subscribers    repo.Subscribers
	selfExclusions repo.SelfExclusions
	decisions      repo.Decisions
	offers         repo.Offers
	disclosures    repo.DisclosureSnapshots
	pools          repo.FundingPools
	advances       repo.Advances
	attempts       repo.Attempts
	outbox         repo.Outbox
	flags          repo.SubscriberFlags
	consents       repo.Consents
	programmes     repo.Programmes
	idem           repo.Idempotency
	audit          repo.Audit
}

func New(pool *pgxpool.Pool, cfg *configsvc.Service, led *ledger.Service, adapter mno.Client, log *slog.Logger) *Service {
	return &Service{Pool: pool, Config: cfg, Ledger: led, Adapter: adapter, Log: log,
		treasury: treasury.New(pool, cfg, log)}
}

type productCfg struct {
	Currency           entity.Currency `json:"currency"`
	DenominationsMinor []int64         `json:"denominations_minor"`
	FeeBps             int64           `json:"fee_bps"`
	FeeModel           entity.FeeModel `json:"fee_model"`
	OfferExpiryMinutes int             `json:"offer_expiry_minutes"`
}

type fulfilmentCfg struct {
	StatusEnquiryDelaysSeconds []int `json:"status_enquiry_delays_seconds"`
	UnknownEscalationMinutes   int   `json:"unknown_escalation_minutes"`
}

type overlaysCfg struct {
	BlockingFlags       []string `json:"blocking_flags"`
	SimSwapCooloffHours int      `json:"sim_swap_cooloff_hours"`
	CheckAt             []string `json:"check_at"`
}

// disclosureCfg is the governed disclosure.policy (R-P0-7). The disclosure a
// customer sees is config, not code — de-hardcoding the previously literal
// Channel:"USSD". The validator guarantees the templates disclose the repayment
// total and that allowed_channels/supported_locales are non-empty.
type disclosureCfg struct {
	TemplateID        string   `json:"template_id"`
	TemplateVersion   string   `json:"template_version"`
	DefaultLocale     string   `json:"default_locale"`
	SupportedLocales  []string `json:"supported_locales"`
	AllowedChannels   []string `json:"allowed_channels"`
	BodyTemplate      string   `json:"body_template"`
	TotalCostTemplate string   `json:"total_cost_template"`
}

func (d disclosureCfg) channelAllowed(ch string) bool {
	for _, c := range d.AllowedChannels {
		if c == ch {
			return true
		}
	}
	return false
}

// OfferView pairs an offer with the exact disclosure snapshot presented for it
// (R-P0-7). The channel renders Disclosure.RenderedBody and echoes
// Disclosure.DisclosureSnapshotID back at confirm as proof of what was shown.
type OfferView struct {
	Offer      entity.Offer
	Disclosure entity.DisclosureSnapshot
}

// renderTemplate substitutes the money placeholders in a disclosure template.
// Deterministic — the same offer always renders byte-identical text, so the
// content hash is stable and the snapshot is reproducible.
func renderTemplate(tmpl string, o entity.Offer) string {
	return strings.NewReplacer(
		"{{face}}", o.FaceValue.String(),
		"{{fee}}", o.Fee.String(),
		"{{disbursed}}", o.Disbursed.String(),
		"{{repayment}}", o.Repayment.String(),
		"{{currency}}", string(o.FaceValue.Currency()),
	).Replace(tmpl)
}

// buildDisclosureSnapshot renders and hashes the disclosure for one offer. The
// content hash covers the identifying + money + rendered fields, so any later
// tampering with the stored row is detectable (mirrors decision_snapshots).
func buildDisclosureSnapshot(dc disclosureCfg, cfgVersionID, locale string, o entity.Offer, now time.Time) entity.DisclosureSnapshot {
	body := renderTemplate(dc.BodyTemplate, o)
	totalCost := renderTemplate(dc.TotalCostTemplate, o)
	canonical, _ := json.Marshal(struct {
		Template  string `json:"template_id"`
		Version   string `json:"template_version"`
		Locale    string `json:"locale"`
		Currency  string `json:"currency"`
		Face      int64  `json:"face_value_minor"`
		Fee       int64  `json:"fee_minor"`
		Disbursed int64  `json:"disbursed_minor"`
		Repayment int64  `json:"repayment_minor"`
		Body      string `json:"rendered_body"`
		TotalCost string `json:"total_cost_text"`
		OfferID   string `json:"offer_id"`
	}{dc.TemplateID, dc.TemplateVersion, locale, string(o.FaceValue.Currency()),
		o.FaceValue.Amount(), o.Fee.Amount(), o.Disbursed.Amount(), o.Repayment.Amount(),
		body, totalCost, o.OfferID})
	sum := sha256.Sum256(canonical)
	return entity.DisclosureSnapshot{
		DisclosureSnapshotID:      platform.NewID("dsc"),
		TelcoID:                   o.TelcoID,
		ProgrammeID:               o.ProgrammeID,
		OfferID:                   o.OfferID,
		TemplateID:                dc.TemplateID,
		TemplateVersion:           dc.TemplateVersion,
		Locale:                    locale,
		DisclosureConfigVersionID: cfgVersionID,
		Currency:                  o.FaceValue.Currency(),
		FaceValue:                 o.FaceValue,
		Fee:                       o.Fee,
		Disbursed:                 o.Disbursed,
		Repayment:                 o.Repayment,
		RenderedBody:              body,
		TotalCostText:             totalCost,
		ContentHash:               hex.EncodeToString(sum[:]),
		IssuedAt:                  now,
		ExpiresAt:                 o.ExpiresAt, // short-lived: tied to offer validity
	}
}

// loadDisclosureCfg reads the ACTIVE disclosure.policy for a programme. Absent
// or unparseable = fail-closed (ErrDisclosureUnavailable): a programme that
// cannot disclose its terms must not serve or confirm an advance.
func (s *Service) loadDisclosureCfg(ctx context.Context, programmeID string, now time.Time) (disclosureCfg, string, error) {
	cfgV, err := s.Config.ActiveAt(ctx, "disclosure.policy", "programme:"+programmeID, now)
	if err != nil {
		return disclosureCfg{}, "", fmt.Errorf("%w: %v", ErrDisclosureUnavailable, err)
	}
	var dc disclosureCfg
	if err := json.Unmarshal(cfgV.Content, &dc); err != nil {
		return disclosureCfg{}, "", fmt.Errorf("%w: parse: %v", ErrDisclosureUnavailable, err)
	}
	return dc, cfgV.ConfigVersionID, nil
}

// checkOverlays applies the real-time risk overlays (V2-SCR-015) at the given
// checkpoint (OFFER or CONFIRM). Config-driven; the validator guarantees
// CONFIRM can never be configured off. SIM_SWAP blocks only inside its
// cool-off window; every other blocking flag blocks while open.
func (s *Service) checkOverlays(ctx context.Context, tx pgx.Tx, telcoID, subscriberAccountID, checkpoint string, now time.Time) error {
	cv, err := s.Config.ActiveAt(ctx, "overlays.policy", "telco:"+telcoID, now)
	if err != nil {
		return fmt.Errorf("overlays.policy config: %w", err)
	}
	var oc overlaysCfg
	if err := json.Unmarshal(cv.Content, &oc); err != nil {
		return err
	}
	applies := false
	for _, c := range oc.CheckAt {
		if c == checkpoint {
			applies = true
		}
	}
	if !applies {
		return nil
	}
	blocking := map[string]bool{}
	for _, f := range oc.BlockingFlags {
		blocking[f] = true
	}
	open, err := s.flags.ListOpen(ctx, tx, subscriberAccountID)
	if err != nil {
		return err
	}
	for _, f := range open {
		if !blocking[f.Flag] {
			continue
		}
		if f.Flag == "SIM_SWAP" &&
			now.After(f.EffectiveFrom.Add(time.Duration(oc.SimSwapCooloffHours)*time.Hour)) {
			continue // cool-off elapsed: a settled SIM swap no longer blocks
		}
		s.Log.Warn("overlay blocked", "subscriber", subscriberAccountID,
			"flag", f.Flag, "checkpoint", checkpoint)
		return fmt.Errorf("%w (%s)", ErrOverlayBlocked, checkpoint)
	}
	return nil
}

// requireValidDecision enforces decision validity at the lending boundary
// (EDG-014 / V2-SCR-015): a scored decision past valid_until or ineligible
// never serves an offer; seeds (no expiry) pass — they exist only in
// pre-scoring environments.
func requireValidDecision(dec entity.DecisionSnapshot, now time.Time) error {
	if dec.ValidUntil != nil && !dec.ValidUntil.After(now) {
		return fmt.Errorf("%w: decision expired %s", ErrDecisionUnavailable, dec.ValidUntil.UTC().Format(time.RFC3339))
	}
	if !dec.MaxFaceValue.IsPositive() {
		return fmt.Errorf("%w: not eligible", ErrDecisionUnavailable)
	}
	return nil
}

// GetOffers returns the subscriber's valid offers, generating the ladder from
// the governed product config when none exist (V2-OFR-009 reuse). Every value
// on an offer derives from config + the pinned decision — nothing hardcoded.
// resolveEconomics reads the programme's economic/legal identity fail-closed
// (Build 1). A programme with no programme.economics config — or a malformed one —
// cannot originate. Mirrors feepolicy.Resolve's single-fail-closed-read discipline
// but adds a scope-exactness guard: because configsvc.ActiveAt falls back
// scope->global, a stray GLOBAL economics row would otherwise authorise EVERY
// programme to lend. Economics are per-programme by definition, so only a
// programme:<id>-scoped config counts; anything else is treated as "not set". The
// content is re-validated here (floor lives in code too) so a raw-seeded config that
// bypassed the maker-checker validator is still refused.
func (s *Service) resolveEconomics(ctx context.Context, programmeID string) (economics.Terms, string, error) {
	cv, err := s.Config.ActiveAt(ctx, "programme.economics", "programme:"+programmeID, time.Now().UTC())
	if err != nil {
		return economics.Terms{}, "", fmt.Errorf("%w (programme %s)", ErrProgrammeEconomicsNotSet, programmeID)
	}
	if cv.Scope != "programme:"+programmeID {
		return economics.Terms{}, "", fmt.Errorf("%w (programme %s: only a programme-scoped economics config authorises lending, got scope %q)", ErrProgrammeEconomicsNotSet, programmeID, cv.Scope)
	}
	terms, err := economics.Parse(cv.Content)
	if err != nil {
		return economics.Terms{}, "", fmt.Errorf("%w (programme %s): %v", ErrProgrammeEconomicsInvalid, programmeID, err)
	}
	return terms, cv.ConfigVersionID, nil
}

// requireNINVerified reads origination.nin_gate (global) FAIL-CLOSED (Build 2): an
// absent OR malformed config means REQUIRE — absent must never mean "off". Only a
// config that is present and explicitly sets require_nin_verified=false disables the
// gate (a governed maker-checker opt-out). Never returns an error: any read problem
// resolves to "require" (the safe direction).
func (s *Service) requireNINVerified(ctx context.Context) bool {
	cv, err := s.Config.ActiveAt(ctx, "origination.nin_gate", entity.ScopeGlobal, time.Now().UTC())
	if err != nil {
		return true
	}
	var v struct {
		RequireNINVerified *bool `json:"require_nin_verified"`
	}
	if err := json.Unmarshal(cv.Content, &v); err != nil || v.RequireNINVerified == nil {
		return true
	}
	return *v.RequireNINVerified
}

// assertNINVerified is the Build 2 fail-closed eligibility check: when the gate is
// required, a subscriber whose nin_verified flag is not TRUE — NULL (unknown, MTN
// has not sent it) or false — cannot borrow. Reuses ErrSubscriberIneligible so the
// refusal is non-revealing at the channel (the specific reason stays in logs).
func assertNINVerified(sub entity.SubscriberAccount, require bool) error {
	if require && (sub.NINVerified == nil || !*sub.NINVerified) {
		return fmt.Errorf("%w: NIN not verified", ErrSubscriberIneligible)
	}
	return nil
}

func (s *Service) GetOffers(ctx context.Context, programmeID, msisdnToken string) ([]OfferView, error) {
	now := time.Now().UTC()
	cfgV, err := s.Config.ActiveAt(ctx, "product.airtime", "programme:"+programmeID, now)
	if err != nil {
		return nil, fmt.Errorf("product config: %w", err)
	}
	var pc productCfg
	if err := json.Unmarshal(cfgV.Content, &pc); err != nil {
		return nil, fmt.Errorf("product config parse: %w", err)
	}
	// R-P0-7: the disclosure policy is required to serve an offer — a programme
	// that cannot disclose its terms must not present one (fail-closed).
	dc, dcVer, err := s.loadDisclosureCfg(ctx, programmeID, now)
	if err != nil {
		return nil, err
	}
	locale := dc.DefaultLocale

	var out []OfferView
	err = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// M3d: a suspended programme serves NOTHING (guardrail tripped or
		// operator action) — fail closed at the first touch.
		if status, err := s.programmes.GetStatus(ctx, tx, programmeID); err != nil {
			return err
		} else if status != entity.ProgrammeActive {
			return fmt.Errorf("%w (programme %s)", treasury.ErrProgrammeSuspended, status)
		}
		// Build 1 go-live gate: an ACTIVE programme still cannot lend until its
		// economic/legal identity is configured (fail-closed, mirrors the
		// recharge-feed "row required -> deny"). Serving an offer commits us to a
		// lend we could then complete, so the gate sits at GetOffers too.
		if _, _, err := s.resolveEconomics(ctx, programmeID); err != nil {
			return err
		}
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, msisdnToken)
		if err != nil {
			return err
		}
		if sub.Status != "ACTIVE" {
			return fmt.Errorf("%w: status %s", ErrSubscriberIneligible, sub.Status)
		}
		// Build 2 NIN eligibility gate: a subscriber whose identity MTN has not
		// verified cannot borrow (fail-closed — NULL/unknown or false => no loan).
		if err := assertNINVerified(sub, s.requireNINVerified(ctx)); err != nil {
			return err
		}
		// Register-authoritative self-exclusion check (R1-MUST): refuse on an ACTIVE
		// self-exclusion directly, so the control never depends on the status mirror
		// being in sync (safety-floor discipline).
		if err := s.assertNotSelfExcluded(ctx, tx, sub.SubscriberAccountID); err != nil {
			return err
		}
		if err := s.checkOverlays(ctx, tx, sub.TelcoID, sub.SubscriberAccountID, "OFFER", now); err != nil {
			return err
		}
		// VR-7a: serialize ladder generation per (subscriber, programme) so
		// concurrent first-time enquiries cannot mint duplicate ladders
		// (double USSD menu entries). The second enquirer waits here, then
		// sees the winner's offers in ListValid below.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || '/' || $2))`,
			sub.SubscriberAccountID, programmeID); err != nil {
			return err
		}
		existing, err := s.offers.ListValid(ctx, tx, sub.SubscriberAccountID, programmeID, now)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			// Re-serve: the disclosure snapshot minted with each offer is the
			// canonical record; load it so the channel re-renders exactly what
			// was first presented (never a fresh reconstruction).
			for _, o := range existing {
				snap, err := s.disclosures.GetByOffer(ctx, tx, o.OfferID)
				if err != nil {
					return err
				}
				out = append(out, OfferView{Offer: o, Disclosure: snap})
			}
			return nil
		}
		dec, err := s.decisions.GetCurrent(ctx, tx, sub.SubscriberAccountID)
		if errors.Is(err, repo.ErrNotFound) {
			return fmt.Errorf("%w: no decision on file", ErrDecisionUnavailable)
		}
		if err != nil {
			return err
		}
		if err := requireValidDecision(dec, now); err != nil {
			return err
		}
		built, err := buildLadder(sub, dec, programmeID, cfgV.ConfigVersionID, pc, now)
		if err != nil {
			return err
		}
		for _, o := range built {
			if err := s.offers.Insert(ctx, tx, o); err != nil {
				return err
			}
			// R-P0-7: mint the disclosure snapshot in the SAME tx as the offer,
			// so an offer never exists without the exact terms presented for it.
			snap := buildDisclosureSnapshot(dc, dcVer, locale, o, now)
			if err := s.disclosures.Insert(ctx, tx, snap); err != nil {
				return err
			}
			out = append(out, OfferView{Offer: o, Disclosure: snap})
		}
		return nil
	})
	return out, err
}

// buildLadder computes the offer set: every config denomination within the
// decision's max, priced per the config fee model — all Money arithmetic,
// PercentBps as the single rounding site (ADR-0002).
func buildLadder(sub entity.SubscriberAccount, dec entity.DecisionSnapshot, programmeID, productCfgVersion string, pc productCfg, now time.Time) ([]entity.Offer, error) {
	expiry := now.Add(time.Duration(pc.OfferExpiryMinutes) * time.Minute)
	var out []entity.Offer
	for _, denom := range pc.DenominationsMinor {
		face, err := entity.NewMoney(denom, pc.Currency)
		if err != nil {
			return nil, err
		}
		if cmp, err := face.Cmp(dec.MaxFaceValue); err != nil {
			return nil, err
		} else if cmp > 0 {
			continue // above the subscriber's limit
		}
		fee, err := face.PercentBps(pc.FeeBps)
		if err != nil {
			return nil, err
		}
		var disbursed, repayment entity.Money
		switch pc.FeeModel {
		case entity.FeeDeductedUpfront:
			if disbursed, err = face.Sub(fee); err != nil {
				return nil, err
			}
			repayment = face
		case entity.FeeAddedToRepayment:
			disbursed = face
			if repayment, err = face.Add(fee); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported fee model %q", pc.FeeModel)
		}
		if !disbursed.IsPositive() {
			// fee consumes the whole denomination — unofferable, skip loudly
			// in caller logs rather than mint a zero-value credit.
			continue
		}
		out = append(out, entity.Offer{
			OfferID:                platform.NewID("off"),
			TelcoID:                sub.TelcoID,
			ProgrammeID:            programmeID,
			SubscriberAccountID:    sub.SubscriberAccountID,
			DecisionSnapshotID:     dec.DecisionSnapshotID,
			FaceValue:              face,
			Fee:                    fee,
			Disbursed:              disbursed,
			Repayment:              repayment,
			FeeModel:               pc.FeeModel,
			ProductConfigVersionID: productCfgVersion,
			State:                  entity.OfferGenerated,
			ExpiresAt:              expiry,
			CreatedAt:              now,
		})
	}
	return out, nil
}

// ConfirmCmd is one customer confirmation (channel-idempotent via IdemKey).
// R-P0-7: DisclosureRef echoes the disclosure snapshot the customer was shown,
// and Channel/SessionID/AcceptedAt are the acceptance evidence (channel + the
// telco-supplied session and acceptance timestamp — DD-06). TelcoEvidence is
// the optional telco acceptance signature.
type ConfirmCmd struct {
	ProgrammeID   string
	OfferID       string
	MSISDNToken   string
	IdemKey       string
	CorrelationID string

	DisclosureRef string
	Channel       string
	SessionID     string
	AcceptedAt    time.Time
	TelcoEvidence json.RawMessage
}

// ConfirmResult reports the (possibly replayed) advance.
type ConfirmResult struct {
	Advance  entity.Advance
	Replayed bool
}

// Confirm executes the origination saga.
func (s *Service) Confirm(ctx context.Context, cmd ConfirmCmd) (ConfirmResult, error) {
	if cmd.IdemKey == "" || cmd.CorrelationID == "" {
		return ConfirmResult{}, fmt.Errorf("idempotency key and correlation id are required")
	}
	// R-P0-7: consent/channel disclosure evidence is mandatory. Cheap presence
	// checks fail fast, before any DB work, so a malformed confirm never claims
	// an idempotency slot. A confirm without proof of what was disclosed and
	// that it was accepted through a real channel session is refused.
	if cmd.DisclosureRef == "" {
		return ConfirmResult{}, ErrDisclosureRequired
	}
	if cmd.Channel == "" || cmd.SessionID == "" || cmd.AcceptedAt.IsZero() {
		return ConfirmResult{}, ErrAcceptanceEvidenceMissing
	}

	telcoID, err := platform.TenantFrom(ctx)
	if err != nil {
		return ConfirmResult{}, err
	}
	reqHash := confirmRequestHash(cmd)

	// The channel must be one the programme's disclosure policy permits — this
	// de-hardcodes the previously literal "USSD" and is governed config.
	now0 := time.Now().UTC()
	dc, _, err := s.loadDisclosureCfg(ctx, cmd.ProgrammeID, now0)
	if err != nil {
		return ConfirmResult{}, err
	}
	if !dc.channelAllowed(cmd.Channel) {
		return ConfirmResult{}, fmt.Errorf("%w: %q", ErrChannelNotAllowed, cmd.Channel)
	}

	// ---- tx1: accept + reserve + record attempt ---------------------------
	var adv entity.Advance
	var attempt entity.FulfilmentAttempt
	var offer entity.Offer
	replayed := false
	divergent := false
	err = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// R-P0-1: claim the idempotency record FIRST, atomically, in this same
		// durable tx. The DB PK (telco, operation, idem_key) arbitrates — not
		// application memory. A duplicate is only a valid replay when its
		// request hash MATCHES; a divergent body is refused (API-002/003).
		// Claim and advance commit together, so a concurrent duplicate blocks
		// here until we commit, then reads the record and replays the advance.
		rec, stored, err := s.idem.PutIfAbsent(ctx, tx, entity.IdempotencyRecord{
			TelcoID: telcoID, Operation: "advance.confirm", IdemKey: cmd.IdemKey,
			RequestHash: reqHash, ResponseStatus: 0, ResponseBody: []byte(`{"kind":"advance.confirm"}`),
		})
		if err != nil {
			return err
		}
		if !stored {
			if rec.RequestHash != reqHash {
				// Same key, different request — the money-command-integrity
				// violation. Abort loudly; the audit is written out-of-band.
				divergent = true
				return ErrDivergentDuplicate
			}
			// Genuine replay: return the EXACT original confirm response (BX-MED-002), not the
			// advance's since-progressed current state. The winner's response is persisted after
			// its outcome resolves; until then this falls back to the current advance.
			existing, err := s.replayConfirmAdvance(ctx, tx, telcoID, cmd.IdemKey)
			if err != nil {
				return err
			}
			adv, replayed = existing, true
			return nil
		}

		// M3d: suspended programme = lending stopped, fail closed.
		if status, err := s.programmes.GetStatus(ctx, tx, cmd.ProgrammeID); err != nil {
			return err
		} else if status != entity.ProgrammeActive {
			return fmt.Errorf("%w (programme %s)", treasury.ErrProgrammeSuspended, status)
		}
		// Build 1 go-live gate: refuse to book a loan on a programme whose economic/
		// legal identity is not configured (fail-closed). Resolved here on the fresh
		// path only (a replay returned above); the terms are recorded on the consent
		// (origination audit trail) below.
		econTerms, econVer, err := s.resolveEconomics(ctx, cmd.ProgrammeID)
		if err != nil {
			return err
		}
		sub, err := s.subscribers.GetLiveByToken(ctx, tx, cmd.MSISDNToken)
		if err != nil {
			return err
		}
		if sub.Status != "ACTIVE" {
			return fmt.Errorf("%w: status %s", ErrSubscriberIneligible, sub.Status)
		}
		// Build 2 NIN eligibility gate (fail-closed): an unverified identity cannot
		// borrow — enforced at Confirm too, not just at the offer.
		if err := assertNINVerified(sub, s.requireNINVerified(ctx)); err != nil {
			return err
		}
		// Register-authoritative self-exclusion check (R1-MUST): refuse on an ACTIVE
		// self-exclusion directly, so the control never depends on the status mirror
		// being in sync (safety-floor discipline).
		if err := s.assertNotSelfExcluded(ctx, tx, sub.SubscriberAccountID); err != nil {
			return err
		}
		// S3-C2 re-origination hold: refuse a fresh advance for a subscriber whose
		// debt was CLOSED by a webhook recovery the EOD recon has not yet confirmed —
		// a phantom close must not free the advance slot for a new advance before the
		// feed confirms it (and the later reversal reopen would deadlock otherwise).
		if held, err := (repo.RecoveryHolds{}).HasUncleared(ctx, tx, sub.SubscriberAccountID); err != nil {
			return err
		} else if held {
			return ErrRecoveryUnconfirmedHold
		}
		// Real-time overlays at the money-moving moment (V2-SCR-015). The
		// validator guarantees CONFIRM cannot be configured off.
		if err := s.checkOverlays(ctx, tx, sub.TelcoID, sub.SubscriberAccountID, "CONFIRM", time.Now().UTC()); err != nil {
			return err
		}

		offer, err = s.offers.GetForUpdate(ctx, tx, cmd.OfferID)
		if errors.Is(err, repo.ErrNotFound) {
			return ErrOfferNotFound
		}
		if err != nil {
			return err
		}
		if offer.SubscriberAccountID != sub.SubscriberAccountID {
			return ErrOfferNotFound // someone else's offer is invisible, not forbidden
		}
		// BX-P0-001: the money side is bound to the OFFER's programme (advance, pool,
		// fee and the treasury guardrail all use offer.ProgrammeID), but the suspension
		// kill-switch (GetStatus, :649), economics/lender-of-record (resolveEconomics,
		// :658) and the channel disclosure (:603) were resolved from the caller-supplied
		// cmd.ProgrammeID above. A confirm that names a different (e.g. ACTIVE) programme
		// than the offer's (e.g. SUSPENDED) would bypass the kill-switch and record the
		// wrong lender-of-record while booking the offer's money. Require equality so
		// every gate provably evaluates the offer's programme. Fresh path only — a genuine
		// replay returned at the idempotency claim above; a mismatch rolls back the whole
		// tx, so no advance and no idempotency record persist.
		if offer.ProgrammeID != cmd.ProgrammeID {
			return ErrProgrammeMismatch
		}

		now := time.Now().UTC()
		switch {
		case offer.State == entity.OfferAccepted:
			// EDG-001 replay path: the advance for this offer already exists — return the exact
			// original confirm response (BX-MED-002), not its current state.
			existing, err := s.replayConfirmAdvance(ctx, tx, telcoID, cmd.IdemKey)
			if err == nil {
				adv, replayed = existing, true
				return nil
			}
			return ErrOfferNotAcceptable
		case offer.State != entity.OfferGenerated:
			return ErrOfferNotAcceptable
		case !offer.ExpiresAt.After(now):
			// EDG-011: expired between menu and confirm — fail safely.
			_ = s.offers.SetState(ctx, tx, offer.OfferID, entity.OfferGenerated, entity.OfferExpired)
			return ErrOfferExpired
		}

		// The offer's pinned decision must still be valid AT CONFIRM: an
		// offer whose decision expired between menu and confirm is a stale
		// lend (EDG-014) — refuse, never honour.
		dec, err := s.decisions.Get(ctx, tx, offer.DecisionSnapshotID)
		if err != nil {
			return err
		}
		if err := requireValidDecision(dec, now); err != nil {
			return err
		}

		// R-P0-7: bind the confirm to the exact disclosure the customer was
		// shown. The client echoes DisclosureRef; we load the canonical snapshot
		// for this offer (server truth) and require them to match — a fabricated
		// or foreign reference cannot resolve to this offer. Acceptance must have
		// happened inside the disclosure's validity window (issued..expires, with
		// modest skew), so a replayed or backdated acceptance is refused.
		snap, err := s.disclosures.GetByOffer(ctx, tx, offer.OfferID)
		if errors.Is(err, repo.ErrNotFound) {
			return fmt.Errorf("%w: no disclosure on record for offer", ErrDisclosureMismatch)
		}
		if err != nil {
			return err
		}
		if snap.DisclosureSnapshotID != cmd.DisclosureRef {
			return ErrDisclosureMismatch
		}
		if cmd.AcceptedAt.Before(snap.IssuedAt.Add(-acceptanceSkew)) ||
			cmd.AcceptedAt.After(snap.ExpiresAt.Add(acceptanceSkew)) ||
			cmd.AcceptedAt.After(now.Add(acceptanceSkew)) {
			return ErrDisclosureExpired
		}

		// Create the advance FIRST, pool-less (0006): the one-active contest
		// is decided at this INSERT, before any pool lock exists — a losing
		// contender therefore never holds the pool row, which is what broke
		// the tx1/tx2 deadlock cycle found by the EDG-002 test.
		adv = entity.Advance{
			AdvanceID:           platform.NewID("adv"),
			TelcoID:             sub.TelcoID,
			ProgrammeID:         offer.ProgrammeID,
			SubscriberAccountID: sub.SubscriberAccountID,
			OfferID:             offer.OfferID,
			IdempotencyKey:      cmd.IdemKey,
			CorrelationID:       cmd.CorrelationID,
			State:               entity.AdvRequested,
			Version:             1,
			FaceValue:           offer.FaceValue,
			Fee:                 offer.Fee,
			Disbursed:           offer.Disbursed,
			Outstanding:         offer.Repayment, // obligation = repayment amount
		}
		// PIN the fee_recognition policy on the advance at origination. This is the
		// single fail-closed read (issuance refuses if the config is absent or
		// invalid); recovery/reversal/write-off replay adv.FeeRecognition and never
		// re-read config, so a mid-life policy flip cannot desync them.
		pol, err := feepolicy.Resolve(ctx, s.Config, offer.ProgrammeID)
		if err != nil {
			return err
		}
		adv.FeeRecognition = pol
		created, err := s.advances.Insert(ctx, tx, adv)
		if err != nil {
			return err
		}
		if !created {
			// Same idem key raced us in another request: replay outside.
			return errReplayRace
		}
		if err := s.advances.Transition(ctx, tx, adv.AdvanceID, 1, entity.AdvRequested, entity.AdvValidated); err != nil {
			return err
		}

		// Only the one-active winner reaches the pool (exposure = repayment
		// obligation). Reservation + EXPOSURE_RESERVED are one atomic step.
		poolID, err := s.pools.Reserve(ctx, tx, offer.ProgrammeID, offer.Repayment)
		if err != nil {
			return err
		}

		// M3d: guardrails measure HERE, with the pool row locked — the
		// serialization point concurrent confirms queue behind (EDG-024). A
		// breach aborts this confirm; the trip records out-of-band below.
		// NO nil-skip (M3D-F1): the guardrail is structurally always armed.
		if err := s.treasury.EvaluateInTx(ctx, tx, offer.ProgrammeID, poolID, offer.Disbursed); err != nil {
			return err
		}
		adv.FundingPoolID = poolID
		if err := s.advances.ReserveTransition(ctx, tx, adv.AdvanceID, 2, poolID); err != nil {
			return err
		}
		if err := s.offers.SetState(ctx, tx, offer.OfferID, entity.OfferGenerated, entity.OfferAccepted); err != nil {
			return err
		}

		// Consent/disclosure evidence IN the confirm transaction (V2-REG-001,
		// R-P0-7): an advance cannot exist without proof of WHAT was disclosed
		// and THAT it was accepted through a real channel session. The record
		// binds the disclosure snapshot the customer was shown (its exact
		// rendered text, template, version, locale and content hash) plus the
		// channel/session/acceptance evidence — no server-reconstructed terms,
		// no hardcoded channel.
		terms, err := json.Marshal(map[string]any{
			"disclosure_snapshot_id": snap.DisclosureSnapshotID,
			"template_id":            snap.TemplateID,
			"template_version":       snap.TemplateVersion,
			"locale":                 snap.Locale,
			"rendered_body":          snap.RenderedBody,
			"total_cost_text":        snap.TotalCostText,
			"face_value_minor":       snap.FaceValue.Amount(),
			"fee_minor":              snap.Fee.Amount(),
			"disbursed_minor":        snap.Disbursed.Amount(),
			"repayment_minor":        snap.Repayment.Amount(),
			"currency":               string(snap.Currency),
			"decision_snapshot_id":   offer.DecisionSnapshotID,
			"product_config":         offer.ProductConfigVersionID,
			// Build 1: the programme economic/legal identity applied to THIS loan,
			// pinned on the origination record (who funds it, who is the lender of
			// record, who bears losses, how it settles + is taxed).
			"economics_config_version_id": econVer,
			"economic_terms": map[string]any{
				"funding_model":     econTerms.FundingModel,
				"lender_of_record":  econTerms.LenderOfRecord,
				"loss_bearer":       econTerms.LossBearer,
				"settlement_method": econTerms.SettlementMethod,
				"tax_treatment":     econTerms.TaxTreatment,
			},
		})
		if err != nil {
			return err
		}
		if err := s.consents.Insert(ctx, tx, repo.Consent{
			ConsentID: platform.NewID("cns"), TelcoID: sub.TelcoID,
			AdvanceID: adv.AdvanceID, SubscriberAccountID: sub.SubscriberAccountID,
			DisclosureSnapshotID: snap.DisclosureSnapshotID,
			DisclosedTerms:       terms,
			ContentHash:          snap.ContentHash, // the hash of what we disclosed
			Channel:              cmd.Channel,
			SessionID:            cmd.SessionID,
			AcceptedAt:           cmd.AcceptedAt,
			TelcoEvidence:        cmd.TelcoEvidence,
		}); err != nil {
			return err
		}

		// Record the attempt BEFORE the network call: a crash after commit
		// leaves durable evidence the resolver can act on (EDG-007/008).
		wire, err := json.Marshal(map[string]any{
			"platform_request_id": adv.AdvanceID,
			"face_value_minor":    offer.FaceValue.Amount(),
			"currency":            string(offer.FaceValue.Currency()),
			"offer_snapshot_id":   offer.OfferID,
		})
		if err != nil {
			return err
		}
		attempt = entity.FulfilmentAttempt{
			AttemptID:           platform.NewID("att"),
			AdvanceID:           adv.AdvanceID,
			AttemptNo:           1,
			TelcoIdempotencyKey: platform.NewID("tik"),
			State:               entity.AttemptSent,
			RequestEvidence:     wire,
		}
		if err := s.attempts.Insert(ctx, tx, attempt); err != nil {
			return err
		}
		if err := s.advances.Transition(ctx, tx, adv.AdvanceID, 3, entity.AdvExposureReserved, entity.AdvPendingFulfilment); err != nil {
			return err
		}
		adv.State = entity.AdvPendingFulfilment
		adv.Version = 4
		return nil
	})
	var breach *treasury.BreachError
	switch {
	case errors.Is(err, ErrDivergentDuplicate) && divergent:
		// R-P0-1: idempotency key reused with a different request. The tx
		// rolled back (no effect), so record the security-audit in a fresh tx
		// — a reused key with a divergent body is either a client bug or an
		// attempt to piggyback a changed command on a completed one.
		s.recordDivergentDuplicate(ctx, telcoID, cmd, reqHash)
		return ConfirmResult{}, ErrDivergentDuplicate
	case errors.As(err, &breach):
		// M3d: the confirm aborted on a guardrail breach. Record the trip +
		// suspend the programme in a SEPARATE transaction (it must survive
		// the abort), then decline customer-safe. Crash between the two:
		// the next confirm re-detects and converges.
		if telcoID, terr := platform.TenantFrom(ctx); terr == nil {
			if terr := s.treasury.RecordTrip(ctx, telcoID, cmd.ProgrammeID, breach); terr != nil {
				s.Log.Error("guardrail trip recording failed — next confirm will re-detect", "err", terr)
			}
		}
		return ConfirmResult{}, fmt.Errorf("%w: %s", treasury.ErrProgrammeSuspended, breach.Guardrail)
	case errors.Is(err, errReplayRace):
		// Our idempotency key already has an advance (EDG-001): replay it.
		return s.replayByIdemKey(ctx, cmd.IdemKey)
	case errors.Is(err, repo.ErrConcurrentAdvanceBlocked):
		// One-active backstop fired. If OUR key created the open advance, a
		// concurrent duplicate of this very request won — replay. Otherwise
		// it is a genuine concurrency block (EDG-002): deterministic decline.
		if cmdHasExistingAdvance(ctx, s, cmd) {
			return s.replayByIdemKey(ctx, cmd.IdemKey)
		}
		return ConfirmResult{}, err
	case err != nil:
		return ConfirmResult{}, err
	}
	if replayed {
		return ConfirmResult{Advance: adv, Replayed: true}, nil
	}

	// ---- network: NO transaction open (V2-ADV-006) ------------------------
	res, err := s.Adapter.SubmitFulfilment(ctx, adv.TelcoID, attempt.TelcoIdempotencyKey, mno.FulfilmentRequest{
		PlatformRequestID:   adv.AdvanceID,
		SubscriberAccountID: adv.SubscriberAccountID,
		MSISDNToken:         cmd.MSISDNToken,
		ProductType:         "AIRTIME_ADVANCE",
		FaceValue:           adv.FaceValue,
		OfferSnapshotID:     adv.OfferID,
	})
	if err != nil {
		// BX-HIGH-010: SubmitFulfilment classifies every reachable outcome itself — a
		// pre-send fault is FAILED+NotSent (reservation released), a maybe-sent is UNKNOWN.
		// A bare error is therefore UNEXPECTED; resolve CONSERVATIVELY as Unknown (assume
		// the request may have been sent — never release blind, which risks a double credit).
		s.Log.Error("unexpected adapter error during submit; classifying unknown (conservative)", "advance", adv.AdvanceID, "err", err)
		res = mno.Result{Outcome: mno.OutcomeUnknown, ResponseEvidence: []byte(fmt.Sprintf(`{"adapter_fault":%q}`, err.Error()))}
	}

	// ---- tx2: resolve outcome --------------------------------------------
	final, err := s.ResolveOutcome(ctx, adv.AdvanceID, attempt.AttemptID, res)
	if err != nil {
		return ConfirmResult{}, err
	}
	// BX-MED-002: persist the EXACT confirm response so a later replay returns THIS, not the
	// advance's since-progressed current state (idempotent response, V2-API-003). Best-effort in
	// its own tx after the outcome commits; on failure a replay safely falls back to current state.
	if body, encErr := encodeConfirmResponse(final); encErr == nil {
		if e := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
			return s.idem.SetResponse(ctx, tx, telcoID, "advance.confirm", cmd.IdemKey, 200, body)
		}); e != nil {
			s.Log.Warn("confirm response snapshot not persisted — replay falls back to current state",
				"advance", final.AdvanceID, "err", e)
		}
	}
	return ConfirmResult{Advance: final}, nil
}

var errReplayRace = errors.New("origination: idempotency replay race")

// recordDivergentDuplicate writes the DIVERGENT_DUPLICATE security-audit
// out-of-band (the confirm tx rolled back). No PII: the subscriber token is
// not logged, only the operation and the mismatching hash.
func (s *Service) recordDivergentDuplicate(ctx context.Context, telcoID string, cmd ConfirmCmd, reqHash string) {
	tctx := platform.WithTenant(ctx, telcoID)
	err := repo.WithTenantTx(tctx, s.Pool, func(tx pgx.Tx) error {
		return s.audit.Insert(ctx, tx, entity.AuditEvent{
			ID: platform.NewID("aud"), TelcoID: telcoID, Actor: "channel:telco",
			Action: "advance.confirm.divergent_duplicate", TargetType: "idempotency_key", TargetID: cmd.IdemKey,
			Reason: fmt.Sprintf("idempotency key reused with a divergent request (programme=%s offer=%s hash=%s)",
				cmd.ProgrammeID, cmd.OfferID, reqHash),
		})
	})
	if err != nil {
		s.Log.Error("failed to record DIVERGENT_DUPLICATE audit", "idem_key", cmd.IdemKey, "err", err)
	}
	s.Log.Warn("DIVERGENT_DUPLICATE: idempotency key reused with a different request",
		"idem_key", cmd.IdemKey, "programme", cmd.ProgrammeID, "offer", cmd.OfferID)
}

func cmdHasExistingAdvance(ctx context.Context, s *Service, cmd ConfirmCmd) bool {
	found := false
	_ = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := s.advances.GetByIdemKey(ctx, tx, cmd.IdemKey)
		found = err == nil
		return nil
	})
	return found
}

// GetAdvance is the status-route accessor (EDG-004: the durable status
// enquiry a customer uses after a dropped session). Tenant-scoped by RLS.
func (s *Service) GetAdvance(ctx context.Context, advanceID string) (entity.Advance, error) {
	var adv entity.Advance
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		adv, e = s.advances.Get(ctx, tx, advanceID)
		return e
	})
	return adv, err
}

func (s *Service) replayByIdemKey(ctx context.Context, idemKey string) (ConfirmResult, error) {
	telcoID, err := platform.TenantFrom(ctx)
	if err != nil {
		return ConfirmResult{}, err
	}
	var adv entity.Advance
	err = repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var e error
		// BX-MED-002: the exact original response when finalised, else the current advance.
		adv, e = s.replayConfirmAdvance(ctx, tx, telcoID, idemKey)
		return e
	})
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Advance: adv, Replayed: true}, nil
}

// ResolveOutcome applies a fulfilment result to the advance — shared by the
// saga (tx2) and the M1b-4 resolver worker, so both paths have IDENTICAL
// semantics: ACTIVE+journal / FAILED+release / UNKNOWN+enquiry schedule.
func (s *Service) ResolveOutcome(ctx context.Context, advanceID, attemptID string, res mno.Result) (entity.Advance, error) {
	var out entity.Advance
	err := repo.WithTenantTx(ctx, s.Pool, func(tx pgx.Tx) error {
		adv, err := s.advances.Get(ctx, tx, advanceID)
		if err != nil {
			return err
		}
		// Already terminal/active (resolver raced us): idempotent no-op.
		if adv.State != entity.AdvPendingFulfilment && adv.State != entity.AdvFulfilmentUnknown {
			out = adv
			return nil
		}

		switch res.Outcome {
		case mno.OutcomeConfirmed:
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptConfirmed, res.TelcoReference, res.ResponseEvidence, nil); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvActive); err != nil {
				return err
			}
			if err := s.pools.ConfirmUtilisation(ctx, tx, adv.FundingPoolID, adv.Outstanding); err != nil {
				return err
			}
			// Ledger: recognition at confirmed fulfilment (A-10/V2-LED-006),
			// rendered from the governed template (CFG-012, M3e).
			// Deferred fee recognition: FEE_DEFER_ADJ moves the fee from FEE_INCOME
			// to the UNEARNED_FEE liability at origination. Under DEFERRED it is
			// adv.Fee (fee lands in the liability, FEE_INCOME nets 0); under UPFRONT/
			// legacy it is zero and the deferral legs omit (byte-identical journal).
			// Always bound — PostEvent checks bound-before-omit.
			feeDeferAdj, _ := entity.ZeroMoney(adv.Fee.Currency())
			if adv.FeeRecognition == entity.FeeRecognitionDeferred {
				feeDeferAdj = adv.Fee
			}
			if _, _, err := s.Ledger.PostEvent(ctx, tx, ledger.Journal{
				BusinessEventKey: adv.AdvanceID + "/issued",
				EventType:        ledger.EventAdvanceIssued,
				TelcoID:          adv.TelcoID,
				ProgrammeID:      adv.ProgrammeID,
				AdvanceID:        adv.AdvanceID,
				CorrelationID:    adv.CorrelationID,
			}, ledger.Bindings{
				ledger.SymOutstanding: adv.Outstanding,
				ledger.SymDisbursed:   adv.Disbursed,
				ledger.SymFee:         adv.Fee,
				ledger.SymFeeDeferAdj: feeDeferAdj,
			}); err != nil {
				return err
			}
			if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentConfirmed"); err != nil {
				return err
			}

		case mno.OutcomeFailed, mno.OutcomeNotFound:
			// NotFound (from enquiry) = provably never landed = safe to fail.
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptFailed, res.TelcoReference, res.ResponseEvidence, nil); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvFulfilmentFailed); err != nil {
				return err
			}
			// Release exactly once — guarded by the FSM transition above
			// succeeding (V2-ADV-010).
			if err := s.pools.Release(ctx, tx, adv.FundingPoolID, adv.Outstanding); err != nil {
				return err
			}
			if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentFailed"); err != nil {
				return err
			}

		case mno.OutcomeUnknown:
			// Schedule the first enquiry from governed config (V2-ADV-009).
			next, err := s.firstEnquiryAt(ctx, adv.TelcoID)
			if err != nil {
				return err
			}
			if err := s.attempts.Resolve(ctx, tx, attemptID, currentAttemptState(adv), entity.AttemptUnknown, "", res.ResponseEvidence, &next); err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			// VR-7b: the FulfilmentUnknown event is emitted on STATE ENTRY
			// only — repeated still-unknown enquiry cycles reschedule quietly
			// instead of flooding the outbox with duplicates.
			if adv.State == entity.AdvPendingFulfilment {
				if err := s.advances.Transition(ctx, tx, adv.AdvanceID, adv.Version, adv.State, entity.AdvFulfilmentUnknown); err != nil {
					return err
				}
				if err := s.emitOutbox(ctx, tx, adv, "advance.FulfilmentUnknown"); err != nil {
					return err
				}
			}
			// NO ledger entry, NO utilisation, reservation stays (V2-LED-006).

		default:
			return fmt.Errorf("unrecognised adapter outcome %q", res.Outcome)
		}

		out, err = s.advances.Get(ctx, tx, adv.AdvanceID)
		return err
	})
	return out, err
}

// currentAttemptState infers the guard state for attempt resolution from the
// advance state (SENT before first resolution, UNKNOWN thereafter).
func currentAttemptState(adv entity.Advance) entity.FulfilmentAttemptState {
	if adv.State == entity.AdvFulfilmentUnknown {
		return entity.AttemptUnknown
	}
	return entity.AttemptSent
}

func (s *Service) firstEnquiryAt(ctx context.Context, telcoID string) (time.Time, error) {
	cv, err := s.Config.ActiveAt(ctx, "advance.fulfilment", "telco:"+telcoID, time.Now().UTC())
	if err != nil {
		return time.Time{}, fmt.Errorf("advance.fulfilment config: %w", err)
	}
	var fc fulfilmentCfg
	if err := json.Unmarshal(cv.Content, &fc); err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC().Add(time.Duration(fc.StatusEnquiryDelaysSeconds[0]) * time.Second), nil
}

func (s *Service) emitOutbox(ctx context.Context, tx pgx.Tx, adv entity.Advance, eventType string) error {
	payload, err := json.Marshal(map[string]string{
		"advance_id":     adv.AdvanceID,
		"programme_id":   adv.ProgrammeID,
		"correlation_id": adv.CorrelationID, // BC-6 lineage; no PII (V2-EVT-010)
	})
	if err != nil {
		return err
	}
	return s.outbox.Append(ctx, tx, entity.OutboxEvent{
		ID: platform.NewID("evt"), TelcoID: adv.TelcoID, AggregateType: "Advance",
		AggregateID: adv.AdvanceID, EventType: eventType, SchemaVersion: 1,
		Payload: payload, OccurredAt: time.Now().UTC(),
	})
}
