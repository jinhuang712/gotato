package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A stream that is open while its Session is deleted and created again under
// the same ID must not go on with the new Record's Events under the old
// numbers: it ends with an error, whichever instance deleted the Session.
func TestOpenStreamEndsWhenItsSessionIsCreatedAgain(t *testing.T) {
	for _, where := range []string{"on the same instance", "on another instance"} {
		t.Run(where, func(t *testing.T) {
			store := session.NewMemoryStore()
			build := func(name string) *service.Runner {
				runner, err := service.New(service.Config{
					Store: store, Instance: name, StreamPoll: 10 * time.Millisecond,
					Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = runner.Close(context.Background()) })
				return runner
			}
			a, b := build("a"), build("b")
			other := a
			if where == "on another instance" {
				other = b
			}
			ctx := context.Background()
			first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
			if err != nil {
				t.Fatal(err)
			}
			id := first.SessionID
			stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			readNumbers(t, stream, int(stream.Head))

			if err := other.DeleteSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := other.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if _, err := other.Run(ctx, service.RunRequest{SessionID: id, Prompt: "new"}); err != nil {
					t.Fatal(err)
				}
			}
			read, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				event, err := stream.Next(read)
				if err != nil {
					if !errors.Is(err, service.ErrCannotResume) && !errors.Is(err, session.ErrNotFound) {
						t.Fatalf("the stream ended with %v, want cannot_resume or not found", err)
					}
					return
				}
				if event.Kind().Class() == gotato.ClassStored {
					t.Fatalf("the stream delivered Event %d (%s) of the Record created again", event.Head().Number, event.Kind())
				}
			}
		})
	}
}
