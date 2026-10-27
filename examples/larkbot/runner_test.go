package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// The bot creates a chat's Session on every message and ignores "exists".
func TestCreateExclusiveIsIdempotentByError(t *testing.T) {
	agent, err := buildAgent("echo")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRunner(runner)
	id := sessionIDFor("oc_test")
	if _, err := runner.CreateSessionExclusive(context.Background(), "echo", id, map[string]string{metaChatID: "oc_test"}); err != nil {
		t.Fatal(err)
	}
	_, err = runner.CreateSessionExclusive(context.Background(), "echo", id, nil)
	if !errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("second create: %v", err)
	}
	rec, err := runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if chat, _ := rec.Get(metaChatID); chat != "oc_test" {
		t.Fatalf("chat_id metadata = %q", chat)
	}
}

func TestBuildAgentUnknown(t *testing.T) {
	if _, err := buildAgent("nope"); err == nil {
		t.Fatal("want error")
	}
}
