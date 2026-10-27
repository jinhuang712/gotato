package gotato

import (
	"context"
	"testing"
	"time"
)

// blockingToolSet resolves only when its context ends.
type blockingToolSet struct{ entered chan struct{} }

func (blockingToolSet) Spec() ToolSetSpec { return ToolSetSpec{Name: "slow"} }

func (b blockingToolSet) Tools(ctx context.Context) ([]Tool, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNewSessionContextCancelsAResolutionInProgress(t *testing.T) {
	set := blockingToolSet{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewSessionContext(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}),
			WithToolSet(set))
		done <- err
	}()
	select {
	case <-set.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the resolution never reached the ToolSet")
	}
	cancel()
	select {
	case err := <-done:
		if !IsCode(err, ErrCanceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NewSessionContext did not return after the context ended")
	}
}

func TestNewSessionContextRefusesAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSessionContext(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}})); !IsCode(err, ErrCanceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}
