package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type stranger struct{}

// guarded refuses the callers whose ctx carries stranger{}, the way a store
// wrapper of an embedding application answers a caller it does not authorize.
type guarded struct{ session.Store }

func refused(ctx context.Context) bool { return ctx.Value(stranger{}) != nil }

func (g guarded) Load(ctx context.Context, id string) (session.Loaded, error) {
	if refused(ctx) {
		return session.Loaded{}, session.ErrNotFound
	}
	return g.Store.Load(ctx, id)
}

func (g guarded) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if refused(ctx) {
		return nil, session.ErrNotFound
	}
	return g.Store.Log(ctx, id, after, limit)
}

// a caller the Store refuses must change nothing. Compact, Notice
// and Run reach the warm copy before they reach the Store: the refused caller
// is answered not_found, and the owner's warm Session is dropped
// ("taken_over") all the same.
func TestRefusedCallerDoesNotEvictTheWarmSession(t *testing.T) {
	calls := map[string]func(ctx context.Context, r *service.Runner, id string) error{
		"compact": func(ctx context.Context, r *service.Runner, id string) error {
			_, err := r.Compact(ctx, id, modelctx.CompactConfig{})
			return err
		},
		"notice": func(ctx context.Context, r *service.Runner, id string) error {
			_, err := r.Notice(ctx, service.NoticeRequest{SessionID: id, Kind: "k", Text: "t"})
			return err
		},
		"run": func(ctx context.Context, r *service.Runner, id string) error {
			_, err := r.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hi"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			runner := newRunnerOn(t, guarded{session.NewMemoryStore()}, service.Agent{Name: "a", Model: testkit.EchoModel{}})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := runner.CreateSession(ctx, "a", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hello"}); err != nil {
				t.Fatal(err)
			}
			if runner.WarmSessions() != 1 {
				t.Fatalf("warm sessions = %d, want 1 after the owner's Run", runner.WarmSessions())
			}
			err = call(context.WithValue(ctx, stranger{}, true), runner, s.ID())
			if !gotato.IsCode(err, gotato.CodeNotFound) {
				t.Fatalf("refused caller got %v, want not_found", err)
			}
			if got := runner.WarmSessions(); got != 1 {
				t.Errorf("warm sessions = %d after a refused %s, want 1: the refused caller evicted the owner's copy", got, name)
			}
		})
	}
}
