package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Session whose last Checkpoint holds one summary written before Segments is
// not compacted: the command exits 1 (runtime error) and says why on stderr.
func TestContextCompactRefusesASessionWithOneOldSummary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	store, err := session.NewFileStore(filepath.Join(h.home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	record := session.New(session.WithID("old-summary"))
	summary := gotato.UserMessage("Summary of earlier conversation:\nthey talked")
	summary.ID = "summary-1"
	summary.Parts[0].Metadata = map[string]string{"compaction": "summary"}
	record.Checkpoint(gotato.CompactionOnDemand, []gotato.Message{summary, gotato.UserMessage("one"), gotato.AssistantMessage("two")}, &session.Compaction{ReplacedMessages: 2, Recap: "truncate"})
	writer, err := session.Create(ctx, store, record, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := h.run("context", "compact", "old-summary", "--keep", "1", "--json")
	var failure struct {
		Error    string `json:"error"`
		ExitCode int    `json:"exit_code"`
	}
	h.mustJSON(out, &failure)
	if code != ExitError || failure.ExitCode != ExitError || !strings.HasPrefix(failure.Error, "invalid_state:") || !strings.Contains(errOut, "summary") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}

// --min-tokens skips a stretch too short to compact and says so; --keep 0
// (the default) keeps the latest Run's Turns; --recap truncate asks for a Recap.
func TestContextCompactMinTokensAndTheDefaultTail(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	for _, prompt := range []string{"one", "two", "three"} {
		h.ok("run", "--session", id, "--quiet", prompt)
	}
	type answer struct {
		Replaced       bool   `json:"replaced"`
		Reason         string `json:"reason"`
		MessagesBefore int    `json:"messages_before"`
		MessagesAfter  int    `json:"messages_after"`
		Compaction     *struct {
			Segment     int `json:"segment"`
			ReplacedMsg int `json:"replaced_messages"`
		} `json:"compaction"`
	}
	var skipped answer
	h.mustJSON(h.ok("context", "compact", id, "--min-tokens", "100000", "--json"), &skipped)
	if skipped.Replaced || skipped.Reason != "below_min_tokens" || skipped.Compaction != nil || skipped.MessagesAfter != skipped.MessagesBefore {
		t.Fatalf("below --min-tokens: %+v", skipped)
	}
	if code, _, errOut := h.run("context", "compact", id, "--min-tokens", "-1", "--json"); code != ExitUsage {
		t.Fatalf("a negative --min-tokens exits %d (%s), want usage", code, errOut)
	}
	var done answer
	h.mustJSON(h.ok("context", "compact", id, "--min-tokens", "1", "--json"), &done)
	// No --keep: the latest Run ("three" and its reply) stays, four Messages close.
	if !done.Replaced || done.Compaction == nil || done.Compaction.Segment != 1 || done.Compaction.ReplacedMsg != 4 || done.MessagesAfter != 6 {
		t.Fatalf("compact: %+v", done)
	}
	var again answer
	h.mustJSON(h.ok("context", "compact", id, "--json"), &again)
	if again.Replaced || again.Reason != "nothing_to_close" {
		t.Fatalf("compact again: %+v", again)
	}
	h.ok("run", "--session", id, "--quiet", "four")
	var withRecap struct {
		Compaction struct {
			Recap string `json:"recap"`
		} `json:"compaction"`
	}
	h.mustJSON(h.ok("context", "compact", id, "--recap", "truncate", "--json"), &withRecap)
	if withRecap.Compaction.Recap != "truncate" {
		t.Fatalf("recap = %q", withRecap.Compaction.Recap)
	}
}

// --recap model has the session's Agent write the Recap on the Context the
// next call would send; the default writes none, and a model that does not
// follow the five headings leaves the Segment without one.
func TestContextCompactRecapModelIsAChoice(t *testing.T) {
	h := newHarness(t)
	type answer struct {
		Replaced   bool `json:"replaced"`
		Compaction struct {
			Segment int    `json:"segment"`
			Recap   string `json:"recap"`
		} `json:"compaction"`
	}
	for name, tc := range map[string]struct {
		model string
		args  []string
		recap string
	}{
		"default is none":    {"demo", nil, "none"},
		"demo writes one":    {"demo", []string{"--recap", "model"}, "model:demo"},
		"echo does not":      {"echo", []string{"--recap", "model"}, "none"},
		"output limit given": {"demo", []string{"--recap", "model", "--recap-max-tokens", "4000"}, "model:demo"},
	} {
		var created map[string]any
		h.mustJSON(h.ok("session", "create", "--model", tc.model, "--json"), &created)
		id := created["id"].(string)
		for _, prompt := range []string{"one", "two", "three"} {
			h.ok("run", "--session", id, "--model", tc.model, "--quiet", prompt)
		}
		var got answer
		h.mustJSON(h.ok(append([]string{"context", "compact", id, "--model", tc.model, "--json"}, tc.args...)...), &got)
		if !got.Replaced || got.Compaction.Segment != 1 || got.Compaction.Recap != tc.recap {
			t.Errorf("%s: %+v, want recap %q", name, got, tc.recap)
		}
		var doc struct {
			Messages []gotato.Message `json:"messages"`
		}
		h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
		hasRecap := false
		for _, m := range doc.Messages {
			text := gotato.TextOf(m)
			hasRecap = hasRecap || strings.Contains(text, "<recap n=\"1\">")
			if strings.Contains(text, "Write a recap") {
				t.Errorf("%s: the ask was stored", name)
			}
		}
		if hasRecap != (tc.recap != "none") {
			t.Errorf("%s: recap in the Context = %v", name, hasRecap)
		}
	}
}
