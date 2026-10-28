package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func TestRunnerKeysEachSessionsPromptCache(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	other, err := runner.Run(ctx, service.RunRequest{Prompt: "three"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls = %d, want 3", len(requests))
	}
	// One stable key per Session, and never the raw Session ID.
	want := service.CacheKey(first.SessionID)
	if requests[0].CacheKey != want || requests[1].CacheKey != want ||
		requests[2].CacheKey != service.CacheKey(other.SessionID) || requests[2].CacheKey == want ||
		strings.Contains(want, first.SessionID) {
		t.Fatalf("cache keys = %q, %q, %q; want %q twice, then another", requests[0].CacheKey, requests[1].CacheKey, requests[2].CacheKey, want)
	}
}

func TestForkKeepsTheParentsCacheKey(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	parent, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, parent.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := runner.Fork(ctx, child.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{child.ID(), grandchild.ID()} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "branch"}); err != nil {
			t.Fatal(err)
		}
	}
	// A fork starts with its parent's history, so it reads the parent's cached
	// prefix: every generation carries the first parent's key.
	want := service.CacheKey(parent.SessionID)
	for i, request := range model.Requests() {
		if request.CacheKey != want {
			t.Fatalf("request %d cache key = %q, want the parent's %q", i, request.CacheKey, want)
		}
	}
	stored, err := session.View(ctx, store, grandchild.ID())
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := stored.Get(service.MetaCacheKey); key != want {
		t.Fatalf("stored %s = %q, want %q", service.MetaCacheKey, key, want)
	}
}
