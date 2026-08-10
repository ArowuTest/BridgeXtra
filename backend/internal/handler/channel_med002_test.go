package handler_test

// BX-MED-002 over the wire: a confirm REPLAY must return the same status class the ORIGINAL
// confirm earned. An UNKNOWN confirm is a 202 PROCESSING ("poll the status route") — so its
// retry under the same idempotency key must be 202 as well, never a 200 that a status-polling
// channel reads as "settled". This composes with the usecase fix (the replay now hands back the
// advance's persisted confirm-time state), so the handler's state-based status derivation yields
// the original class on replay; only a freshly-created ACTIVE flips 201 -> 200.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestBXMED002_WireReplayOfUnknown_Stays202_NotSettled200(t *testing.T) {
	// Sim holds past the adapter timeout → the confirm is a post-send-timeout UNKNOWN (202).
	f := newChannelFixture(t, "chan_med002", 2*time.Second, 300)
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO subscriber_accounts (subscriber_account_id, telco_id, msisdn_token, status, nin_verified)
		VALUES ('sub_m2','SIM_NG','tok_TIMEOUT_m2','ACTIVE',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO decision_snapshots (decision_snapshot_id, telco_id, subscriber_account_id,
		  max_face_value_minor, currency, config_version_id)
		VALUES ('dec_m2','SIM_NG','sub_m2',50000,'NGN','cfg_seed_product_airtime_v1')`); err != nil {
		t.Fatal(err)
	}

	resp, body := f.do(t, http.MethodPost, "/v1/offers", nil,
		map[string]string{"programme_id": "prg_sim_airtime01", "msisdn_token": "tok_TIMEOUT_m2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("offers: %d %s", resp.StatusCode, body)
	}
	var offers []struct {
		OfferID       string `json:"offer_id"`
		DisclosureRef string `json:"disclosure_ref"`
	}
	if err := json.Unmarshal(body, &offers); err != nil {
		t.Fatal(err)
	}

	confirmBody := map[string]any{
		"programme_id": "prg_sim_airtime01", "offer_id": offers[0].OfferID, "msisdn_token": "tok_TIMEOUT_m2",
		"disclosure_ref": offers[0].DisclosureRef, "channel": "USSD",
		"session_id": "m2-sess", "accepted_at": time.Now().UTC().Format(time.RFC3339),
	}
	hdr := map[string]string{"Idempotency-Key": "wire-med002-1"}

	// Fresh confirm: 202 PROCESSING.
	resp, body = f.do(t, http.MethodPost, "/v1/advances", hdr, confirmBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("fresh UNKNOWN confirm must be 202: %d %s", resp.StatusCode, body)
	}
	var adv1 struct {
		AdvanceID string `json:"advance_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(body, &adv1); err != nil {
		t.Fatal(err)
	}
	if adv1.Status != "PROCESSING" {
		t.Fatalf("fresh UNKNOWN must be PROCESSING: %s", body)
	}

	// Retry the SAME confirm under the SAME key (first response assumed lost). It must REPLAY as
	// 202 PROCESSING — the pending advance is still pending — not a 200 that reads as settled.
	// Mutation proof: restore the handler's `case res.Replayed: 200` ahead of the state cases and
	// this retry becomes 200 — RED.
	resp, body = f.do(t, http.MethodPost, "/v1/advances", hdr, confirmBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("BX-MED-002: replay of an UNKNOWN confirm must stay 202 (still pending), got %d %s", resp.StatusCode, body)
	}
	var adv2 struct {
		AdvanceID string `json:"advance_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(body, &adv2); err != nil {
		t.Fatal(err)
	}
	if adv2.Status != "PROCESSING" || adv2.AdvanceID != adv1.AdvanceID {
		t.Fatalf("replay must carry the original PROCESSING advance %s, got %s", adv1.AdvanceID, body)
	}
}
