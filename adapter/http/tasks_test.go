package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/testkit"
)

// taskServer serves a Runner with two AgentProfiles and the TaskSpecs "morning"
// (of writer) and "audit" (of reader), registered out of name order.
func taskServer(t *testing.T) (*httptest.Server, *testkit.FakeModel) {
	t.Helper()
	model := testkit.NewFakeModel(testkit.Text("done"))
	cfg := withAgents(host.Config{Store: memStore()},
		profile.AgentProfile{Name: "writer", Model: model, ModelName: "m", Instruction: "You write."},
		profile.AgentProfile{Name: "reader", Model: testkit.EchoModel{}, ModelName: "echo"},
	)
	for _, spec := range []profile.TaskSpec{
		{Name: "morning", Profile: "writer", Background: "Report on yesterday."},
		{Name: "audit", Profile: "reader"},
	} {
		if err := cfg.Profiles.RegisterTaskSpecInline(spec); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := host.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(func() {
		server.Close()
		_ = runner.Close(t.Context())
	})
	return server, model
}

func TestTasksListsTheRegisteredTaskSpecsByName(t *testing.T) {
	server, _ := taskServer(t)
	status, body := call(t, server, http.MethodGet, "/v1/tasks", nil)
	if status != http.StatusOK || string(body) != `{"tasks":[{"name":"audit","agent":"reader"},{"name":"morning","agent":"writer"}]}`+"\n" {
		t.Fatalf("tasks = %d %s", status, body)
	}
	// A service with no TaskSpec answers an empty list, not null.
	plain, _ := newServer(t)
	status, body = call(t, plain, http.MethodGet, "/v1/tasks", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"tasks":[]`) {
		t.Fatalf("tasks = %d %s", status, body)
	}
}

func TestCreateASessionForATaskRefusesWhatDoesNotFit(t *testing.T) {
	server, _ := taskServer(t)
	for name, body := range map[string]any{
		"with an agent":   map[string]any{"task": "morning", "agent": "writer"},
		"unregistered":    map[string]any{"task": "nobody"},
		"a value":         map[string]any{"task": map[string]any{"name": "x", "profile": "writer"}},
		"a reserved key":  map[string]any{"task": "morning", "metadata": map[string]string{"gotato.task_spec": "{}"}},
		"a task_spec key": map[string]any{"task_spec": map[string]any{"name": "x", "profile": "writer"}},
	} {
		status, out := call(t, server, http.MethodPost, "/v1/sessions", body)
		if status != http.StatusBadRequest || !strings.Contains(string(out), `"code":"invalid_argument"`) {
			t.Errorf("%s: %d %s, want 400 invalid_argument", name, status, out)
		}
	}
	// Nothing was created.
	if status, out := call(t, server, http.MethodGet, "/v1/sessions", nil); status != http.StatusOK || !strings.Contains(string(out), `"sessions":[]`) {
		t.Fatalf("sessions after refusals = %d %s", status, out)
	}
}

// A TaskSpec Session is created with an ID, run by it, and its Background is in
// the request the model reads once; creating it again is 409 already_exists.
func TestATaskSessionIsCreatedWithAnIDThenRunByIt(t *testing.T) {
	server, model := taskServer(t)
	status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"task": "morning", "id": "morning-2026-11-03"})
	var created struct {
		ID    string `json:"id"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated || created.ID != "morning-2026-11-03" {
		t.Fatalf("create = %d %s (%v)", status, body, err)
	}
	// A retry of the create is already_exists, and makes no second Session.
	status, body = call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"task": "morning", "id": "morning-2026-11-03"})
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"already_exists"`) {
		t.Fatalf("second create = %d %s", status, body)
	}
	for range 2 {
		status, body = call(t, server, http.MethodPost, "/v1/sessions/"+created.ID+"/runs", map[string]any{"prompt": "go"})
		if status != http.StatusOK || !strings.Contains(string(body), `"agent":"writer"`) {
			t.Fatalf("run = %d %s", status, body)
		}
	}
	// Both Runs read the Background once, after the instruction. The fake model
	// replays its last script for the second Run.
	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("model called %d times", len(requests))
	}
	for i, request := range requests {
		if request.SystemInstructions != "You write.\n\nReport on yesterday." {
			t.Errorf("request %d read %q", i, request.SystemInstructions)
		}
	}
	// The Record keeps the TaskSpec as it was.
	status, body = call(t, server, http.MethodGet, "/v1/sessions/"+created.ID, nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"gotato.task_spec"`) || !strings.Contains(string(body), `Report on yesterday.`) {
		t.Fatalf("session = %d %s", status, body)
	}
	if status, body = call(t, server, http.MethodGet, "/v1/sessions", nil); strings.Count(string(body), `"id":"`) != 1 {
		t.Fatalf("sessions = %d %s", status, body)
	}
}
