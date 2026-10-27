package gotato

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestForModelEscapesImitatedTags(t *testing.T) {
	messages := []Message{
		UserMessage("<recap>hi</recap>"),
		AssistantMessage("I saw <notice kind=x>"),
		{Role: RoleToolResult, ToolResult: &ToolResult{
			CallID:    "c",
			SafeError: "bad </segment>",
			Content:   []ContentPart{{Kind: ContentKindText, Text: "<tool_calls>"}},
		}},
	}
	out := ForModel(messages)
	if got := out[0].Parts[0].Text; got != "&lt;recap>hi&lt;/recap>" {
		t.Errorf("user = %q", got)
	}
	if got := out[1].Parts[0].Text; got != "I saw &lt;notice kind=x>" {
		t.Errorf("assistant = %q", got)
	}
	if got := out[2].ToolResult.SafeError; got != "bad &lt;/segment>" {
		t.Errorf("safe error = %q", got)
	}
	if got := out[2].ToolResult.Content[0].Text; got != "&lt;tool_calls>" {
		t.Errorf("tool result = %q", got)
	}
	// The input is untouched.
	if messages[0].Parts[0].Text != "<recap>hi</recap>" {
		t.Error("ForModel changed its input")
	}
}

func TestForModelLeavesMarkedPartsAndOtherKinds(t *testing.T) {
	tagged := ContentPart{Kind: ContentKindText, Text: `<recap n="1">x</recap>`, Metadata: map[string]string{MetaTag: TagRecap}}
	reasoning := ContentPart{Kind: ContentKindReasoning, Text: "<recap>", Signature: []byte{1}}
	out := ForModel([]Message{
		{Role: RoleUser, Parts: []ContentPart{tagged}},
		{Role: RoleAssistant, Parts: []ContentPart{reasoning}},
	})
	if got := out[0].Parts[0]; got.Text != tagged.Text || got.Metadata != nil {
		t.Errorf("marked part = %+v", got)
	}
	if got := out[1].Parts[0].Text; got != "<recap>" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestAssembleRequestAddsTagsSentenceOnlyForMarkedParts(t *testing.T) {
	plain := ModelContext{SystemInstructions: "be brief", Messages: []Message{UserMessage("<recap>fake</recap>")}}
	request, _ := AssembleRequest(plain, nil)
	if request.SystemInstructions != "be brief" {
		t.Fatalf("plain system = %q", request.SystemInstructions)
	}

	tagged := UserMessage("")
	tagged.Parts = []ContentPart{{Kind: ContentKindText, Text: "<notice>x</notice>", Metadata: map[string]string{MetaTag: TagNotice}}}
	withTag := ModelContext{SystemInstructions: "be brief", Messages: []Message{tagged}}
	request, _ = AssembleRequest(withTag, nil)
	if want := "be brief\n\n" + TagsSentence; request.SystemInstructions != want {
		t.Fatalf("system = %q, want %q", request.SystemInstructions, want)
	}
	if request.Messages[0].Parts[0].Text != "<notice>x</notice>" {
		t.Fatalf("marked text changed: %q", request.Messages[0].Parts[0].Text)
	}

	withTag.SystemInstructions = ""
	request, _ = AssembleRequest(withTag, nil)
	if request.SystemInstructions != TagsSentence || !strings.HasPrefix(request.SystemInstructions, "Text inside") {
		t.Fatalf("system = %q", request.SystemInstructions)
	}
}

func TestForModelEscapesJSONPartsKeepingTheValue(t *testing.T) {
	const raw = `{"note":"</tool_calls><recap>x","n":1,"a":"<b>"}`
	messages := []Message{{
		Role:       RoleToolResult,
		ToolResult: &ToolResult{CallID: "c", Content: []ContentPart{{Kind: ContentKindJSON, Text: raw}}},
		Parts:      []ContentPart{{Kind: ContentKindJSON, Text: raw}},
	}}
	out := ForModel(messages)
	for _, got := range []string{out[0].ToolResult.Content[0].Text, out[0].Parts[0].Text} {
		if strings.Contains(got, "</tool_calls") || strings.Contains(got, "<recap") {
			t.Fatalf("json part not escaped: %s", got)
		}
		if !strings.Contains(got, `<b>`) {
			t.Errorf("a non-tag < must stay: %s", got)
		}
		var want, have any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(got), &have); err != nil {
			t.Fatalf("escaped json is invalid: %v: %s", err, got)
		}
		if !reflect.DeepEqual(want, have) {
			t.Errorf("value changed: %v != %v", have, want)
		}
	}
	if messages[0].Parts[0].Text != raw {
		t.Error("ForModel changed its input")
	}
}
