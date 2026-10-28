package gotato

import (
	"context"
	"errors"
	"io"
	"testing"
)

// Next returns the context's error, not io.EOF, when the context given to
// Subscribe is canceled.
func TestCanceledSubscribeContextEndsWithItsError(t *testing.T) {
	feed := NewFeed(nil)
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := feed.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	cancel()
	_, err = sub.Next(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Next = %v, want context.Canceled", err)
	}
}

// Close is still a clean end.
func TestCloseStillEndsWithEOF(t *testing.T) {
	feed := NewFeed(nil)
	sub, err := feed.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = sub.Close()
	if _, err := sub.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next = %v, want io.EOF", err)
	}
}
