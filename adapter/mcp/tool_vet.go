package mcp

import (
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// vetTool decides whether the listed tool t is offered, and under what name:
// the name the Model sees, or the SkippedTool that says why it is left out. It
// records the server's names in seen and the offered names in offered.
func (s *ToolSet) vetTool(t *sdk.Tool, seen, offered map[string]bool) (string, *SkippedTool) {
	switch {
	case t == nil || strings.TrimSpace(t.Name) == "":
		return "", &SkippedTool{Reason: "the server listed a tool with no name"}
	// [CONTRACT] one bad entry (a repeated name) must not take the server's good tools; Skipped reports it.
	case seen[t.Name]:
		return "", &SkippedTool{Name: t.Name, Reason: "the server listed the name more than once; the first is kept"}
	}
	seen[t.Name] = true
	if skip := s.filterSkip(t); skip != nil {
		return "", skip
	}
	name, skip := s.renameSkip(t)
	if skip != nil {
		return "", skip
	}
	// The names the Model sees must differ: a rename can give two tools
	// one name, or a server name that another tool was renamed to.
	if offered[name] {
		return "", &SkippedTool{Name: t.Name, Reason: "the name the Model would see is another tool's; the first is kept"}
	}
	offered[name] = true
	return name, nil
}

// filterSkip runs the host's filter on t and returns the SkippedTool that says
// why it is left out, or nil when it stays.
func (s *ToolSet) filterSkip(t *sdk.Tool) *SkippedTool {
	if s.cfg.filter == nil {
		return nil
	}
	keep, panicked := callFilter(s.cfg.filter, t)
	if panicked {
		return &SkippedTool{Name: t.Name, Reason: "the tool filter panicked; the tool is left out"}
	}
	if !keep {
		return &SkippedTool{Name: t.Name, Reason: "filtered"}
	}
	return nil
}

// renameSkip runs the host's rename on t and returns the name the Model sees,
// or the SkippedTool that says why the tool is left out.
func (s *ToolSet) renameSkip(t *sdk.Tool) (string, *SkippedTool) {
	if s.cfg.rename == nil {
		return t.Name, nil
	}
	renamed, panicked := callRename(s.cfg.rename, t.Name)
	switch {
	case panicked:
		return "", &SkippedTool{Name: t.Name, Reason: "the tool rename panicked; the tool is left out"}
	case strings.TrimSpace(renamed) == "":
		return "", &SkippedTool{Name: t.Name, Reason: "the tool rename gave an empty name"}
	}
	return renamed, nil
}
