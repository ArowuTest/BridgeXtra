package rechargewebhook_test

// Phase 1 S2.2a — the mock field-mapping adapter is fail-closed: the assumed
// contract's required fields must be present and well-typed, amounts are strict
// integer minor-units (no decimal/float/string coercion), unknown fields and
// malformed bodies are refused, and borrowed_balance is accepted-then-dropped.

import (
	"testing"

	rw "github.com/ArowuTest/telco-credit-platform/backend/internal/rechargewebhook"
)

func TestS22_Mapper_Valid(t *testing.T) {
	m := rw.NewJSONMapper()
	e, err := m.Map([]byte(`{"event_id":"e1","msisdn_token":"tok1","amount_minor":50000,"currency":"NGN","occurred_at":"2026-07-23T10:00:00Z","borrowed_balance_minor":123}`))
	if err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if e.EventID != "e1" || e.MSISDNToken != "tok1" || e.AmountMinor != 50000 || e.Currency != "NGN" {
		t.Fatalf("mapped wrong: %+v", e)
	}
	if e.OccurredAt.IsZero() {
		t.Fatal("occurred_at must be parsed")
	}
}

func TestS22_Mapper_FailClosed(t *testing.T) {
	m := rw.NewJSONMapper()
	const ts = `"occurred_at":"2026-07-23T10:00:00Z"`
	bad := map[string]string{
		"blank event_id":   `{"event_id":"","msisdn_token":"t","amount_minor":1,"currency":"NGN",` + ts + `}`,
		"missing event_id": `{"msisdn_token":"t","amount_minor":1,"currency":"NGN",` + ts + `}`,
		"missing msisdn":   `{"event_id":"e","amount_minor":1,"currency":"NGN",` + ts + `}`,
		"decimal amount":   `{"event_id":"e","msisdn_token":"t","amount_minor":10.5,"currency":"NGN",` + ts + `}`,
		"quoted decimal":   `{"event_id":"e","msisdn_token":"t","amount_minor":"10.5","currency":"NGN",` + ts + `}`,
		"non-numeric amt":  `{"event_id":"e","msisdn_token":"t","amount_minor":"abc","currency":"NGN",` + ts + `}`,
		"zero amount":      `{"event_id":"e","msisdn_token":"t","amount_minor":0,"currency":"NGN",` + ts + `}`,
		"negative amount":  `{"event_id":"e","msisdn_token":"t","amount_minor":-5,"currency":"NGN",` + ts + `}`,
		"bad currency":     `{"event_id":"e","msisdn_token":"t","amount_minor":1,"currency":"naira",` + ts + `}`,
		"missing occurred": `{"event_id":"e","msisdn_token":"t","amount_minor":1,"currency":"NGN"}`,
		"unknown field":    `{"event_id":"e","msisdn_token":"t","amount_minor":1,"currency":"NGN",` + ts + `,"evil":1}`,
		"malformed json":   `{not json`,
	}
	for label, body := range bad {
		if _, err := m.Map([]byte(body)); err == nil {
			t.Errorf("%s: mapper must fail-closed", label)
		}
	}
}
