package origination_test

// R-P0-7 adversarial pack: consent must be PROVEN by channel evidence, not
// inferred. A confirm must echo the exact disclosure the customer was shown and
// carry channel/session/acceptance evidence. Every way of NOT proving consent —
// a dropped session, a missing or foreign disclosure reference, a disallowed
// channel, an acceptance outside the disclosure's validity window — is refused,
// and no advance or financial effect is created. The programme cannot even
// serve an offer without an active disclosure policy (fail-closed).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/origination"
)

// The disclosure snapshot minted at menu time carries the exact rendered terms,
// is tied to the offer's lifetime, and its content hash is non-empty.
func TestRP07_DisclosureSnapshot_MintedWithOffer(t *testing.T) {
	f := newFixture(t, "rp07_mint", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	d := ov.Disclosure
	if d.DisclosureSnapshotID == "" || d.ContentHash == "" {
		t.Fatalf("offer must carry a hashed disclosure snapshot: %+v", d)
	}
	if d.OfferID != ov.Offer.OfferID || !d.ExpiresAt.Equal(ov.Offer.ExpiresAt) {
		t.Fatalf("disclosure must bind the offer and share its lifetime: %+v", d)
	}
	// The rendered body must actually disclose the repayment total.
	if want := ov.Offer.Repayment.String(); !strings.Contains(d.RenderedBody, want) {
		t.Fatalf("rendered disclosure must state the repayment %q: %q", want, d.RenderedBody)
	}
}

func TestRP07_MissingDisclosureRef_Refused(t *testing.T) {
	f := newFixture(t, "rp07_noref", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	cmd := acceptFor(ov, "tok_sim_0001", "rp07-noref", "cor")
	cmd.DisclosureRef = ""
	_, err := f.svc.Confirm(tenantCtx(), cmd)
	if !errors.Is(err, origination.ErrDisclosureRequired) {
		t.Fatalf("a confirm without a disclosure reference must be refused, got %v", err)
	}
	assertNoAdvance(t, f)
}

func TestRP07_DroppedSession_Refused(t *testing.T) {
	f := newFixture(t, "rp07_nosess", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	// A dropped USSD session: the disclosure was shown but no session/acceptance
	// evidence came back. Consent is not proven — refuse.
	cmd := acceptFor(ov, "tok_sim_0001", "rp07-nosess", "cor")
	cmd.SessionID = ""
	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrAcceptanceEvidenceMissing) {
		t.Fatalf("a dropped session must be refused, got %v", err)
	}
	// Same for a missing acceptance timestamp.
	cmd2 := acceptFor(ov, "tok_sim_0001", "rp07-nots", "cor")
	cmd2.AcceptedAt = time.Time{}
	if _, err := f.svc.Confirm(tenantCtx(), cmd2); !errors.Is(err, origination.ErrAcceptanceEvidenceMissing) {
		t.Fatalf("a missing acceptance timestamp must be refused, got %v", err)
	}
	assertNoAdvance(t, f)
}

func TestRP07_ForeignDisclosureRef_Mismatch(t *testing.T) {
	f := newFixture(t, "rp07_foreign", 0, 2_000)
	offers := f.offersFor(t, "tok_sim_0001")
	if len(offers) < 2 {
		t.Fatal("need >=2 offers")
	}
	// Echo the disclosure of a DIFFERENT offer than the one being confirmed —
	// the terms shown for offer[1] cannot authorise offer[0].
	cmd := acceptFor(offers[0], "tok_sim_0001", "rp07-foreign", "cor")
	cmd.DisclosureRef = offers[1].Disclosure.DisclosureSnapshotID
	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrDisclosureMismatch) {
		t.Fatalf("a disclosure reference for another offer must be refused, got %v", err)
	}
	// A random, non-existent reference is likewise a mismatch, never a bypass.
	cmd.DisclosureRef = "dsc_does_not_exist"
	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrDisclosureMismatch) {
		t.Fatalf("a fabricated disclosure reference must be refused, got %v", err)
	}
	assertNoAdvance(t, f)
}

func TestRP07_ChannelNotAllowed_Refused(t *testing.T) {
	f := newFixture(t, "rp07_chan", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	cmd := acceptFor(ov, "tok_sim_0001", "rp07-chan", "cor")
	cmd.Channel = "IVR" // not in the seeded allowed_channels [USSD, APP]
	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrChannelNotAllowed) {
		t.Fatalf("a channel outside the disclosure policy must be refused, got %v", err)
	}
	assertNoAdvance(t, f)
}

func TestRP07_AcceptanceOutsideWindow_Refused(t *testing.T) {
	f := newFixture(t, "rp07_window", 0, 2_000)
	ov := f.offersFor(t, "tok_sim_0001")[0]
	// Acceptance stamped an hour in the future — well past the disclosure's
	// validity (and impossible), so consent cannot be attributed to it.
	cmd := acceptFor(ov, "tok_sim_0001", "rp07-window", "cor")
	cmd.AcceptedAt = time.Now().UTC().Add(time.Hour)
	if _, err := f.svc.Confirm(tenantCtx(), cmd); !errors.Is(err, origination.ErrDisclosureExpired) {
		t.Fatalf("an acceptance outside the disclosure window must be refused, got %v", err)
	}
	assertNoAdvance(t, f)
}

// Fail-closed: with no active disclosure policy the programme cannot disclose
// its terms, so it must not serve (or confirm) an advance at all.
func TestRP07_NoDisclosurePolicy_FailsClosed(t *testing.T) {
	f := newFixture(t, "rp07_nopolicy", 0, 2_000)
	// Retire the seeded disclosure policy (admin surgery — no runtime role can).
	if _, err := f.db.Admin.Exec(context.Background(),
		`UPDATE config_versions SET state='SUPERSEDED', effective_to=now()
		 WHERE domain='disclosure.policy' AND state='ACTIVE'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetOffers(tenantCtx(), "prg_sim_airtime01", "tok_sim_0001"); !errors.Is(err, origination.ErrDisclosureUnavailable) {
		t.Fatalf("no disclosure policy must fail closed at OFFER, got %v", err)
	}
}

func assertNoAdvance(t *testing.T, f *fixture) {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(context.Background(), `SELECT count(*) FROM advances`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a refused confirm must create no advance, got %d", n)
	}
}
