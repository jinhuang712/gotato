package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// taskTool is a Tool with a stated Effect.
type taskTool struct {
	id     string
	effect gotato.ToolEffect
}

func (t taskTool) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: t.id, Name: t.id, Description: t.id, InputSchema: []byte(`{"type":"object"}`), Effect: t.effect}
}

func (t taskTool) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "ok"}}}, nil
}

// taskRunner is a Runner over store whose registry holds profiles, the first of
// them the default.
func taskRunner(t *testing.T, store session.Store, instance string, profiles *profile.Registry, mutate ...func(*service.Config)) *service.Runner {
	t.Helper()
	cfg := service.Config{Store: store, Instance: instance, Heartbeat: steadyLiveness, Profiles: profiles, DefaultAgent: "writer"}
	for _, m := range mutate {
		m(&cfg)
	}
	runner, err := service.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

func taskRegistry(t *testing.T, model gotato.Model, tools ...gotato.Tool) *profile.Registry {
	t.Helper()
	reg := profile.NewRegistry(nil)
	if err := reg.RegisterAgentProfileInline(profile.AgentProfile{Name: "writer", Model: model, ModelName: "m", Instruction: "You write.", Tools: tools}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func requestTools(r gotato.ModelRequest) []string {
	var ids []string
	for _, spec := range r.Tools {
		ids = append(ids, spec.ID)
	}
	return ids
}

func TestATaskRunOpensANewSessionWithTheBackgroundOnce(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("done"))
	reg := taskRegistry(t, model)
	if err := reg.RegisterTaskSpecInline(profile.TaskSpec{Name: "morning", Profile: "writer", Background: "Report on yesterday."}); err != nil {
		t.Fatal(err)
	}
	runner := taskRunner(t, memStore(), "a", reg)

	first, err := runner.Run(ctx, service.RunRequest{Task: "morning", Prompt: "go"})
	if err != nil || first.SessionID == "" || first.Agent != "writer" {
		t.Fatalf("%+v, %v", first, err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	for i, request := range model.Requests() {
		if request.SystemInstructions != "You write.\n\nReport on yesterday." {
			t.Fatalf("request %d: instruction %q, want the profile's with the Background once after it", i, request.SystemInstructions)
		}
	}
	// A second Run of the Task opens another Session.
	second, err := runner.Run(ctx, service.RunRequest{Task: "morning", Prompt: "go"})
	if err != nil || second.SessionID == first.SessionID {
		t.Fatalf("%+v, %v", second, err)
	}
	rec, err := runner.Record(ctx, first.SessionID)
	if err != nil || rec.Metadata[metaAgentKey] != "writer" || !strings.Contains(rec.Metadata[service.ReservedPrefix+"task_spec"], `"name":"morning"`) {
		t.Fatalf("metadata %v, %v", rec.Metadata, err)
	}
}

func TestTheTaskSnapshotSurvivesASpecChangeAndARebuildOnAnotherInstance(t *testing.T) {
	ctx := context.Background()
	store := memStore()
	modelA, modelB := testkit.NewFakeModel(testkit.Text("a")), testkit.NewFakeModel(testkit.Text("b"))
	regA, regB := taskRegistry(t, modelA), taskRegistry(t, modelB)
	task := func(background string) profile.TaskSpec {
		return profile.TaskSpec{Name: "daily", Profile: "writer", Background: background, Tools: []string{}}
	}
	for _, reg := range []*profile.Registry{regA, regB} {
		if err := reg.RegisterTaskSpecInline(task("v1")); err != nil {
			t.Fatal(err)
		}
	}
	a := taskRunner(t, store, "a", regA)
	b := taskRunner(t, store, "b", regB)

	opened, err := a.Run(ctx, service.RunRequest{Task: "daily", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	// The registered spec changes, on both instances.
	if err := regA.RegisterTaskSpecInline(task("v2")); err != nil {
		t.Fatal(err)
	}
	if err := regB.RegisterTaskSpecInline(task("v3")); err != nil {
		t.Fatal(err)
	}
	// Another instance takes the Session over and builds it again.
	if _, err := b.Run(ctx, service.RunRequest{SessionID: opened.SessionID, Prompt: "next"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: opened.SessionID, Prompt: "and again"}); err != nil {
		t.Fatal(err)
	}
	for name, model := range map[string]*testkit.FakeModel{"a": modelA, "b": modelB} {
		for _, request := range model.Requests() {
			if request.SystemInstructions != "You write.\n\nv1" {
				t.Fatalf("instance %s read %q, want the snapshot's Background", name, request.SystemInstructions)
			}
		}
	}
	if modelB.Calls() != 1 {
		t.Fatalf("instance b ran %d times", modelB.Calls())
	}
	// A new Run of the Task reads the spec as it is now.
	if _, err := a.Run(ctx, service.RunRequest{Task: "daily", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	last, _ := modelA.LastRequest()
	if last.SystemInstructions != "You write.\n\nv2" {
		t.Fatalf("a new Run read %q", last.SystemInstructions)
	}
}

func TestATaskSpecNarrowsTheSession(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("done"))
	tools := []gotato.Tool{taskTool{"read_a", gotato.ToolEffectRead}, taskTool{"read_b", gotato.ToolEffectRead}, taskTool{"change", gotato.ToolEffectWriteLocal}}
	reg := taskRegistry(t, model, tools...)
	runner := taskRunner(t, memStore(), "a", reg)

	for name, c := range map[string]struct {
		spec profile.TaskSpec
		want []string
	}{
		"all":    {profile.TaskSpec{}, []string{"change", "read_a", "read_b"}},
		"subset": {profile.TaskSpec{Tools: []string{"read_b", "change"}}, []string{"change", "read_b"}},
		"cap":    {profile.TaskSpec{MaxEffect: gotato.ToolEffectRead}, []string{"read_a", "read_b"}},
		"none":   {profile.TaskSpec{Tools: []string{}}, nil},
	} {
		spec := c.spec
		spec.Name, spec.Profile = "job", "writer"
		if _, err := runner.Run(ctx, service.RunRequest{TaskSpec: &spec, Prompt: "go"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		last, _ := model.LastRequest()
		// read_result comes with any Tool, and is the Runner's.
		got := slices.DeleteFunc(requestTools(last), func(id string) bool { return id == "read_result" })
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: tools %v, want %v", name, got, c.want)
		}
	}
}

func TestARunRefusesAWideningTaskSpecAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("done"))
	reg := profile.NewRegistry(nil)
	if err := reg.RegisterAgentProfileInline(profile.AgentProfile{
		Name: "writer", Model: model, Tools: []gotato.Tool{taskTool{"read_a", gotato.ToolEffectRead}},
		MaxEffect: gotato.ToolEffectWriteLocal, RunTimeout: time.Minute, Sampling: gotato.Sampling{MaxTokens: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTaskSpecInline(profile.TaskSpec{Name: "wide", Profile: "writer", MaxEffect: gotato.ToolEffectDestructive}); err != nil {
		t.Fatal(err)
	}
	store := memStore()
	runner := taskRunner(t, store, "a", reg)
	for name, request := range map[string]service.RunRequest{
		"tools":       {TaskSpec: &profile.TaskSpec{Name: "w", Profile: "writer", Tools: []string{"other"}}},
		"max_effect":  {Task: "wide"},
		"run_timeout": {TaskSpec: &profile.TaskSpec{Name: "w", Profile: "writer", RunTimeout: time.Hour}},
		"max_tokens":  {TaskSpec: &profile.TaskSpec{Name: "w", Profile: "writer", Sampling: gotato.Sampling{MaxTokens: 101}}},
	} {
		request.Prompt = "go"
		_, err := runner.Run(ctx, request)
		var typed *gotato.RuntimeError
		if name == "tools" {
			// "other" is in no Catalog: refused all the same.
			if !errors.Is(err, profile.ErrUnknownName) && !errors.Is(err, profile.ErrWidens) {
				t.Errorf("%s: %v", name, err)
			}
		} else if !errors.Is(err, profile.ErrWidens) {
			t.Errorf("%s: %v", name, err)
		}
		if !errors.As(err, &typed) || !typed.NotRun || typed.Code != gotato.CodeInvalidArgument {
			t.Errorf("%s: %v is not a refused request", name, err)
		}
	}
	if model.Calls() != 0 {
		t.Fatalf("a refused Run called the model %d times", model.Calls())
	}
	if page, err := runner.Sessions(ctx, session.ListQuery{}); err != nil || len(page.Infos) != 0 {
		t.Fatalf("a refused Run left Sessions: %+v, %v", page, err)
	}
}

func TestATaskRunRefusesWhatDoesNotFit(t *testing.T) {
	ctx := context.Background()
	reg := taskRegistry(t, testkit.NewFakeModel(testkit.Text("done")))
	if err := reg.RegisterTaskSpecInline(profile.TaskSpec{Name: "job", Profile: "writer"}); err != nil {
		t.Fatal(err)
	}
	runner := taskRunner(t, memStore(), "a", reg)
	existing, err := runner.CreateSession(ctx, service.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	spec := &profile.TaskSpec{Name: "job", Profile: "writer"}
	for name, request := range map[string]service.RunRequest{
		"both":       {Task: "job", TaskSpec: spec},
		"a session":  {Task: "job", SessionID: existing.ID},
		"an agent":   {TaskSpec: spec, Agent: "writer"},
		"unknown":    {Task: "nobody"},
		"no profile": {TaskSpec: &profile.TaskSpec{Name: "x", Profile: "nobody"}},
	} {
		request.Prompt = "go"
		_, err := runner.Run(ctx, request)
		var typed *gotato.RuntimeError
		if !errors.As(err, &typed) || !typed.NotRun || typed.Code != gotato.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A Session opened for a TaskSpec keeps its AgentProfile.
	opened, err := runner.Run(ctx, service.RunRequest{Task: "job", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: opened.SessionID, Agent: "writer", Prompt: "again"}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("an Agent on a task Session: %v", err)
	}
}

// A profile replaced between Runs reaches the next Session built, and never a
// Run in flight: the Run keeps the profile it was built from through every Turn.
func TestAReplacedProfileReachesTheNextRunAndNotOneInFlight(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "probe", Arguments: []byte("{}")}),
		testkit.Text("done"),
	)
	reg := profile.NewRegistry(nil)
	profileWith := func(instruction string, tools ...gotato.Tool) profile.AgentProfile {
		return profile.AgentProfile{Name: "writer", Model: model, Instruction: instruction, Tools: tools}
	}
	probe := testkit.NewFakeTool("probe", "ok")
	probe.Handler = func(context.Context, gotato.ToolUse) (gotato.ToolResult, error) {
		// The service replaces the profile while the Run is between two Turns.
		if err := reg.RegisterAgentProfileInline(profileWith("two")); err != nil {
			return gotato.ToolResult{}, err
		}
		return gotato.ToolResult{Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "ok"}}}, nil
	}
	if err := reg.RegisterAgentProfileInline(profileWith("one", probe)); err != nil {
		t.Fatal(err)
	}
	runner := taskRunner(t, memStore(), "a", reg, func(c *service.Config) { c.Warm = service.WarmPolicy{TTL: -1} })

	first, err := runner.Run(ctx, service.RunRequest{Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 2 || requests[0].SystemInstructions != "one" || requests[1].SystemInstructions != "one" {
		t.Fatalf("the Run in flight read %d requests, first %q", len(requests), requests[0].SystemInstructions)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "next"}); err != nil {
		t.Fatal(err)
	}
	last, _ := model.LastRequest()
	if last.SystemInstructions != "two" || len(last.Tools) != 0 {
		t.Fatalf("the next Run read %q with tools %v", last.SystemInstructions, requestTools(last))
	}
}

func TestAStoredProfileIsReadWhenASessionIsBuilt(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("done"))
	catalog, err := profile.NewCatalog(map[string]gotato.Model{"m": model}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg := profile.NewRegistry(catalog)
	defs := session.NewMemoryDefinitions()
	save := func(doc string) {
		t.Helper()
		if err := defs.SaveDefinition(ctx, profile.KindAgentProfile, "writer", []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	save(`{"model":"m","instruction":"from the store"}`)
	if err := reg.RegisterAgentProfileWithStore("writer", defs); err != nil {
		t.Fatal(err)
	}
	runner := taskRunner(t, memStore(), "a", reg)
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	save(`{"model":"m","instruction":"edited"}`)
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if last, _ := model.LastRequest(); last.SystemInstructions != "edited" {
		t.Fatalf("read %q", last.SystemInstructions)
	}
	// A row the Catalog cannot satisfy refuses the Run before it starts.
	save(`{"model":"nowhere"}`)
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "once more"}); !errors.Is(err, profile.ErrUnknownName) {
		t.Fatalf("%v", err)
	}
}

// readBackProbe is a ReadBack that offers one Tool of its own.
type readBackProbe struct{}

func (readBackProbe) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{taskTool{"probe_read_back", gotato.ToolEffectRead}}, nil
}

func (readBackProbe) PageBytes() int { return 4000 }

func TestAnAgentProfileReplacesTheReadBack(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("done"))
	reg := profile.NewRegistry(nil)
	for name, readBack := range map[string]gotato.ReadBack{"custom": readBackProbe{}, "default": nil} {
		if err := reg.RegisterAgentProfileInline(profile.AgentProfile{
			Name: name, Model: model, Tools: []gotato.Tool{taskTool{"work", gotato.ToolEffectRead}}, ReadBack: readBack,
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := taskRunner(t, memStore(), "a", reg)
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "custom", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	last, _ := model.LastRequest()
	if got := strings.Join(requestTools(last), ","); got != "probe_read_back,work" {
		t.Fatalf("the Session offers %s; want its own ReadBack's Tool and not read_result", got)
	}
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "default", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	last, _ = model.LastRequest()
	if got := strings.Join(requestTools(last), ","); !strings.Contains(got, "read_result") || strings.Contains(got, "probe_read_back") {
		t.Fatalf("the default ReadBack offers %s", got)
	}
}

// recordingAdmitter keeps every Admission it is asked about.
type recordingAdmitter struct {
	mu   sync.Mutex
	seen []service.Admission
}

func (a *recordingAdmitter) Admit(_ context.Context, admission service.Admission, _ service.Load) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, admission)
	return nil, nil
}

func TestAdmissionCarriesTheProfileAndTheTaskName(t *testing.T) {
	ctx := context.Background()
	reg := taskRegistry(t, testkit.NewFakeModel(testkit.Text("done")))
	if err := reg.RegisterTaskSpecInline(profile.TaskSpec{Name: "job", Profile: "writer"}); err != nil {
		t.Fatal(err)
	}
	admitter := &recordingAdmitter{}
	runner := taskRunner(t, memStore(), "a", reg, func(c *service.Config) { c.Admitter = admitter })
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "writer", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{Task: "job", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{TaskSpec: &profile.TaskSpec{Name: "ad hoc", Profile: "writer"}, Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	want := []service.Admission{{Agent: "writer"}, {Agent: "writer", Task: "job"}, {Agent: "writer", Task: "ad hoc"}}
	if len(admitter.seen) != len(want) {
		t.Fatalf("admitted %+v", admitter.seen)
	}
	for i := range want {
		if admitter.seen[i] != want[i] {
			t.Errorf("admission %d = %+v, want %+v", i, admitter.seen[i], want[i])
		}
	}
}

func TestRunnerRequiresProfilesAndAnUnknownDefaultIsRefused(t *testing.T) {
	if _, err := service.New(service.Config{Store: memStore()}); err == nil {
		t.Fatal("a Runner without Profiles was built")
	}
	reg := taskRegistry(t, testkit.NewFakeModel(testkit.Text("done")))
	runner := taskRunner(t, memStore(), "a", reg, func(c *service.Config) { c.DefaultAgent = "" })
	if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go"}); !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("no Agent and no default: %v", err)
	}
	if names := runner.AgentProfiles(); len(names) != 1 || names[0] != "writer" {
		t.Fatalf("%v", names)
	}
}
