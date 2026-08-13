package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func makeCLITestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "candidate.md", "Review this bounded change")
	writeTestFile(t, root, "backend/migrations/0085_test.sql", "-- m")
	writeTestFile(t, root, "review-council/context/PRODUCT.md", "product")
	writeTestFile(t, root, "review-council/context/ENGINEERING_GUARDRAILS.md", "guard")
	writeTestFile(t, root, "review-council/context/CURRENT_STATUS.md", "status")
	writeTestFile(t, root, "review-council/ADJUDICATION.md", "rules")
	for _, r := range roleRegistry {
		writeTestFile(t, root, r.PromptPath, r.Name+" lens")
	}
	initGitRepo(t, root)
	return root
}

func TestRunCLIDryRunUsesNoNetworkAndWritesPrompts(t *testing.T) {
	root := makeCLITestRepo(t)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_BASE_URL", "http://127.0.0.1:1")
	out := filepath.Join(root, "out")
	var stdout, stderr bytes.Buffer
	err = runCLI(context.Background(), []string{"--dry-run", "--candidate", "candidate.md", "--base", "HEAD", "--include", "candidate.md", "--roles", "correctness,security", "--out", out}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("dry run: %v stderr=%s", err, stderr.String())
	}
	for _, name := range []string{"prompt-correctness.txt", "prompt-security.txt", "manifest.json", "adjudication-packet.md"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestRunCLILiveRequiresAPIKey(t *testing.T) {
	root := makeCLITestRepo(t)
	old, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv("OPENROUTER_API_KEY", "")
	var stdout, stderr bytes.Buffer
	if err := runCLI(context.Background(), []string{"--candidate", "candidate.md", "--base", "HEAD", "--include", "candidate.md", "--out", filepath.Join(root, "out")}, &stdout, &stderr); err == nil {
		t.Fatal("expected missing API key error")
	}
}
