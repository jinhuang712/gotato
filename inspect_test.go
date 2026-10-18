package gotato_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// policyNote is a ContextTransformer that puts a note in front of the
// history, so a request that skips Extensions is visibly different.
type policyNote struct{}

func (policyNote) Transform(_ context.Context, snapshot gotato.ContextSnapshot) ([]gotato.Message, error) {
	return append([]gotato.Message{gotato.UserMessage("policy: answer briefly")}, snapshot.Messages...), nil
}

// shout is a MessageConverter that upper-cases user text.
type shout struct{}

func (shout) Convert(_ context.Context, messages []gotato.Message) ([]gotato.Message, error) {
	for i := range messages {
		if messages[i].Role != gotato.RoleUser {
			continue
		}
		for j := range messages[i].Parts {
			messages[i].Parts[j].Text = strings.ToUpper(messages[i].Parts[j].Text)
		}
	}
	return messages, nil
}

func TestInspectContextMatchesTheNextTurn(t *testing.T) {
	s := testkit.NewRecord("where is order 42?", "let me check", "please hurry")
	model := testkit.NewFakeModel(testkit.Text("on its way"))
	temperature := 0.2
	agent, err := gotato.NewSession(
		gotato.WithModel(model),
		gotato.WithInstruction("You track orders."),
		gotato.WithTranscript(s),
		gotato.WithTool(testkit.NewFakeTool("orders.lookup", "shipped")),
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &temperature, MaxTokens: 512}),
		gotato.WithExtensions(policyNote{}, shout{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	recorder := testkit.RecordEvents(agent)

	inspection, err := agent.InspectContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourceMessages != 3 || inspection.Request.Options.MaxTokens != 512 {
		t.Fatalf("inspection = %+v", inspection)
	}
	if first := gotato.TextOf(inspection.Request.Messages[0]); first != "POLICY: ANSWER BRIEFLY" {
		t.Fatalf("inspection skipped the Extensions: first message %q", first)
	}

	if _, err := agent.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent, ok := model.LastRequest()
	if !ok {
		t.Fatal("the model was not called")
	}
	want, _ := json.Marshal(inspection.Request)
	got, _ := json.Marshal(sent)
	if string(got) != string(want) {
		t.Fatalf("the Turn sent a different request\n got: %s\nwant: %s", got, want)
	}
	built := recorder.OfKind(gotato.KindContextBuilt)
	if len(built) != 1 {
		t.Fatalf("context_built = %+v", built)
	}
	if event, ok := built[0].(gotato.ContextBuilt); !ok || event.PrefixHash != inspection.PrefixHash {
		t.Fatalf("context_built = %+v, inspection prefix %s", built[0], inspection.PrefixHash)
	}
}

func TestInspectContextIsRefusedDuringARun(t *testing.T) {
	called := make(chan struct{})
	release := make(chan struct{})
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		close(called)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testkit.Text("done"), nil
	})
	agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(session.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), gotato.UserMessage("hi"))
		done <- err
	}()
	<-called
	if _, err := agent.InspectContext(context.Background()); !gotato.IsCode(err, gotato.ErrBusy) {
		t.Fatalf("InspectContext during a Run: err = %v, want busy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := agent.InspectContext(context.Background()); err != nil {
		t.Fatalf("InspectContext after the Run: %v", err)
	}
}
