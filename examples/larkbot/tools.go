package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/service"
)

// mcpToolSet is the name of the MCP server's ToolSet; its tools are crm.<tool>.
const mcpToolSet = "crm"

// toolKinds are the values of -tools.
var toolKinds = []string{"export", "mcp", "mcp-search"}

// toolSpec is -tools, parsed: what the Agent gets on top of its model.
type toolSpec struct {
	export    bool   // test only: orders.export, a tool whose result of 1 MB the core spills
	mcpActive bool   // the MCP server's tools, always visible (WithActiveToolSet)
	mcpSearch bool   // the MCP server's tools in the catalog, loaded with search_tools (WithToolSet)
	mcpURL    string // the MCP server's streamable HTTP endpoint
}

// parseTools reads -tools, a comma-separated list of toolKinds ("" for none).
func parseTools(list, mcpURL string) (toolSpec, error) {
	var t toolSpec
	for kind := range strings.SplitSeq(list, ",") {
		switch kind = strings.TrimSpace(kind); kind {
		case "":
		case "export":
			t.export = true
		case "mcp":
			t.mcpActive = true
		case "mcp-search":
			t.mcpSearch = true
		default:
			return t, fmt.Errorf("unknown -tools %q (%s)", kind, strings.Join(toolKinds, ", "))
		}
	}
	if t.mcpActive && t.mcpSearch {
		return t, fmt.Errorf("-tools: mcp and mcp-search are two ways to install the same ToolSet")
	}
	t.mcpURL = mcpURL
	if (t.mcpActive || t.mcpSearch) && mcpURL == "" {
		return t, fmt.Errorf("-tools mcp needs -mcp-url")
	}
	return t, nil
}

// apply adds the tools to the Agent. The returned close func ends the MCP connection (nil when there is none).
func (t toolSpec) apply(agent *service.Agent, log *slog.Logger) (func() error, error) {
	if t.export {
		agent.Tools = append(agent.Tools, reftasks.LargeExport())
	}
	if !t.mcpActive && !t.mcpSearch {
		return nil, nil
	}
	set, err := mcp.NewToolSet(mcpToolSet, mcp.StreamableHTTP(t.mcpURL), mcp.WithDescription("The shop's orders, customers, invoices, shipments and stock."))
	if err != nil {
		return nil, err
	}
	logged := loggedToolSet{set, log}
	if t.mcpActive {
		agent.Options = append(agent.Options, gotato.WithActiveToolSet(logged))
	} else {
		agent.Options = append(agent.Options, gotato.WithToolSet(logged))
	}
	return set.Close, nil
}

// loggedToolSet logs why a ToolSet could not be resolved. Gotato skips such a ToolSet for the Run and stores
// only extension_failed, with no cause; the cause is what an operator needs.
type loggedToolSet struct {
	gotato.ToolSet
	log *slog.Logger
}

func (s loggedToolSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	tools, err := s.ToolSet.Tools(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("toolset unavailable", "toolset", s.Spec().Name, "err", err)
	}
	return tools, err
}

// names lists what -tools turned on, for the startup log.
func (t toolSpec) names() []string {
	var out []string
	for kind, on := range map[string]bool{"export": t.export, "mcp": t.mcpActive, "mcp-search": t.mcpSearch} {
		if on {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out
}
