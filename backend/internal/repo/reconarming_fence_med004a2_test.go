package repo

// BX-MED-004-A2 structural fence #2 — Phase-1 compensating control for AdvanceFreshness's
// remaining production authority.
//
// AdvanceFreshness is the sole structural writer of recon_layer_arming.last_recon_at during
// Phase 1 (LegacyRecoveryPublisher's call, preserved unchanged from before the MED-004
// extraction — see recon_recovery.go's own doc comment: "DELETE AT MED-004-A2 CUTOVER, when
// FencedControlPublisher becomes the sole, atomically-fenced authority+mutation writer"). This
// fence is the Phase-1-scoped compensating control: an exhaustive, whole-backend-tree scan
// (BX-MED-006's pattern — cardinality/call-site-enumeration claims need exhaustive scanning,
// not MED-004-A1's single-entry forward-reachability walk) proving every non-test call site is
// on an explicit allowlist, today exactly ONE entry (recon.LegacyRecoveryPublisher). It exists
// to catch exactly the mistake this tranche itself nearly shipped: cmd/seed-dev's own direct
// call, discovered and fixed during BX-MED-004-A2 implementation — a second, unreviewed
// production write path to the money gate, reachable entirely outside the fenced publisher.
//
// At Phase-2 cutover (LegacyRecoveryPublisher deleted, FencedControlPublisher sole authority,
// tcp_app's UPDATE grant on recon_layer_arming revoked) this fence's allowlist becomes empty and
// the whole mechanism is superseded by tcp_freshness's own DB-level grant boundary — delete this
// file then, alongside LegacyRecoveryPublisher itself.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// med004a2AdvanceFreshnessAllowlist: {file path suffix, enclosing function name}. A call site
// must match BOTH to be allowed. Kept as an explicit, named, reviewable list — not a wildcard —
// so a NEW call site anywhere (including a second one inside an already-allowlisted file) fails
// closed until a human adds it here.
var med004a2AdvanceFreshnessAllowlist = []struct{ fileSuffix, funcName string }{
	{filepath.FromSlash("usecase/recon/recon_recovery.go"), "LegacyRecoveryPublisher"},
}

func med004a2IsAllowlisted(path, funcName string) bool {
	for _, a := range med004a2AdvanceFreshnessAllowlist {
		if strings.HasSuffix(path, a.fileSuffix) && funcName == a.funcName {
			return true
		}
	}
	return false
}

func TestMED004A2_AdvanceFreshnessCallSitesAreAllowlisted(t *testing.T) {
	root := filepath.Join("..", "..") // backend/internal/repo -> backend (covers cmd/ + internal/)
	filesScanned := 0
	callsChecked := 0
	var violations []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// Hard failure, not a skip — the tree compiles (go build gates this suite), so an
			// unparseable file is an anomaly, and silently skipping what can't be read is exactly
			// where a violation could hide.
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		filesScanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AdvanceFreshness" {
					return true
				}
				callsChecked++
				pos := fset.Position(call.Pos())
				if !med004a2IsAllowlisted(path, fn.Name.Name) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: %s calls AdvanceFreshness — not on the Phase-1 allowlist",
						path, pos.Line, fn.Name.Name))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Anti-vacuity: a scan that read almost nothing, or found no call at all, would pass while
	// proving nothing — including after someone deletes AdvanceFreshness's only allowed caller.
	if filesScanned < 50 {
		t.Fatalf("fence scanned only %d files under %s — the walk root is wrong and this test proves nothing",
			filesScanned, root)
	}
	if callsChecked == 0 {
		t.Fatal("fence found NO AdvanceFreshness call site — either it was removed (Phase-2 cutover: " +
			"delete this fence too) or this fence no longer matches it. Both are failures if unintentional.")
	}
	if len(violations) > 0 {
		t.Fatalf("AdvanceFreshness must only be called from its Phase-1 allowlist:\n  %s\n"+
			"If this is a genuine, reviewed new caller, add it to med004a2AdvanceFreshnessAllowlist.",
			strings.Join(violations, "\n  "))
	}
}

// Mutation-grade control: the SAME allowlist check, exercised against a known-bad and a
// known-good (file,func) pair, must reject the former and accept the latter. Proves the
// primary test isn't vacuously green because every real call site happens to already match.
func TestMED004A2_AdvanceFreshnessAllowlistRejectsUnknownCaller(t *testing.T) {
	if med004a2IsAllowlisted(filepath.FromSlash("cmd/seed-dev/heldrecharges.go"), "seedHeldRecharges") {
		t.Fatal("matcher accepted a known-not-allowlisted (file,func) pair — the allowlist check is vacuous")
	}
	if med004a2IsAllowlisted(filepath.FromSlash("usecase/recon/recon_recovery.go"), "RunRecoveryControl") {
		t.Fatal("matcher accepted the right FILE but wrong FUNCTION — both fields must be checked, not just the file")
	}
	if !med004a2IsAllowlisted(filepath.FromSlash("usecase/recon/recon_recovery.go"), "LegacyRecoveryPublisher") {
		t.Fatal("matcher rejected the one genuine allowlisted (file,func) pair — the allowlist check is broken")
	}
}
