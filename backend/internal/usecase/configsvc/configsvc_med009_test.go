package configsvc_test

// BX-MED-009: the config content hash must be a CANONICAL, DB-reproducible digest. content is a
// jsonb column, so PG normalises it on storage (keys sorted, duplicates collapsed, whitespace
// stripped, numbers normalised). Hashing the raw request bytes made the hash (a) diverge for two
// semantically-identical drafts and (b) impossible to reproduce from the stored row — so it could
// never back an integrity check. Hashing the jsonb-canonical form fixes both.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/entity"
)

func TestBXMED009_ConfigContentHash_CanonicalAndReproducible(t *testing.T) {
	svc, _ := newSvc(t, "cfg_med009")
	ctx := context.Background()

	// Two drafts, same domain/scope, that are SEMANTICALLY identical but differ only in key order,
	// whitespace, and number spelling (1e2 vs 100) — exactly what jsonb normalises away.
	a, err := svc.CreateDraft(ctx, "platform.outbox", entity.ScopeGlobal, "alice", "v1",
		[]byte(`{"claim_batch_size":100,"max_attempts":5,"retry_backoff_seconds":10}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateDraft(ctx, "platform.outbox", entity.ScopeGlobal, "alice", "v2",
		[]byte(`{ "max_attempts": 5 ,
		          "retry_backoff_seconds": 10,
		          "claim_batch_size": 1e2 }`))
	if err != nil {
		t.Fatal(err)
	}

	// (1) Stable content identity: reordered / reformatted but equal content hashes IDENTICALLY.
	// Mutation proof: revert CreateDraft to sha256(raw content) and these two diverge — RED.
	if a.ContentHash != b.ContentHash {
		t.Fatalf("BX-MED-009: semantically-identical drafts must hash identically\n a=%s\n b=%s", a.ContentHash, b.ContentHash)
	}

	// (2) Reproducible integrity: the hash re-derived from the STORED jsonb matches content_hash —
	// impossible under the old raw-bytes hash, since the column never holds those bytes.
	for _, id := range []string{a.ConfigVersionID, b.ConfigVersionID} {
		ok, err := svc.VerifyContentHash(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("BX-MED-009: stored content of %s must re-hash to its content_hash", id)
		}
	}

	// (3) Distinct content still hashes distinctly — the canonical hash is not a constant.
	c, err := svc.CreateDraft(ctx, "platform.outbox", entity.ScopeGlobal, "alice", "v3",
		[]byte(`{"claim_batch_size":101,"max_attempts":5,"retry_backoff_seconds":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentHash == a.ContentHash {
		t.Fatal("BX-MED-009: a materially different config must not collide with another's hash")
	}
}
