package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Session whose Context holds Segments is not compacted into one summary:
// the route answers 409 with the refusal.
func TestCompactRefusesASessionWithSegments(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
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
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	status, body := call(t, server, http.MethodPost, "/v1/sessions/with-segments/compact", map[string]any{"keep": 1})
	if status != http.StatusConflict || !strings.Contains(string(body), "Segments") {
		t.Fatalf("compact = %d %s, want 409 naming the Segments", status, body)
	}
}
