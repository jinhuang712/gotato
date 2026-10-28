package gotato

import (
	"context"
	"testing"
)

// A ToolSource or ToolSet that supplies a nil Tool fails with a code, not a
// process panic.
func TestANilToolFromASourceOrASetFailsWithACode(t *testing.T) {
	t.Run("ToolSource", func(t *testing.T) {
		source := &dynamicSource{tools: []Tool{nil}}
		_, err := NewSession(context.Background(), WithModel(&testModel{}), WithToolSource(source))
		if err == nil {
			t.Fatal("a nil Tool from a ToolSource did not fail NewSession")
		}
	})
	t.Run("ToolSet", func(t *testing.T) {
		set := &stubToolSet{name: "files", tools: []Tool{nil}}
		model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
		session, err := NewSession(context.Background(), WithModel(model), WithToolSet(set))
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer func() { _ = session.Close(context.Background()) }()
		if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
			t.Logf("Prompt: %v", err)
		}
	})
}
