package gateway

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

// protocolOf gives the protocol family of the files that speak one wire
// protocol. A file of one family never uses a declaration of another: what two
// protocols share lives in a file of no family (encode_common, sse, errors,
// http, headers, ...), so a protocol can change or go without the others.
func protocolOf(file string) string {
	switch {
	case strings.HasPrefix(file, "chat"):
		return "chat"
	case strings.HasPrefix(file, "responses"):
		return "responses"
	case strings.HasPrefix(file, "anthropic"):
		return "anthropic"
	}
	return ""
}

// knownEdges lists the uses across families, as "user -> used" with the
// reason. The list may only shrink: a new cross use fails
// TestProtocolSiblings, and so does an entry that no longer exists.
var knownEdges = map[string]string{}

// TestProtocolSiblings enforces that the chat, responses and anthropic files
// never use each other's declarations.
func TestProtocolSiblings(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("gateway", fset, files, info)
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
		if pu, pd := protocolOf(user), protocolOf(used); pu != "" && pd != "" && pu != pd {
			seen[user+" -> "+used] = true
		}
	}
	for edge := range seen {
		if _, ok := knownEdges[edge]; !ok {
			t.Errorf("%s crosses protocol families; move the shared code to a file of no family", edge)
		}
	}
	var stale []string
	for edge := range knownEdges {
		if !seen[edge] {
			stale = append(stale, edge)
		}
	}
	slices.Sort(stale)
	for _, edge := range stale {
		t.Errorf("knownEdges lists %q, which no longer exists; remove it", edge)
	}
}
