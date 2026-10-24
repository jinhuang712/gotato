package gotato

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type overflowError struct{ overflow bool }

func (e overflowError) Error() string         { return "refused: too long" }
func (e overflowError) ContextOverflow() bool { return e.overflow }

type failingModel struct{ err error }

func (m failingModel) Stream(context.Context, ModelRequest) (ModelStream, error) { return nil, m.err }

func TestModelErrorThatSaysOverflowEndsTheRunWithContextOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"overflow", overflowError{overflow: true}, ErrContextOverflow},
		{"wrapped overflow", fmt.Errorf("model: %w", overflowError{overflow: true}), ErrContextOverflow},
		{"refused for another reason", overflowError{}, ErrModelFailure},
		{"plain failure", errors.New("boom"), ErrModelFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := NewSession(WithModel(failingModel{err: tc.err}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), UserMessage("hi"))
			if err == nil || result.Status != RunFailed || result.Error == nil || result.Error.Code != tc.want {
				t.Fatalf("result = %+v, err = %v; want %s with %s", result, err, RunFailed, tc.want)
			}
			if !IsCode(err, tc.want) {
				t.Fatalf("err = %v, want code %s", err, tc.want)
			}
		})
	}
}
