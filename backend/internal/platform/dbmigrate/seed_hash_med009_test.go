package dbmigrate_test

// BX-MED-009 (regression): EVERY governed config row must reproduce its own evidence hash.
//
// MED-009 made configsvc hash the DB-CANONICAL jsonb form rather than the raw request bytes, so
// VerifyContentHash can re-derive the hash from storage. That closed the APPLICATION write path.
// It did not cover the other way rows get into config_versions: migration seeds, which INSERT
// directly and compute content_hash from the compact SQL literal with
// encode(sha256('{...}'::bytea),'hex').
//
// Those two are not the same bytes. Postgres rewrites jsonb on storage — keys reordered (by length,
// then bytewise), insignificant whitespace stripped or ADDED (jsonb renders `{"a": 1, "b": 2}` with
// spaces), numbers normalised. A compact literal therefore hashes to something the stored row can
// never reproduce, and the seeded config fails the governance integrity check it exists to support.
//
// This test is deliberately GENERIC: it walks every row rather than naming the one that was wrong.
// A test that asserted only `cfg_seed_ratelimit_v3` would pass the moment that row was corrected
// while leaving every other seed — and every future seed written by copying an existing migration,
// which is exactly how this spread — silently unverifiable.
//
// IT LIVES IN dbmigrate, NOT configsvc, for two reasons. It is a property of the MIGRATION-SEEDED
// corpus, which is what this package is responsible for. And configsvc is already one of the
// slowest packages in the suite (~473s); CI runs `go test -race` with no explicit -timeout, so
// Go's 600s per-package default applies, and adding a from-zero fixture there would have turned CI
// red on a timeout that had nothing to do with the invariant.

import (
	"context"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/testutil"
	"github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/configsvc"
)

func TestBXMED009_EverySeededConfigReproducesItsCanonicalHash(t *testing.T) {
	db := testutil.MustSetup(t, "med009_seed_hash")
	ctx := context.Background()

	// Cover the APPLICATION write path in the same invariant, not just the migration seeds. The
	// content below is deliberately non-canonical — padded whitespace, and keys in an order jsonb
	// will not preserve — so a writer that hashed the raw request bytes would land a row this check
	// rejects, exactly as the seeds did.
	svc := configsvc.New(db.Config)
	if _, err := svc.CreateDraft(ctx, "platform.outbox", "global", "med009-test", "med009 app-path coverage",
		[]byte(`{ "zzz_last_key" : 1,  "a" : 2,   "middle_length_key": 3 }`)); err != nil {
		t.Fatalf("create draft through configsvc: %v", err)
	}

	// The canonical form is exactly what configsvc hashes: the value of the jsonb column rendered
	// as text (canonicalJSONB does `SELECT $1::jsonb` and hashes the returned bytes). Re-deriving it
	// here in SQL keeps the test independent of the Go code path it is checking — if this test
	// simply called the same helper, it could only prove the helper agrees with itself.
	rows, err := db.Admin.Query(ctx, `
		SELECT config_version_id,
		       domain,
		       content_hash,
		       encode(sha256(content::text::bytea), 'hex') AS canonical_hash
		  FROM config_versions
		 ORDER BY config_version_id`)
	if err != nil {
		t.Fatalf("query config_versions: %v", err)
	}
	defer rows.Close()

	type bad struct{ id, domain, stored, want string }
	var mismatches []bad
	total := 0
	for rows.Next() {
		var id, domain, stored, want string
		if err := rows.Scan(&id, &domain, &stored, &want); err != nil {
			t.Fatalf("scan: %v", err)
		}
		total++
		if stored != want {
			mismatches = append(mismatches, bad{id, domain, stored, want})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// Anti-vacuity: a green run over zero rows proves nothing, and would hide a migration set that
	// failed to seed at all.
	if total < 10 {
		t.Fatalf("only %d config_versions rows found; the seed migrations did not run and this test "+
			"is not checking anything", total)
	}

	if len(mismatches) > 0 {
		t.Errorf("%d of %d governed config rows cannot reproduce their stored evidence hash from the "+
			"canonical jsonb they actually hold (BX-MED-009). A seed must hash content::text, not the "+
			"raw SQL literal:", len(mismatches), total)
		for _, m := range mismatches {
			t.Errorf("  %s (%s)\n    stored:    %s\n    canonical: %s", m.id, m.domain, m.stored, m.want)
		}
	}
}
