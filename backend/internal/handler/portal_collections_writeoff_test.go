package handler_test

// Wave B.3 Phase B — the write-off MONEY DOOR through the HTTP layer. The deep money invariants
// (crystallisation, WRITE_OFF_EXPENSE, balanced books, schema tamper-resistance) are already
// locked at the usecase level (collections.TestM3C_WriteOff_FullMakerCheckerJourney). These
// tests prove the properties that are NEW at the door:
//   1. Four-eyes THROUGH the session: the same actor cannot approve their own request; a
//      distinct actor can. The approver is the SESSION identity — a forged approved_by in the
//      body is ignored.
//   2. The real RBAC split as a flow: OPS makes, FINANCE checks (request {ADMIN,OPS} / approve
//      {ADMIN,FINANCE} — the per-route allow-lists are separately locked by the driven RBAC
//      matrix in TestM4A_RBACMatrix_DenyByDefault, which iterates the production route map).
//   3. FSM guard: a decided write-off cannot be approved again.
//   4. CSRF is required on the money door.
//   5. The DISPUTE GATE: an open DEBT dispute soft-blocks; the checker proceeds only with an
//      audited override; the gate is scoped to debt categories (a service complaint does not
//      block); and it FAILS SAFE — if the governed collections.policy is unresolvable the gate
//      hard-blocks even a supplied override (zero-config floor: a config gap never opens it).
//   6. The approvals inbox lists REQUESTED write-offs with governed money views.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// --- helpers -------------------------------------------------------------------------------

// openComplaintCat opens an OPEN complaint in a given category for the subscriber (the debt vs
// non-debt distinction the dispute gate turns on).
func openComplaintCat(t *testing.T, f *portalFixture, sub, category string, n int) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `
		INSERT INTO complaints (complaint_id, telco_id, subscriber_account_id, channel, category, narrative, state)
		VALUES ($1,'SIM_NG',$2,'IVR',$3,'seeded complaint','OPEN')`,
		fmt.Sprintf("cmp_wo_%d", n), sub, category); err != nil {
		t.Fatalf("open complaint (%s): %v", category, err)
	}
}

// seedWriteOffCandidate seeds a top-bucket, write-off-eligible advance AND reserves its
// outstanding against the funding pool — mirroring the pool state a real origination leaves
// behind (utilised_minor = outstanding). ApproveWriteOff RELEASES that utilisation, so without
// this the crystallisation tx would underflow the pool. pool_sim_01 has ₦1,000,000 committed, so
// the modest per-advance reservations never over-allocate.
func seedWriteOffCandidate(t *testing.T, f *portalFixture, n int, outMinor, activatedDaysAgo int64) (advID, sub string) {
	t.Helper()
	advID, sub = seedDelinquent(t, f, n, "DPD_90_PLUS", outMinor, int(activatedDaysAgo))
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE funding_pools SET utilised_minor = utilised_minor + $1 WHERE pool_id='pool_sim_01'`, outMinor); err != nil {
		t.Fatalf("reserve pool utilisation: %v", err)
	}
	return advID, sub
}

type reqWriteOffResp struct {
	WriteOffID string `json:"write_off_id"`
	State      string `json:"state"`
	Disputed   bool   `json:"disputed"`
}

// requestWriteOff drives the maker door for an advance and returns the parsed response.
func requestWriteOff(t *testing.T, f *portalFixture, s *session, advID, reason string) (int, reqWriteOffResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"reason": reason})
	code, raw := f.callBody(t, s, "POST", "/v1/portal/ops/collections/"+advID+"/request-writeoff", string(body))
	var out reqWriteOffResp
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return code, out
}

// approveWriteOff drives the checker door. override/complaint are sent verbatim; approvedBy is a
// deliberately-forged body field the door must ignore.
func approveWriteOff(t *testing.T, f *portalFixture, s *session, woID, overrideReason, complaintID, forgedApprovedBy string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"override_reason": overrideReason, "complaint_id": complaintID, "approved_by": forgedApprovedBy,
	})
	code, raw := f.callBody(t, s, "POST", "/v1/portal/ops/collections/"+woID+"/approve-writeoff", string(body))
	var e struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(raw, &e)
	return code, e.ErrorCode
}

func advanceStateOutstanding(t *testing.T, f *portalFixture, advID string) (state string, outstanding int64) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT state, outstanding_minor FROM advances WHERE advance_id=$1`, advID).Scan(&state, &outstanding); err != nil {
		t.Fatalf("read advance: %v", err)
	}
	return state, outstanding
}

