package store

import (
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

// Layers of the files of this package, in three groups. A file may use a
// declaration of its own group or of a lower one, never of a higher one: a
// Store keeps Entries and knows nothing of a Record's state; only the Writer
// joins the two. Inside a group files may use each other freely: FileStore is
// one type spread over several files, so the rule is between groups.
const (
	layerContract = 1 // the contract: the Store interfaces, what they return, and the errors
	layerStores   = 2 // the Stores that persist entries: MemoryStore and FileStore
	layerWriter   = 3 // the Writer, which binds a Record to a Store
)

// fileLayer gives the group of every non-test file. A new file fails
// TestFileLayers until it is listed here.
var fileLayer = map[string]int{
	// the contract
	"definitions.go": layerContract,
	"doc.go":         layerContract,
	"errors.go":      layerContract,
	"store.go":       layerContract,
	// the Stores
	"definitions_file.go":   layerStores,
	"definitions_memory.go": layerStores,
	"file.go":               layerStores,
	"file_batch.go":         layerStores,
	"file_instance.go":      layerStores,
	"file_name.go":          layerStores,
	"file_sweep.go":         layerStores,
	"memory.go":             layerStores,
	"store_check.go":        layerStores,
	// the Writer
	"writer.go": layerWriter,
}

// knownBackEdges lists the uses that point up, as "user -> used" with the
// reason. The list may only shrink: a new upward use fails TestFileLayers, and
// so does an entry that no longer exists.
var knownBackEdges = map[string]string{}

// TestFileLayers enforces the layers of fileLayer: for every identifier of a
// non-test file that names a declaration of this package, the declaring file
// is in the same group or a lower one.
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

	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("store", fset, files, info)
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
			t.Errorf("%s points up (a file may use only its own group or a lower one); move the declaration down or its user up", edge)
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
