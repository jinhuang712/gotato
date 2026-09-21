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
	forbiddenForInner := []string{modulePath + "/service"}
	forbiddenForAll := []string{modulePath + "/adapter", modulePath + "/cmd"}

	// Standard runtime and provider-adapter packages depend inward only: they
	// may use the service layer only through the root contract, never import it.
	for _, name := range []string{"session", "modelctx", "toolregistry", "testkit", "gateway"} {
		for _, imported := range imports(filepath.Join(root, name)) {
			for _, bad := range append(append([]string{}, forbiddenForInner...), forbiddenForAll...) {
				if strings.HasPrefix(imported, bad) {
					t.Fatalf("%s imports %q; runtime packages must depend inward", name, imported)
				}
			}
		}
	}
	// The service layer and its HTTP adapter may reach inward, but not the
	// gRPC adapter module or the CLI.
	for _, name := range []string{"service", "service/httpapi"} {
		for _, imported := range imports(filepath.Join(root, name)) {
			for _, bad := range forbiddenForAll {
				if strings.HasPrefix(imported, bad) {
					t.Fatalf("%s imports %q; the service layer must not depend on adapters or the CLI", name, imported)
				}
			}
		}
	}
}
