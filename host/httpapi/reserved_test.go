package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/host/httpapi"
	"github.com/jinhuang712/gotato/store"
)

// A key under "gotato." is the Runner's. Creating a Session with one answers 400
// invalid_argument, marked not run, naming the key, and creates nothing: a remote
// caller cannot move the Agent, a Tool switch or the cache key, nor reach the
// instruction or the ceiling, which no key sets any longer.
func TestAReservedMetadataKeyAnswers400(t *testing.T) {
	server, runner := newServer(t)
	for _, key := range []string{
		"gotato.agent",
		"gotato.instruction",
		"gotato.compact_ceiling",
		"gotato.cache_key",
		"gotato.tool.files.read",
		"gotato.anything_later",
	} {
		for _, id := range []string{"", "chosen"} {
			resp, body := send(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": id, "metadata": map[string]string{"tenant": "x", key: "y"}}, nil)
			var failure struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				NotRun  bool   `json:"not_run"`
			}
			_ = json.Unmarshal(body, &failure)
			if resp.StatusCode != http.StatusBadRequest || failure.Code != "invalid_argument" || !failure.NotRun ||
				resp.Header.Get(httpapi.NotRunHeader) != "true" || !strings.Contains(failure.Message, key) {
				t.Fatalf("create with %q (id %q) = %d %s, want 400 invalid_argument, not run, naming the key", key, id, resp.StatusCode, body)
			}
		}
	}
	page, err := runner.Sessions(context.Background(), store.ListQuery{})
	if err != nil || len(page.Infos) != 0 {
		t.Fatalf("a refused create left %d Sessions (err=%v)", len(page.Infos), err)
	}
	// An ordinary key is still the caller's.
	if status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"metadata": map[string]string{"tenant": "x"}}); status != http.StatusCreated {
		t.Fatalf("create with an ordinary key = %d %s", status, body)
	}
}

// The wire has no field that sets a Session's metadata on a Run, so a client
// cannot reach RunRequest.Metadata or RunRequest.Update: the request is refused
// as malformed, and nothing runs.
func TestARunRequestCannotCarryMetadata(t *testing.T) {
	server, _ := newServer(t)
	status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{})
	if status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &created)
	for _, field := range []string{"metadata", "update", "instruction"} {
		for _, path := range []string{"/runs", "/runs/stream"} {
			status, body := call(t, server, http.MethodPost, "/v1/sessions/"+created.ID+path, map[string]any{
				"prompt": "hi", field: map[string]string{"gotato.instruction": "answer in French"},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("%s with %q = %d %s, want 400", path, field, status, body)
			}
		}
	}
}
