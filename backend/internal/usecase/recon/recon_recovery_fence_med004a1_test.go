package recon

// BX-MED-004-A1 structural fence — design_MED-004_v11_FINAL.md, item "the fence".
//
// RunRecoveryControl must never advance recon_layer_arming.last_recon_at. This
// proves it the way the codebase's other fences do (MED-006's telco fence,
// MED-007's log-token fence, the portal gate-check): a go/ast walk, here backed
// by go/types (via golang.org/x/tools/go/packages) so a call through an
// interface value or a differently-named local variable still resolves to the
// real, cross-package method being invoked.
//
// SCOPE (deliberately, not accidentally, narrow — see the design doc): this
// proves no code reachable from RunRecoveryControl via ORDINARY NAMED FUNCTION
// CALLS AND METHOD CALLS ON STATICALLY-KNOWN CONCRETE TYPES invokes
// (*repo.ReconArming).AdvanceFreshness. It does NOT follow:
//   - reflection — closed separately by TestMED004A1_NoReflectImport (an
//     import ban on `reflect` within this package, checked below);
//   - interface dispatch through recoveryfeed.Adapter — closed by review
//     convention + the two-implementer targeted test in recoveryfeed_test.go
//     (neither MockAdapter nor HTTPAdapter references AdvanceFreshness/
//     ReconArming at all, so this is not a live path today);
//   - a bound method value stored in a struct field and invoked later — the
//     one on-path instance of this pattern in the codebase is
//     layerSpec.fetchPlatform (populated by recoverySpec()), whose body is
//     verified to be DB-reads-only.
// A1 introduces NO new production write path — LegacyRecoveryPublisher,
// unchanged, remains the only thing that stamps last_recon_at — so this fence
// exists to catch an accidental regression during the refactor, not to gate a
// live new authority. MED-004-A2's fenced publisher is a new write path with
// real authority and gets the stronger fail-closed, module-bounded call-graph
// guarantee this design deliberately defers to that tranche.
//
// TestMED004A1_FenceMatcherDetectsKnownCaller is the mutation-grade half of
// this fence: it runs the IDENTICAL walk from LegacyRecoveryPublisher — a
// function that DOES call AdvanceFreshness directly — and asserts the matcher
// finds it. Without this, a typo in the forbidden-method comparison (wrong
// package path, wrong receiver name) would make the primary test vacuously
// pass forever, the exact "vacuous fence" shape an earlier design round of
// this critique caught.

import (
	"fmt"
	"go/ast"
	"go/types"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	med004a1RecoverPkg = "github.com/ArowuTest/telco-credit-platform/backend/internal/usecase/recon"
	med004a1ForbidPkg  = "github.com/ArowuTest/telco-credit-platform/backend/internal/repo"
	med004a1ForbidRecv = "ReconArming"
	med004a1ForbidMeth = "AdvanceFreshness"
)

// med004a1LoadOnce memoizes packages.Load's result across every fence test in
// this binary. Loading with NeedDeps+NeedTypes type-checks the recon package's
// entire transitive dependency graph — genuinely expensive (multiple seconds),
// unlike this codebase's other, syntax-only go/ast.Inspect fences — so paying
// that cost twice (once per test function) rather than once is pure waste on
// an already time-boxed (10-minute, -p 2) package test binary.
var (
	med004a1LoadOnce  sync.Once
	med004a1LoadedPkg *packages.Package
	med004a1LoadErr   error
)

// med004a1LoadPackage loads the recon package's own (non-test) syntax with full
// type information, so named calls and method calls can be resolved to the
// declaring package + receiver type + method name, not just an identifier.
func med004a1LoadPackage(t *testing.T) *packages.Package {
	t.Helper()
	med004a1LoadOnce.Do(func() {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
				packages.NeedSyntax | packages.NeedDeps | packages.NeedImports | packages.NeedModule,
		}
		pkgs, err := packages.Load(cfg, med004a1RecoverPkg)
		if err != nil {
			med004a1LoadErr = fmt.Errorf("packages.Load(%s): %w", med004a1RecoverPkg, err)
			return
		}
		if packages.PrintErrors(pkgs) > 0 {
			med004a1LoadErr = fmt.Errorf("packages.Load(%s) reported errors (see above)", med004a1RecoverPkg)
			return
		}
		if len(pkgs) != 1 {
			med004a1LoadErr = fmt.Errorf("expected exactly 1 package for %s, got %d", med004a1RecoverPkg, len(pkgs))
			return
		}
		med004a1LoadedPkg = pkgs[0]
	})
	if med004a1LoadErr != nil {
		t.Fatal(med004a1LoadErr)
	}
	return med004a1LoadedPkg
}

// med004a1FuncIndex maps every function/method DECLARED in pkg (by its *types.Func
// object) to its *ast.FuncDecl, so the walk can descend into a callee that is
// local to this package.
func med004a1FuncIndex(pkg *packages.Package) map[types.Object]*ast.FuncDecl {
	idx := map[types.Object]*ast.FuncDecl{}
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if obj := pkg.TypesInfo.Defs[fd.Name]; obj != nil {
					idx[obj] = fd
				}
			}
		}
	}
	return idx
}

