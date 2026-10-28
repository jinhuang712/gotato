package gotato

import (
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/jinhuang712/gotato"

// allowedImports is the layering of the shipped packages: for each package
// (a path relative to the module root), the only packages of this repository
// its non-test code may import, again as paths relative to the root. It is
// the real import graph, written down. An edge missing from the table fails
// TestLayering, and so does an entry no code uses any more; so a change of
// the graph is a change of this table, made on purpose and seen in review.
//
// internal/text is the one shared package of internal/ (standard library
// only). The rest of internal/ is our test tools, which no shipped package
// may import, and so never appear here.
//
// adapter/mcp is a module of its own, but it is a shipped package, so it is
// held to the same table: it may import the core and nothing else.
var allowedImports = map[string][]string{
	"gotato":            {"internal/text"},
	"session":           {"gotato"},
	"gateway":           {"gotato"},
	"modelctx":          {"gotato", "internal/text", "session"},
	"testkit":           {"gotato", "internal/text", "session"}, // text.IsRecapAsk: the fake models recognize the Recap ask by the one definition
	"session/storetest": {"gotato", "session"},
	"service":           {"gotato", "modelctx", "session"},
	"service/httpapi":   {"gotato", "modelctx", "service", "session"},
	"adapter/mcp":       {"gotato"},
}

// standardOnly lists the packages that may import the standard library alone
// from outside this repository: the core, and the shared package it uses.
var standardOnly = []string{"gotato", "internal/text"}

// TestLayering enforces DESIGNS.md → Dependencies point inward: every shipped
// package imports only what allowedImports lists for it, the core and the
// shared package of internal/ use the standard library alone, and the module
// root holds no Go package.
func TestLayering(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(file)) // the module root: documents, and directories of packages
	if files, _ := filepath.Glob(filepath.Join(root, "*.go")); len(files) > 0 {
		t.Fatalf("the module root holds Go files %v; every package lives in a directory", files)
	}

	// Every package that ships must be in the table, so a new one cannot
	// slip past it.
	shipped := append(modulePackages(t, root), "adapter/mcp")
	for _, rel := range shipped {
		if _, ok := allowedImports[rel]; !ok {
			t.Errorf("package %s is not in allowedImports (gotato/layering_test.go); add it with the in-repo packages it may import", rel)
		}
	}

	for rel, allowed := range allowedImports {
		imported := importsOf(t, filepath.Join(root, filepath.FromSlash(rel)))
		for _, problem := range layeringProblems(rel, imported, allowed) {
			t.Error(problem)
		}
		if slices.Contains(standardOnly, rel) {
			for _, path := range imported {
				if !strings.HasPrefix(path, modulePath+"/") && strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
					t.Errorf("%s imports %q; it may import only the standard library and the packages of this repository that allowedImports lists", rel, path)
				}
			}
		}
	}

	// A shared package of internal/ imports the standard library only.
	for _, path := range importsOf(t, filepath.Join(root, "internal", "text")) {
		if strings.Contains(path, ".") {
			t.Errorf("internal/text imports %q; it may import only the standard library", path)
		}
	}
}

// layeringProblems returns one message for each edge of pkg that the table
// does not allow, and one for each allowed edge that pkg does not use. imported
// is the full list of import paths of pkg's non-test code.
func layeringProblems(pkg string, imported, allowed []string) []string {
	var problems []string
	used := map[string]bool{}
	for _, path := range imported {
		rel, ok := strings.CutPrefix(path, modulePath+"/")
		if !ok {
			continue
		}
		used[rel] = true
		if !slices.Contains(allowed, rel) {
			problems = append(problems, fmt.Sprintf(
				"%s imports %s, which allowedImports does not list for it; change the code so that it does not, or, if the dependency is meant, extend the table in gotato/layering_test.go and say why in the commit (tools under internal/ may never be added)",
				pkg, rel))
		}
	}
	for _, rel := range allowed {
		if !used[rel] {
			problems = append(problems, fmt.Sprintf(
				"allowedImports lists %s for %s, but it no longer imports it; remove the entry so that the table stays the real graph",
				rel, pkg))
		}
	}
	return problems
}

// importsOf returns the import paths of the non-test code of the package in dir.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	return pkg.Imports
}

// modulePackages returns the packages of the root module that ship, as paths
// relative to root: every directory with Go code except internal/ (our test
// tools and the shared package) and adapter/ (a module of its own, checked by
// name). A nested module (examples/larkbot, bench/refservice) is an
// application above the layers, and is skipped.
func modulePackages(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var rels []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return fs.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return fs.SkipDir
				}
			}
			rel, _ := filepath.Rel(root, path)
			for _, outer := range []string{"adapter", "internal"} {
				if rel == outer || strings.HasPrefix(rel, outer+string(filepath.Separator)) {
					return fs.SkipDir
				}
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
		rel, _ := filepath.Rel(root, dir)
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module tree: %v", err)
	}
	return rels
}

// TestLayeringProblems tests the check itself: an edge outside the table, an
// edge to a test tool, and a stale entry each fail, and the real edges pass.
func TestLayeringProblems(t *testing.T) {
	p := func(rel string) string { return modulePath + "/" + rel }
	cases := []struct {
		name     string
		imported []string
		allowed  []string
		want     int
		contains string
	}{
		{"exact", []string{"fmt", p("gotato"), "gopkg.in/yaml.v3"}, []string{"gotato"}, 0, ""},
		{"edge outside the table", []string{p("gotato"), p("session")}, []string{"gotato"}, 1, "extend the table"},
		{"test tool", []string{p("internal/reftasks")}, []string{}, 1, "internal/reftasks"},
		{"stale entry", []string{p("gotato")}, []string{"gotato", "session"}, 1, "remove the entry"},
	}
	for _, tc := range cases {
		got := layeringProblems("pkg", tc.imported, tc.allowed)
		if len(got) != tc.want {
			t.Errorf("%s: %d problems %v, want %d", tc.name, len(got), got, tc.want)
			continue
		}
		if tc.want > 0 && !strings.Contains(got[0], tc.contains) {
			t.Errorf("%s: message %q lacks %q", tc.name, got[0], tc.contains)
		}
	}
}
