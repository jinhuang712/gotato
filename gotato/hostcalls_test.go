package gotato

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

// hostInterfaces are the interfaces whose implementations the host supplies.
var hostInterfaces = []string{
	"Tool", "ToolSet", "ToolSource", "Transcript", "Journal", "Model", "ModelStream",
	"ContextTransformer", "MessageConverter", "PreToolUse", "PostToolUse", "TurnStopper",
	"AdvisoryExtension", "TurnPreparer", "LoadedToolSource", "SpillKeeper", "durable",
	"ToolSearcher", "ContextBuilder", "EventSource", "PublicError",
}

// hostFuncFields are the struct fields that hold a function the host supplied.
var hostFuncFields = []string{"filter", "labeled", "until", "verify", "toolFilter", "onRunStart"}

// hostCallAllowed lists the calls that may stand outside callHost, as
// "file.go:function" with the reason. The list may only shrink: an entry that
// no longer matches a call fails the test.
var hostCallAllowed = map[string]string{
	"reply.go:closeStream": "recovers inline: the error of a Close that already has its reply is ignored",
	"tool.go:Spec":         "effectTool decorates a Tool: whoever calls its Spec guards the call, as for any Tool",
}

// TestEveryHostCallIsGuarded holds each call into host code to the panic
// boundary: a call whose receiver is one of hostInterfaces, or of a function
// field of hostFuncFields, is inside a function literal given to callHost,
// guard or transcriptCall, or in a function that defers recoverPanic, or is in hostCallAllowed.
func TestEveryHostCallIsGuarded(t *testing.T) {
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
	std := importer.ForCompiler(fset, "source", nil)
	var imp importerFunc
	imp = func(path string) (*types.Package, error) {
		if path == modulePath+"/internal/text" {
			return checkDir(fset, "../internal/text", imp)
		}
		return std.Import(path)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	if _, err := (&types.Config{Importer: imp}).Check("gotato", fset, files, info); err != nil {
		t.Fatal(err)
	}

	used := map[string]bool{}
	for _, file := range files {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := name + ":" + fn.Name.Name
			var stack []ast.Node
			ast.Inspect(fn, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				call, ok := n.(*ast.CallExpr)
				if !ok || !isHostCall(call, info) || guardedBy(stack) {
					return true
				}
				if _, ok := hostCallAllowed[key]; ok {
					used[key] = true
					return true
				}
				t.Errorf("%s: a host call outside callHost/guard in %s; wrap it, or list %q in hostCallAllowed with a reason",
					fset.Position(call.Pos()), fn.Name.Name, key)
				return true
			})
		}
	}
	for key := range hostCallAllowed {
		if !used[key] {
			t.Errorf("hostCallAllowed lists %q, which has no unguarded host call; remove it", key)
		}
	}
}

// isHostCall reports whether call calls a method of a host interface, or a
// function field the host supplied.
func isHostCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || info.Selections[sel] == nil {
		return false
	}
	selection := info.Selections[sel]
	switch selection.Kind() {
	case types.FieldVal:
		return slices.Contains(hostFuncFields, sel.Sel.Name)
	case types.MethodVal:
		recv := selection.Recv()
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		named, ok := recv.(*types.Named)
		return ok && types.IsInterface(named) && slices.Contains(hostInterfaces, named.Obj().Name())
	}
	return false
}

// guardedBy reports whether the innermost call is inside a function literal
// that is an argument of callHost, guard or transcriptCall, or inside a function that defers
// recoverPanic.
func guardedBy(stack []ast.Node) bool {
	for i, node := range slices.Backward(stack) {
		switch n := node.(type) {
		case *ast.FuncLit:
			if i > 0 {
				if parent, ok := stack[i-1].(*ast.CallExpr); ok && callsOneOf(parent, "callHost", "guard", "transcriptCall") {
					return true
				}
			}
			if defersRecover(n.Body) {
				return true
			}
		case *ast.FuncDecl:
			return defersRecover(n.Body)
		}
	}
	return false
}

func callsOneOf(call *ast.CallExpr, names ...string) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && slices.Contains(names, id.Name)
}

// defersRecover reports whether body defers recoverPanic at its top level.
func defersRecover(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok && callsOneOf(d.Call, "recoverPanic") {
			return true
		}
	}
	return false
}
