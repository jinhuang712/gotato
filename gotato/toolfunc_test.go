package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type weatherInput struct {
	City  string            `json:"city" description:"City to look up."`
	Unit  string            `json:"unit,omitempty" enum:"celsius,fahrenheit"`
	Days  int               `json:"days,omitempty"`
	Exact *bool             `json:"exact,omitempty"`
	Score float64           `json:"score,omitempty"`
	Tags  []string          `json:"tags,omitempty"`
	Notes map[string]string `json:"notes,omitempty"`
	Extra struct {
		Nested string `json:"nested"`
	} `json:"extra,omitzero"`
	Skipped string `json:"-"`
	private string //nolint:unused // fixture: the derived schema must skip unexported fields
}

type weatherOutput struct {
	Summary string `json:"summary"`
}

func TestFuncToolDerivesInputSchema(t *testing.T) {
	tool, err := NewFuncTool("get_weather", "Return the weather.", func(ctx context.Context, in weatherInput) (weatherOutput, error) {
		return weatherOutput{Summary: in.City}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := tool.Spec()
	if spec.ID != "get_weather" || spec.Name != "get_weather" || spec.Description != "Return the weather." {
		t.Fatalf("spec = %+v", spec)
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema root = %#v", schema)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Fatalf("required = %#v", schema["required"])
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) != 8 {
		t.Fatalf("properties = %#v", properties)
	}
	for name, want := range map[string]string{
		"city":  "string",
		"unit":  "string",
		"days":  "integer",
		"exact": "boolean",
		"score": "number",
		"tags":  "array",
		"notes": "object",
		"extra": "object",
	} {
		property, _ := properties[name].(map[string]any)
		if property["type"] != want {
			t.Fatalf("property %s = %#v", name, properties[name])
		}
	}
	if _, declared := properties["Skipped"]; declared {
		t.Fatal("json:\"-\" field reached the Schema")
	}
	if _, declared := properties["private"]; declared {
		t.Fatal("unexported field reached the Schema")
	}
	city, _ := properties["city"].(map[string]any)
	if city["description"] != "City to look up." {
		t.Fatalf("city description = %#v", city)
	}
	unit, _ := properties["unit"].(map[string]any)
	enum, _ := unit["enum"].([]any)
	if len(enum) != 2 || enum[0] != "celsius" || enum[1] != "fahrenheit" {
		t.Fatalf("unit enum = %#v", unit["enum"])
	}
	tags, _ := properties["tags"].(map[string]any)
	items, _ := tags["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("tags items = %#v", tags["items"])
	}
}

func TestFuncToolRejectsUnsupportedInput(t *testing.T) {
	if _, err := NewFuncTool("bad", "", func(ctx context.Context, in string) (string, error) { return in, nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for non-struct input, got %v", err)
	}
	type cyclic struct {
		Next *cyclic `json:"next,omitempty"`
	}
	if _, err := NewFuncTool("cycle", "", func(ctx context.Context, in cyclic) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for recursive input, got %v", err)
	}
	type EmbeddedCycle struct {
		*EmbeddedCycle
	}
	if _, err := NewFuncTool("embedded-cycle", "", func(ctx context.Context, in EmbeddedCycle) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for embedded recursive input, got %v", err)
	}
	type channels struct {
		Ch chan int `json:"ch"`
	}
	if _, err := NewFuncTool("chan", "", func(ctx context.Context, in channels) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for channel field, got %v", err)
	}
	if _, err := NewFuncTool[weatherInput, string]("nil", "", nil); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for nil function, got %v", err)
	}
	if _, err := NewFuncTool(" ", "", func(ctx context.Context, in weatherInput) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("expected invalid argument for empty name, got %v", err)
	}
}

type recursiveList []recursiveList
type recursiveMap map[string]recursiveMap

func TestFuncToolRejectsRecursiveSliceAndMap(t *testing.T) {
	type withList struct {
		Items recursiveList `json:"items"`
	}
	if _, err := NewFuncTool("list", "", func(ctx context.Context, in withList) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("recursive slice = %v, want invalid_argument", err)
	}
	type withMap struct {
		Items recursiveMap `json:"items"`
	}
	if _, err := NewFuncTool("map", "", func(ctx context.Context, in withMap) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("recursive map = %v, want invalid_argument", err)
	}
}

func TestFuncToolRejectsUnsupportedJSONOptions(t *testing.T) {
	type stringly struct {
		Count int `json:"count,string"`
	}
	if _, err := NewFuncTool("stringly", "", func(ctx context.Context, in stringly) (string, error) { return "", nil }); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf(`json:",string" = %v, want invalid_argument`, err)
	}
}

type funcToolModel struct {
	arguments string
	calls     int
}

func (m *funcToolModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	call := m.calls
	m.calls++
	if call == 0 {
		return &testStream{events: []ModelEvent{
			{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "get_weather", Arguments: []byte(m.arguments)}},
			{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls},
		}}, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "done"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, nil
}

func TestFuncToolRunsThroughTheLoop(t *testing.T) {
	var seen weatherInput
	agent, err := NewSession(
		context.Background(),
		WithModel(&funcToolModel{arguments: `{"city":"Shenzhen","unit":"celsius"}`}),
		WithTools(weatherTool(t, func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			seen = in
			return weatherOutput{Summary: "sunny in " + in.City}, nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("weather"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	if seen.City != "Shenzhen" || seen.Unit != "celsius" {
		t.Fatalf("decoded arguments = %+v", seen)
	}
}

func TestFuncToolMalformedArgumentsNeverExecute(t *testing.T) {
	executed := false
	agent, err := NewSession(
		context.Background(),
		WithModel(&funcToolModel{arguments: `{"unit":"celsius"}`}),
		WithTools(weatherTool(t, func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			executed = true
			return weatherOutput{}, nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	// Invalid arguments settle as a failed Tool Result the Model can correct;
	// the Run continues (GOALS.md G-G04).
	result, err := agent.Prompt(context.Background(), UserMessage("weather"))
	if err != nil || result.Status != RunStatusCompleted {
		t.Fatalf("run = %+v, %v; invalid arguments must not end the Run", result, err)
	}
	if executed {
		t.Fatal("executor ran with missing required field")
	}
	var failed *ToolResult
	for _, m := range agent.Transcript().Messages() {
		if m.ToolResult != nil {
			failed = m.ToolResult
		}
	}
	if failed == nil || failed.Status != ToolResultStatusFailed || failed.Executed || !strings.Contains(failed.SafeError, "city is required") || !strings.Contains(failed.SafeError, string(CodeToolArgumentFailure)) {
		t.Fatalf("tool result = %+v", failed)
	}
}

func TestUnknownToolBecomesFailedToolResult(t *testing.T) {
	model := &namedToolModel{tool: "get_wether"}
	agent, err := NewSession(
		context.Background(),
		WithModel(model),
		WithTools(weatherTool(t, func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			return weatherOutput{}, nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("weather"))
	if err != nil || result.Status != RunStatusCompleted {
		t.Fatalf("run = %+v, %v; an unknown tool must not end the Run", result, err)
	}
	var failed *ToolResult
	for _, m := range agent.Transcript().Messages() {
		if m.ToolResult != nil {
			failed = m.ToolResult
		}
	}
	if failed == nil || failed.Status != ToolResultStatusFailed || failed.Executed || !strings.Contains(failed.SafeError, `unknown tool "get_wether"`) || !strings.Contains(failed.SafeError, "get_weather") {
		t.Fatalf("tool result = %+v", failed)
	}
	if model.calls != 2 {
		t.Fatalf("the Model should get a second turn to recover, calls = %d", model.calls)
	}
}

type namedToolModel struct {
	tool  string
	calls int
}

func (m *namedToolModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.calls++
	if m.calls == 1 {
		return &testStream{events: []ModelEvent{
			{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: m.tool, Arguments: []byte(`{}`)}},
			{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls},
		}}, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "recovered"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, nil
}

func TestFuncToolErrorBecomesFailedToolResult(t *testing.T) {
	agent, err := NewSession(
		context.Background(),
		WithModel(&funcToolModel{arguments: `{"city":"Shenzhen"}`}),
		WithTools(weatherTool(t, func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			return weatherOutput{}, errors.New("upstream is down")
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()
	if _, err := agent.Prompt(context.Background(), UserMessage("weather")); err != nil {
		t.Fatal(err)
	}
	var status ToolResultStatus
	for {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatalf("event stream ended before run_ended: %v", nextErr)
		}
		if ended, ok := event.(ToolEnded); ok {
			status = ended.Status
		}
		if event.Kind() == EventKindRunEnded {
			break
		}
	}
	if status != ToolResultStatusFailed {
		t.Fatalf("tool result status = %v, want %q", status, ToolResultStatusFailed)
	}
}

func TestFuncToolStringOutputBecomesText(t *testing.T) {
	tool, err := NewFuncToolWithProgress("say", "Say something.", func(ctx context.Context, in struct{}, progress ToolProgressFunc) (string, error) {
		progress("working")
		return "spoken", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	result, err := tool.Execute(context.Background(), ToolUse{}, func(string) { updates++ })
	if err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("progress updates = %d", updates)
	}
	if result.Status != ToolResultStatusOK || len(result.Content) != 1 || result.Content[0].Kind != ContentKindText || result.Content[0].Text != "spoken" {
		t.Fatalf("result = %+v", result)
	}
}

type embeddedPrivateFields struct {
	Value string `json:"value"`
}

type withEmbeddedPrivate struct {
	embeddedPrivateFields
	Name string `json:"name"`
}

type byteArrayInput struct {
	Hash [4]byte `json:"hash"`
	Blob []byte  `json:"blob,omitempty"`
}

func TestFuncToolSchemaForByteArraysAndEmbeddedUnexported(t *testing.T) {
	arrays, err := NewFuncTool("bytes", "", func(ctx context.Context, in byteArrayInput) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(arrays.Spec().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	hash, _ := properties["hash"].(map[string]any)
	if hash["type"] != "array" {
		t.Fatalf("[4]byte schema = %#v", hash)
	}
	items, _ := hash["items"].(map[string]any)
	if items["type"] != "integer" {
		t.Fatalf("[4]byte items = %#v", hash["items"])
	}
	blob, _ := properties["blob"].(map[string]any)
	if blob["type"] != "string" {
		t.Fatalf("[]byte schema = %#v", blob)
	}

	embedded, err := NewFuncTool("embedded", "", func(ctx context.Context, in withEmbeddedPrivate) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(embedded.Spec().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	promoted, _ := schema["properties"].(map[string]any)
	if len(promoted) != 2 {
		t.Fatalf("promoted properties = %#v", promoted)
	}
	if _, ok := promoted["value"]; !ok {
		t.Fatal("exported field of an embedded unexported struct was dropped")
	}
}

type stringlyInput struct {
	At      time.Time `json:"at"`
	Address net.IP    `json:"address,omitempty"`
}

func TestFuncToolMapsStringLikeTypes(t *testing.T) {
	tool, err := NewFuncTool("schedule", "Schedule something.", func(ctx context.Context, in stringlyInput) (string, error) {
		return in.At.Format(time.RFC3339), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Spec().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	at, _ := properties["at"].(map[string]any)
	if at["type"] != "string" || at["format"] != "date-time" {
		t.Fatalf("time.Time schema = %#v", at)
	}
	address, _ := properties["address"].(map[string]any)
	if address["type"] != "string" {
		t.Fatalf("net.IP schema = %#v", address)
	}
	result, err := tool.Execute(context.Background(), ToolUse{Arguments: []byte(`{"at":"2026-09-01T10:00:00Z"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content[0].Text != "2026-09-01T10:00:00Z" {
		t.Fatalf("decoded time = %+v", result.Content)
	}
}

func weatherTool(t *testing.T, fn func(context.Context, weatherInput) (weatherOutput, error)) Tool {
	t.Helper()
	tool, err := NewFuncTool("get_weather", "Return the weather.", fn)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}
