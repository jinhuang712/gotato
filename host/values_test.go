package host_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/testkit"
)

// The Runner hands out values and not handles: CreateSession and Fork answer a
// Info, Record a Document, and the Runner gives no way to its Store.
func TestTheRunnerReturnsValuesNotHandles(t *testing.T) {
	runner := reflect.TypeFor[*host.Runner]()
	if _, has := runner.MethodByName("Store"); has {
		t.Error("Runner.Store is still there: a caller keeps the Store it gave to New")
	}
	if _, has := runner.MethodByName("CreateSessionExclusive"); has {
		t.Error("CreateSessionExclusive is still there: CreateSession takes an ID")
	}
	want := map[string]reflect.Type{
		"CreateSession": reflect.TypeFor[record.Info](),
		"Fork":          reflect.TypeFor[record.Info](),
		"Record":        reflect.TypeFor[record.Document](),
	}
	for name, value := range want {
		method, ok := runner.MethodByName(name)
		if !ok {
			t.Errorf("Runner has no %s", name)
			continue
		}
		if method.Type.NumOut() != 2 || method.Type.Out(0) != value {
			t.Errorf("Runner.%s returns %v, want %v first", name, method.Type.Out(0), value)
		}
	}
}

// CreateSession is the one way to make a Session: the Agent, an ID that must be
// free, the caller's metadata and a starting history. It answers the listing
// columns, and the stored Record is read with Record, which is a copy.
func TestCreateSessionIsOneRequestAndAnswersAValue(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	info, err := runner.CreateSession(ctx, host.CreateSessionRequest{
		ID:       "chosen",
		Metadata: map[string]string{"tenant": "a"},
		Messages: []gotato.Message{gotato.UserMessage("earlier")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "chosen" || info.Messages != 1 || info.Metadata["tenant"] != "a" || info.Metadata[metaAgentKey] != "echo" {
		t.Fatalf("CreateSession answered %+v", info)
	}
	// An ID that is taken is refused, with its own code, and changes nothing.
	if _, err := runner.CreateSession(ctx, host.CreateSessionRequest{ID: "chosen", Metadata: map[string]string{"tenant": "b"}}); !errors.Is(err, host.ErrSessionExists) {
		t.Fatalf("a taken ID = %v, want ErrSessionExists", err)
	}
	if got := storedMetadata(t, store, "chosen")["tenant"]; got != "a" {
		t.Fatalf("a refused create changed the Session: tenant = %q", got)
	}
	// A blank ID is refused, not generated.
	if _, err := runner.CreateSession(ctx, host.CreateSessionRequest{ID: "  "}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("a blank ID = %v, want invalid_argument", err)
	}
	// No ID generates one.
	generated, err := runner.CreateSession(ctx, host.CreateSessionRequest{})
	if err != nil || generated.ID == "" || generated.ID == "chosen" {
		t.Fatalf("CreateSession with no ID = %+v, %v", generated, err)
	}
	// An unknown Agent is refused before anything is written.
	if _, err := runner.CreateSession(ctx, host.CreateSessionRequest{Agent: "nope"}); !errors.Is(err, host.ErrUnknownAgent) {
		t.Fatalf("an unknown Agent = %v", err)
	}

	// Record is a copy: changing it changes nothing that is stored.
	doc, err := runner.Record(ctx, "chosen")
	if err != nil {
		t.Fatal(err)
	}
	doc.Metadata["tenant"] = "changed"
	doc.Messages[0] = gotato.UserMessage("changed")
	again, err := runner.Record(ctx, "chosen")
	if err != nil || again.Metadata["tenant"] != "a" || gotato.TextOf(again.Messages[0]) != "earlier" {
		t.Fatalf("Record is not a copy: %+v, %v", again.Metadata, err)
	}
	if _, err := runner.Record(ctx, "missing"); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("Record of a missing Session = %v, want not_found", err)
	}
}

// Fork answers the child's listing columns; it keeps its parent's cache key.
func TestForkAnswersAValue(t *testing.T) {
	runner, store := newRunner(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}})
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, host.CreateSessionRequest{ID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, parent.ID, "child")
	if err != nil || child.ID != "child" || child.ParentID != "parent" {
		t.Fatalf("Fork = %+v, %v", child, err)
	}
	if got := storedMetadata(t, store, "child")[metaCacheKeyKey]; got != host.CacheKey("parent") {
		t.Fatalf("the child's cache key = %q, want the parent's", got)
	}
	if _, err := runner.Fork(ctx, parent.ID, "child"); !errors.Is(err, host.ErrSessionExists) {
		t.Fatalf("a Fork onto a taken ID = %v, want ErrSessionExists", err)
	}
}
