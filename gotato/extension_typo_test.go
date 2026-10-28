package gotato

import (
	"context"
	"testing"
)

// typoHook is a ContextTransformer whose permission hook has the wrong
// signature: it must not install silently as a transformer alone.
type typoHook struct{}

func (typoHook) Transform(context.Context, ContextSnapshot) ([]Message, error) { return nil, nil }
func (typoHook) Before(ToolUse) (PreToolDecision, error)                       { return PreToolDecision{}, nil }

type noHook struct{}

func (noHook) Frobnicate() {}

func TestWithExtensionFailsLoudlyOnAMismatchedHook(t *testing.T) {
	model := &testModel{}
	for name, ext := range map[string]any{"wrong Before signature": typoHook{}, "no hook at all": noHook{}} {
		_, err := NewSession(context.Background(), WithModel(model), WithExtension(ext))
		if !IsCode(err, CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
}
