package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		out, err := gitOutput(context.Background(), root, args...)
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", ".")
	run("commit", "-m", "base")
}

func TestBuildBundleDeterministicOrderAndLineNumbers(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "candidate.md", "candidate\n")
	writeTestFile(t, root, "src/z.go", "z1\nz2\n")
	writeTestFile(t, root, "src/a.go", "a1\na2\n")
	writeTestFile(t, root, "backend/migrations/0085_test.sql", "-- m\n")
	initGitRepo(t, root)
	writeTestFile(t, root, "src/a.go", "a1\na2 changed\n")

	p, err := captureGitProvenance(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildBundle(context.Background(), BundleInput{
		Root: root, CandidatePath: "candidate.md", BaseRef: "HEAD", Includes: []string{"src"}, MaxBytes: 100000, Provenance: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(b.Source, "FILE: src/a.go") > strings.Index(b.Source, "FILE: src/z.go") {
		t.Fatalf("source files not sorted:\n%s", b.Source)
	}
	if !strings.Contains(b.Source, "L0001 a1") || !strings.Contains(b.Source, "L0002 a2 changed") {
		t.Fatalf("line numbers missing:\n%s", b.Source)
	}
	if b.SHA256 == "" {
		t.Fatal("bundle hash empty")
	}
}

func TestBuildBundleRejectsBinaryAndBudgetOverflow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "candidate.md", "candidate")
	writeTestFile(t, root, "backend/migrations/0085_test.sql", "-- m")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "bad.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, root)
	p, err := captureGitProvenance(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildBundle(context.Background(), BundleInput{Root: root, CandidatePath: "candidate.md", BaseRef: "HEAD", Includes: []string{"src/bad.bin"}, MaxBytes: 100000, Provenance: p}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary rejection, got %v", err)
	}

	writeTestFile(t, root, "src/good.txt", strings.Repeat("x", 100))
	if _, err := buildBundle(context.Background(), BundleInput{Root: root, CandidatePath: "candidate.md", BaseRef: "HEAD", Includes: []string{"src/good.txt"}, MaxBytes: 20, Provenance: p}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget failure, got %v", err)
	}
}

func TestBuildBundleRejectsSymlinkInclude(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "candidate.md", "candidate")
	writeTestFile(t, root, "backend/migrations/0085_test.sql", "-- m")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	initGitRepo(t, root)
	p, err := captureGitProvenance(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildBundle(context.Background(), BundleInput{Root: root, CandidatePath: "candidate.md", BaseRef: "HEAD", Includes: []string{"link.txt"}, MaxBytes: 100000, Provenance: p}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestBuildPromptIncludesContextProvenanceRoleAndSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "review-council/context/PRODUCT.md", "PRODUCT-CONTEXT")
	writeTestFile(t, root, "review-council/context/ENGINEERING_GUARDRAILS.md", "GUARDRAILS")
	writeTestFile(t, root, "review-council/context/CURRENT_STATUS.md", "CURRENT-STATUS")
	writeTestFile(t, root, "review-council/roles/correctness.md", "CORRECTNESS-LENS")
	p := Provenance{RepoPath: "C:\\repo", HeadSHA: "abc", OriginMainSHA: "def", MigrationHead: "0085_x.sql", GitStatus: "CLEAN", BaseRef: "HEAD^", BaseSHA: "base"}
	b := Bundle{Candidate: "CANDIDATE", Combined: "SOURCE-BUNDLE", Source: "SOURCE-BUNDLE", SHA256: "hash"}
	prompt, err := buildPrompt(root, roleRegistry[0], p, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PRODUCT-CONTEXT", "GUARDRAILS", "CURRENT-STATUS", "CORRECTNESS-LENS", "headSHA: abc", "migrationHead: 0085_x.sql", "CANDIDATE", "SOURCE-BUNDLE"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
