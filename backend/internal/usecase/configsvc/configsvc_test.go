package configsvc_test

// Config lifecycle (V2-CFG-001/002/006), maker-checker, decision pinning
// (V1-CFG-007), SF-2 concurrency guard, SF-5 TTL floor.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func newSvc(t *testing.T, suffix string) (*configsvc.Service, *testutil.DB) {
	db := testutil.MustSetup(t, suffix)
	// Config writes use the worker/admin path in M0; the service gets the
	// worker pool (INSERT/UPDATE granted) — matching production wiring.
	return configsvc.New(db.Worker), db
}

func TestV2_CFG_002_MakerCannotApproveOwnChange(t *testing.T) {
	svc, _ := newSvc(t, "cfg_makerchecker")
	ctx := context.Background()

	c, err := svc.CreateDraft(ctx, "platform.outbox", entity.ScopeGlobal, "alice", "tune batch",
		[]byte(`{"claim_batch_size":100,"max_attempts":5,"retry_backoff_seconds":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	err = svc.Approve(ctx, c.ConfigVersionID, "alice")
	if err == nil || !strings.Contains(err.Error(), "maker-checker") {
		t.Fatalf("maker approving own change must fail, got: %v", err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("distinct approver must succeed: %v", err)
	}
}

func TestLifecycle_ActivateSupersedesAndPins(t *testing.T) {
	svc, _ := newSvc(t, "cfg_lifecycle")
	ctx := context.Background()
	// Activation boundary strictly after the seed's effective_from (set at
	// migration time moments ago): history is queried just before `at`.
	at := time.Now().UTC().Add(2 * time.Second)

	// The seeded outbox default is ACTIVE; activating v2 must supersede it atomically.
	c, err := svc.CreateDraft(ctx, "platform.outbox", entity.ScopeGlobal, "alice", "bigger batches",
		[]byte(`{"claim_batch_size":200,"max_attempts":8,"retry_backoff_seconds":15}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Activate(ctx, c.ConfigVersionID, "bob", at); err != nil {
		t.Fatal(err)
	}

	// ActiveAt after the boundary resolves the new version; just before the
	// boundary resolves the seed — historical decisions stay pinned to what
	// was effective then (V1-CFG-007).
	cur, err := svc.ActiveAt(ctx, "platform.outbox", entity.ScopeGlobal, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if cur.ConfigVersionID != c.ConfigVersionID {
		t.Fatalf("current active = %s, want %s", cur.ConfigVersionID, c.ConfigVersionID)
	}
	old, err := svc.ActiveAt(ctx, "platform.outbox", entity.ScopeGlobal, at.Add(-time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if old.ConfigVersionID == c.ConfigVersionID {
		t.Fatal("historical lookup must resolve the superseded version, not the new one")
	}
}

func TestSF2_ConcurrencyAboveOne_RejectedWhileSchemaBackstopInForce(t *testing.T) {
	svc, _ := newSvc(t, "cfg_sf2")
	ctx := context.Background()

	// advances table does not exist yet (M0): fail-safe floor still rejects >1.
	c, err := svc.CreateDraft(ctx, "product.concurrency", entity.ScopeGlobal, "alice", "try 3",
		[]byte(`{"max_concurrent_advances":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	err = svc.Approve(ctx, c.ConfigVersionID, "bob")
	if err == nil || !strings.Contains(err.Error(), "ADR-0001 SF-2") {
		t.Fatalf("SF-2: N>1 must be rejected with the schema-backstop explanation, got: %v", err)
	}

	// Value 1 passes.
	c1, err := svc.CreateDraft(ctx, "product.concurrency", entity.ScopeGlobal, "alice", "explicit 1",
		[]byte(`{"max_concurrent_advances":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c1.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, c1.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("value 1 must be approvable: %v", err)
	}
}

func TestSF5_IdempotencyTTL_FloorEnforced(t *testing.T) {
	svc, _ := newSvc(t, "cfg_sf5")
	ctx := context.Background()

	for _, bad := range []string{
		`{"ttl_hours":48,"min_floor_hours":72}`, // ttl below floor
		`{"ttl_hours":96,"min_floor_hours":24}`, // floor below hard floor
	} {
		c, err := svc.CreateDraft(ctx, "platform.idempotency", entity.ScopeGlobal, "alice", "shrink", []byte(bad))
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
			t.Fatal(err)
		}
		if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err == nil {
			t.Fatalf("SF-5: %s must be rejected", bad)
		}
	}
}

func TestSeededDefaultsExist(t *testing.T) {
	// V1 no-hardcoding: every domain the code reads has a seeded ACTIVE default.
	svc, _ := newSvc(t, "cfg_seeds")
	ctx := context.Background()
	for _, domain := range []string{"platform.idempotency", "product.concurrency", "platform.outbox"} {
		if _, err := svc.ActiveAt(ctx, domain, entity.ScopeGlobal, time.Now().UTC()); err != nil {
			t.Errorf("missing seeded default for %s: %v", domain, err)
		}
	}
}
