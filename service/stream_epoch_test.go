package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// readUntilEnd reads a stream on its own goroutine, as an HTTP handler does,
// and reports the numbers of the stored Events it delivered and the error it
// ended with.
func readUntilEnd(stream gotato.Subscription) <-chan streamEnd {
	ended := make(chan streamEnd, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var end streamEnd
		for {
			event, err := stream.Next(ctx)
			if err != nil {
				end.err = err
				ended <- end
				return
			}
			if event.Kind().Class() == gotato.ClassStored {
				end.numbers = append(end.numbers, event.Head().Number)
			}
		}
	}()
	return ended
}

type streamEnd struct {
	numbers []uint64
	err     error
}

// Opening a Record created again for a tool toggle or a compaction writes a
// claim numbered one past an old stream's cursor when the two Records have
// the same length. The claim never reaches the old stream, which ends.
func TestOpeningARecordCreatedAgainSendsNoClaimToAnOldStream(t *testing.T) {
	ctx := context.Background()
	changes := []struct {
		name   string
		change func(runner *service.Runner, id string)
	}{
		{"a tool toggle", func(runner *service.Runner, id string) {
			// An unknown tool: the Record is opened and claimed, then refused.
			_, _ = runner.SetToolActive(ctx, id, "no.such.tool", false)
		}},
		{"a compaction", func(runner *service.Runner, id string) {
			_, _ = runner.Compact(ctx, id, modelctx.CompactOptions{Keep: 2})
		}},
	}
	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			// Each try leaked the claim about half the time before the fix.
			for try := range 20 {
				store := session.NewMemoryStore()
				a, b := recreatedRunner(t, "a", store, -1), recreatedRunner(t, "b", store, -1)
				id := fmt.Sprintf("session-%d", try)
				shape := func(runner *service.Runner) {
					if _, err := runner.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
						t.Fatal(err)
					}
					if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "one"}); err != nil {
						t.Fatal(err)
					}
				}
				shape(a)
				stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
				if err != nil {
					t.Fatal(err)
				}
				readNumbers(t, stream, int(stream.Head))
				// The catch-up's last, empty read, before the delete.
				time.Sleep(20 * time.Millisecond)
				ended := readUntilEnd(stream)
				if err := b.DeleteSession(ctx, id); err != nil {
					t.Fatal(err)
				}
				shape(b)
				if again, err := store.Load(ctx, id); err != nil || again.Head != stream.Head {
					t.Fatalf("the new Record ends at %d (%v), want the old one's %d", again.Head, err, stream.Head)
				}
				c.change(a, id)
				select {
				case end := <-ended:
					if len(end.numbers) > 0 {
						t.Fatalf("try %d: the old stream delivered Events %v of the Record created again", try, end.numbers)
					}
					if !errors.Is(end.err, service.ErrCannotResume) {
						t.Fatalf("try %d: the old stream ended with %v, want cannot_resume", try, end.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("try %d: the old stream never ended", try)
				}
				_ = stream.Close()
			}
		})
	}
}
