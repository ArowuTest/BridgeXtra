package configsvc_test

// EXT-4: a governed config draft with a field the validator does not model —
// a typo like `max_attempt` for `max_attempts`, or a stray `fee_bp` — must be
// REJECTED at approval, not silently accepted with the intended value
// defaulting to zero. Positive control: the same content without the stray
// field approves, so the rejection is provably the strict decoder.

import (
	"context"
	"testing"
)

func TestEXT4_StrictDecode_RejectsUnknownConfigField(t *testing.T) {
	svc, _ := newSvc(t, "cfg_ext4")
	ctx := context.Background()

	// All real fields are valid; a single stray field is the only difference.
	const stray = `{"claim_batch_size":50,"max_attempts":10,"retry_backoff_seconds":30,"fee_bp":123}`
	c, err := svc.CreateDraft(ctx, "platform.outbox", "global", "alice", "stray field", []byte(stray))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, c.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, c.ConfigVersionID, "bob"); err == nil {
		t.Fatal("EXT-4: a config draft with an unmodelled field must be rejected at approval")
	}

	// Positive control: identical content WITHOUT the stray field approves.
	const clean = `{"claim_batch_size":50,"max_attempts":10,"retry_backoff_seconds":30}`
	g, err := svc.CreateDraft(ctx, "platform.outbox", "global", "alice", "clean", []byte(clean))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(ctx, g.ConfigVersionID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, g.ConfigVersionID, "bob"); err != nil {
		t.Fatalf("clean content must approve (proves it's the stray field, not something else): %v", err)
	}
}
