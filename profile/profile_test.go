package profile_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// namedSet is a ToolSet with no Tools, enough to be named.
type namedSet string

func (n namedSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: string(n)} }

func (namedSet) Tools(context.Context) ([]gotato.Tool, error) { return nil, nil }

var (
	modelA = testkit.NewFakeModel()
	modelB = testkit.NewFakeModel()
)

func catalog(t *testing.T) *profile.Catalog {
	t.Helper()
	c, err := profile.NewCatalog(
		map[string]gotato.Model{"a": modelA, "b": modelB},
		[]gotato.Tool{testkit.NewFakeTool("search", "s"), testkit.NewFakeTool("write", "w"), testkit.NewFakeTool("spare", "x")},
		[]gotato.ToolSet{namedSet("docs"), namedSet("mail")},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func inline() profile.AgentProfile {
	return profile.AgentProfile{
		Name: "writer", Model: modelA, ModelName: "a", Instruction: "write",
		Tools:     []gotato.Tool{testkit.NewFakeTool("search", "s"), testkit.NewFakeTool("write", "w")},
		ToolSets:  []profile.ToolSet{{Set: namedSet("docs")}, {Set: namedSet("mail"), Mode: profile.ToolSetFolded}},
		MaxEffect: gotato.ToolEffectWriteLocal, RunTimeout: 10 * time.Minute,
		Sampling: gotato.Sampling{MaxTokens: 1000},
	}
}

func store(t *testing.T, kind, name, doc string) *session.MemoryDefinitions {
	t.Helper()
	defs := session.NewMemoryDefinitions()
	if err := defs.SaveDefinition(context.Background(), kind, name, []byte(doc)); err != nil {
		t.Fatal(err)
	}
	return defs
}

func TestInlineAndStoredRegistration(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(catalog(t))
	if err := reg.RegisterAgentProfileInline(inline()); err != nil {
		t.Fatal(err)
	}
	defs := store(t, profile.KindAgentProfile, "stored", `{"model":"b","instruction":"from a row","tools":["search"],"toolsets":[{"name":"mail","mode":"active"}],"max_effect":"read","run_timeout":"5m","sampling":{"max_tokens":50},"compact":{"ceiling_tokens":9000,"location":"UTC"}}`)
	if err := reg.RegisterAgentProfileWithStore("stored", defs); err != nil {
		t.Fatal(err)
	}

	got, err := reg.AgentProfile(ctx, "writer")
	if err != nil || got.Instruction != "write" || got.Model != modelA {
		t.Fatalf("inline: %+v, %v", got, err)
	}
	got, err = reg.AgentProfile(ctx, "stored")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "stored" || got.Model != modelB || got.ModelName != "b" || got.Instruction != "from a row" ||
		len(got.Tools) != 1 || got.Tools[0].Spec().ID != "search" || got.ToolSets[0].Mode != profile.ToolSetActive ||
		got.MaxEffect != gotato.ToolEffectRead || got.RunTimeout != 5*time.Minute || got.Sampling.MaxTokens != 50 || got.Compact.CeilingTokens != 9000 {
		t.Fatalf("stored: %+v", got)
	}
	if names := reg.AgentProfiles(); !slices.Equal(names, []string{"stored", "writer"}) {
		t.Fatalf("names: %v", names)
	}
	if _, err := reg.AgentProfile(ctx, "nobody"); !errors.Is(err, profile.ErrNotRegistered) {
		t.Fatalf("an unregistered name: %v", err)
	}
}

func TestStoredIsReadAtEveryLookupAndNeedsItsRow(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(catalog(t))
	defs := session.NewMemoryDefinitions()
	if err := reg.RegisterAgentProfileWithStore("late", defs); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.AgentProfile(ctx, "late"); !errors.Is(err, profile.ErrNotRegistered) {
		t.Fatalf("a registered name with no row: %v", err)
	}
	if err := defs.SaveDefinition(ctx, profile.KindAgentProfile, "late", []byte(`{"model":"a","instruction":"one"}`)); err != nil {
		t.Fatal(err)
	}
	if p, err := reg.AgentProfile(ctx, "late"); err != nil || p.Instruction != "one" {
		t.Fatalf("%+v, %v", p, err)
	}
	if err := defs.SaveDefinition(ctx, profile.KindAgentProfile, "late", []byte(`{"model":"a","instruction":"two"}`)); err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.AgentProfile(ctx, "late"); p.Instruction != "two" {
		t.Fatalf("a changed row is read at the next lookup: %+v", p)
	}
}

func TestALaterRegistrationReplaces(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(catalog(t))
	first := inline()
	if err := reg.RegisterAgentProfileInline(first); err != nil {
		t.Fatal(err)
	}
	second := inline()
	second.Instruction = "second"
	if err := reg.RegisterAgentProfileInline(second); err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.AgentProfile(ctx, "writer"); p.Instruction != "second" {
		t.Fatalf("inline over inline: %q", p.Instruction)
	}
	defs := store(t, profile.KindAgentProfile, "writer", `{"model":"b","instruction":"third"}`)
	if err := reg.RegisterAgentProfileWithStore("writer", defs); err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.AgentProfile(ctx, "writer"); p.Instruction != "third" {
		t.Fatalf("store over inline: %q", p.Instruction)
	}
	if err := reg.RegisterAgentProfileInline(first); err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.AgentProfile(ctx, "writer"); p.Instruction != "write" {
		t.Fatalf("inline over store: %q", p.Instruction)
	}
	if n := len(reg.AgentProfiles()); n != 1 {
		t.Fatalf("a name is registered once, got %d", n)
	}

	task := profile.TaskSpec{Name: "daily", Profile: "writer", Background: "one"}
	if err := reg.RegisterTaskSpecInline(task); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTaskSpecWithStore("daily", store(t, profile.KindTaskSpec, "daily", `{"profile":"writer","background":"two"}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := reg.TaskSpec(ctx, "daily"); err != nil || got.Background != "two" {
		t.Fatalf("%+v, %v", got, err)
	}
	if names := reg.TaskSpecs(); !slices.Equal(names, []string{"daily"}) {
		t.Fatalf("names: %v", names)
	}
}

func TestRegistrationsRefuseWhatCannotWork(t *testing.T) {
	reg := profile.NewRegistry(nil)
	for name, p := range map[string]profile.AgentProfile{
		"no name":       {Model: modelA},
		"no model":      {Name: "x"},
		"unknown cap":   {Name: "x", Model: modelA, MaxEffect: "root"},
		"nil toolset":   {Name: "x", Model: modelA, ToolSets: []profile.ToolSet{{}}},
		"unknown mode":  {Name: "x", Model: modelA, ToolSets: []profile.ToolSet{{Set: namedSet("s"), Mode: "wide"}}},
		"nil extension": {Name: "x", Model: modelA, Extensions: []any{nil}},
	} {
		if err := reg.RegisterAgentProfileInline(p); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, task := range map[string]profile.TaskSpec{
		"no name":          {Profile: "p"},
		"no profile":       {Name: "t"},
		"unknown cap":      {Name: "t", Profile: "p", MaxEffect: "root"},
		"negative timeout": {Name: "t", Profile: "p", RunTimeout: -1},
	} {
		if err := reg.RegisterTaskSpecInline(task); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := reg.RegisterAgentProfileWithStore("p", nil); err == nil {
		t.Error("a nil store was accepted")
	}
	if err := reg.RegisterTaskSpecWithStore(" ", session.NewMemoryDefinitions()); err == nil {
		t.Error("a blank name was accepted")
	}
}

func TestAStoredDefinitionNamesOnlyWhatTheCatalogOffers(t *testing.T) {
	ctx := context.Background()
	for name, doc := range map[string]string{
		"model":   `{"model":"gone"}`,
		"tool":    `{"model":"a","tools":["rm_rf"]}`,
		"toolset": `{"model":"a","toolsets":[{"name":"vault"}]}`,
	} {
		reg := profile.NewRegistry(catalog(t))
		if err := reg.RegisterAgentProfileWithStore("p", store(t, profile.KindAgentProfile, "p", doc)); err != nil {
			t.Fatal(err)
		}
		_, err := reg.AgentProfile(ctx, "p")
		var unknown *profile.UnknownNameError
		if !errors.Is(err, profile.ErrUnknownName) || !errors.As(err, &unknown) || unknown.Kind != name {
			t.Errorf("%s: %v", name, err)
		}
		if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: not invalid_argument: %v", name, err)
		}
	}
}

func TestAStoredDocumentMustBeWhatItSays(t *testing.T) {
	ctx := context.Background()
	for name, doc := range map[string]string{
		"unknown field": `{"model":"a","tools_and_more":["x"]}`,
		"other name":    `{"name":"other","model":"a"}`,
		"bad timeout":   `{"model":"a","run_timeout":"soon"}`,
		"bad cap":       `{"model":"a","max_effect":"root"}`,
		"bad mode":      `{"model":"a","toolsets":[{"name":"docs","mode":"wide"}]}`,
		"bad zone":      `{"model":"a","compact":{"location":"Mars/Base"}}`,
		"trailing":      `{"model":"a"} {}`,
		"not json":      `model: a`,
	} {
		reg := profile.NewRegistry(catalog(t))
		if err := reg.RegisterAgentProfileWithStore("p", store(t, profile.KindAgentProfile, "p", doc)); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.AgentProfile(ctx, "p"); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAgentProfileDocumentRoundTrips(t *testing.T) {
	p := inline()
	p.Limits = &gotato.CoreLimits{MaxMessages: 7}
	doc, err := profile.EncodeAgentProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := profile.DecodeAgentProfile("writer", doc, catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "writer" || got.ModelName != "a" || got.Instruction != "write" || len(got.Tools) != 2 || len(got.ToolSets) != 2 ||
		got.ToolSets[1].Mode != profile.ToolSetFolded || got.MaxEffect != p.MaxEffect || got.RunTimeout != p.RunTimeout ||
		got.Sampling != p.Sampling || got.Limits.MaxMessages != 7 {
		t.Fatalf("%+v", got)
	}
	for name, mutate := range map[string]func(*profile.AgentProfile){
		"tool sources": func(p *profile.AgentProfile) { p.ToolSources = []gotato.ToolSource{gotato.ToolSource(nil)} },
		"extension":    func(p *profile.AgentProfile) { p.Extensions = []any{1} },
		"read back":    func(p *profile.AgentProfile) { p.ReadBack = noReadBack{} },
		"context":      func(p *profile.AgentProfile) { p.ContextBuilder = gotato.FullHistoryContext() },
	} {
		bad := inline()
		mutate(&bad)
		if _, err := profile.EncodeAgentProfile(bad); !errors.Is(err, profile.ErrNotData) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type noReadBack struct{ gotato.ReadBack }

func TestTaskSpecDocumentRoundTrips(t *testing.T) {
	temp := 0.2
	spec := profile.TaskSpec{Name: "daily", Profile: "writer", Background: "Today: report.", Tools: []string{"search"}, ToolSets: []string{},
		MaxEffect: gotato.ToolEffectRead, RunTimeout: time.Minute, Sampling: gotato.Sampling{MaxTokens: 9, Temperature: &temp}}
	doc, err := profile.EncodeTaskSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := profile.DecodeTaskSpec("daily", doc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "daily" || got.Profile != "writer" || got.Background != spec.Background || !slices.Equal(got.Tools, spec.Tools) ||
		got.Tools == nil || got.ToolSets == nil || len(got.ToolSets) != 0 || got.MaxEffect != spec.MaxEffect || got.RunTimeout != time.Minute ||
		got.Sampling.MaxTokens != 9 || *got.Sampling.Temperature != 0.2 {
		t.Fatalf("%+v", got)
	}
	bare, err := profile.DecodeTaskSpec("t", []byte(`{"profile":"p"}`))
	if err != nil || bare.Tools != nil || bare.ToolSets != nil {
		t.Fatalf("absent tools keep all (nil): %+v, %v", bare, err)
	}
	if _, err := profile.DecodeTaskSpec("t", []byte(`{"profile":"p","facts":{}}`)); err == nil {
		t.Fatal("a TaskSpec cannot carry a model's Facts")
	}
}

func TestATaskSpecThatWidensIsRefused(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(catalog(t))
	if err := reg.RegisterAgentProfileInline(inline()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]profile.TaskSpec{
		"tools":       {Tools: []string{"spare"}},
		"toolsets":    {ToolSets: []string{"docs", "ghost"}},
		"max_effect":  {MaxEffect: gotato.ToolEffectWriteExternal},
		"run_timeout": {RunTimeout: 11 * time.Minute},
		"max_tokens":  {Sampling: gotato.Sampling{MaxTokens: 1001}},
	}
	// "toolsets" names a ToolSet the Catalog lacks, a different refusal.
	// "tools" names a Tool the Catalog offers and the profile does not.
	for bound, spec := range cases {
		spec.Name, spec.Profile = "job", "writer"
		_, err := reg.Narrowed(ctx, spec)
		var widen *profile.WidenError
		var unknown *profile.UnknownNameError
		switch {
		case bound == "toolsets" && errors.As(err, &unknown) && unknown.Kind == profile.KindToolSet:
		case errors.As(err, &widen) && widen.Bound == bound:
		default:
			t.Errorf("%s: %v", bound, err)
		}
	}
	ok := profile.TaskSpec{Name: "job", Profile: "writer", Tools: []string{"search"}, ToolSets: []string{"docs"},
		MaxEffect: gotato.ToolEffectRead, RunTimeout: 10 * time.Minute, Sampling: gotato.Sampling{MaxTokens: 1000}}
	if _, err := reg.Narrowed(ctx, ok); err != nil {
		t.Fatalf("a TaskSpec at the bounds: %v", err)
	}
	// A ToolSet the Catalog offers but the profile does not install widens.
	spec := profile.TaskSpec{Name: "job", Profile: "writer", ToolSets: []string{"docs"}}
	only := inline()
	only.ToolSets = only.ToolSets[1:]
	if err := reg.RegisterAgentProfileInline(only); err != nil {
		t.Fatal(err)
	}
	var widen *profile.WidenError
	if _, err := reg.Narrowed(ctx, spec); !errors.As(err, &widen) || widen.Bound != "toolsets" {
		t.Fatalf("%v", err)
	}
}

func TestNarrowedAppliesTheSpec(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(catalog(t))
	base := inline()
	base.ToolSources = []gotato.ToolSource{nil}
	if err := reg.RegisterAgentProfileInline(base); err != nil {
		t.Fatal(err)
	}
	temp := 0.1
	spec := profile.TaskSpec{Name: "job", Profile: "writer", Background: "Report for Monday.", Tools: []string{"search"}, ToolSets: []string{"mail"},
		MaxEffect: gotato.ToolEffectRead, RunTimeout: time.Minute, Sampling: gotato.Sampling{MaxTokens: 100, Temperature: &temp, ReasoningEffort: "low"}}
	got, err := reg.Narrowed(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Instruction != "write\n\nReport for Monday." || len(got.Tools) != 1 || got.Tools[0].Spec().ID != "search" || got.ToolSources != nil ||
		len(got.ToolSets) != 1 || got.ToolSets[0].Set.Spec().Name != "mail" || got.MaxEffect != gotato.ToolEffectRead || got.RunTimeout != time.Minute ||
		got.Sampling.MaxTokens != 100 || *got.Sampling.Temperature != 0.1 || got.Sampling.ReasoningEffort != "low" {
		t.Fatalf("%+v", got)
	}
	// The registered profile is as it was.
	if again, _ := reg.AgentProfile(ctx, "writer"); len(again.Tools) != 2 || again.Instruction != "write" || len(again.ToolSets) != 2 {
		t.Fatalf("the profile changed: %+v", again)
	}
	// A spec that names nothing keeps everything, and a Background alone is the instruction of a profile with none.
	keep, err := reg.Narrowed(ctx, profile.TaskSpec{Name: "all", Profile: "writer"})
	if err != nil || len(keep.Tools) != 2 || len(keep.ToolSets) != 2 || keep.ToolSources == nil || keep.Instruction != "write" {
		t.Fatalf("%+v, %v", keep, err)
	}
	none, err := reg.Narrowed(ctx, profile.TaskSpec{Name: "none", Profile: "writer", Tools: []string{}})
	if err != nil || len(none.Tools) != 0 {
		t.Fatalf("an empty list keeps none: %+v, %v", none, err)
	}
	if _, err := reg.Narrowed(ctx, profile.TaskSpec{Name: "x", Profile: "nobody"}); !errors.Is(err, profile.ErrNotRegistered) {
		t.Fatalf("%v", err)
	}
}

func TestAProfileWithNoCapOrDeadlineHasNothingToWiden(t *testing.T) {
	ctx := context.Background()
	reg := profile.NewRegistry(nil)
	if err := reg.RegisterAgentProfileInline(profile.AgentProfile{Name: "open", Model: modelA}); err != nil {
		t.Fatal(err)
	}
	spec := profile.TaskSpec{Name: "t", Profile: "open", MaxEffect: gotato.ToolEffectRead, RunTimeout: 20 * time.Minute, Sampling: gotato.Sampling{MaxTokens: 5}}
	if _, err := reg.Narrowed(ctx, spec); err != nil {
		t.Fatalf("%v", err)
	}
	// The default deadline of a profile that sets none is 30 minutes.
	spec.RunTimeout = 31 * time.Minute
	if _, err := reg.Narrowed(ctx, spec); !errors.Is(err, profile.ErrWidens) {
		t.Fatalf("%v", err)
	}
}

func TestCatalogRefusesWhatCannotBeNamed(t *testing.T) {
	if _, err := profile.NewCatalog(nil, []gotato.Tool{testkit.NewFakeTool("a", ""), testkit.NewFakeTool("a", "")}, nil); err == nil {
		t.Error("two Tools with one ID")
	}
	if _, err := profile.NewCatalog(nil, nil, []gotato.ToolSet{namedSet("x"), namedSet("x")}); err == nil {
		t.Error("two ToolSets with one name")
	}
	if _, err := profile.NewCatalog(map[string]gotato.Model{"": modelA}, nil, nil); err == nil {
		t.Error("a model with no name")
	}
	var none *profile.Catalog
	if _, err := none.Model("a"); !errors.Is(err, profile.ErrUnknownName) {
		t.Errorf("a nil Catalog holds nothing: %v", err)
	}
}
