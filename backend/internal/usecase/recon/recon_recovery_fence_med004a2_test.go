package recon

// BX-MED-004-A2 structural fence #1 — writeQualification's single legitimate call site.
//
// writeQualification is unexported: Go's own visibility rules already prove no OTHER
// package can call it. What they do NOT prove is (a) that THIS package never grows a
// second call site — e.g. a new code path that persists a qualification without
// dayConfirmed having verified it — or (b) that the one call it has today stays inside
// a genuine "dayConfirmed just returned true" branch, not an unconditional path or the
// else branch.
//
// This is a cardinality + branch-position claim ("Y is reachable ONLY from inside X's
// true-branch"). BX-MED-004-A1's fence (recon_recovery_fence_med004a1_test.go) proves a
// structurally different thing — single-entry forward reachability ("can entry E reach
// forbidden target F") — which cannot prove an inverse/cardinality claim like this one.
// The right precedent is BX-MED-006's fence (aggregate_telco_fence_med006_test.go):
// exhaustive whole-tree call-site enumeration with anti-vacuity thresholds. This is that
// pattern, scoped to this package (unexported name — no call site can exist elsewhere).
//
// Branch-position is checked by position-range containment: collect every IfStmt whose
// Cond contains a call to dayConfirmed anywhere in its expression tree, record that
// IfStmt's Body's own [Pos,End) range (which — by Go's grammar — excludes its Else
// branch, a sibling node positioned after Body.End()), then require every
// writeQualification call site's position to fall inside at least one such range.

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

// med004a2ConfirmedBranchRanges returns the [Pos,End) ranges of every IfStmt body,
// anywhere in fn, whose condition contains a call to dayConfirmed.
func med004a2ConfirmedBranchRanges(fn *ast.FuncDecl) []struct{ start, end token.Pos } {
	var ranges []struct{ start, end token.Pos }
	ast.Inspect(fn, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		condCallsDayConfirmed := false
		ast.Inspect(ifs.Cond, func(cn ast.Node) bool {
			call, ok := cn.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "dayConfirmed" {
				condCallsDayConfirmed = true
			}
			return true
		})
		if condCallsDayConfirmed {
			ranges = append(ranges, struct{ start, end token.Pos }{ifs.Body.Pos(), ifs.Body.End()})
		}
		return true
	})
	return ranges
}

func med004a2PosInRanges(pos token.Pos, ranges []struct{ start, end token.Pos }) bool {
	for _, r := range ranges {
		if pos >= r.start && pos < r.end {
			return true
		}
	}
	return false
}

func TestMED004A2_WriteQualificationOnlyFromPostConfirmBranch(t *testing.T) {
	root := "."
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
			ranges := med004a2ConfirmedBranchRanges(fn)
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "writeQualification" {
					return true
				}
				callsChecked++
				pos := fset.Position(call.Pos())
				if fn.Name.Name != "RunRecoveryControl" {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: writeQualification called from %s, not RunRecoveryControl",
						path, pos.Line, fn.Name.Name))
					return true
				}
				if !med004a2PosInRanges(call.Pos(), ranges) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: writeQualification called outside an `if dayConfirmed(...)` true-branch",
						path, pos.Line))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Anti-vacuity: a scan that read nothing, or found no call at all, would pass while
	// proving nothing — including after someone deletes writeQualification's only caller.
	if filesScanned < 3 {
		t.Fatalf("fence scanned only %d non-test .go files in %s — the walk root is wrong "+
			"and this test proves nothing", filesScanned, root)
	}
	if callsChecked == 0 {
		t.Fatal("fence found NO call site of writeQualification — either it was removed/renamed, " +
			"or this fence no longer matches it. Both are failures.")
	}
	if callsChecked != 1 {
		t.Fatalf("writeQualification must have EXACTLY ONE call site in this package, found %d — a "+
			"second call site could persist a qualification without dayConfirmed having verified it",
			callsChecked)
	}
	if len(violations) > 0 {
		t.Fatalf("writeQualification's call site must be inside RunRecoveryControl's "+
			"post-dayConfirmed-true branch:\n  %s", strings.Join(violations, "\n  "))
	}
}

