package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Session whose Context holds Segments is not compacted into one summary:
// the command exits 1 (runtime error) and says why on stderr.
func TestContextCompactRefusesASessionWithSegments(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	store, err := session.NewFileStore(filepath.Join(h.home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	record := session.New(session.WithID("with-segments"))
	for _, m := range []gotato.Message{gotato.UserMessage("one"), gotato.AssistantMessage("two")} {
		m.ID = gotato.MessageID("m-" + gotato.TextOf(m))
		if err := record.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := record.CloseSegment(gotato.Segment{Messages: record.Messages(), Recap: "recap"}); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	writer, err := session.Create(ctx, store, record, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := h.run("context", "compact", "with-segments", "--keep", "1", "--json")
	var failure struct {
		Error    string `json:"error"`
		ExitCode int    `json:"exit_code"`
	}
	h.mustJSON(out, &failure)
	if code != ExitError || failure.ExitCode != ExitError || !strings.HasPrefix(failure.Error, "invalid_state:") || !strings.Contains(errOut, "Segments") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}
