package gotato

import (
	"context"
	"slices"
	"sync"
	"testing"
)

// requestModel keeps every whole ModelRequest it is sent.
type requestModel struct {
	mu       sync.Mutex
	scripts  [][]ModelEvent
	requests []ModelRequest
}

func (m *requestModel) Stream(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := len(m.requests)
	m.requests = append(m.requests, request)
	return &testStream{events: m.scripts[index]}, nil
}

func TestTheRequestNamesTheLoadedAndTheUnloadedCatalogTools(t *testing.T) {
	set, _ := ordersSet()
	model := &requestModel{scripts: [][]ModelEvent{
		searchCall("s", map[string]any{"names": []string{"orders.lookup"}}),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set))
	runCollecting(t, agent, "find order 7")

	first, second := model.requests[0], model.requests[1]
	if len(first.LoadedTools) != 0 || !slices.Equal(unloadedIDs(first), []string{"orders.list_open", "orders.lookup", "orders.refund"}) {
		t.Fatalf("first request: loaded %v, unloaded %v", first.LoadedTools, unloadedIDs(first))
	}
	if !slices.Equal(second.LoadedTools, []string{"orders.lookup"}) || !slices.Equal(unloadedIDs(second), []string{"orders.list_open", "orders.refund"}) {
		t.Fatalf("second request: loaded %v, unloaded %v", second.LoadedTools, unloadedIDs(second))
	}
	// The load is on the Tool Result that made it, for a gateway to show.
	var found bool
	for _, message := range second.Messages {
		if message.ToolResult != nil && slices.Equal(message.ToolResult.LoadedTools, []string{"orders.lookup"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the search_tools result does not list the load: %+v", second.Messages)
	}
}

func unloadedIDs(request ModelRequest) []string {
	var ids []string
	for _, spec := range request.UnloadedTools {
		ids = append(ids, spec.ID)
	}
	return ids
}
