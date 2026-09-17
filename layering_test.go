package gotato

import (
	"go/build"
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
	forbidden := []string{
		modulePath + "/service",
		modulePath + "/adapter",
		modulePath + "/cmd",
		modulePath + "/gateway",
	}
	for _, name := range []string{"session", "modelctx", "toolregistry", "testkit"} {
		for _, imported := range imports(filepath.Join(root, name)) {
			for _, bad := range forbidden {
				if strings.HasPrefix(imported, bad) {
					t.Fatalf("%s imports %q; standard runtime packages must not depend on the service layer", name, imported)
				}
			}
		}
	}
}
