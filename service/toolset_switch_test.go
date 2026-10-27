package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// twoToolSet is a ToolSet of a read tool and a write tool.
type twoToolSet struct{}

func (twoToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "files"} }

func (twoToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{
		gotato.ToolWithEffect(testkit.NewFakeTool("read", "contents"), gotato.EffectRead),
		gotato.ToolWithEffect(testkit.NewFakeTool("write", "done"), gotato.EffectWriteLocal),
	}, nil
}

// downToolSet never resolves.
type downToolSet struct{}

func (downToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "crm"} }

func (downToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return nil, errors.New("crm is down")
}

func entryOf(entries []service.ToolEntry, id string) (service.ToolEntry, bool) {
	i := slices.IndexFunc(entries, func(e service.ToolEntry) bool { return e.Spec.ID == id })
	if i < 0 {
		return service.ToolEntry{}, false
	}
	return entries[i], true
}

// A Session's tools can be listed and switched by ID for the tools of a
// ToolSet too: the listing says which are loaded and which are switched off,
// the switch is the metadata fact every instance reads, and a ToolSet that is
// down is shown as unavailable.
func TestAToolOfAToolSetIsListedAndSwitchedByID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	arguments := `{"names":["files.write"]}`
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "load-1", ToolID: gotato.SearchToolName, Arguments: []byte(arguments)}),
		testkit.Text("loaded"))
	agent := service.Agent{Name: "a", Model: model, Options: []gotato.Option{gotato.WithToolSet(twoToolSet{}), gotato.WithToolSet(downToolSet{})}}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()

	// Before anything: both tools are listed, unloaded and on; crm is unavailable.
	entries, err := runner.Tools(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"files.read", "files.write"} {
		entry, ok := entryOf(entries, want)
		if !ok || entry.ToolSet != "files" || entry.Loaded || !entry.Active {
			t.Fatalf("%s = %+v, %v; want a files tool, unloaded and active", want, entry, ok)
		}
	}
	if entry, _ := entryOf(entries, "files.write"); entry.Spec.Effect != gotato.EffectWriteLocal {
		t.Fatalf("files.write effect = %q", entry.Spec.Effect)
	}
	if crm, ok := entryOf(entries, "crm"); !ok || !crm.Unavailable {
		t.Fatalf("crm = %+v, %v; want unavailable", crm, ok)
	}
	if _, ok := entryOf(entries, gotato.SearchToolName); !ok {
		t.Fatal("search_tools is not listed")
	}

	// Switch a ToolSet tool off by its ID.
	entry, err := runner.SetToolActive(ctx, id, "files.read", false)
	if err != nil || entry.Active || entry.ToolSet != "files" {
		t.Fatalf("SetToolActive off = %+v, %v", entry, err)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := record.Get(service.MetaToolPrefix + "files.read"); v != "inactive" {
		t.Fatalf("metadata = %q, want inactive", v)
	}
	// Unknown names and names in a ToolSet that is down are refused.
	for _, bad := range []string{"files.nope", "crm.lookup"} {
		if _, err := runner.SetToolActive(ctx, id, bad, false); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Fatalf("SetToolActive(%s) = %v, want invalid argument", bad, err)
		}
	}
	if _, err := runner.SetToolActive(ctx, id, "crm.lookup", false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("crm.lookup error = %v, want it to say unavailable", err)
	}

	// A Run loads files.write; the catalog the model read leaves files.read out.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	for _, request := range model.Requests() {
		for _, spec := range request.Tools {
			if spec.ID == gotato.SearchToolName && strings.Contains(spec.Description, "files.read") {
				t.Fatalf("the catalog lists the switched-off files.read: %s", spec.Description)
			}
		}
	}

	// Another Runner on the same store reads the same facts.
	other, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = other.Tools(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := entryOf(entries, "files.read")
	write, _ := entryOf(entries, "files.write")
	if read.Active || read.Loaded || !write.Active || !write.Loaded {
		t.Fatalf("after the Run: read = %+v, write = %+v", read, write)
	}

	// A switched-off tool can be named again to switch it on.
	entry, err = other.SetToolActive(ctx, id, "files.read", true)
	if err != nil || !entry.Active {
		t.Fatalf("SetToolActive on = %+v, %v", entry, err)
	}
	entries, _ = other.Tools(ctx, "", id)
	if read, _ := entryOf(entries, "files.read"); !read.Active {
		t.Fatalf("files.read = %+v, want active", read)
	}
}
