package reftasks_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/profile"
)

// play runs a task on a plain live Session and returns its Record and each Run's
// final text.
func play(t *testing.T, task reftasks.Task) (*record.Record, []string) {
	t.Helper()
	s := record.New()
	for _, message := range task.History {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	options := []gotato.SessionOption{
		gotato.WithModel(task.Model),
		gotato.WithInstruction(task.Instruction),
		gotato.WithTranscript(s),
	}
	for _, tool := range task.Tools {
		options = append(options, gotato.WithTools(tool))
	}
	for _, set := range task.ToolSets {
		switch set.Mode {
		case profile.ToolSetFolded:
			options = append(options, gotato.WithFoldedToolSet(set.Set))
		case profile.ToolSetActive:
			options = append(options, gotato.WithActiveToolSet(set.Set))
		default:
			options = append(options, gotato.WithToolSet(set.Set))
		}
	}
	if task.Limits != nil {
		options = append(options, gotato.WithLimits(*task.Limits))
	}
	agent, err := gotato.NewSession(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	var answers []string
	for _, prompt := range task.Prompts {
		result, err := agent.Prompt(context.Background(), gotato.UserMessage(prompt))
		if err != nil {
			t.Fatalf("%s: %q: %v", task.Name, prompt, err)
		}
		answers = append(answers, gotato.TextOf(*result.FinalMessage))
	}
	return s, answers
}
