package handler_test

// GATE-CHK-3 (EXT-1 recurrence-proof, generalized in the self-audit): the
// structural guarantee that NO parallel, RBAC-bypassing door to the operator
// console can be (re)introduced. Two invariants, enforced at the source so
// they hold for routes that don't exist yet:
//
//  1. Every /v1/portal/ route reaches its handler ONLY through p.mountRBAC(...)
//     — which requires a routeRoles entry. A portal route wired DIRECTLY on the
//     mux (mux.Handle("/v1/portal/…", …)) bypasses RBAC + scope. The only two
//     exceptions are login and logout, mounted directly BY DESIGN (login mints
//     the session; logout only needs one). This covers config, risk, finance —
//     every operator capability, not just the config door that EXT-1 exposed.
//  2. The original EXT-1 vector — a "config" door at ANY path (it lived at
//     /v1/admin/config/*) — stays forbidden regardless of prefix.
//
// mountRBAC's own internal mux.Handle uses a VARIABLE pattern, so it is not a
// string literal and is never flagged; only hard-coded bypass patterns are.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ArowuTest/telco-credit-platform/backend/internal/handler"
)

// wiring files where HTTP routes are registered: the handler package (Mount
// methods) and the API's mux assembly.
func wiringGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
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
	files = append(files, filepath.Join("..", "..", "cmd", "api", "main.go"))
	return files
}

// scanBypassDoors reports direct ServeMux registrations (mux.Handle/HandleFunc
// with a STRING-LITERAL pattern) that bypass the RBAC chain: any /v1/portal/
// route other than login/logout, or any route whose path mentions "config".
func scanBypassDoors(fset *token.FileSet, f *ast.File) []string {
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
		pat, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		// login/logout are the only portal routes mounted directly, by design.
		if pat == "POST /v1/portal/login" || pat == "POST /v1/portal/logout" {
			return true
		}
		low := strings.ToLower(pat)
		if strings.Contains(low, "/v1/portal/") || strings.Contains(low, "config") {
			out = append(out, fset.Position(call.Pos()).String()+": "+pat)
		}
		return true
	})
	return out
}

func TestGATECHK3_NoPortalRouteMountedOutsideRBAC(t *testing.T) {
	fset := token.NewFileSet()
	var violations []string
	for _, path := range wiringGoFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		violations = append(violations, scanBypassDoors(fset, f)...)
	}
	if len(violations) > 0 {
		t.Fatalf("RBAC-bypass door(s) mounted directly on the mux (must go through mountRBAC):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// Positive controls: the scanner MUST catch each bypass shape, so the guard is
// provably not vacuously green.
func TestGATECHK3_ScannerCatchesDoors(t *testing.T) {
	cases := map[string]string{
		"legacy config door (EXT-1)": `mux.Handle("POST /v1/admin/config/drafts", h)`,
		"portal bypass (treasury)":   `mux.Handle("POST /v1/portal/risk/trips/{id}/approve-rearm", h)`,
		"portal bypass (settlement)": `mux.HandleFunc("POST /v1/portal/finance/settlements/{id}/verify", nil)`,
	}
	for label, stmt := range cases {
		src := "package x\nimport \"net/http\"\nfunc wire(mux *http.ServeMux, h http.Handler) {\n\t" + stmt + "\n}"
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := scanBypassDoors(fset, f); len(got) != 1 {
			t.Errorf("%s: scanner must catch this door; got %d hits: %v", label, len(got), got)
		}
	}
	// And it must NOT flag the two sanctioned direct mounts.
	for _, ok := range []string{
		`mux.HandleFunc("POST /v1/portal/login", p.login)`,
		`mux.Handle("POST /v1/portal/logout", h)`,
	} {
		src := "package x\nimport \"net/http\"\nfunc wire(mux *http.ServeMux, h http.Handler, p *struct{ login http.HandlerFunc }) {\n\t" + ok + "\n}"
		fset := token.NewFileSet()
		f, _ := parser.ParseFile(fset, "synthetic.go", src, 0)
		if got := scanBypassDoors(fset, f); len(got) != 0 {
			t.Errorf("login/logout must NOT be flagged: %v", got)
		}
	}
}

// Every config-MUTATION route in the RBAC map must be ADMIN-only — a
// non-admin operator can never draft/submit/approve/activate money-config.
func TestGATECHK3_ConfigMutationsAreAdminOnly(t *testing.T) {
	for route, roles := range handler.RBACRoutes() {
		if !strings.Contains(route, "/config/") || !strings.HasPrefix(route, "POST ") {
			continue
		}
		if len(roles) != 1 || roles[0] != "ADMIN" {
			t.Errorf("config-mutation route %q must be ADMIN-only, got %v", route, roles)
		}
	}
}
