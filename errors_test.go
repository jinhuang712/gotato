package gotato

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// TestErrorCodesListsEveryCode keeps ErrorCodes whole: a new constant of type
// ErrorCode that is not listed fails here, before a transport can miss it.
func TestErrorCodesListsEveryCode(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []ErrorCode
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if id, ok := value.Type.(*ast.Ident); !ok || id.Name != "ErrorCode" {
				continue
			}
			for _, lit := range value.Values {
				text := lit.(*ast.BasicLit).Value
				declared = append(declared, ErrorCode(text[1:len(text)-1]))
			}
		}
	}
	listed := ErrorCodes()
	if len(listed) != len(declared) {
		t.Fatalf("ErrorCodes lists %d codes, errors.go declares %d", len(listed), len(declared))
	}
	for _, code := range declared {
		if !slices.Contains(listed, code) {
			t.Errorf("ErrorCodes does not list %q", code)
		}
	}
}

func TestErrorsSayWhetherTheyRanAndWhetherARetryMaySucceed(t *testing.T) {
	for _, tc := range []struct {
		code              ErrorCode
		notRun, retryable bool
	}{
		{ErrDraining, true, true},
		{ErrOverloaded, true, true},
		{ErrBusy, true, true},
		{ErrNotFound, true, false},
		{ErrSessionClosing, true, false},
		{ErrSessionClosed, true, false},
		{ErrStoreFailure, false, true},
		{ErrInvalidArgument, false, false},
		{ErrContextOverflow, false, false},
		{ErrModelFailure, false, false},
		{ErrConflict, false, false},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", ErrorOf(tc.code, "x"))
			if got := NotRun(err); got != tc.notRun {
				t.Errorf("NotRun = %v, want %v", got, tc.notRun)
			}
			if got := Retryable(err); got != tc.retryable {
				t.Errorf("Retryable = %v, want %v", got, tc.retryable)
			}
			internal := runtimeError(tc.code, "Op", "x", nil)
			if internal.NotRun != tc.notRun || internal.Retryable != tc.retryable {
				t.Errorf("runtimeError = not run %v, retryable %v", internal.NotRun, internal.Retryable)
			}
		})
	}
	if NotRun(nil) || Retryable(nil) || NotRun(errors.New("plain")) || Retryable(errors.New("plain")) {
		t.Error("nil and plain errors say neither")
	}
	// A joined error answers for the first RuntimeError in it.
	if joined := errors.Join(ErrorOf(ErrBusy, "x"), errors.New("y")); !NotRun(joined) {
		t.Error("a joined error hides its RuntimeError")
	}
}

type transientError struct{ transient bool }

func (e transientError) Error() string     { return "provider: slow down" }
func (e transientError) IsRetryable() bool { return e.transient }

func TestModelFailureIsRetryableWhenTheModelSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limit", transientError{true}, true},
		{"wrapped rate limit", fmt.Errorf("model: %w", transientError{true}), true},
		{"refused for good", transientError{false}, false},
		{"plain failure", errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := NewSession(WithModel(failingModel{err: tc.err}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), UserMessage("hi"))
			if result.Status != RunFailed || !IsCode(err, ErrModelFailure) {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if Retryable(err) != tc.want || result.Error.Retryable != tc.want {
				t.Errorf("Retryable = %v, want %v", Retryable(err), tc.want)
			}
			if NotRun(err) {
				t.Error("the Model was called: the request ran")
			}
		})
	}
}
