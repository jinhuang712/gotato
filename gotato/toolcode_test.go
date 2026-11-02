package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// claimingCodeTool returns a result that claims a Code Core owns.
type claimingCodeTool struct{}

func (claimingCodeTool) Spec() ToolSpec { return ToolSpec{ID: "claim", Effect: ToolEffectRead} }

func (claimingCodeTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{Code: CodeToolNotLoaded, Content: []ContentPart{{Kind: ContentKindText, Text: "ok"}}}, nil
}

// codeFailingTool returns an error a Tool made.
type codeFailingTool struct{ err error }

func (codeFailingTool) Spec() ToolSpec { return ToolSpec{ID: "fail", Effect: ToolEffectRead} }

func (f codeFailingTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{}, f.err
}

// useRecordingTool keeps the ToolUse it was called with.
type useRecordingTool struct{ got *ToolUse }

func (useRecordingTool) Spec() ToolSpec { return ToolSpec{ID: "use", Effect: ToolEffectRead} }

func (u useRecordingTool) Execute(_ context.Context, use ToolUse, _ ToolProgressFunc) (ToolResult, error) {
	*u.got = use
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "ok"}}}, nil
}

// A failed Tool Result says why through Code, and ToolEnded carries the same
// Code, so a subscriber does not read SafeError.
func TestAFailedToolResultCarriesItsCode(t *testing.T) {
	set, _ := ordersSet()
	cases := []struct {
		name   string
		script []ModelEvent
		want   ErrorCode
	}{
		{"a call to a tool that was not loaded", callTool("c1", "orders.refund"), CodeToolNotLoaded},
		{"a call to an unknown tool", callTool("c1", "nothing"), CodeToolResolutionFailure},
		{"a Tool that returns an error", callTool("c1", "fail"), CodeToolExecutionFailure},
		{"a Tool that returns a RuntimeError", callTool("c1", "failrt"), CodeDeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &recordingModel{scripts: [][]ModelEvent{tc.script, finalScript("done")}}
			agent := newSearchSession(t, model, WithToolSet(set), WithTools(
				codeFailingTool{err: errors.New("boom")},
				renamedFailing{codeFailingTool{err: runtimeError(CodeDeadlineExceeded, "Tool", "too slow", nil)}},
			))
			events, err := agent.Subscribe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = events.Close() }()
			if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
				t.Fatal(err)
			}
			result := onlyCommittedResult(t, agent)
			if result.Code != tc.want {
				t.Fatalf("result code = %q, want %q (text %q)", result.Code, tc.want, result.SafeError)
			}
			for {
				event, err := events.Next(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if ended, ok := event.(ToolEnded); ok {
					if ended.Code != tc.want {
						t.Fatalf("tool_ended code = %q, want %q", ended.Code, tc.want)
					}
					return
				}
			}
		})
	}
}

type renamedFailing struct{ codeFailingTool }

func (renamedFailing) Spec() ToolSpec { return ToolSpec{ID: "failrt", Effect: ToolEffectRead} }

// A call that succeeded has no Code, and a Tool cannot give its result one.
func TestAToolCannotSetTheCodeOfItsResult(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{callTool("c1", "claim"), finalScript("done")}}
	agent := newSearchSession(t, model, WithTools(claimingCodeTool{}))
	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	results := committedResults(agent)
	if len(results) != 1 || results[0].Code != "" || !results[0].Executed {
		t.Fatalf("committed = %+v, want an executed result with no code", results)
	}
}

// A malformed argument fails with tool_argument_failure.
func TestAnInvalidArgumentCarriesItsCode(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{
		{{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "c1", ToolID: "claim", Arguments: []byte("{")}}, {Kind: ModelEventKindDone, StopReason: StopReasonToolCalls}},
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithTools(claimingCodeTool{}))
	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if result := onlyCommittedResult(t, agent); result.Code != CodeToolArgumentFailure {
		t.Fatalf("code = %q, want %q", result.Code, CodeToolArgumentFailure)
	}
}

// A Result stored before Code existed reads back with no Code, and a Result with
// none writes no `code` field.
func TestAToolResultWithoutACodeKeepsItsJSON(t *testing.T) {
	var old ToolResult
	if err := json.Unmarshal([]byte(`{"call_id":"c1","status":"failed","safe_error":"tool_resolution_failure: unknown tool \"x\"","executed":false}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Code != "" {
		t.Fatalf("code = %q, want none", old.Code)
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"code"`) {
		t.Fatalf("json = %s, want no code field", raw)
	}
}

// onlyCommittedResult is the one Tool Result in the Session's Transcript.
func onlyCommittedResult(t *testing.T, agent *Session) ToolResult {
	t.Helper()
	results := committedResults(agent)
	if len(results) != 1 {
		t.Fatalf("committed %d Tool Results, want 1", len(results))
	}
	return results[0]
}

// A Tool and a PreToolUse read which Session a call belongs to.
func TestToolUseNamesItsSession(t *testing.T) {
	var got ToolUse
	model := &recordingModel{scripts: [][]ModelEvent{callTool("c1", "use"), finalScript("done")}}
	agent := newSearchSession(t, model, WithTools(useRecordingTool{got: &got}))
	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if got.SessionID == "" || got.SessionID != agent.ID() {
		t.Fatalf("ToolUse.SessionID = %q, want %q", got.SessionID, agent.ID())
	}
}

// beforeRecorder keeps the ToolUse a PreToolUse was called with.
type beforeRecorder struct{ got *ToolUse }

func (b beforeRecorder) Before(_ context.Context, use ToolUse) (PreToolDecision, error) {
	*b.got = use
	return PreToolDecision{}, nil
}

// A PreToolUse reads the Session of the call, so a person can be asked in the
// right conversation.
func TestPreToolUseSeesTheSessionOfTheCall(t *testing.T) {
	var got ToolUse
	var ran ToolUse
	model := &recordingModel{scripts: [][]ModelEvent{callTool("c1", "use"), finalScript("done")}}
	agent := newSearchSession(t, model, WithTools(useRecordingTool{got: &ran}), WithExtension(beforeRecorder{got: &got}))
	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if got.CallID != "c1" {
		t.Fatalf("Before saw call %q, want c1", got.CallID)
	}
	if got.SessionID == "" || got.SessionID != agent.ID() {
		t.Fatalf("Before: ToolUse.SessionID = %q, want %q", got.SessionID, agent.ID())
	}
}