func writeOffApprovedBy(t *testing.T, f *portalFixture, woID string) (approvedBy, state string) {
	t.Helper()
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(approved_by,''), state FROM write_offs WHERE write_off_id=$1`, woID).Scan(&approvedBy, &state); err != nil {
		t.Fatalf("read write-off: %v", err)
	}
	return approvedBy, state
}

// booksBalanced asserts every journal nets to zero (the global money invariant survives the door).
func booksBalanced(t *testing.T, f *portalFixture) {
	t.Helper()
	var unbalanced int
	if err := f.db.Admin.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM (
		  SELECT journal_id FROM journal_entries GROUP BY journal_id, currency
		  HAVING SUM(debit_minor) <> SUM(credit_minor)) x`).Scan(&unbalanced); err != nil {
		t.Fatalf("balance check: %v", err)
	}
	if unbalanced != 0 {
		t.Fatalf("write-off through the door must leave balanced books, got %d unbalanced journals", unbalanced)
	}
}

// --- 1–4: four-eyes, session-approver, FSM ------------------------------------------------

func TestWriteOff_MoneyDoor_FourEyes_SessionApprover_FSM(t *testing.T) {
	f := newPortalFixture(t, "wo_foureyes")
	ops := f.login(t, roleKeys["OPS"])         // maker (request: {ADMIN,OPS})
	finance := f.login(t, roleKeys["FINANCE"]) // checker (approve: {ADMIN,FINANCE})
	admin1 := f.login(t, roleKeys["ADMIN"])
	admin2 := f.login(t, "portal-key-admin-000002")

	// Two eligible advances (top bucket, well past the governed write-off floor).
	advHappy, _ := seedWriteOffCandidate(t, f, 60, 9000, 120)
	advSelf, _ := seedWriteOffCandidate(t, f, 61, 8000, 120)

	// (Happy path, real role split) OPS makes, FINANCE checks — distinct actors, distinct roles.
	code, rr := requestWriteOff(t, f, &ops, advHappy, "uncollectable 120d")
	if code != http.StatusOK || rr.WriteOffID == "" || rr.State != "REQUESTED" {
		t.Fatalf("OPS request-writeoff: %d %+v", code, rr)
	}
	if rr.Disputed {
		t.Fatalf("no complaint seeded ⇒ disputed must be false, got true")
	}
	code, ec := approveWriteOff(t, f, &finance, rr.WriteOffID, "", "", "")
	if code != http.StatusOK {
		t.Fatalf("FINANCE approve (distinct actor) must succeed: %d %s", code, ec)
	}
	// The loss crystallised through the door.
	if st, out := advanceStateOutstanding(t, f, advHappy); st != "WRITTEN_OFF" || out != 0 {
		t.Fatalf("approved write-off must crystallise: state=%s outstanding=%d", st, out)
	}
	booksBalanced(t, f)
	// The approver recorded is the FINANCE SESSION identity.
	if ab, st := writeOffApprovedBy(t, f, rr.WriteOffID); ab != finance.actor || st != "POSTED" {
		t.Fatalf("approved_by must be the session actor %q (POSTED), got %q (%s)", finance.actor, ab, st)
	}

	// (Four-eyes) ADMIN requests, the SAME admin tries to approve → schema-enforced refusal.
	code, sr := requestWriteOff(t, f, &admin1, advSelf, "self approve attempt")
	if code != http.StatusOK || sr.WriteOffID == "" {
		t.Fatalf("admin1 request: %d %+v", code, sr)
	}
	if code, ec := approveWriteOff(t, f, &admin1, sr.WriteOffID, "", "", ""); code != http.StatusConflict || ec != "WRITEOFF_SELF_APPROVAL" {
		t.Fatalf("same actor approving own request must be 409 SELF_APPROVAL, got %d %s", code, ec)
	}

	// (Session-approver, body ignored) admin2 approves with a FORGED approved_by in the body.
	code, ec = approveWriteOff(t, f, &admin2, sr.WriteOffID, "", "", "mallory")
	if code != http.StatusOK {
		t.Fatalf("distinct admin2 approve must succeed: %d %s", code, ec)
	}
	if ab, _ := writeOffApprovedBy(t, f, sr.WriteOffID); ab != admin2.actor {
		t.Fatalf("approved_by must come from the SESSION (%q), not the forged body ('mallory'), got %q", admin2.actor, ab)
	}

	// (FSM guard) a decided write-off cannot be approved again.
	if code, _ := approveWriteOff(t, f, &finance, sr.WriteOffID, "", "", ""); code != http.StatusNotFound {
		t.Fatalf("re-approving a decided write-off must be 404 (not REQUESTED), got %d", code)
	}
}

// --- 4: CSRF on the money door -------------------------------------------------------------

