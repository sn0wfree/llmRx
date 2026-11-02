package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The client's entire value proposition on the distribution side is
// that it cross-compiles to every platform with CGO_ENABLED=0. That
// holds only while it stays clear of internal/store and internal/
// router: both reach internal/store/sqlite.go, which blank-imports
// mattn/go-sqlite3 and would silently turn the client into a CGO
// binary again. This test fails the build the moment someone adds such
// an import — long before a release pipeline discovers it.
//
// Matching is done on exact quoted import paths so comments that merely
// mention the packages (this file, package docs) do not trip it.
func TestClientPackageStaysCGOFree(t *testing.T) {
	forbidden := []string{
		`"github.com/sn0wfree/llmRx/internal/store"`,
		`"github.com/sn0wfree/llmRx/internal/router"`,
		`"github.com/mattn/go-sqlite3"`,
	}

	roots := []string{"."}
	// cmd/gateway-client must obey the same rule.
	if wd, err := os.Getwd(); err == nil {
		cmdDir := filepath.Join(filepath.Dir(wd), "cmd", "gateway-client")
		if fi, err := os.Stat(cmdDir); err == nil && fi.IsDir() {
			roots = append(roots, cmdDir)
		}
	}

	// filepath.Walk(".") yields relative paths, so match the guard file
	// by name rather than by absolute path.
	const thisFile = "cgo_guard_test.go"

	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if filepath.Base(path) == thisFile {
				return nil // this test quotes the paths by design
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, bad := range forbidden {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s imports %s — the client must stay CGO-free", path, bad)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
