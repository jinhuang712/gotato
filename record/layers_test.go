package record

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
// own layer or of a lower one, never of a higher one. The rules that matter:
// the Record knows nothing of the Context built from it, and rendering and
// inspection know nothing about compaction.
const (
	layerValues     = 1 // what a Record is made of and reads through: Entry, Header, the errors, and the seams that read a log
	layerRecord     = 2 // the Record: its state, its log, its Claim, its Segments, and how it is rebuilt and handed to a writer
	layerFoundation = 3 // the Context: capabilities, tokens, inspection, the part interfaces, and how a Segment's text is written
	layerSegments   = 4 // what builds or reads back one Segment or Recap
	layerDecisions  = 5 // what compaction decides: the policy, the Compactor and its default parts, the merge
	layerCompact    = 6 // Compact, which runs the decisions
	layerAuto       = 7 // AutoCompact, the TurnPreparer on top of Compact
)

// fileLayer gives the layer of every non-test file. A new file fails
// TestFileLayers until it is listed here.
var fileLayer = map[string]int{
	// values and seams
	"committed.go":   layerValues,
	"doc.go":         layerValues,
	"entry.go":       layerValues,
	"errors.go":      layerValues,
	"header.go":      layerValues,
	"recordepoch.go": layerValues,
	// the Record
	"document.go":    layerRecord,
	"events.go":      layerRecord,
	"holder.go":      layerRecord,
	"record.go":      layerRecord,
	"record_info.go": layerRecord,
	"record_log.go":  layerRecord,
	"run.go":         layerRecord,
	"segments.go":    layerRecord,
	"spilled.go":     layerRecord,
	"writing.go":     layerRecord,
	// the Context: foundation and rendering
	"context.go":        layerFoundation,
	"inspect.go":        layerFoundation,
	"parts.go":          layerFoundation,
	"readerror.go":      layerFoundation,
	"segment_render.go": layerFoundation,
	"tokens.go":         layerFoundation,
	// build and read-back
	"merge_digest.go":  layerSegments,
	"originals.go":     layerSegments,
	"read_result.go":   layerSegments,
	"read_segment.go":  layerSegments,
	"readback.go":      layerSegments,
	"recap.go":         layerSegments,
	"segment_build.go": layerSegments,
	// compaction decisions
	"compact_cut.go":    layerDecisions,
	"compact_parts.go":  layerDecisions,
	"compact_policy.go": layerDecisions,
	"compactor.go":      layerDecisions,
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
	pkg, err := conf.Check("record", fset, files, info)
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
