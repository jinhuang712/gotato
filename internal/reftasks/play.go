package reftasks

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// lastPrompt returns the text of the last user Message and the tool results
// that follow it.
func lastPrompt(messages []gotato.Message) (string, []gotato.ToolResult) {
	var results []gotato.ToolResult
	for _, message := range slices.Backward(messages) {
		switch message.Role {
		case gotato.RoleToolResult:
			if message.ToolResult != nil {
				results = append([]gotato.ToolResult{*message.ToolResult}, results...)
			}
		case gotato.RoleUser:
			return promptText(message), results
		}
	}
	return "", results
}

func resultText(result gotato.ToolResult) string {
	var b strings.Builder
	for _, part := range result.Content {
		b.WriteString(part.Text)
	}
	return b.String()
}

func containsWord(text, word string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(text)
}

// playScript serves a Script as a Model stream, the way a testkit.FakeModel
// with that one Script does.
func playScript(script testkit.Script) gotato.ModelStream {
	stream, _ := testkit.NewFakeModel(script).Stream(context.Background(), gotato.ModelRequest{})
	return stream
}

// promptText returns the first text part of a Message: the user's prompt.
func promptText(message gotato.Message) string {
	for _, part := range message.Parts {
		if part.Kind == gotato.ContentKindText {
			return part.Text
		}
	}
	return ""
}
