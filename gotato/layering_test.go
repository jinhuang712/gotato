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
// internal/text and internal/hostlink are the shared packages of internal/
// (standard library only): the first is text helpers, the second the calls the
// core hands the host that runs its Sessions (decision 048). The rest of
// internal/ is our test tools, which no shipped package may import, and so
// never appear here.
//
// adapter/mcp and adapter/http are modules of their own, but they are shipped
// packages, so they are held to the same table: adapter/mcp may import the core
// and nothing else, and adapter/http the layers up to host.
var allowedImports = map[string][]string{
	"gotato":          {"internal/hostlink", "internal/text", "ref"},
	"record":          {"gotato", "internal/text", "ref"},
	"store":           {"gotato", "record"},
	"model":           {"gotato", "internal/text"}, // text.Cut: an error body is cut on a rune boundary
	"ref":             {},
	"testkit":         {"gotato", "internal/hostlink", "internal/text", "record"}, // text.IsRecapAsk: the fake models recognize the Recap ask by the one definition
	"store/storetest": {"gotato", "record", "store"},
	// profile is optional: the definitions a host runs, and where they come from
	// (decision 044). It sits between the host and the layers below it, and
	// imports no host.
	"profile": {"gotato", "record", "store"},
	"host":    {"gotato", "internal/hostlink", "profile", "record", "host/internal/clock", "host/internal/heartbeat", "host/internal/runs", "host/internal/warm", "store"},
	// host/internal holds the parts of the Runner (decision 048): each is
	// standard library only, or the core, and none imports another.
	"host/internal/clock":     {},
	"host/internal/heartbeat": {"gotato", "record", "host/internal/clock", "store"},
	"host/internal/runs":      {"host/internal/warm"},
	"host/internal/warm":      {"host/internal/clock"},
	"adapter/http":            {"gotato", "host", "record", "store"},
	"adapter/mcp":             {"gotato"},
}

// layerOrder is the order of the layers of decision 050, from the lowest: a
// package imports its own layer and the ones before it, never one after it, so
// store cannot import profile or host, record cannot import store, and gotato
// cannot import any of them. A sub-package (host/internal/warm,
// store/storetest) is in the layer of its first path element.
var layerOrder = []string{"ref", "gotato", "record", "store", "profile", "host"}

// beside lists the packages that sit beside the layers and import down: no
// layer may import them. Every package under adapter/ is one of them.
var beside = []string{"model", "testkit"}

// layerOf returns the position of pkg's layer in layerOrder, or -1 for a
// package that is in none.
func layerOf(pkg string) int {
	first, _, _ := strings.Cut(pkg, "/")
	return slices.Index(layerOrder, first)
}

// orderProblems returns one message for each import of pkg that points up the
// layers, or that a layer makes of a package beside them. imported is the full
// list of import paths of pkg's non-test code.
func orderProblems(pkg string, imported []string) []string {
	rank := layerOf(pkg)
	if rank < 0 {
		return nil
	}
	var problems []string
	for _, path := range imported {
		rel, ok := strings.CutPrefix(path, modulePath+"/")
		if !ok {
			continue
		}
		if slices.Contains(beside, rel) || strings.HasPrefix(rel, "adapter/") {
			problems = append(problems, fmt.Sprintf("%s (layer %s) imports %s, which sits beside the layers and imports down: no layer imports it", pkg, layerOrder[rank], rel))
		} else if up := layerOf(rel); up > rank {
			problems = append(problems, fmt.Sprintf("%s (layer %s) imports %s (layer %s): imports point down host -> profile -> store -> record -> gotato -> ref, never up", pkg, layerOrder[rank], rel, layerOrder[up]))
		}
	}
	return problems
}

// standardOnly lists the packages that may import the standard library alone
// from outside this repository: the core, and the shared package it uses.
var standardOnly = []string{"gotato", "internal/text", "ref"}

// TestLayering enforces plan/decisions/010 (a light library): dependencies point inward: every shipped
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
	shipped := append(modulePackages(t, root), "adapter/http", "adapter/mcp")
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
		for _, problem := range orderProblems(rel, imported) {
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
	for _, shared := range []string{"text", "hostlink"} {
		for _, path := range importsOf(t, filepath.Join(root, "internal", shared)) {
			if strings.Contains(path, ".") {
				t.Errorf("internal/%s imports %q; it may import only the standard library", shared, path)
			}
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
		{"edge outside the table", []string{p("gotato"), p("store")}, []string{"gotato"}, 1, "extend the table"},
		{"test tool", []string{p("internal/reftasks")}, []string{}, 1, "internal/reftasks"},
		{"stale entry", []string{p("gotato")}, []string{"gotato", "store"}, 1, "remove the entry"},
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

// TestOrderProblems tests the order check: each import that points up the
// layers fails (store to host or profile, record to store, gotato to any of
// them), so does a layer's import of a package beside the layers, and the
// imports that point down, or stay in the layer, pass.
func TestOrderProblems(t *testing.T) {
	p := func(rel string) string { return modulePath + "/" + rel }
	cases := []struct {
		pkg      string
		imported []string
		want     int
	}{
		{"host", []string{p("profile"), p("store"), p("record"), p("gotato"), p("ref"), p("host/internal/warm")}, 0},
		{"profile", []string{p("store"), p("record"), p("gotato")}, 0},
		{"store", []string{p("record"), p("gotato")}, 0},
		{"store/storetest", []string{p("store"), p("record")}, 0},
		{"record", []string{p("gotato"), p("ref"), p("internal/text")}, 0},
		{"gotato", []string{p("ref"), p("internal/hostlink"), p("internal/text")}, 0},
		{"model", []string{p("gotato")}, 0},
		{"adapter/http", []string{p("host"), p("store")}, 0},
		{"store", []string{p("host")}, 1},
		{"store", []string{p("profile")}, 1},
		{"record", []string{p("store")}, 1},
		{"gotato", []string{p("record")}, 1},
		{"gotato", []string{p("store"), p("profile"), p("host")}, 3},
		{"ref", []string{p("gotato")}, 1},
		{"host/internal/warm", []string{p("host")}, 0}, // the layer itself: the table of allowedImports keeps parts from importing their host
		{"host", []string{p("model")}, 1},
		{"profile", []string{p("adapter/http")}, 1},
		{"record", []string{p("testkit")}, 1},
	}
	for _, tc := range cases {
		if got := orderProblems(tc.pkg, tc.imported); len(got) != tc.want {
			t.Errorf("%s importing %v: %d problems %v, want %d", tc.pkg, tc.imported, len(got), got, tc.want)
		}
	}
}
