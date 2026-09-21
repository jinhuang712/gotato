package toolregistry_test

import (
	"context"
	"errors"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
	"github.com/jinhuang712/gotato/toolregistry"
)

func TestRegistryLifecycle(t *testing.T) {
	reg, err := toolregistry.New()
	if err != nil {
		t.Fatal(err)
	}
	var changes []toolregistry.Change
	reg.OnChange(func(change toolregistry.Change) { changes = append(changes, change) })

	a := testkit.NewFakeTool("a", "A")
	b := testkit.NewFakeTool("b", "B")
	if err := reg.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(a); !errors.Is(err, toolregistry.ErrDuplicate) {
		t.Fatalf("duplicate err = %v", err)
	}
	list := reg.List()
	if len(list) != 2 || list[0].Spec.ID != "a" || !list[0].Active {
		t.Fatalf("list = %+v", list)
	}
	if err := reg.Deactivate("b"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Deactivate("b"); err != nil {
		t.Fatal(err) // idempotent
	}
	if active := reg.Active(); len(active) != 1 || active[0].ID != "a" {
		t.Fatalf("active = %+v", active)
	}
	entry, ok := reg.Describe("b")
	if !ok || entry.Active {
		t.Fatalf("describe b = %+v %v", entry, ok)
	}
	if _, ok := reg.Lookup("b"); !ok {
		t.Fatal("deactivated tool must still be found by Lookup")
	}
	if err := reg.Activate("b"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Unregister("a"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Unregister("a"); !errors.Is(err, toolregistry.ErrNotFound) {
		t.Fatalf("unregister missing err = %v", err)
	}
	if err := reg.Activate("zzz"); !errors.Is(err, toolregistry.ErrNotFound) {
		t.Fatalf("activate missing err = %v", err)
	}
	want := []toolregistry.ChangeKind{toolregistry.Registered, toolregistry.Registered, toolregistry.Deactivated, toolregistry.Activated, toolregistry.Unregistered}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v", changes)
	}
	for i, kind := range want {
		if changes[i].Kind != kind {
			t.Fatalf("change %d = %+v, want %s", i, changes[i], kind)
		}
	}
}

func TestRegistryNormalizesIDsAndIsolatesSpecs(t *testing.T) {
	var zero toolregistry.Registry
	if err := zero.Register(testkit.NewFakeTool(" spaced ", "S")); err != nil {
		t.Fatalf("zero-value Register = %v", err)
	}
	if _, ok := zero.Describe("spaced"); !ok {
		t.Fatal("trimmed ID is not addressable")
	}

	reg := toolregistry.MustNew()
	tool := testkit.NewFakeTool("tool", "T").WithSchema(`{"type":"object"}`)
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	list := reg.List()
	list[0].Spec.InputSchema[0] = 'X'
	list[0].Spec.Metadata = map[string]string{"mutated": "yes"}
	if got := reg.List()[0].Spec.InputSchema[0]; got != '{' {
		t.Fatalf("mutating a returned Spec reached the registry: %q", got)
	}
	if _, mutated := reg.List()[0].Spec.Metadata["mutated"]; mutated {
		t.Fatal("mutating a returned Spec Metadata reached the registry")
	}
	if _, dup := reg.Describe("tool"); !dup {
		t.Fatal("Describe lost the entry")
	}
	if err := reg.Register(testkit.NewFakeTool("tool", "T")); !errors.Is(err, toolregistry.ErrDuplicate) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestNewReportsRegistrationFailure(t *testing.T) {
	if _, err := toolregistry.New(nil); err == nil {
		t.Fatal("New dropped a nil Tool instead of failing")
	}
	if _, err := toolregistry.New(testkit.NewFakeTool("x", "X"), testkit.NewFakeTool("x", "X")); !errors.Is(err, toolregistry.ErrDuplicate) {
		t.Fatalf("duplicate New err = %v", err)
	}
}

func TestRegistryDrivesAgentToolSurface(t *testing.T) {
	reg, err := toolregistry.New(testkit.DemoEchoTool())
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"hi"}`)}),
		testkit.Text("done"),
	)
	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithToolSource(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if err := reg.Deactivate(testkit.DemoToolID); err != nil {
		t.Fatal(err)
	}
	model2 := testkit.NewFakeModel(testkit.Text("no tools"))
	agent2, err := gotato.NewAgent(gotato.WithModel(model2), gotato.WithToolSource(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer agent2.Close(context.Background())
	if _, err := agent2.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	request, _ := model2.LastRequest()
	if len(request.Tools) != 0 {
		t.Fatalf("deactivated tool still visible: %+v", request.Tools)
	}
}
