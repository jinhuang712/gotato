package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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
	} `json:"extra,omitempty"`
	Skipped string `json:"-"`
	private string
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
	if _, err := NewFuncTool("bad", "", func(ctx context.Context, in string) (string, error) { return in, nil }); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument for non-struct input, got %v", err)
	}
	type cyclic struct {
		Next *cyclic `json:"next,omitempty"`
	}
	if _, err := NewFuncTool("cycle", "", func(ctx context.Context, in cyclic) (string, error) { return "", nil }); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument for recursive input, got %v", err)
	}
	type channels struct {
		Ch chan int `json:"ch"`
	}
	if _, err := NewFuncTool("chan", "", func(ctx context.Context, in channels) (string, error) { return "", nil }); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument for channel field, got %v", err)
	}
	if _, err := NewFuncTool[weatherInput, string]("nil", "", nil); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument for nil function, got %v", err)
	}
	if _, err := NewFuncTool(" ", "", func(ctx context.Context, in weatherInput) (string, error) { return "", nil }); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("expected invalid argument for empty name, got %v", err)
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
			{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "get_weather", Arguments: []byte(m.arguments)}},
			{Kind: ModelDone, StopReason: StopToolCalls},
		}}, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}}}, nil
}

func TestWithFuncRunsThroughTheLoop(t *testing.T) {
	var seen weatherInput
	agent, err := NewAgent(
		WithModel(&funcToolModel{arguments: `{"city":"Shenzhen","unit":"celsius"}`}),
		WithFunc("get_weather", "Return the weather.", func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			seen = in
			return weatherOutput{Summary: "sunny in " + in.City}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), UserMessage("weather"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("result = %+v", result)
	}
	if seen.City != "Shenzhen" || seen.Unit != "celsius" {
		t.Fatalf("decoded arguments = %+v", seen)
	}
}

func TestFuncToolMalformedArgumentsNeverExecute(t *testing.T) {
	executed := false
	agent, err := NewAgent(
		WithModel(&funcToolModel{arguments: `{"unit":"celsius"}`}),
		WithFunc("get_weather", "Return the weather.", func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			executed = true
			return weatherOutput{}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("weather")); !IsCode(err, ErrToolArgumentFailure) {
		t.Fatalf("expected argument validation failure, got %v", err)
	}
	if executed {
		t.Fatal("executor ran with missing required field")
	}
}

func TestFuncToolErrorBecomesFailedToolResult(t *testing.T) {
	agent, err := NewAgent(
		WithModel(&funcToolModel{arguments: `{"city":"Shenzhen"}`}),
		WithFunc("get_weather", "Return the weather.", func(ctx context.Context, in weatherInput) (weatherOutput, error) {
			return weatherOutput{}, errors.New("upstream is down")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("weather")); err != nil {
		t.Fatal(err)
	}
}

func TestFuncToolStringOutputBecomesText(t *testing.T) {
	tool, err := NewFuncToolWithProgress("say", "Say something.", func(ctx context.Context, in struct{}, progress ToolProgress) (string, error) {
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
	if result.Status != ToolResultOK || len(result.Content) != 1 || result.Content[0].Kind != ContentText || result.Content[0].Text != "spoken" {
		t.Fatalf("result = %+v", result)
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
	result, err := tool.Execute(context.Background(), ToolUse{ArgumentsJSON: []byte(`{"at":"2026-09-01T10:00:00Z"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content[0].Text != "2026-09-01T10:00:00Z" {
		t.Fatalf("decoded time = %+v", result.Content)
	}
}
