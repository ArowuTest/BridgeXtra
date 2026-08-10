package origination

// BX-MED-001: the confirm idempotency hash must cover every command AND legal/consent evidence
// field, so a retry that changed any of them under the same idempotency key is caught as a
// divergent duplicate — not silently treated as the same command. Only per-attempt tracing
// (correlation_id, idem_key) is excluded, and time / JSON evidence are normalised so an
// equivalent retry does not falsely diverge.

import (
	"encoding/json"
	"testing"
	"time"
)

func baseConfirmCmd() ConfirmCmd {
	return ConfirmCmd{
		ProgrammeID: "prg1", OfferID: "off1", MSISDNToken: "tok1",
		IdemKey: "idem1", CorrelationID: "cor1",
		DisclosureRef: "disc1", Channel: "USSD", SessionID: "sess1",
		AcceptedAt:    time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		TelcoEvidence: json.RawMessage(`{"b":2,"a":1}`),
	}
}

func TestBXMED001_ConfirmHash_EquivalenceAndDivergence(t *testing.T) {
	base := confirmRequestHash(baseConfirmCmd())

	// Identical retry -> identical hash (replays, no false 409).
	if confirmRequestHash(baseConfirmCmd()) != base {
		t.Fatal("an identical confirm must hash identically")
	}

	// Tracing fields must NOT affect the hash — a fresh correlation_id / idem_key still replays.
	if c := baseConfirmCmd(); func() bool { c.CorrelationID = "cor-x"; c.IdemKey = "idem-x"; return confirmRequestHash(c) != base }() {
		t.Fatal("correlation_id / idem_key are tracing — must not change the hash")
	}

	// accepted_at: sub-second precision and a timezone-equivalent instant normalise to the same hash.
	if c := baseConfirmCmd(); func() bool { c.AcceptedAt = time.Date(2026, 8, 10, 12, 0, 0, 999_000_000, time.UTC); return confirmRequestHash(c) != base }() {
		t.Fatal("sub-second accepted_at precision must normalise to the same hash")
	}
	if c := baseConfirmCmd(); func() bool {
		c.AcceptedAt = time.Date(2026, 8, 10, 13, 0, 0, 0, time.FixedZone("WAT", 3600)) // == 12:00 UTC
		return confirmRequestHash(c) != base
	}() {
		t.Fatal("a timezone-equivalent accepted_at must normalise to the same hash")
	}

	// telco_evidence with reordered keys canonicalises to the same hash.
	if c := baseConfirmCmd(); func() bool { c.TelcoEvidence = json.RawMessage(`{"a":1,"b":2}`); return confirmRequestHash(c) != base }() {
		t.Fatal("key-reordered telco_evidence must canonicalise to the same hash")
	}

	// Every command + legal-evidence field, changed, MUST make the hash diverge. Mutation proof:
	// drop any field from the hash struct and its sub-case below stops diverging.
	for name, mut := range map[string]func(*ConfirmCmd){
		"programme_id":   func(c *ConfirmCmd) { c.ProgrammeID = "prg-X" },
		"offer_id":       func(c *ConfirmCmd) { c.OfferID = "off-X" },
		"msisdn_token":   func(c *ConfirmCmd) { c.MSISDNToken = "tok-X" },
		"disclosure_ref": func(c *ConfirmCmd) { c.DisclosureRef = "disc-X" },
		"channel":        func(c *ConfirmCmd) { c.Channel = "SMS" },
		"session_id":     func(c *ConfirmCmd) { c.SessionID = "sess-X" },
		"accepted_at":    func(c *ConfirmCmd) { c.AcceptedAt = baseConfirmCmd().AcceptedAt.Add(time.Hour) },
		"telco_evidence": func(c *ConfirmCmd) { c.TelcoEvidence = json.RawMessage(`{"a":1,"b":999}`) },
	} {
		cc := baseConfirmCmd()
		mut(&cc)
		if confirmRequestHash(cc) == base {
			t.Errorf("changing %s must make the confirm hash diverge (BX-MED-001)", name)
		}
	}
}
