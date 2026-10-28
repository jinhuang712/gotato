package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// resolvingSet is a ToolSet that blocks while it resolves, as one that dials a
// remote server does, and hands the context it was given to the test.
type resolvingSet struct {
	entered chan context.Context
	release chan struct{}
}

func (resolvingSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "resolving"} }

func (s resolvingSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	s.entered <- ctx
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, nil
	}
}

// startResolving starts a Run whose ToolSet blocks and returns once the
// ToolSet is resolving.
func startResolving(t *testing.T) (*service.Runner, string, context.Context, chan error, resolvingSet) {
	t.Helper()
	set := resolvingSet{entered: make(chan context.Context, 1), release: make(chan struct{})}
	r, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "slow", Model: testkit.EchoModel{}, Options: []gotato.SessionOption{gotato.WithToolSet(set)}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	rec, err := r.CreateSession(context.Background(), "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, e := r.Run(context.Background(), service.RunRequest{SessionID: rec.ID(), Prompt: "start"})
		done <- e
	}()
	return r, rec.ID(), <-set.entered, done, set
}

// TestAbortCancelsToolSetResolution: an Abort that arrives while the Run
// builds its Session reaches the ToolSet that is resolving.
func TestAbortCancelsToolSetResolution(t *testing.T) {
	r, id, resolving, done, set := startResolving(t)
	if err := r.AbortSession(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	canceled := resolving.Err()
	close(set.release)
	<-done
	if canceled == nil {
		t.Fatal("Abort returned while the ToolSet's context was still live")
	}
}

// TestDrainCancelsToolSetResolution: a Drain whose grace ends while a Run
// builds its Session stops the ToolSet that is resolving.
func TestDrainCancelsToolSetResolution(t *testing.T) {
	r, _, resolving, done, set := startResolving(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = r.Drain(ctx)
	canceled := resolving.Err()
	close(set.release)
	<-done
	if canceled == nil {
		t.Fatal("Drain ended while the ToolSet's context was still live")
	}
}
