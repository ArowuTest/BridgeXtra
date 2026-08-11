package handler_test

// BX-MED-006: the structural fence that actually bounds which tenant's shared bucket can be spent.
//
// WHY THIS EXISTS INSTEAD OF AN RLS POLICY. The obvious protection for a tenant-keyed table is a
// row-level-security policy with WITH CHECK (telco_id = current_setting('app.telco_id')). On THIS
// path that predicate would be a tautology: repo/tx.go sets the app.telco_id GUC from the very same
// telcoID variable a caller passes, and the bucket row's telco_id would be written from that same
// variable in the same call frame — so the check evaluates x = x and cannot fail, INCLUDING for the
// bug it purports to prevent (a caller-influenced telco reaching the limiter, which would flow into
// both sides of the comparison and pass). A policy like that is worse than none: it reads as tenant
// isolation in review while proving nothing.
//
// The real bounds are two. First, the FK from rate_limit_buckets.telco_id to telcos, which is
// structural and enforced by Postgres. Second, provenance: the telco handed to the aggregate must
// come ONLY from platform.TenantFrom — the validated tenant that TenantAuth (or webhookAuth) stamped
// after resolving a credential — and never from a request field, path segment, header or body. This
// test holds that second property, which is the one a future edit can silently break.
//
// It scans the AST rather than the source text: a regex cannot distinguish AllowTelco(ctx, s, telcoID)
// where telcoID came from TenantFrom from one where it came from r.PathValue("telco").

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

// med006TenantSource reports whether fn contains an assignment whose right-hand side is a call to
// platform.TenantFrom binding the given identifier.
func med006TenantSource(fn *ast.FuncDecl, ident string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		// Does this assignment bind `ident`?
		binds := false
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == ident {
				binds = true
			}
		}
		if !binds {
			return true
		}
		for _, rhs := range as.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "platform" && sel.Sel.Name == "TenantFrom" {
				found = true
			}
		}
		return true
	})
	return found
}

func TestBXMED006_AggregateTelcoComesOnlyFromValidatedTenant(t *testing.T) {
	root := ".."            // backend/internal
	callsChecked := 0       // anti-vacuity: a fence that scanned nothing is not a fence
	filesScanned := 0       //
	var violations []string //

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
			// Hard failure, not a skip. The tree compiles (go build gates this suite), so an
			// unparseable file is an anomaly — and a fence that quietly skips what it cannot read is
			// a fence with holes exactly where someone might hide a violation.
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
				if !ok || sel.Sel.Name != "AllowTelco" {
					return true
				}
				callsChecked++
				// AllowTelco(ctx, scope, telcoID) — the third argument is the tenant.
				if len(call.Args) < 3 {
					violations = append(violations, path+": AllowTelco called with too few arguments")
					return true
				}
				id, ok := call.Args[2].(*ast.Ident)
				if !ok {
					violations = append(violations,
						path+": AllowTelco's telco argument is an expression, not a plain identifier bound from platform.TenantFrom")
					return true
				}
				if !med006TenantSource(fn, id.Name) {
					violations = append(violations,
						path+": AllowTelco's telco argument "+id.Name+" is not bound from platform.TenantFrom in "+fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Anti-vacuity. If the scan found no files, or no AllowTelco call at all, this test would pass
	// while proving nothing — including after someone deletes the aggregate entirely.
	if filesScanned < 50 {
		t.Fatalf("fence scanned only %d files; the walk root is wrong and this test proves nothing", filesScanned)
	}
	if callsChecked == 0 {
		t.Fatal("fence found NO AllowTelco call sites — either the aggregate rate limit has been " +
			"removed from the request path, or this fence no longer matches it. Both are failures.")
	}
	if len(violations) > 0 {
		t.Fatalf("the aggregate rate limit's telco must come only from the validated tenant "+
			"(platform.TenantFrom); the FK on rate_limit_buckets is the only other bound:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
