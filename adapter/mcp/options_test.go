package mcp_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool with no annotations at all (a nil pointer) takes WithDefaultEffect;
// the priority is WithToolEffect, then annotations, then WithDefaultEffect,
// then destructive.
func TestDefaultEffectIsForToolsWithNoAnnotations(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()),
		gmcp.WithDefaultEffect(gotato.ToolEffectRead),
		gmcp.WithToolEffect("get_order", gotato.ToolEffectWriteLocal))
	got := specs(t, set)
	want := map[string]gotato.ToolEffect{
		"bare":       gotato.ToolEffectRead,          // no annotations: the default
		"get_order":  gotato.ToolEffectWriteLocal,    // WithToolEffect beats annotations
		"send_mail":  gotato.ToolEffectWriteExternal, // annotations beat the default
		"delete_all": gotato.ToolEffectDestructive,
	}
	for name, effect := range want {
		if got[name].Effect != effect {
			t.Errorf("%s effect = %q, want %q", name, got[name].Effect, effect)
		}
	}
	// WithToolEffect also beats the default on a tool with no annotations.
	set = newSet(t, inMemory(t, newServer()),
		gmcp.WithDefaultEffect(gotato.ToolEffectRead), gmcp.WithToolEffect("bare", gotato.ToolEffectWriteExternal))
	if got := specs(t, set)["bare"].Effect; got != gotato.ToolEffectWriteExternal {
		t.Errorf("bare effect = %q, want write_external", got)
	}
	// Without the option the fallback is unchanged.
	set = newSet(t, inMemory(t, newServer()))
	if got := specs(t, set)["bare"].Effect; got != gotato.ToolEffectDestructive {
		t.Errorf("bare effect = %q, want destructive", got)
	}
	if _, err := gmcp.NewToolSet("a", inMemory(t, newServer()), gmcp.WithDefaultEffect("bogus")); err == nil {
		t.Error("an unknown default Effect must be refused")
	}
}

// Annotations that are present but declare nothing are still annotations: the
// MCP defaults apply and WithDefaultEffect does not.
func TestDefaultEffectDoesNotTouchEmptyAnnotations(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "s", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "empty", Annotations: &sdk.ToolAnnotations{}, InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) { return textResult("x"), nil })
	set := newSet(t, inMemory(t, server), gmcp.WithDefaultEffect(gotato.ToolEffectRead))
	if got := specs(t, set)["empty"].Effect; got != gotato.ToolEffectDestructive {
		t.Fatalf("empty annotations effect = %q, want destructive", got)
	}
}

func TestSequentialWritesMarksEveryToolAboveRead(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()), gmcp.WithSequentialWrites(), gmcp.WithToolEffect("echo", gotato.ToolEffectWriteLocal))
	got := specs(t, set)
	for name, spec := range got {
		if want := spec.Effect != gotato.ToolEffectRead; spec.Sequential != want {
			t.Errorf("%s (effect %q) Sequential = %v, want %v", name, spec.Effect, spec.Sequential, want)
		}
	}
	if !got["echo"].Sequential || got["get_order"].Sequential {
		t.Error("the overridden echo must be Sequential and the read get_order must not")
	}
	// The default is unchanged.
	for name, spec := range specs(t, newSet(t, inMemory(t, newServer()))) {
		if spec.Sequential {
			t.Errorf("%s is Sequential without the option", name)
		}
	}
}

func toolNames(t *testing.T, set *gmcp.ToolSet) []string {
	t.Helper()
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Spec().Name)
	}
	slices.Sort(names)
	return names
}

func TestToolFilterLeavesTheToolOutAndRecordsIt(t *testing.T) {
	seen := 0
	set := newSet(t, inMemory(t, newServer()), gmcp.WithToolFilter(func(tool *sdk.Tool) bool {
		seen++
		return tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}))
	names := toolNames(t, set)
	if !slices.Equal(names, []string{"echo", "fail", "get_order", "mixed"}) {
		t.Fatalf("tools = %v, want the read-only ones", names)
	}
	skipped := set.Skipped()
	for _, s := range skipped {
		if s.Reason != "filtered" || s.Name == "" {
			t.Errorf("skipped %+v, want reason filtered with the server's name", s)
		}
	}
	if len(skipped) != 4 || seen != 8 {
		t.Fatalf("skipped %d tools, filter saw %d, want 4 and 8", len(skipped), seen)
	}
}

func TestToolRenameChangesOnlyTheNameTheModelSees(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()),
		gmcp.WithToolRename(func(name string) string { return "crm_" + name }),
		// The server's own name keys the Effect and the filter.
		gmcp.WithToolEffect("bare", gotato.ToolEffectRead),
		gmcp.WithToolFilter(func(tool *sdk.Tool) bool { return tool.Name != "delete_all" }))
	got := specs(t, set)
	if _, ok := got["crm_delete_all"]; ok {
		t.Fatal("the filter must see the server's name and drop delete_all")
	}
	if got["crm_bare"].Effect != gotato.ToolEffectRead || got["crm_bare"].Name != "crm_bare" {
		t.Fatalf("crm_bare = %+v, want the Effect set under the server's name", got["crm_bare"])
	}
	if _, ok := got["bare"]; ok {
		t.Fatal("the server's name must not be offered after a rename")
	}
	// The call goes to the server under its own name.
	if result := execute(t, set, "crm_echo", `{"text":"hi"}`); result.Content[0].Text != "echo: hi" {
		t.Fatalf("result = %+v", result)
	}
}

func TestToolRenameCollisionAndEmptyNameGoToSkipped(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()), gmcp.WithToolRename(func(name string) string {
		switch name {
		case "get_order", "echo":
			return "same"
		case "fail":
			return " "
		}
		return name
	}))
	names := toolNames(t, set)
	if slices.Contains(names, "fail") || slices.Contains(names, " ") || slices.Contains(names, "echo") {
		t.Fatalf("tools = %v", names)
	}
	if n := slices.Index(names, "same"); n < 0 || slices.Index(names[n+1:], "same") >= 0 {
		t.Fatalf("tools = %v, want exactly one same", names)
	}
	var collided, empty int
	for _, s := range set.Skipped() {
		switch s.Name {
		case "fail":
			empty++
		case "get_order", "echo":
			collided++
		}
		if s.Reason == "" {
			t.Errorf("%+v has no reason", s)
		}
	}
	if collided != 1 || empty != 1 {
		t.Fatalf("Skipped = %+v, want one collision and one empty name", set.Skipped())
	}
}

func TestAPanicInTheFilterOrTheRenameSkipsOnlyThatTool(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()),
		gmcp.WithToolFilter(func(tool *sdk.Tool) bool {
			if tool.Name == "echo" {
				panic("filter boom")
			}
			return true
		}),
		gmcp.WithToolRename(func(name string) string {
			if name == "fail" {
				panic("rename boom")
			}
			return name
		}))
	names := toolNames(t, set)
	if slices.Contains(names, "echo") || slices.Contains(names, "fail") || !slices.Contains(names, "get_order") {
		t.Fatalf("tools = %v", names)
	}
	skipped := set.Skipped()
	if len(skipped) != 2 {
		t.Fatalf("Skipped = %+v, want the two panicking tools", skipped)
	}
	for _, s := range skipped {
		if s.Reason == "" || strings.Contains(s.Reason, "boom") {
			t.Errorf("reason %q must say what happened without the panic text", s.Reason)
		}
	}
}
