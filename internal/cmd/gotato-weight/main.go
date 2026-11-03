// Command gotato-weight counts what a user of Gotato learns and depends on,
// the weight of decision 053: the terms of GLOSSARY.md, and the exported names
// and the settings of the shipped packages. It prints one line per package and
// the totals. B13 in plan/GOALS.md tracks the totals.
//
//	go run ./internal/cmd/gotato-weight [repository root]
//
// An exported name is a package-level exported identifier, or an exported
// method of an exported type. A setting is an exported field of an exported
// struct whose name ends in Config or Policy, or an exported function that
// returns a type whose name ends in Option.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skipped are the directories that hold no shipped package: our test tools,
// the applications, the reference service, the notes and the worktrees.
var skipped = map[string]bool{"internal": true, "examples": true, "bench": true, "notes": true, "testdata": true, ".claude": true, ".git": true, "bin": true}

type weight struct{ names, settings int }

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	terms, err := countTerms(filepath.Join(root, "GLOSSARY.md"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pkgs := map[string]*weight{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipped[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		w := pkgs[rel]
		if w == nil {
			w = &weight{}
			pkgs[rel] = w
		}
		return count(path, w)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := make([]string, 0, len(pkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	var total weight
	fmt.Printf("%-22s %6s %9s\n", "package", "names", "settings")
	for _, p := range names {
		w := pkgs[p]
		total.names += w.names
		total.settings += w.settings
		fmt.Printf("%-22s %6d %9d\n", p, w.names, w.settings)
	}
	fmt.Printf("%-22s %6d %9d\n", "total", total.names, total.settings)
	fmt.Printf("terms in GLOSSARY.md   %d\n", terms)
}

// count adds the exported names and the settings of one file to w.
func count(path string, w *weight) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	if f.Name.Name == "main" {
		return nil
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			if d.Recv != nil {
				if ast.IsExported(receiverName(d.Recv.List[0].Type)) {
					w.names++
				}
				continue
			}
			w.names++
			if d.Type.Results != nil && len(d.Type.Results.List) == 1 && strings.HasSuffix(typeName(d.Type.Results.List[0].Type), "Option") {
				w.settings++
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					w.names++
					st, ok := s.Type.(*ast.StructType)
					if !ok || !strings.HasSuffix(s.Name.Name, "Config") && !strings.HasSuffix(s.Name.Name, "Policy") {
						continue
					}
					for _, field := range st.Fields.List {
						for _, n := range field.Names {
							if n.IsExported() {
								w.settings++
							}
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							w.names++
						}
					}
				}
			}
		}
	}
	return nil
}

// receiverName is the type a method is declared on, without a pointer or type parameters.
func receiverName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// typeName is the last name of a type expression, such as Option for gotato.Option.
func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// countTerms counts the rows of the tables in GLOSSARY.md, one term to a row.
func countTerms(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| Term ") {
			n++
		}
	}
	return n, sc.Err()
}
