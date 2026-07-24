package handler_test

// Phase 1 S2.3b — the HELD-recharge review queue through the portal: RBAC
// (finance-only), operator-scope enforcement on the explicit telco, the
// four-eyes journey end to end (finance requests, a DISTINCT operator approves,
// the event ingests), and same-actor refusal surfacing as 409.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/platform"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	"github.com/jackc/pgx/v5"
)

// seedPortalHold parks one held recharge for SIM_NG and returns its id.
func seedPortalHold(t *testing.T, f *portalFixture, src string) string {
	t.Helper()
	tctx := platform.WithTenant(context.Background(), "SIM_NG")
	var heldID string
	if err := repo.WithTenantTx(tctx, f.db.App, func(tx pgx.Tx) error {
		if _, err := (repo.HeldRecharge{}).Hold(context.Background(), tx, repo.HeldEvent{
			TelcoID: "SIM_NG", SourceEventID: src, MSISDNToken: "tok_portal_hold",
			AmountMinor: 75_000_000, Currency: "NGN", OccurredAt: time.Now().UTC(),
			Reason: repo.HeldReasonPerEventClamp,
		}); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT held_id FROM held_recharge_events WHERE source_event_id=$1`, src).Scan(&heldID)
	}); err != nil {
		t.Fatal(err)
	}
	return heldID
}

func TestS23b_HeldQueue_FourEyesJourney(t *testing.T) {
	f := newPortalFixture(t, "portal_held")
	id := seedPortalHold(t, f, "wh:ph1")

	fin := f.login(t, roleKeys["FINANCE"])
	admin := f.login(t, roleKeys["ADMIN"])

	// The queue lists the hold (finance).
	code, body := f.callBody(t, &fin, "GET", "/v1/portal/finance/held-recharges?telco=SIM_NG", "")
	if code != http.StatusOK || !strings.Contains(string(body), id) {
		t.Fatalf("finance must see the held queue: %d %s", code, body)
	}

	// OPS is denied by RBAC (deny-by-default map).
	opsSess := f.login(t, roleKeys["OPS"])
	if code, _ := f.callBody(t, &opsSess, "GET", "/v1/portal/finance/held-recharges?telco=SIM_NG", ""); code != http.StatusForbidden {
		t.Fatalf("OPS must be denied the held queue, got %d", code)
	}

	// Maker requests release.
	code, body = f.callBody(t, &fin, "POST", "/v1/portal/finance/held-recharges/"+id+"/request-release",
		`{"telco":"SIM_NG","reason":"verified bulk recharge with MNO"}`)
	if code != http.StatusOK {
		t.Fatalf("request-release: %d %s", code, body)
	}

	// Same actor approving is refused (four-eyes).
	code, body = f.callBody(t, &fin, "POST", "/v1/portal/finance/held-recharges/"+id+"/approve-release",
		`{"telco":"SIM_NG"}`)
	if code != http.StatusConflict {
		t.Fatalf("same-actor approve must 409, got %d %s", code, body)
	}

	// A DISTINCT operator approves: the event ingests and the hold closes.
	code, body = f.callBody(t, &admin, "POST", "/v1/portal/finance/held-recharges/"+id+"/approve-release",
		`{"telco":"SIM_NG"}`)
	if code != http.StatusOK || !strings.Contains(string(body), "RELEASED") {
		t.Fatalf("distinct-actor approve must release: %d %s", code, body)
	}
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id='wh:ph1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("release must ingest exactly one recovery event, got %d", n)
	}
}

func TestS23b_ScopeRefusal_OtherTelcoOperator(t *testing.T) {
	f := newPortalFixture(t, "portal_held_scope")
	seedPortalHold(t, f, "wh:ph2")

	// A FINANCE operator scoped to a DIFFERENT telco must be refused.
	admins := &repo.Admins{Pool: f.db.Admin}
	if err := admins.CreateWithRole(context.Background(), "adm_scoped1", "fin_other_telco",
		"portal-key-fin-scoped-01", "FINANCE", "telco:OTHER_NG"); err != nil {
		t.Fatal(err)
	}
	scoped := f.login(t, "portal-key-fin-scoped-01")
	code, body := f.callBody(t, &scoped, "GET", "/v1/portal/finance/held-recharges?telco=SIM_NG", "")
	if code != http.StatusForbidden {
		t.Fatalf("an out-of-scope operator must be refused (PORTAL_FORBIDDEN), got %d %s", code, body)
	}
	if code, _ := f.callBody(t, &scoped, "POST", "/v1/portal/finance/held-recharges/hld_x/request-release",
		fmt.Sprintf(`{"telco":%q,"reason":"r"}`, "SIM_NG")); code != http.StatusForbidden {
		t.Fatalf("out-of-scope mutation must be refused, got %d", code)
	}
}

func TestS23b_Reject_ClosesWithoutIngest(t *testing.T) {
	f := newPortalFixture(t, "portal_held_reject")
	id := seedPortalHold(t, f, "wh:ph3")
	fin := f.login(t, roleKeys["FINANCE"])

	code, body := f.callBody(t, &fin, "POST", "/v1/portal/finance/held-recharges/"+id+"/reject",
		`{"telco":"SIM_NG","reason":"suspected scaling bug in feed"}`)
	if code != http.StatusOK {
		t.Fatalf("reject: %d %s", code, body)
	}
	var n int
	if err := f.db.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM recovery_events WHERE source_event_id='wh:ph3'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a rejected hold must never ingest, got %d", n)
	}
}
