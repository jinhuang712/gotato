package modelctx_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

func commitTexts(t *testing.T, w *session.Writer, run string, texts ...string) {
	t.Helper()
	ctx := context.Background()
	for i, text := range texts {
		m := gotato.UserMessage(text)
		if i%2 == 1 {
			m = gotato.AssistantMessage(text)
			m.StopReason = gotato.StopReasonEndTurn
		}
		m.RunID = gotato.RunID(run)
		m.ID = gotato.MessageID(run + "-" + string(rune('a'+i)))
		if err := w.Append(m); err != nil {
			t.Fatal(err)
		}
		h := gotato.Header{Time: time.Date(2026, 10, 19, 10, i, 0, 0, time.UTC), RunID: m.RunID}
		if _, err := w.Write(ctx, []gotato.Event{gotato.MessageCommitted{Header: h, Message: m, Source: gotato.MessageSourcePrompt}}); err != nil {
			t.Fatal(err)
		}
	}
}

func readSegmentOf(t *testing.T, record *session.Record, store session.Store, segment int) string {
	t.Helper()
	ctx := context.Background()
	tools, _ := modelctx.SegmentTools(record, store, modelctx.ReadSegmentConfig{}).Tools(ctx)
	res, err := tools[0].Execute(ctx, gotato.ToolUse{Arguments: []byte(`{"segment":` + string(rune('0'+segment)) + `}`)}, nil)
	var out strings.Builder
	for _, p := range res.Content {
		out.WriteString(p.Text)
	}
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String()
}

// A Segment a fork inherits is read from its parent's log. When the parent is
// deleted and another Session takes the ID, the read finds the Messages
// removed, not the other Session's Messages; a fork of the fork is the same.
func TestInheritedSegmentOfARecreatedParentIsNotRead(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	parent := session.New(session.WithID("shared-id"))
	w, err := session.CreateWriter(ctx, store, parent, "a")
	if err != nil {
		t.Fatal(err)
	}
	commitTexts(t, w, "r1", "q1 original", "a1 original", "q2 original", "a2 original")
	if _, err := modelctx.Compact(ctx, parent, modelctx.CompactConfig{KeepMessages: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, "shared-id")
	if err != nil || len(view.Segments()) == 0 {
		t.Fatalf("view: %v segments=%d", err, len(view.Segments()))
	}
	child := session.Fork(view, session.WithID("child"))
	cw, err := session.CreateWriter(ctx, store, child, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	grandchild := session.Fork(child, session.WithID("grandchild"))
	seg := child.Segments()[0].Number
	if out := readSegmentOf(t, grandchild, store, seg); !strings.Contains(out, "q1 original") {
		t.Fatalf("before the delete the grandchild should read the original:\n%s", out)
	}

	if err := store.Delete(ctx, "shared-id"); err != nil {
		t.Fatal(err)
	}
	w2, err := session.CreateWriter(ctx, store, session.New(session.WithID("shared-id")), "a")
	if err != nil {
		t.Fatal(err)
	}
	commitTexts(t, w2, "x1", "SECRET-OF-ANOTHER-TENANT", "reply", "more secret", "reply")
	if err := w2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]*session.Record{"child": child, "grandchild": grandchild} {
		out := readSegmentOf(t, record, store, seg)
		if strings.Contains(out, "SECRET-OF-ANOTHER-TENANT") {
			t.Fatalf("%s read another Session's Messages through a stale parent reference:\n%s", name, out)
		}
		if !strings.Contains(out, "were removed from the Record") {
			t.Fatalf("%s: want the removed notice, got:\n%s", name, out)
		}
	}
}
