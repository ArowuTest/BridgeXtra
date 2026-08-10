package handler_test

// BX-MED-008 (REOPENED) structural fence: NO CLIENT-SIDE MONETARY ARITHMETIC USING JS Number.
//
// The first remediation made the wire format a string, then had the portal call Number() on it to
// compute utilisation ratios — reintroducing the 2^53 precision loss during the arithmetic, which
// is the actual defect. A comment cannot hold that line; this scan does.
//
// It fails the build if any portal source coerces a money value to a JS number, by ANY of the
// coercion spellings (Number/parseInt/parseFloat, unary plus, or implicit arithmetic), on either
// `amount_minor` or `display`. The allowance list is deliberately tiny and named.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The only file permitted to mention amount_minor coercion at all is the API client, which defines
// the type and the safe helpers (and whose own text explains why the coercion is forbidden).
var med008AllowedFiles = map[string]bool{
	filepath.Join("lib", "api.ts"): true,
}

var med008Forbidden = []struct {
	name string
	re   *regexp.Regexp
}{
	{"explicit coercion of a money field", regexp.MustCompile(`(Number|parseInt|parseFloat)\s*\(\s*[A-Za-z_$][\w$.\[\]]*\.(amount_minor|display)\b`)},
	{"unary-plus coercion of a money field", regexp.MustCompile(`[^\w)]\+\s*[A-Za-z_$][\w$.\[\]]*\.(amount_minor|display)\b`)},
	{"implicit arithmetic on a money field", regexp.MustCompile(`\.(amount_minor|display)\s*[-*/%]\s*[\w(]`)},
	{"comparison arithmetic on a money field", regexp.MustCompile(`\.(amount_minor|display)\s*[<>]=?\s*[\w(]`)},
	{"Math.* on a money field", regexp.MustCompile(`Math\.\w+\s*\([^)]*\.(amount_minor|display)\b`)},
}

func TestBXMED008_NoClientSideMoneyArithmetic(t *testing.T) {
	root := filepath.Join("..", "..", "..", "portal")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("portal sources not present: %v", err)
	}
	scanned := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Third-party and build output are not our source.
			switch info.Name() {
			case "node_modules", ".next", "out", "coverage", "test-results", "playwright-report":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".ts" && ext != ".tsx" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if med008AllowedFiles[rel] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue // prose about the rule is not a violation of it
			}
			for _, f := range med008Forbidden {
				if f.re.MatchString(line) {
					t.Errorf("BX-MED-008 %s:%d — %s.\nNo client-side monetary arithmetic using JS Number: render MoneyView.display, "+
						"use moneyIsPositive(m) for sign, and use the server's precomputed *_bps for ratios.\n  %s",
						rel, i+1, f.name, trimmed)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Anti-vacuity: a fence that scanned nothing proves nothing.
	if scanned == 0 {
		t.Fatal("scanned no portal sources — the fence is not actually looking at anything")
	}
	t.Logf("BX-MED-008 fence scanned %d portal source files", scanned)
}
