package modelctx

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

// Layers of the files of this package. A file may use a declaration of its
// own layer or of a lower one, never of a higher one. The rule that matters:
// rendering and inspection know nothing about compaction.
const (
	layerFoundation = 1 // capabilities, errors, tokens, inspection, and how a Segment's text is written
	layerSegments   = 2 // what builds or reads back one Segment or Recap
	layerDecisions  = 3 // what compaction decides: the policy, the cut, the merge
	layerCompact    = 4 // Compact, which runs the decisions
	layerAuto       = 5 // AutoCompact, the TurnPreparer on top of Compact
)

// fileLayer gives the layer of every non-test file. A new file fails
// TestFileLayers until it is listed here.
var fileLayer = map[string]int{
	// foundation and rendering
	"capability.go":     layerFoundation,
	"errors.go":         layerFoundation,
	"inspect.go":        layerFoundation,
	"modelctx.go":       layerFoundation,
	"readerror.go":      layerFoundation,
	"segment_render.go": layerFoundation,
	"tokens.go":         layerFoundation,
	// build and read-back
	"merge_digest.go":  layerSegments,
	"read_result.go":   layerSegments,
	"read_segment.go":  layerSegments,
	"recap.go":         layerSegments,
	"segment_build.go": layerSegments,
	// compaction decisions
	"compact_cut.go":    layerDecisions,
	"compact_policy.go": layerDecisions,
	"merge.go":          layerDecisions,
	// orchestration
	"compact.go":      layerCompact,
	"auto_compact.go": layerAuto,
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

	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("modelctx", fset, files, info)
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
