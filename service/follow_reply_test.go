package service_test

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// replyOf is the loop FEATURES.md (G-F13) shows a chat bot: it returns the
// text of the reply that ended the next Run to settle on a Session's stream,
// that is, the last reply that asked for no tool.
func replyOf(ctx context.Context, sub gotato.Subscription) (string, error) {
	var reply string
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			return "", err
		}
		switch e := event.(type) {
		case gotato.ReplyEnded:
			if e.ToolCalls == 0 {
				reply = gotato.TextOf(e.Message)
			}
		case gotato.RunEnded:
			return reply, nil
		}
	}
}

func TestSessionStreamSnippetReadsTheFinalReply(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "echo", Model: testkit.NewFakeModel(testkit.Text("hello there"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(ctx) })
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	// As in the snippet: follow the Session from its head now.
	record, err := runner.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.SubscribeSession(ctx, s.ID(), record.Head())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	reply, err := replyOf(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "hello there" {
		t.Fatalf("reply = %q, want %q", reply, "hello there")
	}
}
