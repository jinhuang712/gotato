package gotato

import (
	"go/build"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const modulePath = "github.com/jinhuang712/gotato"

// TestLayering enforces DESIGN.md G-D27: the root package imports only the
// standard library, and standard runtime packages never import the optional
// service layer or the CLI.
func TestLayering(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(file)
	imports := func(dir string) []string {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		return pkg.Imports
	}
	for _, imported := range imports(root) {
		if strings.HasPrefix(imported, modulePath) || strings.Contains(imported, ".") {
			t.Fatalf("root package imports %q; it may import only the standard library", imported)
		}
	}
	forbiddenForInner := []string{modulePath + "/service"}
	forbiddenForAll := []string{modulePath + "/adapter", modulePath + "/cmd"}

	// Standard runtime and provider-adapter packages depend inward only: they
	// may use the service layer only through the root contract, never import it.
	// The checked set is derived from the module tree so a new standard-runtime
	// package is covered without editing this test; cmd/ and adapter/ are the
	// layers that are allowed to reach outward.
	for _, dir := range modulePackageDirs(t, root) {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		rel = filepath.ToSlash(rel)
		forbidden := append([]string{}, forbiddenForInner...)
		forbidden = append(forbidden, forbiddenForAll...)
		if rel == "service" || strings.HasPrefix(rel, "service/") {
			// The service layer and its HTTP adapter may reach inward, but not
			// the gRPC adapter module or the CLI.
			forbidden = forbiddenForAll
		}
		for _, imported := range imports(dir) {
			for _, bad := range forbidden {
				if strings.HasPrefix(imported, bad) {
					t.Fatalf("%s imports %q; runtime packages must depend inward", rel, imported)
				}
			}
		}
	}
}

// modulePackageDirs returns every package directory under root except the CLI
// and adapter layers, which are allowed to import outward. Nested modules
// (adapter/grpc) live below the excluded adapter layer and are skipped with it.
func modulePackageDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return fs.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			if rel == "cmd" || rel == "adapter" ||
				strings.HasPrefix(rel, "cmd"+string(filepath.Separator)) ||
				strings.HasPrefix(rel, "adapter"+string(filepath.Separator)) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		if dir == root || seen[dir] {
			return nil
		}
		seen[dir] = true
		dirs = append(dirs, dir)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module tree: %v", err)
	}
	return dirs
}