func TestWriteOff_MoneyDoor_CSRF(t *testing.T) {
	f := newPortalFixture(t, "wo_csrf")
	ops := f.login(t, roleKeys["OPS"])
	finance := f.login(t, roleKeys["FINANCE"])
	adv, _ := seedWriteOffCandidate(t, f, 62, 9000, 120)

	code, rr := requestWriteOff(t, f, &ops, adv, "uncollectable")
	if code != http.StatusOK || rr.WriteOffID == "" {
		t.Fatalf("request: %d %+v", code, rr)
	}
	// Approve WITHOUT the CSRF header (session cookie only) → 403, even for an allowed role.
	if code := f.call(t, &session{cookie: finance.cookie}, "POST",
		"/v1/portal/ops/collections/"+rr.WriteOffID+"/approve-writeoff", `{}`); code != http.StatusForbidden {
		t.Fatalf("approve without CSRF must be 403, got %d", code)
	}
	// The write-off is still REQUESTED (the CSRF-less attempt changed nothing), and a proper
	// CSRF'd approve then succeeds.
	if _, st := writeOffApprovedBy(t, f, rr.WriteOffID); st != "REQUESTED" {
		t.Fatalf("CSRF-blocked approve must not mutate; write-off state=%s", st)
	}
	if code, ec := approveWriteOff(t, f, &finance, rr.WriteOffID, "", "", ""); code != http.StatusOK {
		t.Fatalf("CSRF'd approve must succeed: %d %s", code, ec)
	}
}

// --- 5: the dispute gate (debt vs service, override audited) --------------------------------

