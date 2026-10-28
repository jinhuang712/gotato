package gotato_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

const privateText = "password=private-sentinel"

type fixedSet struct{ tool gotato.Tool }

func (s fixedSet) Spec() gotato.ToolSetSpec                     { return gotato.ToolSetSpec{Name: "catalog"} }
func (s fixedSet) Tools(context.Context) ([]gotato.Tool, error) { return []gotato.Tool{s.tool}, nil }

type rawErrorSearcher struct{}

func (rawErrorSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	return nil, errors.New("private-backend " + privateText)
}

type panicSearcher struct{}

func (panicSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	panic(privateText)
}

type publicSearchError struct{}

func (publicSearchError) Error() string  { return "private-backend " + privateText }
func (publicSearchError) Public() string { return "the index is unavailable" }

type publicErrorSearcher struct{}

func (publicErrorSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	return nil, publicSearchError{}
}

type panicStopper struct{}

func (panicStopper) Stop(context.Context, gotato.TurnSnapshot) (gotato.StopDecision, error) {
	panic(privateText)
}

// searchResultText runs a Session whose Model calls the search Tool and
// returns the text of the failed Tool Result the Model reads.
func searchResultText(t *testing.T, searcher gotato.ToolSearcher) string {
	t.Helper()
	ctx := context.Background()
	tool, err := gotato.NewFuncTool("lookup", "lookup", func(context.Context, struct{}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "search-call", ToolID: gotato.SearchToolName, Arguments: []byte(`{"queries":["lookup"]}`)}), testkit.Text("done"))
	s, err := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithToolSet(fixedSet{tool}), gotato.WithToolSearcher(searcher))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	if _, err = s.Prompt(ctx, gotato.UserMessage("find tool")); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Transcript().Messages() {
		if m.ToolResult != nil {
			return m.ToolResult.SafeError
		}
	}
	t.Fatal("no tool result")
	return ""
}

// A ToolSearcher's error and panic are the application's: the text is kept on
// the Cause, and the Model reads a fixed one.
func TestToolSearcherFailureTextStaysPrivate(t *testing.T) {
	for name, searcher := range map[string]gotato.ToolSearcher{"error": rawErrorSearcher{}, "panic": panicSearcher{}} {
		t.Run(name, func(t *testing.T) {
			if text := searchResultText(t, searcher); text == "" || strings.Contains(text, privateText) {
				t.Fatalf("the Model reads %q", text)
			}
		})
	}
}

// A PublicError of a ToolSearcher keeps its own text.
func TestToolSearcherPublicErrorKeepsItsText(t *testing.T) {
	text := searchResultText(t, publicErrorSearcher{})
	if !strings.Contains(text, "the index is unavailable") || strings.Contains(text, privateText) {
		t.Fatalf("the Model reads %q", text)
	}
}

// A Tool that panics does not hand what it panicked with to the Model.
func TestToolPanicTextStaysOutOfModelInput(t *testing.T) {
	tool, err := gotato.NewFuncTool("probe", "a probe", func(context.Context, struct{}) (string, error) { panic(privateText) })
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "p1", ToolID: "probe", Arguments: []byte(`{}`)}), testkit.Text("done"))
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err = s.Prompt(context.Background(), gotato.UserMessage("probe")); err != nil {
		t.Fatal(err)
	}
	req, ok := model.LastRequest()
	if !ok {
		t.Fatal("no request")
	}
	results := testkit.ToolResults(req)
	if len(results) == 0 || strings.Contains(strings.Join(results, "\n"), privateText) {
		t.Fatalf("the Model reads %q", results)
	}
}

// An Extension that panics fails the Run with a fixed Message, and keeps what
// it panicked with on the Cause.
func TestExtensionPanicTextStaysOnTheCause(t *testing.T) {
	ctx := context.Background()
	s, err := gotato.NewSession(ctx, gotato.WithModel(testkit.EchoModel{}), gotato.WithExtension(panicStopper{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
	var failure *gotato.RuntimeError
	if err == nil || !errors.As(err, &failure) {
		t.Fatalf("err = %v, result = %+v", err, result)
	}
	if strings.Contains(failure.Error(), privateText) || result.Error != nil && strings.Contains(result.Error.Error(), privateText) {
		t.Fatalf("the failure says %q", failure.Error())
	}
	if !strings.Contains(failure.Cause.Error(), privateText) {
		t.Fatalf("the Cause lost the panic: %v", failure.Cause)
	}
}
