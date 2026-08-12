package recoveryfeed

// BX-MED-004-A1 fence residual-gap closure #2 (design_MED-004_v11_FINAL.md).
// recoveryfeed.Adapter is a real, runtime-dispatched interface squarely on
// RunRecoveryControl's call path (adapter.FetchDay(...) inside
// reconcileRecoveryDayWith) — a static named-call fence built inside package
// recon cannot prove interface dispatch stays clean, because it cannot see
// which concrete implementation will be bound at runtime.
//
// Closed here, narrowly, per the design's explicit instruction: a cheap,
// grep-based check scoped to the two known implementers (*MockAdapter,
// *HTTPAdapter), not a full analysis. If a THIRD implementer of Adapter is
// ever added, this test does not automatically cover it — that is a named,
// accepted residual gap closed by review convention (any new
// recoveryfeed.Adapter implementation must be reviewed against this
// invariant specifically), not by this fence.
//
// This checks the two identifiers directly rather than banning the package's
// (legitimate, unrelated) import of internal/repo — recoveryfeed.go uses
// repo.WithTenantTx for its own tenant-scoped feed reads, which has nothing to
// do with the arming gate. Banning the whole import would be a check against a
// premise this package doesn't actually satisfy, not a real guarantee.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// med004a1ForbiddenNames returns the forbidden identifier found in src, or ""
// if src is clean — the matcher shared by the real fence and its own
// mutation-grade positive control below.
func med004a1ForbiddenNames(src string) string {
	if strings.Contains(src, "AdvanceFreshness") {
		return "AdvanceFreshness"
	}
	if strings.Contains(src, "ReconArming") {
		return "ReconArming"
	}
	return ""
}

func TestMED004A1_RecoveryfeedNeverReachesAdvanceFreshness(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate package directory")
	}
	dir := filepath.Dir(thisFile)
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(matches) == 0 {
		t.Fatalf("no .go files found in %s — fence cannot run", dir)
	}
	var sawImplementerFile bool
	checked := 0
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue // this file and its siblings are allowed to reference the forbidden names
		}
		checked++
		if strings.HasSuffix(path, string(filepath.Separator)+"recoveryfeed.go") {
			sawImplementerFile = true
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if bad := med004a1ForbiddenNames(string(b)); bad != "" {
			t.Fatalf("BX-MED-004-A1 fence violated: %s references %s — the recoveryfeed package "+
				"(reached via the recoveryfeed.Adapter interface on RunRecoveryControl's call path) "+
				"must never touch the freshness gate", path, bad)
		}
	}
	if checked == 0 {
		t.Fatal("every .go file in the package matched the _test.go exclusion — fence checked nothing")
	}
	if !sawImplementerFile {
		t.Fatal("recoveryfeed.go (MockAdapter/HTTPAdapter's home) was not among the checked files — " +
			"fence did not check what it claims to check")
	}
}

// The mutation-grade control: the SAME matcher, run against synthetic source
// text that DOES contain the forbidden identifiers, must find them. Without
// this, a typo in med004a1ForbiddenNames could make the primary test
// vacuously green forever.
func TestMED004A1_ForbiddenNamesMatcherDetectsKnownViolations(t *testing.T) {
	if got := med004a1ForbiddenNames(`func x() { s.Arming.AdvanceFreshness(ctx, id, layer, 1) }`); got != "AdvanceFreshness" {
		t.Fatalf("matcher failed to detect a direct AdvanceFreshness call, got %q", got)
	}
	if got := med004a1ForbiddenNames(`type x struct { arm *repo.ReconArming }`); got != "ReconArming" {
		t.Fatalf("matcher failed to detect a ReconArming reference, got %q", got)
	}
	if got := med004a1ForbiddenNames(`func FetchDay(ctx context.Context) error { return nil }`); got != "" {
		t.Fatalf("matcher false-positived on clean source, got %q", got)
	}
}
