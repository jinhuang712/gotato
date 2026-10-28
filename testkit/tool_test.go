package testkit_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestFakeToolUsesAreIsolated(t *testing.T) {
	tool := testkit.NewFakeTool("fake", "result")
	use := gotato.ToolUse{Arguments: []byte(`{"x":1}`)}
	if _, err := tool.Execute(context.Background(), use, nil); err != nil {
		t.Fatal(err)
	}
	uses := tool.Uses()
	uses[0].Arguments[0] = 'X'

	again := tool.Uses()
	if string(again[0].Arguments) != `{"x":1}` {
		t.Fatalf("arguments mutation reached the recording: %s", again[0].Arguments)
	}
}