// The mutation-grade control: prove the matcher isn't vacuously satisfied by a Cond
// check that never actually fires. A synthetic FuncDecl with writeQualification called
// OUTSIDE any dayConfirmed branch must be flagged by the same range logic.
func TestMED004A2_ConfirmedBranchRangesRejectsUnguardedCall(t *testing.T) {
	src := `package recon
func fakeCaller(s *Service) {
	s.writeQualification(nil, "", "", recoveryCfg{})
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok {
		t.Fatal("synthetic source did not parse to a single FuncDecl")
	}
	ranges := med004a2ConfirmedBranchRanges(fn)
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "writeQualification" {
			return true
		}
		found = true
		if med004a2PosInRanges(call.Pos(), ranges) {
			t.Fatal("matcher found the unguarded synthetic call INSIDE a dayConfirmed branch — " +
				"the range logic is vacuous and the primary test proves nothing")
		}
		return true
	})
	if !found {
		t.Fatal("synthetic source's own writeQualification call was not located — test setup is broken")
	}
}

// BX-MED-004-A2 structural fence #2 (findings F2 / F3 / F3b) — the money-gate publisher's config
// resolution must never depend on the Go process clock. Publish is the sole function that resolves
// which recon.recovery config version is "currently effective" at publish time; since finding F3b
// that resolution lives INLINE in Publish's final publication statement, using Postgres's own
// statement_timestamp() (the earlier currentRecoveryCfg/recoveryConfigContentAt helpers were folded
// into that single statement and removed). Publish must therefore contain NO call to time.Now()
// anywhere in its body — the only clock it may consult is Postgres's, embedded directly in the SQL
// text. A skewed worker process clock must never be able to select an older/looser governed config
// than the one the database itself considers effective right now, and must never stamp the money
// gate from a process-side timestamp.
//
// Whole-package scan (not a single hardcoded filename) so this survives a future file reorganization
// — matches the pattern above. Anti-vacuity: the named function must actually be found
// (funcsChecked == 1), or this fence is silently checking nothing.

func TestMED004A2_ConfigResolutionNeverCallsProcessClock(t *testing.T) {
	root := "."
	filesScanned := 0
	funcsChecked := 0
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
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		filesScanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name != "Publish" {
				continue
			}
			funcsChecked++
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == "time" && sel.Sel.Name == "Now" {
					pos := fset.Position(call.Pos())
					violations = append(violations, fmt.Sprintf(
						"%s:%d: %s calls time.Now() — config resolution must use ONLY Postgres's own "+
							"now(), never the process clock", path, pos.Line, fn.Name.Name))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if filesScanned < 3 {
		t.Fatalf("fence scanned only %d non-test .go files in %s — the walk root is wrong and this "+
			"test proves nothing", filesScanned, root)
	}
	if funcsChecked != 1 {
		t.Fatalf("fence checked %d functions, want exactly 1 (Publish) — it was renamed/removed/duplicated "+
			"and this fence no longer matches what it claims to", funcsChecked)
	}
	if len(violations) > 0 {
		t.Fatalf("money-gate config resolution must never call time.Now():\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// Mutation-grade control: the SAME matcher, run against a synthetic function that DOES call
// time.Now(), must catch it. Without this, a typo in the "time"/"Now" comparison would make the
// primary test vacuously pass forever.
func TestMED004A2_ProcessClockMatcherDetectsKnownCaller(t *testing.T) {
	src := `package recon
import "time"
func Publish() time.Time {
	return time.Now()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" && sel.Sel.Name == "Now" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("matcher failed to detect a known time.Now() call in synthetic source — the primary test is vacuous")
	}
}
