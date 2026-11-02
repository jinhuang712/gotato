package host_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/testkit"
)

// effectTool is a Tool of a stated Effect.
type effectTool struct {
	id     string
	effect gotato.ToolEffect
}

func (t effectTool) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: t.id, Name: t.id, Description: "tool " + t.id, InputSchema: []byte(`{"type":"object"}`), Effect: t.effect}
}

func (effectTool) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{Status: gotato.ToolResultStatusOK, Executed: true, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "ok"}}}, nil
}

// effectSet is a ToolSet of Tools of stated Effects.
type effectSet struct {
	name  string
	tools []gotato.Tool
}

func (s effectSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: s.name} }

func (s effectSet) Tools(context.Context) ([]gotato.Tool, error) { return s.tools, nil }

// An Agent holds typed values and no function that runs on the Session the Runner
// builds, so it cannot replace the Runner's Writer, raise its own cap or put back
// a Tool a Session switched off. The Runner builds every SessionOption.
func TestAnAgentHoldsNoSessionOption(t *testing.T) {
	option := reflect.TypeFor[gotato.SessionOption]()
	agent := reflect.TypeFor[profile.AgentProfile]()
	for i := range agent.NumField() {
		field := agent.Field(i)
		ft := field.Type
		if ft == option || (ft.Kind() == reflect.Slice && ft.Elem() == option) {
			t.Errorf("Agent.%s is a SessionOption: the Runner builds them all", field.Name)
		}
	}
	if _, ok := agent.FieldByName("Options"); ok {
		t.Error("Agent still has Options")
	}
}

// The cap of an Agent covers the Tools of its ToolSets, however they are
// installed: nothing the Agent holds sets another cap or lifts it.
func TestAnAgentsCapHidesTheToolsOfItsToolSets(t *testing.T) {
	for _, mode := range []profile.ToolSetMode{profile.ToolSetListed, profile.ToolSetFolded, profile.ToolSetActive} {
		t.Run(string(mode), func(t *testing.T) {
			set := effectSet{name: "files", tools: []gotato.Tool{effectTool{"read", gotato.ToolEffectRead}, effectTool{"wipe", gotato.ToolEffectDestructive}}}
			model := testkit.NewFakeModel(testkit.Text("ok"))
			runner, _ := newRunner(t, profile.AgentProfile{Name: "capped", Model: model, MaxEffect: gotato.ToolEffectRead, ToolSets: []profile.ToolSet{{Set: set, Mode: mode}}})
			ctx := context.Background()
			result, err := runner.Run(ctx, host.RunRequest{Prompt: "go"})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := model.LastRequest()
			if mode == profile.ToolSetActive && !slices.Contains(toolIDsOf(request), "files.read") {
				t.Fatalf("an active ToolSet's Tool within the cap is not offered: %v", toolIDsOf(request))
			}
			if slices.Contains(toolIDsOf(request), "files.wipe") {
				t.Fatalf("a Tool above the cap is offered: %v", toolIDsOf(request))
			}
			entries, err := runner.Tools(ctx, "", result.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Spec.ID == "files.wipe" {
					t.Fatalf("Runner.Tools lists a Tool above the cap: %+v", entry)
				}
			}
			if _, err := runner.SetToolActive(ctx, result.SessionID, "files.wipe", true); err == nil {
				t.Fatal("a Session switched on a Tool above its Agent's cap")
			}
		})
	}
}

// A ToolSet installed listed or folded is behind search_tools, and one installed
// active is visible always.
func TestToolSetModesSetHowTheModelSeesATool(t *testing.T) {
	set := effectSet{name: "files", tools: []gotato.Tool{effectTool{"read", gotato.ToolEffectRead}}}
	for mode, visible := range map[profile.ToolSetMode]bool{profile.ToolSetListed: false, profile.ToolSetFolded: false, profile.ToolSetActive: true, "": false} {
		model := testkit.NewFakeModel(testkit.Text("ok"))
		runner, _ := newRunner(t, profile.AgentProfile{Name: "a", Model: model, ToolSets: []profile.ToolSet{{Set: set, Mode: mode}}})
		if _, err := runner.Run(context.Background(), host.RunRequest{Prompt: "go"}); err != nil {
			t.Fatal(err)
		}
		request, _ := model.LastRequest()
		ids := toolIDsOf(request)
		if got := slices.Contains(ids, "files.read"); got != visible {
			t.Errorf("mode %q: files.read offered = %v, want %v (%v)", mode, got, visible, ids)
		}
		if !visible && !slices.Contains(ids, gotato.SearchToolName) {
			t.Errorf("mode %q: search_tools is not offered: %v", mode, ids)
		}
	}
}

// recordingSearcher is a ToolSearcher that records its queries.
type recordingSearcher struct {
	mu      sync.Mutex
	queries []string
}

func (s *recordingSearcher) Search(_ context.Context, query string, tools []gotato.ToolSpec) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	var ids []string
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return ids, nil
}

// The Agent's Sampling, Extensions and ToolSearcher reach the Session the Runner
// builds, each through the Runner's own option.
func TestAnAgentsSamplingExtensionsAndSearcherReachItsSession(t *testing.T) {
	arguments, _ := json.Marshal(map[string][]string{"queries": {"read the file"}})
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "q1", ToolID: gotato.SearchToolName, Arguments: arguments}),
		testkit.Text("done"),
	)
	searcher := &recordingSearcher{}
	set := effectSet{name: "files", tools: []gotato.Tool{effectTool{"read", gotato.ToolEffectRead}}}
	runner, _ := newRunner(t, profile.AgentProfile{
		Name:         "a",
		Model:        model,
		Sampling:     gotato.Sampling{MaxTokens: 77},
		Extensions:   []any{briefNote{}},
		ToolSets:     []profile.ToolSet{{Set: set}},
		ToolSearcher: searcher,
	})
	if _, err := runner.Run(context.Background(), host.RunRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	request, _ := model.LastRequest()
	if request.Sampling.MaxTokens != 77 {
		t.Errorf("Sampling.MaxTokens = %d, want the Agent's 77", request.Sampling.MaxTokens)
	}
	if len(request.Messages) == 0 || gotato.TextOf(request.Messages[0]) != "note: answer briefly" {
		t.Errorf("the Agent's Extension did not run: %+v", request.Messages)
	}
	searcher.mu.Lock()
	defer searcher.mu.Unlock()
	if !slices.Equal(searcher.queries, []string{"read the file"}) {
		t.Errorf("the Agent's ToolSearcher saw %v", searcher.queries)
	}
}

// New needs a Registry of profiles; the profile rules themselves are enforced at
// registration (see profile's tests).
func TestNewRefusesNilProfiles(t *testing.T) {
	if _, err := host.New(host.Config{Store: memStore()}); err == nil {
		t.Error("New accepted a nil Profiles")
	}
}
