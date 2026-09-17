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
	reg := toolregistry.New()
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

func TestRegistryDrivesAgentToolSurface(t *testing.T) {
	reg := toolregistry.New(testkit.DemoEchoTool())
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
