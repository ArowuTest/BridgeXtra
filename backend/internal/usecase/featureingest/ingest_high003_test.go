package featureingest

// BX-HIGH-003: the telco kill-switch reaches the feature-feed boundary too. A SUSPENDED
// telco's feed must be refused up front — before the file is even parsed — so no feature
// cut for a halted telco can land. Asserting the error REASON (not merely that it errored)
// isolates the gate from the file-validity checks that would reject a bad file anyway.

import (
	"context"
	"strings"
	"testing"
)

func TestBXHIGH003_SuspendedTelcoFeedRefused(t *testing.T) {
	svc, db, _ := setup(t, "feat_h003")
	ctx := context.Background()

	// Flip the kill-switch on the seeded ACTIVE telco the setup uses.
	if _, err := db.Admin.Exec(ctx, `UPDATE telcos SET status='SUSPENDED' WHERE telco_id='SIM_NG'`); err != nil {
		t.Fatal(err)
	}

	// The feed is refused BY THE KILL-SWITCH (not incidentally by a later validity check).
	// Mutation proof: remove the telco-active gate and this same call fails with "no as_of"
	// instead — the "not ACTIVE" reason disappears.
	_, err := svc.IngestRaw(ctx, "SIM_NG", "h003-suspended", []byte(`{"telco_id":"SIM_NG"}`))
	if err == nil || !strings.Contains(err.Error(), "not ACTIVE") {
		t.Fatalf("a suspended telco's feature feed must be refused by the kill-switch (BX-HIGH-003), got: %v", err)
	}
}
