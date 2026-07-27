package simseed

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestImportFence_ProdBinariesDoNotImportSimseed (design §3, fence c): cmd/api and
// cmd/worker must NEVER import internal/simseed. The synthetic always-CONFIRMED telco
// adapter lives here behind a build tag; this fence makes it structurally impossible
// for a production binary to depend on the package at all — belt to the build tag's
// braces. Untagged so it runs in every build.
func TestImportFence_ProdBinariesDoNotImportSimseed(t *testing.T) {
	root := repoRoot(t)
	const forbidden = "github.com/ArowuTest/telco-credit-platform/backend/internal/simseed"
	for _, cmd := range []string{"backend/cmd/api", "backend/cmd/worker"} {
		dir := filepath.Join(root, cmd)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("fence target %s not found (repo layout changed?): %v", cmd, err)
		}
		fset := token.NewFileSet()
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == forbidden {
					t.Errorf("%s imports %s — cmd/api and cmd/worker must NOT import internal/simseed (fence c)", path, forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", cmd, err)
		}
	}
}

// repoRoot walks up from this test file to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
	}
}
