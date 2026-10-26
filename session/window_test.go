package session_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Session over a Record that is not stored can read again only the Events its
// window holds. A stream that starts before the window says so, with
// ErrEventsLost, where it used to start at the window as if nothing was missing.
func TestStreamBeforeTheWindowOfAnUnstoredRecordIsLost(t *testing.T) {
	ctx := context.Background()
	r := session.New()
	s, err := gotato.NewSession(gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(r))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	for i := range 200 {
		if _, err := s.Prompt(ctx, gotato.UserMessage(fmt.Sprintf("p%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := s.SubscribeFrom(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if event, err := sub.Next(read); !errors.Is(err, gotato.ErrEventsLost) {
		t.Fatalf("SubscribeFrom(0) after the window = %v, %v; want ErrEventsLost", event, err)
	}
	// A stream that starts inside the window is fine.
	events := r.Events()
	from := events[len(events)/2].Head().Number
	inside, err := s.SubscribeFrom(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inside.Close() }()
	if event, err := inside.Next(read); err != nil || event.Head().Number != from+1 {
		t.Fatalf("SubscribeFrom(%d) = %v, %v; want %d", from, event, err, from+1)
	}
}
