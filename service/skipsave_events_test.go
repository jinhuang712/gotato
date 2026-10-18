package service_test

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
)

// RunResult.Events, the CLI's "events" field, still counts the Events a Run
// produced when it is not saved: those of its own stream.
func TestSkipSaveRunReportsItsEvents(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	for _, existing := range []bool{false, true} {
		request := service.RunRequest{Prompt: "hi", SkipSave: true}
		if existing {
			first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
			if err != nil {
				t.Fatal(err)
			}
			request.SessionID = first.SessionID
		}
		var streamed []gotato.Event
		out, err := runner.StreamRun(ctx, request, func(e gotato.Event) error {
			streamed = append(streamed, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		stored := len(storedOnly(streamed))
		if stored == 0 || out.Events != stored {
			t.Fatalf("existing session %v: RunResult.Events = %d, the stream carried %d stored Events", existing, out.Events, stored)
		}
	}
}
