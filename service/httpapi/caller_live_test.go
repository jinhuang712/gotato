package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// While the owner's Run is live and held on this instance, another caller
// cannot cancel it, steer it or send it a notice: each call reads through the
// store first, so the wrapper's not_found answers 404. The owner still can.
func TestARefusalAlsoGuardsALiveRun(t *testing.T) {
	store := &ownerStore{Store: session.NewMemoryStore()}
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "slow", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := httpapi.New(runner)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, r.Header.Get("X-Caller"))))
	}))
	defer server.Close()

	alice := context.WithValue(context.Background(), callerKey{}, "alice")
	created, err := runner.CreateSession(alice, "slow", map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(alice, service.RunRequest{SessionID: id, Prompt: "hi"})
	}()
	defer func() { <-done }()
	defer close(block)
	for model.Calls() == 0 {
		runtime.Gosched()
	}

	steer := map[string]any{"text": "change course"}
	notice := map[string]any{"kind": "hint", "text": "be brief"}
	for _, tc := range []struct {
		name, path string
		body       any
	}{
		{"steer", "/messages", steer},
		{"notice", "/notices", notice},
		{"cancel", "/cancel", nil},
	} {
		if status, body := callAs(t, server, "mallory", http.MethodPost, "/v1/sessions/"+id+tc.path, tc.body); status != http.StatusNotFound {
			t.Errorf("%s by another caller = %d %s, want 404", tc.name, status, body)
		}
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/messages", steer); status >= 300 {
		t.Errorf("steer by the owner = %d %s", status, body)
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/notices", notice); status >= 300 {
		t.Errorf("notice by the owner = %d %s", status, body)
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/cancel", nil); status != http.StatusAccepted {
		t.Errorf("cancel by the owner = %d %s", status, body)
	}
}