func TestWriteOff_DisputeGate_DebtVsService_OverrideAudited(t *testing.T) {
	f := newPortalFixture(t, "wo_dispute")
	ops := f.login(t, roleKeys["OPS"])
	finance := f.login(t, roleKeys["FINANCE"])

	// (Debt dispute) an OPEN DISPUTED_ADVANCE complaint gates the write-off (seeded
	// collections.policy: DISPUTED_ADVANCE/DISPUTED_RECOVERY gate; override allowed).
	advDebt, subDebt := seedWriteOffCandidate(t, f, 70, 9000, 120)
	openComplaintCat(t, f, subDebt, "DISPUTED_ADVANCE", 70)
	code, rr := requestWriteOff(t, f, &ops, advDebt, "uncollectable")
	if code != http.StatusOK {
		t.Fatalf("request (debt): %d", code)
	}
	if !rr.Disputed {
		t.Fatalf("the request response must warn the maker of the open debt dispute (disputed=true)")
	}
	// Approve WITHOUT an override → soft-blocked, override required.
	if code, ec := approveWriteOff(t, f, &finance, rr.WriteOffID, "", "", ""); code != http.StatusConflict || ec != "WRITEOFF_DISPUTE_OVERRIDE_REQUIRED" {
		t.Fatalf("disputed write-off without override must be 409 OVERRIDE_REQUIRED, got %d %s", code, ec)
	}
	// Approve WITH an override (reason + complaint id) → proceeds, and the deliberate override is
	// audited on the checker BEFORE the loss crystallises.
	if code, ec := approveWriteOff(t, f, &finance, rr.WriteOffID, "customer contacted, debt confirmed valid", "cmp_wo_70", ""); code != http.StatusOK {
		t.Fatalf("disputed write-off WITH audited override must succeed: %d %s", code, ec)
	}
	var overrides int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE action='writeoff.dispute_override' AND target_id=$1 AND actor=$2`,
		advDebt, finance.actor).Scan(&overrides); err != nil {
		t.Fatalf("audit read: %v", err)
	}
	if overrides != 1 {
		t.Fatalf("exactly one dispute-override audit row (on the checker) must exist, got %d", overrides)
	}
	if st, out := advanceStateOutstanding(t, f, advDebt); st != "WRITTEN_OFF" || out != 0 {
		t.Fatalf("overridden write-off must still crystallise: state=%s outstanding=%d", st, out)
	}

	// (Unrelated service complaint) a SERVICE complaint is NOT a debt dispute — the gate must not
	// fire, so a legitimate write-off proceeds with no override.
	advSvc, subSvc := seedWriteOffCandidate(t, f, 71, 8000, 120)
	openComplaintCat(t, f, subSvc, "SERVICE", 71)
	code, rr2 := requestWriteOff(t, f, &ops, advSvc, "uncollectable")
	if code != http.StatusOK {
		t.Fatalf("request (service): %d", code)
	}
	if rr2.Disputed {
		t.Fatalf("a SERVICE complaint is not a debt dispute — disputed must be false")
	}
	if code, ec := approveWriteOff(t, f, &finance, rr2.WriteOffID, "", "", ""); code != http.StatusOK {
		t.Fatalf("a service complaint must NOT gate the write-off: got %d %s", code, ec)
	}
}

// The zero-config floor for the money door: if the governed collections.policy is unresolvable,
// the dispute gate FAILS SAFE — a debt dispute hard-blocks even a supplied override (override
// disallowed by the strict default). A config gap can never silently open the gate.
func TestWriteOff_DisputeGate_ZeroConfigFloor_HardBlocks(t *testing.T) {
	f := newPortalFixture(t, "wo_zerocfg")
	ops := f.login(t, roleKeys["OPS"])
	finance := f.login(t, roleKeys["FINANCE"])

	// Simulate a governance gap: remove the seeded collections.policy entirely.
	if _, err := f.db.Admin.Exec(context.Background(),
		`DELETE FROM config_versions WHERE domain='collections.policy'`); err != nil {
		t.Fatalf("delete collections.policy: %v", err)
	}

	advDebt, subDebt := seedWriteOffCandidate(t, f, 80, 9000, 120)
	openComplaintCat(t, f, subDebt, "DISPUTED_ADVANCE", 80) // a debt dispute (strict-default category)

	code, rr := requestWriteOff(t, f, &ops, advDebt, "uncollectable")
	if code != http.StatusOK {
		t.Fatalf("request: %d", code)
	}
	if !rr.Disputed {
		t.Fatalf("even with no config, the strict-default debt categories must flag the dispute (disputed=true)")
	}
	// With config absent, override is DISALLOWED by the fail-safe default → hard block, even
	// though a full override was supplied.
	if code, ec := approveWriteOff(t, f, &finance, rr.WriteOffID, "override attempt", "cmp_wo_80", ""); code != http.StatusConflict || ec != "WRITEOFF_DISPUTE_BLOCKED" {
		t.Fatalf("config gap must FAIL SAFE (hard block even with override), want 409 WRITEOFF_DISPUTE_BLOCKED, got %d %s", code, ec)
	}
	// The advance is untouched — no crystallisation slipped through.
	if st, _ := advanceStateOutstanding(t, f, advDebt); st == "WRITTEN_OFF" {
		t.Fatalf("a blocked write-off must NOT crystallise; advance state=%s", st)
	}
}

// --- 6: the approvals inbox ----------------------------------------------------------------

func TestWriteOff_Inbox_ListsPending(t *testing.T) {
	f := newPortalFixture(t, "wo_inbox")
	ops := f.login(t, roleKeys["OPS"])
	finance := f.login(t, roleKeys["FINANCE"])

	adv1, _ := seedWriteOffCandidate(t, f, 90, 9000, 120)
	adv2, _ := seedWriteOffCandidate(t, f, 91, 8000, 120)
	_, r1 := requestWriteOff(t, f, &ops, adv1, "uncollectable 1")
	_, r2 := requestWriteOff(t, f, &ops, adv2, "uncollectable 2")
	if r1.WriteOffID == "" || r2.WriteOffID == "" {
		t.Fatalf("both requests must open write-offs: %+v %+v", r1, r2)
	}

	type inboxRow struct {
		WriteOffID  string `json:"write_off_id"`
		AdvanceID   string `json:"advance_id"`
		RequestedBy string `json:"requested_by"`
		Principal   struct {
			AmountMinor int64  `json:"amount_minor"`
			Display     string `json:"display"`
		} `json:"principal"`
	}
	var inbox struct {
		Pending []inboxRow `json:"pending"`
	}
	code, raw := f.callBody(t, &finance, "GET", "/v1/portal/ops/collections/writeoffs", "")
	if code != http.StatusOK {
		t.Fatalf("inbox: %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &inbox); err != nil {
		t.Fatalf("unmarshal inbox: %v — %s", err, raw)
	}
	if len(inbox.Pending) != 2 {
		t.Fatalf("inbox must list the 2 pending write-offs, got %d", len(inbox.Pending))
	}
	byID := map[string]inboxRow{}
	for _, r := range inbox.Pending {
		byID[r.WriteOffID] = r
		if r.RequestedBy != ops.actor {
			t.Fatalf("requested_by must be the maker %q, got %q", ops.actor, r.RequestedBy)
		}
		if r.Principal.Display == "" {
			t.Fatalf("principal must carry a governed money display, got empty (%s)", r.WriteOffID)
		}
	}
	if _, ok := byID[r1.WriteOffID]; !ok {
		t.Fatalf("inbox missing the first pending write-off")
	}

	// After a checker decides one, it leaves the pending queue.
	if code, ec := approveWriteOff(t, f, &finance, r1.WriteOffID, "", "", ""); code != http.StatusOK {
		t.Fatalf("approve one: %d %s", code, ec)
	}
	code, raw = f.callBody(t, &finance, "GET", "/v1/portal/ops/collections/writeoffs", "")
	if code != http.StatusOK {
		t.Fatalf("inbox re-read: %d %s", code, raw)
	}
	_ = json.Unmarshal(raw, &inbox)
	if len(inbox.Pending) != 1 || inbox.Pending[0].WriteOffID != r2.WriteOffID {
		t.Fatalf("after approving one, exactly the other must remain pending, got %d", len(inbox.Pending))
	}
}
