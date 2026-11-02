package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// windowedEcho is a Model that says how large its window is.
type windowedEcho struct {
	testkit.EchoModel
	window int
}

func (m windowedEcho) Config() gotato.ModelConfig {
	return gotato.ModelConfig{Facts: gotato.ModelFacts{Window: gotato.WindowTokens(m.window)}}
}

// A compaction on demand takes what the call leaves unset from the Agent: its
// Recap writer, its time zone, and the Model's window.
func TestRunnerCompactTakesTheAgentsCompactionSettings(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	agent := profile.AgentProfile{Name: "echo", Model: windowedEcho{window: 1 << 20}, Compact: record.Compactor{CompactPolicy: record.CompactPolicy{Location: zone}, Recap: record.TruncateRecap{}}}
	runner, st := newRunner(t, agent)
	ctx := context.Background()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	skipped, err := runner.Compact(ctx, id, record.CompactConfig{MinTokens: 1 << 20})
	if err != nil || skipped.Replaced || skipped.Reason != record.CompactReasonBelowMinTokens {
		t.Fatalf("below min_tokens = %+v, %v", skipped, err)
	}
	result, err := runner.Compact(ctx, id, record.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "truncate" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	rec, err := store.View(ctx, st.Records, id)
	if err != nil {
		t.Fatal(err)
	}
	segments := rec.Segments()
	if len(segments) != 1 || !strings.Contains(gotato.TextOf(segments[0].Messages[0]), "+08:00") {
		t.Fatalf("Segments = %+v, want times in the Agent's zone", segments)
	}
	if tail := rec.Tail(); len(tail) != 2 || gotato.TextOf(tail[0]) != "three" {
		t.Fatalf("tail = %+v, want the latest Run", tail)
	}
}

// A model that says its window is compacted past the default ceiling, the
// window less its reserve, with no ceiling set; the Agent's own ceiling wins,
// and a ceiling stored in a Session's metadata is ignored.
func TestTheModelsWindowGivesTheDefaultCeiling(t *testing.T) {
	runner, store := newRunner(t,
		profile.AgentProfile{Name: "echo", Model: windowedEcho{window: 200_000}},
		profile.AgentProfile{Name: "capped", Model: windowedEcho{window: 200_000}, Compact: record.Compactor{CompactPolicy: record.CompactPolicy{CeilingTokens: 9000}}})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Inspect(ctx, first.SessionID, service.InspectConfig{})
	if err != nil || report.CompactCeilingTokens != 180_000 {
		t.Fatalf("ceiling = %d, %v; want 180000 from the window", report.CompactCeilingTokens, err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Agent: "capped", Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if report, err = runner.Inspect(ctx, first.SessionID, service.InspectConfig{}); err != nil || report.CompactCeilingTokens != 9000 {
		t.Fatalf("ceiling = %d, %v; want the Agent's 9000", report.CompactCeilingTokens, err)
	}
	// A Record of an earlier version may still carry a ceiling of its own; the
	// Runner ignores it, and the window gives the ceiling.
	old := plant(t, store, record.WithMetadata(map[string]string{metaAgentKey: "echo", metaCeilingKey: "100"}))
	if report, err = runner.Inspect(ctx, old, service.InspectConfig{}); err != nil || report.CompactCeilingTokens != 180_000 {
		t.Fatalf("ceiling = %d, %v; want 180000: a stored ceiling is ignored", report.CompactCeilingTokens, err)
	}
	// Without a window there is no ceiling.
	plain, _ := newRunner(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}})
	one, err := plain.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if report, err = plain.Inspect(ctx, one.SessionID, service.InspectConfig{}); err != nil || report.CompactCeilingTokens != 0 {
		t.Fatalf("ceiling = %d, %v; want none", report.CompactCeilingTokens, err)
	}
}

// A ModelRecap written as a pointer gets the Agent's Model like the value does.
func TestRunnerCompactFillsTheModelOfAPointerModelRecap(t *testing.T) {
	recap := &record.ModelRecap{}
	runner, _ := newRunner(t, profile.AgentProfile{Name: "echo", Model: windowedEcho{window: 1 << 20}, Compact: record.Compactor{Recap: recap}})
	ctx := context.Background()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	result, err := runner.Compact(ctx, id, record.CompactConfig{})
	if errors.Is(err, record.ErrNoRecapModel) {
		t.Fatalf("compact = %+v, %v: the pointer ModelRecap got no Model", result, err)
	}
	if recap.Model != nil {
		t.Fatal("the caller's ModelRecap was changed")
	}
}
