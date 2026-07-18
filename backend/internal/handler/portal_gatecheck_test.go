package handler_test

// GATE-CHK-3 (EXT-1 recurrence-proof): the structural guarantee that the
// role-unaware parallel config door can never be reintroduced. Config routes
// may reach configsvc ONLY through the RBAC-gated portal chain — i.e. via
// p.mountRBAC(...), which requires a routeRoles entry. A config route wired
// directly on the mux (mux.Handle("…/config/…", …)) bypasses RBAC and is
// exactly the EXT-1 defect; this test fails the build if one appears.
//
// The scan is at the SOURCE level because the guarantee is structural: it must
// hold for routes that don't exist yet, which a runtime probe can't cover.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
)

// wiring files where HTTP routes are registered: the handler package (Mount
// methods) and the API's mux assembly.
func wiringGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	// handler package (current dir), non-test sources
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	// the API mux assembly
	files = append(files, filepath.Join("..", "..", "cmd", "api", "main.go"))
	return files
}

// scanConfigDoors reports direct ServeMux registrations of a config route
// (mux.Handle/HandleFunc with a string pattern containing "config"). A
// non-literal pattern — as in mountRBAC's internal mux.Handle(pattern, …) — is
// sanctioned and not reported. Returns human-readable violation strings.
func scanConfigDoors(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true // variable pattern (mountRBAC) — sanctioned
		}
		if strings.Contains(strings.ToLower(lit.Value), "config") {
			out = append(out, fset.Position(call.Pos()).String()+": "+lit.Value)
		}
		return true
	})
	return out
}

func TestGATECHK3_NoConfigRouteMountedOutsideRBAC(t *testing.T) {
	fset := token.NewFileSet()
	var violations []string
	for _, path := range wiringGoFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		violations = append(violations, scanConfigDoors(fset, f)...)
	}
	if len(violations) > 0 {
		t.Fatalf("EXT-1 recurrence: config route(s) mounted directly on the mux, bypassing mountRBAC:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// Positive control: the scanner MUST catch a synthetic parallel door — proves
// the guard is not vacuously green (the EXT-1 door would have looked like this).
func TestGATECHK3_ScannerCatchesADoor(t *testing.T) {
	const door = `package x
import "net/http"
func wire(mux *http.ServeMux, h http.Handler) {
	mux.Handle("POST /v1/admin/config/drafts", h) // the EXT-1 defect
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", door, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanConfigDoors(fset, f); len(got) != 1 {
		t.Fatalf("scanner must catch a synthetic config door; got %d hits: %v", len(got), got)
	}
}

// Every config-MUTATION route in the RBAC map must be ADMIN-only — a
// non-admin operator can never draft/submit/approve/activate money-config.
func TestGATECHK3_ConfigMutationsAreAdminOnly(t *testing.T) {
	mutations := []string{"/drafts", "/submit", "/approve", "/activate"}
	for route, roles := range handler.RBACRoutes() {
		if !strings.Contains(route, "/config/") && !strings.HasSuffix(route, "/config/drafts") {
			// only config routes
			if !strings.Contains(route, "/config") {
				continue
			}
		}
		isMutation := strings.HasPrefix(route, "POST ")
		relevant := false
		for _, m := range mutations {
			if strings.HasSuffix(route, m) {
				relevant = true
			}
		}
		if !isMutation || !relevant {
			continue
		}
		if len(roles) != 1 || roles[0] != "ADMIN" {
			t.Errorf("config-mutation route %q must be ADMIN-only, got %v", route, roles)
		}
	}
}
