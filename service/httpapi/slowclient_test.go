package httpapi_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// stalledWriter is a ResponseWriter whose client has stopped reading: its
// first Write blocks until the gate opens, as a full socket would.
type stalledWriter struct {
	header http.Header
	gate   chan struct{}
	mu     sync.Mutex
	body   strings.Builder
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              {}

func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *stalledWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func endedIn(events []gotato.Event) bool {
	return slices.ContainsFunc(events, func(event gotato.Event) bool { return event.Kind() == gotato.KindRunEnded })
}

// An SSE client that stopped reading does not slow the Run: the Run completes
// and its Record holds every Event while the handler is stuck writing the
// first one; and when the client reads again it receives every stored Event
// once, in order.
func TestSSEClientThatStopsReadingDoesNotSlowTheRun(t *testing.T) {
	const calls = 400
	uses := make([]gotato.ToolCall, calls)
	for i := range uses {
		uses[i] = gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprintf("call-%d", i)), ToolID: "lookup", Arguments: []byte(`{}`)}
	}
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{{
			Name:  "bulk",
			Model: testkit.NewFakeModel(testkit.ToolCalls(uses...), testkit.Text("done")),
			Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "bulk", nil)
	if err != nil {
		t.Fatal(err)
	}

	w := &stalledWriter{header: http.Header{}, gate: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+s.ID()+"/runs/stream", strings.NewReader(`{"prompt":"go"}`))
	request.Header.Set("Content-Type", "application/json")
	served := make(chan struct{})
	go func() {
		defer close(served)
		httpapi.New(runner).ServeHTTP(w, request)
	}()

	// The Run completes, and every Event is in the Record, with the handler
	// blocked on its first write.
	var stored []gotato.Event
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stored, err = session.StoredEvents(ctx, runner.Store(), s.ID(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if endedIn(stored) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !endedIn(stored) {
		t.Fatalf("the Run did not settle while the client was not reading: %d stored Events, body %.200q", len(stored), w.text())
	}
	select {
	case <-served:
		t.Fatal("the handler returned while its client was not reading")
	default:
	}
	counts := map[gotato.EventKind]int{}
	for _, event := range stored {
		counts[event.Kind()]++
	}
	if counts[gotato.KindToolStarted] != calls || counts[gotato.KindToolEnded] != calls || counts[gotato.KindToolResultCommitted] != calls {
		t.Fatalf("the Record is incomplete: %v", counts)
	}

	// The client reads again: it still gets every stored Event, once, in order.
	close(w.gate)
	select {
	case <-served:
	case <-time.After(30 * time.Second):
		t.Fatal("the handler did not finish after the client went on")
	}
	var got []string
	var last string
	scanner := bufio.NewScanner(strings.NewReader(w.text()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if kind, ok := strings.CutPrefix(line, "event: "); ok {
			last = kind
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || last == "result" || last == "error" || last == "reconnect" {
			continue
		}
		event, err := gotato.UnmarshalEvent([]byte(data))
		if err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		if event.Kind().Class() == gotato.ClassStored {
			got = append(got, fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number))
		}
	}
	if last != "result" {
		t.Fatalf("the stream ended with %q, want result", last)
	}
	// The stream starts at the Run's first Event; the Record's from there on
	// is what it must equal.
	var first uint64
	if len(got) > 0 {
		_, _ = fmt.Sscanf(got[0][strings.Index(got[0], "#")+1:], "%d", &first)
	}
	var want []string
	for _, event := range stored {
		if event.Head().Number >= first {
			want = append(want, fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number))
		}
		// The stream ends at the Run's run_ended.
		if event.Kind() == gotato.KindRunEnded {
			break
		}
	}
	if len(got) < 3*calls || !slices.Equal(got, want) {
		diff := 0
		for diff < len(got) && diff < len(want) && got[diff] == want[diff] {
			diff++
		}
		t.Fatalf("the SSE stream's stored Events differ from the Record's: %d against %d, first difference at %d", len(got), len(want), diff)
	}
}
