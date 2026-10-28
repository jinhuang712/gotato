package gotato

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Layers of the files of this package. A file may use a declaration of its
// own layer or of a lower one, never of a higher one, so a reader can follow
// the package from the bottom up. See "Where things are" in doc.go.
const (
	layerValues    = 1 // values and contracts: what a Session, its Events and its Tools are made of
	layerMechanism = 2 // mechanism that needs no Session: Transcript, Tool sets, the Feed, Context, Extensions
	layerSession   = 3 // the Session, its Loop, and everything that runs on it
)

// fileLayer gives the layer of every non-test file. A new file fails
// TestFileLayers until it is listed here.
var fileLayer = map[string]int{
	// values and contracts
	"doc.go":         layerValues,
	"clone.go":       layerValues,
	"errors.go":      layerValues,
	"eventcodec.go":  layerValues,
	"eventheader.go": layerValues,
	"eventkinds.go":  layerValues,
	"events.go":      layerValues,
	"limits.go":      layerValues,
	"model.go":       layerValues,
	"retry.go":       layerValues,
	"segment.go":     layerValues,
	"tags.go":        layerValues,
	"tool.go":        layerValues,
	"toolschema.go":  layerValues,
	"types.go":       layerValues,
	"validate.go":    layerValues,
	// mechanism
	"bm25.go":         layerMechanism,
	"coalesce.go":     layerMechanism,
	"context.go":      layerMechanism,
	"cutoff.go":       layerMechanism,
	"extensions.go":   layerMechanism,
	"feed.go":         layerMechanism,
	"journal.go":      layerMechanism,
	"subscription.go": layerMechanism,
	"toolfunc.go":     layerMechanism,
	"toolset.go":      layerMechanism,
	"transcript.go":   layerMechanism,
	"turnprep.go":     layerMechanism,
	// Session and Loop
	"commit.go":           layerSession,
	"eventwrite.go":       layerSession,
	"inspect.go":          layerSession,
	"loop.go":             layerSession,
	"notice.go":           layerSession,
	"queue.go":            layerSession,
	"reply.go":            layerSession,
	"request.go":          layerSession,
	"session.go":          layerSession,
	"sessionlifecycle.go": layerSession,
	"sessionoptions.go":   layerSession,
	"spill.go":            layerSession,
	"toolbatch.go":        layerSession,
	"toolrefresh.go":      layerSession,
	"toolregistry.go":     layerSession,
	"toolsearch.go":       layerSession,
	"toolsource.go":       layerSession,
}

// knownBackEdges lists the uses that point up, as "user -> used" with the
// reason. The list may only shrink: a new upward use fails TestFileLayers, and
// so does an entry that no longer exists.
var knownBackEdges = map[string]string{}

// TestFileLayers enforces the layers of fileLayer: for every identifier of a
// non-test file that names a declaration of this package, the declaring file
// is in the same layer or a lower one.
func TestFileLayers(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	seenFile := map[string]bool{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		seenFile[p] = true
		if fileLayer[p] == 0 {
			t.Errorf("%s has no layer; add it to fileLayer", p)
		}
	}
	for p := range fileLayer {
		if !seenFile[p] {
			t.Errorf("fileLayer lists %s, which does not exist; remove it", p)
		}
	}

	// Type-check with the standard library from source. internal/text is the
	// one other import of the core; it imports the standard library only.
	std := importer.ForCompiler(fset, "source", nil)
	var imp importerFunc
	imp = func(path string) (*types.Package, error) {
		if path == modulePath+"/internal/text" {
			return checkDir(fset, "../internal/text", imp)
		}
		return std.Import(path)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check("gotato", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for id, obj := range info.Uses {
		if obj.Pkg() != pkg || !obj.Pos().IsValid() {
			continue
		}
		user := filepath.Base(fset.Position(id.Pos()).Filename)
		used := filepath.Base(fset.Position(obj.Pos()).Filename)
		if user != used && fileLayer[used] > fileLayer[user] {
			seen[user+" -> "+used] = true
		}
	}
	for edge := range seen {
		if _, ok := knownBackEdges[edge]; !ok {
			t.Errorf("%s points up (a file may use only its own layer or a lower one); move the declaration down or its user up", edge)
		}
	}
	var stale []string
	for edge := range knownBackEdges {
		if !seen[edge] {
			stale = append(stale, edge)
		}
	}
	slices.Sort(stale)
	for _, edge := range stale {
		t.Errorf("knownBackEdges lists %q, which no longer exists; remove it", edge)
	}
}

// checkDir type-checks the non-test files of dir, which import the standard
// library only.
func checkDir(fset *token.FileSet, dir string, imp types.Importer) (*types.Package, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}
	return (&types.Config{Importer: imp}).Check(files[0].Name.Name, fset, files, nil)
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
