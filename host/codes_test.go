package host

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// ErrorCodes lists every code of codes.go: a code added there and not listed
// fails here, before a transport can miss it.
func TestErrorCodesListsEveryCodeOfTheService(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []gotato.ErrorCode
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for _, lit := range value.Values {
				text := lit.(*ast.BasicLit).Value
				declared = append(declared, gotato.ErrorCode(text[1:len(text)-1]))
			}
		}
	}
	listed := ErrorCodes()
	if len(listed) != len(declared) {
		t.Fatalf("ErrorCodes lists %d codes, codes.go declares %d", len(listed), len(declared))
	}
	for _, code := range declared {
		if !slices.Contains(listed, code) {
			t.Errorf("ErrorCodes does not list %q", code)
		}
	}
	// A code is the host's or the core's, never both.
	for _, code := range listed {
		if slices.Contains(gotato.ErrorCodes(), code) {
			t.Errorf("%q is listed by the core and by the host", code)
		}
	}
}

// The service's codes keep the strings they had in the core, so no client sees
// a different code; and a Subscription's lost Events end with the same string
// as a stream that cannot resume.
func TestServiceCodesKeepTheirStrings(t *testing.T) {
	for code, text := range map[gotato.ErrorCode]string{
		CodeNotWriter: "not_writer", CodeDraining: "draining", CodeOverloaded: "overloaded",
		CodeCannotResume: "cannot_resume", CodeAlreadyExists: "already_exists",
	} {
		if string(code) != text {
			t.Errorf("code %q, want %q", code, text)
		}
	}
	if !gotato.IsCode(gotato.ErrEventsLost, CodeCannotResume) {
		t.Errorf("gotato.ErrEventsLost has code %q, want %q", gotato.ErrEventsLost.Code, CodeCannotResume)
	}
}

// What the host's codes say about the request they refuse: the codes the
// core used to default.
func TestServiceCodesSayWhetherTheyRan(t *testing.T) {
	for _, tc := range []struct {
		code              gotato.ErrorCode
		notRun, retryable bool
	}{
		{CodeDraining, true, true},
		{CodeOverloaded, true, true},
		{CodeNotWriter, true, true},
		{CodeCannotResume, false, false},
		{CodeAlreadyExists, false, false},
	} {
		err := errorOf(tc.code, "x")
		if gotato.NotRun(err) != tc.notRun || gotato.Retryable(err) != tc.retryable {
			t.Errorf("%s: not run %v, retryable %v; want %v and %v", tc.code, gotato.NotRun(err), gotato.Retryable(err), tc.notRun, tc.retryable)
		}
	}
	for _, sentinel := range []error{ErrDraining, ErrOverloaded} {
		if !gotato.NotRun(sentinel) || !gotato.Retryable(sentinel) {
			t.Errorf("%v is not marked not run and retryable", sentinel)
		}
	}
	if err := notWriter("i-1", "http://i-1:8080"); !gotato.NotRun(err) || !gotato.Retryable(err) || !gotato.IsCode(err, CodeNotWriter) {
		t.Errorf("notWriter = %v", err)
	}
	// The core's ErrorOf knows none of them: they are neither, until the host says.
	if bare := gotato.ErrorOf(CodeDraining, "x"); bare.NotRun || bare.Retryable {
		t.Error("gotato.ErrorOf still defaults a code of the host")
	}
	if errors.Is(ErrDraining, ErrOverloaded) {
		t.Error("ErrDraining matches ErrOverloaded")
	}
}
