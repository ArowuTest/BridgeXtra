package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSelectedRolesDefaultAndSubset(t *testing.T) {
	got, err := selectedRoles("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"correctness", "security", "postgres-concurrency", "architecture-recovery", "premise-provenance"}
	var slugs []string
	for _, r := range got {
		slugs = append(slugs, r.Slug)
	}
	if !reflect.DeepEqual(slugs, want) {
		t.Fatalf("default roles=%v want=%v", slugs, want)
	}

	got, err = selectedRoles("security,correctness,security")
	if err != nil {
		t.Fatal(err)
	}
	slugs = slugs[:0]
	for _, r := range got {
		slugs = append(slugs, r.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"security", "correctness"}) {
		t.Fatalf("subset roles=%v", slugs)
	}

	if _, err := selectedRoles("bogus"); err == nil {
		t.Fatal("unknown role must fail")
	}
}

func TestDetectMigrationHead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backend", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0084_a.sql", "0007_old.sql", "0085_b.sql", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := detectMigrationHead(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "0085_b.sql" {
		t.Fatalf("got %q want 0085_b.sql", got)
	}
}
