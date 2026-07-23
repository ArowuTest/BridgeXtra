package rechargewebhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RechargeEvent is the canonical internal recharge, independent of any vendor
// wire format. It maps 1:1 onto recovery.IngestCmd downstream.
type RechargeEvent struct {
	EventID     string // the money-core idempotency key (namespaced "wh:" at ingest)
	MSISDNToken string
	AmountMinor int64
	Currency    string
	OccurredAt  time.Time
}

// Mapper maps a vendor recharge payload to the canonical event. It is fail-closed:
// a blank/absent event_id, a non-integer amount, a bad currency, or a malformed
// body is an error — never a defaulted or coerced value. The real MTN mapper is
// a future config-selected implementation of this SAME interface.
type Mapper interface {
	Scheme() string
	Map(rawBody []byte) (RechargeEvent, error)
}

type jsonMapper struct{}

// NewJSONMapper is the mock-first field-mapping adapter (UNVERIFIED vs real MTN,
// see build/PHASE1_S2_ASSUMED_CONTRACT.md). It expects the assumed contract:
//
//	{event_id, msisdn_token, amount_minor (integer minor units), currency,
//	 occurred_at (RFC3339), borrowed_balance_minor (accepted but IGNORED)}
func NewJSONMapper() Mapper { return jsonMapper{} }

func (jsonMapper) Scheme() string { return "webhook_push" }

func (jsonMapper) Map(rawBody []byte) (RechargeEvent, error) {
	var w struct {
		EventID     string      `json:"event_id"`
		MSISDNToken string      `json:"msisdn_token"`
		AmountMinor json.Number `json:"amount_minor"`
		Currency    string      `json:"currency"`
		OccurredAt  time.Time   `json:"occurred_at"`
		// Accepted so the strict decoder does not reject it, then DROPPED —
		// borrowed_balance is telco-supplied and is never a money authority.
		BorrowedBalanceMinor json.Number `json:"borrowed_balance_minor"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return RechargeEvent{}, fmt.Errorf("recharge payload: %w", err)
	}
	if strings.TrimSpace(w.EventID) == "" {
		return RechargeEvent{}, errors.New("event_id is required (the money-core idempotency key)")
	}
	if w.MSISDNToken == "" {
		return RechargeEvent{}, errors.New("msisdn_token is required")
	}
	// Strict integer minor-units: a decimal/float/non-numeric amount is refused,
	// never rounded or truncated.
	amt, err := strconv.ParseInt(w.AmountMinor.String(), 10, 64)
	if err != nil {
		return RechargeEvent{}, fmt.Errorf("amount_minor must be an integer in minor units, got %q", w.AmountMinor.String())
	}
	if amt <= 0 {
		return RechargeEvent{}, errors.New("amount_minor must be > 0")
	}
	if len(w.Currency) != 3 {
		return RechargeEvent{}, errors.New("currency must be a 3-letter code")
	}
	if w.OccurredAt.IsZero() {
		return RechargeEvent{}, errors.New("occurred_at is required")
	}
	return RechargeEvent{
		EventID:     w.EventID,
		MSISDNToken: w.MSISDNToken,
		AmountMinor: amt,
		Currency:    w.Currency,
		OccurredAt:  w.OccurredAt.UTC(),
	}, nil
}
