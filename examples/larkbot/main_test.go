package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jinhuang712/gotato/host"
)

func TestReadyzFollowsDraining(t *testing.T) {
	draining := false
	h := readyHandler(func() bool { return draining })
	for _, c := range []struct {
		draining bool
		want     int
	}{{false, http.StatusOK}, {true, http.StatusServiceUnavailable}} {
		draining = c.draining
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
		if rec.Code != c.want {
			t.Fatalf("draining=%v: status %d, want %d", c.draining, rec.Code, c.want)
		}
	}
}

// The bot creates a chat's Session on every message and ignores "exists".
func TestCreateExclusiveIsIdempotentByError(t *testing.T) {
	agent, err := buildAgent("echo")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := host.New(host.Config{Store: memStore(), Profiles: registryOf(agent), DefaultAgent: agent.Name})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRunner(runner)
	id := sessionIDFor("oc_test")
	if _, err := runner.CreateSession(context.Background(), host.CreateSessionRequest{Agent: "echo", ID: id, Metadata: map[string]string{metaChatID: "oc_test"}}); err != nil {
		t.Fatal(err)
	}
	_, err = runner.CreateSession(context.Background(), host.CreateSessionRequest{Agent: "echo", ID: id})
	if !errors.Is(err, host.ErrSessionExists) {
		t.Fatalf("second create: %v", err)
	}
	rec, err := runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if chat := rec.Metadata[metaChatID]; chat != "oc_test" {
		t.Fatalf("chat_id metadata = %q", chat)
	}
}

func TestBuildAgentUnknown(t *testing.T) {
	if _, err := buildAgent("nope"); err == nil {
		t.Fatal("want error")
	}
}
