package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Session whose last Checkpoint holds one summary written before Segments is
// not compacted: the route answers 409 with the refusal.
func TestCompactRefusesASessionWithOneOldSummary(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
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
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	status, body := call(t, server, http.MethodPost, "/v1/sessions/old-summary/compact", map[string]any{"keep": 1})
	if status != http.StatusConflict || !strings.Contains(string(body), "summary") {
		t.Fatalf("compact = %d %s, want 409 naming the summary", status, body)
	}
}

// {"min_tokens":N} skips a stretch estimated below N and says so; with no
// "keep" the latest Run's Turns stay.
func TestCompactTakesMinTokensAndKeepsTheLatestRun(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: "echo", Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	decode := func(body []byte) map[string]any {
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return out
	}
	status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": 100000})
	skipped := decode(body)
	if status != http.StatusOK || skipped["replaced"] != false || skipped["reason"] != "below_min_tokens" || skipped["messages_after"] != float64(6) {
		t.Fatalf("below min_tokens = %d %v", status, skipped)
	}
	if status, _ = call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": -1}); status != http.StatusBadRequest {
		t.Fatalf("a negative min_tokens = %d, want 400", status)
	}
	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": 1})
	done := decode(body)
	if status != http.StatusOK || done["replaced"] != true || done["messages_after"] != float64(6) {
		t.Fatalf("compact = %d %v", status, done)
	}
	if compaction, _ := done["compaction"].(map[string]any); compaction["segment"] != float64(1) || compaction["recap"] != "none" || compaction["replaced_messages"] != float64(4) {
		t.Fatalf("compaction = %v", done["compaction"])
	}
}

// The compact route says when it merged the oldest Segments.
func TestCompactAnswersWithTheMergeOfTheOldestSegments(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{
		Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Compact: modelctx.CompactPolicy{SegmentBudget: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	run := func(prompts ...string) {
		for _, prompt := range prompts {
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: "echo", Prompt: prompt})
			if err != nil {
				t.Fatal(err)
			}
			id = result.SessionID
		}
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	compact := func() map[string]any {
		status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{})
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil || status != http.StatusOK {
			t.Fatalf("compact = %d %s (%v)", status, body, err)
		}
		return out
	}
	run("one", "two", "three")
	if first := compact(); first["replaced"] != true || first["merge"] != nil {
		t.Fatalf("the first compaction: %v", first)
	}
	run("four", "five")
	second := compact()
	merge, _ := second["merge"].(map[string]any)
	if second["replaced"] != true || merge["merged"] != true || merge["recap"] != "digest" || len(merge["replaced"].([]any)) != 2 {
		t.Fatalf("the second compaction: %v", second)
	}
}
