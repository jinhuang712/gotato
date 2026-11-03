package host_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// A Session CreateSession opens for a TaskSpec and one a Run opens are the same
// Session: the same snapshot and Agent in the Record, the same Messages, and the
// Background once after the instruction, whether the TaskSpec is named or carried.
func TestCreateSessionForATaskOpensTheSessionARunOfTheTaskOpens(t *testing.T) {
	ctx := context.Background()
	spec := profile.TaskSpec{Name: "morning", Profile: "writer", Background: "Report on yesterday."}
	type opened struct {
		meta         map[string]string
		messages     []string
		instructions []string
	}
	open := func(start func(*host.Runner) (host.RunRequest, error)) opened {
		model := testkit.NewFakeModel(testkit.Text("done"))
		reg := taskRegistry(t, model)
		if err := reg.RegisterTaskSpecInline(spec); err != nil {
			t.Fatal(err)
		}
		runner := taskRunner(t, memStore(), "a", reg)
		request, err := start(runner)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runner.Run(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := runner.Record(ctx, out.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		got := opened{meta: map[string]string{}}
		for key, value := range rec.Metadata {
			if !strings.HasSuffix(key, "cache_key") { // the cache key is per Session
				got.meta[key] = value
			}
		}
		for _, m := range rec.Messages {
			got.messages = append(got.messages, string(m.Role)+": "+gotato.TextOf(m))
		}
		for _, r := range model.Requests() {
			got.instructions = append(got.instructions, r.SystemInstructions)
		}
		return got
	}
	want := open(func(*host.Runner) (host.RunRequest, error) {
		return host.RunRequest{Task: "morning", Prompt: "go"}, nil
	})
	if want.meta[host.ReservedPrefix+"task_spec"] == "" || !slices.Equal(want.instructions, []string{"You write.\n\nReport on yesterday."}) {
		t.Fatalf("a Run of the Task wrote %v and read %q", want.meta, want.instructions)
	}
	for name, create := range map[string]host.CreateSessionRequest{
		"by name":      {Task: "morning"},
		"with a value": {TaskSpec: &spec},
	} {
		got := open(func(runner *host.Runner) (host.RunRequest, error) {
			info, err := runner.CreateSession(ctx, create)
			return host.RunRequest{SessionID: info.ID, Prompt: "go"}, err
		})
		if !maps.Equal(got.meta, want.meta) {
			t.Errorf("%s: metadata %v, a Run writes %v", name, got.meta, want.meta)
		}
		if !slices.Equal(got.messages, want.messages) {
			t.Errorf("%s: messages %q, a Run writes %q", name, got.messages, want.messages)
		}
		if !slices.Equal(got.instructions, want.instructions) {
			t.Errorf("%s: instructions %q, a Run reads %q", name, got.instructions, want.instructions)
		}
	}
}

// CreateSession refuses what a Run of a TaskSpec refuses and writes nothing; a
// caller-chosen ID that is retried is already_exists, with no second Session.
func TestCreateSessionForATaskRefusesWhatDoesNotFitAndItsIDIsTheRetryPoint(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(nil)
	if err := reg.RegisterAgentProfileInline(profile.AgentProfile{Name: "writer", Model: testkit.NewFakeModel(testkit.Text("x")), MaxEffect: gotato.ToolEffectRead}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []profile.TaskSpec{
		{Name: "job", Profile: "writer", Background: "B"},
		{Name: "wide", Profile: "writer", MaxEffect: gotato.ToolEffectDestructive},
	} {
		if err := reg.RegisterTaskSpecInline(spec); err != nil {
			t.Fatal(err)
		}
	}
	runner := taskRunner(t, memStore(), "a", reg)
	for name, request := range map[string]host.CreateSessionRequest{
		"both":    {Task: "job", TaskSpec: &profile.TaskSpec{Name: "job", Profile: "writer"}},
		"agent":   {Task: "job", Agent: "writer"},
		"unknown": {Task: "nobody"},
		"widens":  {Task: "wide"},
		"carried": {TaskSpec: &profile.TaskSpec{Name: "w", Profile: "writer", MaxEffect: gotato.ToolEffectDestructive}},
	} {
		_, err := runner.CreateSession(ctx, request)
		var typed *gotato.RuntimeError
		if !errors.As(err, &typed) || !typed.NotRun || typed.Code != gotato.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
		if name == "unknown" && !errors.Is(err, host.ErrUnknownTask) {
			t.Errorf("unknown task = %v, want ErrUnknownTask", err)
		}
	}
	if page, err := runner.Sessions(ctx, store.ListQuery{}); err != nil || len(page.Infos) != 0 {
		t.Fatalf("a refused create left Sessions: %+v, %v", page, err)
	}
	if _, err := runner.CreateSession(ctx, host.CreateSessionRequest{Task: "job", ID: "job-2026-11-03"}); err != nil {
		t.Fatal(err)
	}
	_, err := runner.CreateSession(ctx, host.CreateSessionRequest{Task: "job", ID: "job-2026-11-03"})
	if !errors.Is(err, host.ErrSessionExists) || !gotato.IsCode(err, host.CodeAlreadyExists) {
		t.Fatalf("a retried create = %v, want already_exists", err)
	}
	if page, err := runner.Sessions(ctx, store.ListQuery{}); err != nil || len(page.Infos) != 1 {
		t.Fatalf("a retried create left %+v, %v", page, err)
	}
}

// Tasks lists the registered TaskSpecs by name with the AgentProfile each runs,
// and lists a stored one whose row cannot be read without its profile.
func TestTasksListsTheRegisteredTaskSpecsWithTheirAgents(t *testing.T) {
	ctx := context.Background()
	reg := taskRegistry(t, testkit.NewFakeModel(testkit.Text("x")))
	defs := store.NewMemoryDefinitions()
	if err := defs.SaveDefinition(ctx, profile.KindTaskSpec, "stored", []byte(`{"profile":"writer"}`)); err != nil {
		t.Fatal(err)
	}
	for _, register := range []error{
		reg.RegisterTaskSpecInline(profile.TaskSpec{Name: "morning", Profile: "writer"}),
		reg.RegisterTaskSpecWithStore("stored", defs),
		reg.RegisterTaskSpecWithStore("missing", defs),
	} {
		if register != nil {
			t.Fatal(register)
		}
	}
	runner := taskRunner(t, memStore(), "a", reg)
	want := []host.TaskInfo{{Name: "missing"}, {Name: "morning", Agent: "writer"}, {Name: "stored", Agent: "writer"}}
	if got := runner.Tasks(ctx); !slices.Equal(got, want) {
		t.Fatalf("Tasks = %+v, want %+v", got, want)
	}
}
