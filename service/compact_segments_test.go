package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// windowedEcho is a Model that says how large its window is.
type windowedEcho struct {
	testkit.EchoModel
	window int
}

func (m windowedEcho) ContextWindowTokens() int { return m.window }

// A compaction on demand takes what the call leaves unset from the Agent: its
// Recap writer, its time zone, and the Model's window.
func TestRunnerCompactTakesTheAgentsCompactionSettings(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	agent := service.Agent{Name: "echo", Model: windowedEcho{window: 1 << 20}, Compact: modelctx.CompactPolicy{
		Recap:           modelctx.TruncateRecap{},
		SegmentSettings: modelctx.SegmentSettings{Location: zone},
	}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	skipped, err := runner.Compact(ctx, id, modelctx.CompactConfig{MinTokens: 1 << 20})
	if err != nil || skipped.Replaced || skipped.Reason != modelctx.CompactReasonBelowMinTokens {
		t.Fatalf("below min_tokens = %+v, %v", skipped, err)
	}
	result, err := runner.Compact(ctx, id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "truncate" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	segments := record.Segments()
	if len(segments) != 1 || !strings.Contains(gotato.TextOf(segments[0].Messages[0]), "+08:00") {
		t.Fatalf("Segments = %+v, want times in the Agent's zone", segments)
	}
	if tail := record.Tail(); len(tail) != 2 || gotato.TextOf(tail[0]) != "three" {
		t.Fatalf("tail = %+v, want the latest Run", tail)
	}
}

// A model that says its window is compacted past the default ceiling, the
// window less its reserve, with no ceiling set; the Session's own ceiling
// wins.
func TestTheModelsWindowGivesTheDefaultCeiling(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: windowedEcho{window: 200_000}})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Inspect(ctx, first.SessionID)
	if err != nil || report.CompactCeiling != 180_000 {
		t.Fatalf("ceiling = %d, %v; want 180000 from the window", report.CompactCeiling, err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again",
		Update: map[string]string{service.MetaCompactCeiling: "9000"}}); err != nil {
		t.Fatal(err)
	}
	if report, err = runner.Inspect(ctx, first.SessionID); err != nil || report.CompactCeiling != 9000 {
		t.Fatalf("ceiling = %d, %v; want the Session's 9000", report.CompactCeiling, err)
	}
	// Without a window there is no ceiling.
	plain, _ := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}})
	one, err := plain.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if report, err = plain.Inspect(ctx, one.SessionID); err != nil || report.CompactCeiling != 0 {
		t.Fatalf("ceiling = %d, %v; want none", report.CompactCeiling, err)
	}
}
