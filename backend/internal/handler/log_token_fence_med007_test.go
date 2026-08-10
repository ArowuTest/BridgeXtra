package handler_test

// BX-MED-007 (follow-up): pin the audit acceptance permanently with a STRUCTURAL scan — no raw
// subscriber token may reach a structured log or error-telemetry call.
//
// MED-007 removed tokens from URLs and masked them at the known log sites. That closes the sites
// that existed on the day; it does not stop the next one being added. This fence walks the backend
// AST and fails the build if any logging call is passed a subscriber-token expression that is not
// wrapped in platform.MaskToken.
//
// AST rather than regex, deliberately: a regex over source text cannot tell `MaskToken(tok)` from
// `tok` inside a larger expression, and would either miss violations or drown the build in false
// positives. Walking CallExpr arguments answers the real question — "is this identifier handed to a
// log call, and is it masked on the way?"

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Method names on a logger (slog.Logger and the slog package function forms).
var med007LogMethods = map[string]bool{
	"Info": true, "Warn": true, "Error": true, "Debug": true,
	"InfoContext": true, "WarnContext": true, "ErrorContext": true, "DebugContext": true,
}

// Identifier fragments that denote a RAW subscriber token. Deliberately narrow: these name the
// token itself, not an id or a masked form.
func med007IsTokenIdent(name string) bool {
	l := strings.ToLower(name)
	switch {
	case l == "msisdn" || l == "msisdntoken" || l == "msisdn_token":
		return true
	case strings.HasPrefix(l, "msisdntoken"), strings.HasPrefix(l, "msisdn_token"):
		return true
	case l == "tok" || l == "token" || l == "fulltoken" || l == "rawtoken":
		return true
	}
	return false
}

// masked reports whether the expression is (or contains) a MaskToken call — the sanctioned way to
// put token-derived text in a log.
func med007Masked(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if strings.Contains(fn.Sel.Name, "Mask") {
				found = true
			}
		case *ast.Ident:
			if strings.Contains(fn.Name, "Mask") || strings.Contains(fn.Name, "mask") {
				found = true
			}
		}
		return !found
	})
	return found
}

// tokenish reports whether the expression references a raw-token identifier or selector.
func med007Tokenish(e ast.Expr) bool {
	hit := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			if med007IsTokenIdent(v.Name) {
				hit = true
			}
		case *ast.SelectorExpr:
			if med007IsTokenIdent(v.Sel.Name) {
				hit = true
			}
		}
		return !hit
	})
	return hit
}

func TestBXMED007_NoRawSubscriberTokenInLogs(t *testing.T) {
	roots := []string{
		filepath.Join("..", ".."),       // backend/internal/...
		filepath.Join("..", "..", ".."), // backend/cmd/... (walk filters below)
	}
	seen := map[string]bool{}
	scanned := 0
	fset := token.NewFileSet()

	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil //nolint:nilerr // an unreadable path is not a finding
			}
			if info.IsDir() {
				switch info.Name() {
				case "node_modules", ".git", ".next", "testdata", "migrations", "docs", "portal", "outputs":
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Two roots overlap, so de-duplicate by absolute path where possible; if the path
			// cannot be absolutised, the raw path is still a stable key for this walk.
			key := path
			if abs, aerr := filepath.Abs(path); aerr == nil {
				key = abs
			}
			if seen[key] {
				return nil
			}
			seen[key] = true

			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil //nolint:nilerr // not our concern here; the compiler is the parser of record
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !med007LogMethods[sel.Sel.Name] {
					return true
				}
				for _, arg := range call.Args {
					if med007Tokenish(arg) && !med007Masked(arg) {
						pos := fset.Position(arg.Pos())
						t.Errorf("BX-MED-007 %s:%d — a raw subscriber token reaches a %s log call. "+
							"Wrap it in platform.MaskToken (subscriber tokens are PII and logs are retained).",
							filepath.ToSlash(path), pos.Line, sel.Sel.Name)
					}
				}
				return true
			})
			return nil
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no Go sources — the fence is not actually looking at anything")
	}
	t.Logf("BX-MED-007 log fence scanned %d Go source files", scanned)
}