// med004a1FindFunc locates the *ast.FuncDecl for a top-level function named
// name in pkg (RunRecoveryControl / LegacyRecoveryPublisher are both package-
// level methods with no overloads, so name is unambiguous here).
func med004a1FindFunc(idx map[types.Object]*ast.FuncDecl, name string) *ast.FuncDecl {
	for obj, fd := range idx {
		if obj.Name() == name {
			return fd
		}
	}
	return nil
}

// med004a1ReachesForbidden walks every named call and concrete-receiver method
// call transitively reachable from entry (following calls to functions/methods
// declared in THIS package via idx; a call to anything outside the package is
// checked but not followed further). It returns the call path (function names,
// outermost first) to the forbidden method, or nil if it is not reachable.
func med004a1ReachesForbidden(pkg *packages.Package, idx map[types.Object]*ast.FuncDecl, entry *ast.FuncDecl, entryName string) []string {
	visited := map[*ast.FuncDecl]bool{}
	var found []string
	var walk func(fd *ast.FuncDecl, path []string)
	walk = func(fd *ast.FuncDecl, path []string) {
		if found != nil || visited[fd] {
			return
		}
		visited[fd] = true
		ast.Inspect(fd, func(n ast.Node) bool {
			if found != nil {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var calleeIdent *ast.Ident
			switch f := call.Fun.(type) {
			case *ast.Ident:
				calleeIdent = f
			case *ast.SelectorExpr:
				calleeIdent = f.Sel
			default:
				return true
			}
			obj := pkg.TypesInfo.Uses[calleeIdent]
			if obj == nil {
				return true
			}
			fn, ok := obj.(*types.Func)
			if !ok {
				return true
			}
			if med004a1IsForbidden(fn) {
				found = append(append([]string{}, path...), fn.FullName())
				return false
			}
			if callFd, ok := idx[fn]; ok {
				walk(callFd, append(append([]string{}, path...), fn.Name()))
			}
			return true
		})
	}
	walk(entry, []string{entryName})
	return found
}

// med004a1IsForbidden reports whether fn IS (*repo.ReconArming).AdvanceFreshness,
// matched structurally (declaring package path + receiver type name + method
// name) rather than by a fragile pre-formatted string, so the comparison can't
// silently drift out of sync with go/types' own formatting.
func med004a1IsForbidden(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	if fn.Name() != med004a1ForbidMeth {
		return false
	}
	recvType := sig.Recv().Type()
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == med004a1ForbidPkg && obj.Name() == med004a1ForbidRecv
}

func TestMED004A1_NoNamedCallPathToAdvanceFreshness(t *testing.T) {
	pkg := med004a1LoadPackage(t)
	idx := med004a1FuncIndex(pkg)

	entry := med004a1FindFunc(idx, "RunRecoveryControl")
	if entry == nil {
		t.Fatal("RunRecoveryControl not found in package syntax — fence cannot run")
	}
	if path := med004a1ReachesForbidden(pkg, idx, entry, "RunRecoveryControl"); path != nil {
		t.Fatalf("BX-MED-004-A1 fence violated: RunRecoveryControl reaches %s.%s via named-call path %v",
			med004a1ForbidRecv, med004a1ForbidMeth, path)
	}
}

// The mutation-grade control: the SAME matcher, run from a function that DOES
// call AdvanceFreshness directly, must find it. Proves the primary test isn't
// vacuously green.
func TestMED004A1_FenceMatcherDetectsKnownCaller(t *testing.T) {
	pkg := med004a1LoadPackage(t)
	idx := med004a1FuncIndex(pkg)

	entry := med004a1FindFunc(idx, "LegacyRecoveryPublisher")
	if entry == nil {
		t.Fatal("LegacyRecoveryPublisher not found in package syntax — fence cannot run")
	}
	path := med004a1ReachesForbidden(pkg, idx, entry, "LegacyRecoveryPublisher")
	if path == nil {
		t.Fatal("fence matcher failed to detect LegacyRecoveryPublisher's own direct call to " +
			"AdvanceFreshness — the matcher is vacuous and TestMED004A1_NoNamedCallPathToAdvanceFreshness " +
			"proves nothing")
	}
}

// I5 (fence residual gap #1, named): reflection could call AdvanceFreshness
// through no named call this fence would ever see. Close it structurally by
// banning the reflect import in this package's non-test source.
func TestMED004A1_NoReflectImport(t *testing.T) {
	pkg := med004a1LoadPackage(t)
	for _, file := range pkg.Syntax {
		for _, imp := range file.Imports {
			path := imp.Path.Value
			if path == `"reflect"` {
				fname := pkg.Fset.Position(imp.Pos()).Filename
				t.Fatalf("package recon must not import reflect (MED-004-A1 fence residual-gap "+
					"closure) — found in %s", fname)
			}
		}
	}
}
