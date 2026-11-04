package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
)

// with no AgentProfiles, GET /v1/agents answers [] as GET /v1/sessions does.
func TestEmptyAgentsIsAnEmptyList(t *testing.T) {
	runner, err := host.New(host.Config{Store: memStore(), Profiles: profile.NewRegistry(nil)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	_, sessions := call(t, server, http.MethodGet, "/v1/sessions", nil)
	if !strings.Contains(string(sessions), `"sessions":[]`) {
		t.Fatalf("GET /v1/sessions = %s", sessions)
	}
	status, agents := call(t, server, http.MethodGet, "/v1/agents", nil)
	if status != http.StatusOK || !strings.Contains(string(agents), `"agents":[]`) {
		t.Fatalf("GET /v1/agents = %d %s, want \"agents\":[] (sessions gave %s)", status, strings.TrimSpace(string(agents)), strings.TrimSpace(string(sessions)))
	}
}
