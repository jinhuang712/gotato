package gateway

import (
	"sort"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// The native form of deferred tools (Config.DeferredTools). The core hands a
// wire API the visible tools (ModelRequest.Tools), which of them were loaded
// (LoadedTools) and the catalog tools still to load (UnloadedTools). In the
// list form an adapter sends Tools and ignores the rest, so its tool list grows
// when a tool loads. In the native form it sends:
//
//   - the tools that are not in the catalog, first, in ID order;
//   - then every catalog tool, loaded or not, deferred, in ID order.
//
// Whether a tool is loaded never moves it, so the tool list is the same bytes
// on every Turn of a Session while tools load, and a prompt cache that covers
// the tool list survives. A load shows as a reference to the tool in the Tool
// Result that made it (ToolResult.Loaded), which the history carries from
// then on, in the wire API's own form.
//
// A loaded tool that no Tool Result in the history references any more (its
// result was compacted away while the tool stayed loaded) is sent as a plain
// tool in the first group: nothing in the history would otherwise show it to
// the model. That changes the tool list, once, at a compaction, which has
// changed the history already.

// deferredLayout is the native form of one request's tools.
type deferredLayout struct {
	// tools are the tools in the order they are sent.
	tools []layoutTool
	// deferred holds the IDs of the deferred tools, the ones a reference may
	// name.
	deferred map[string]bool
	// searchCalls holds the IDs of the calls to the search tool in the
	// history.
	searchCalls map[string]bool
}

type layoutTool struct {
	spec     gotato.ToolSpec
	deferred bool
}

// isSearchToolID reports whether a Tool ID is the search tool of a Session,
// whatever namespace its root carries.
func isSearchToolID(id string) bool {
	return id == gotato.SearchToolName || strings.HasSuffix(id, "."+gotato.SearchToolName)
}

// newDeferredLayout lays the request's tools out for the native form. It
// returns nil when the form is off or the Session has no catalog, and the
// adapter then sends the plain list.
func newDeferredLayout(request gotato.ModelRequest, rules providerRules) *deferredLayout {
	if !rules.deferredNative || (len(request.LoadedTools) == 0 && len(request.UnloadedTools) == 0) {
		return nil
	}
	loaded := make(map[string]bool, len(request.LoadedTools))
	for _, id := range request.LoadedTools {
		loaded[id] = true
	}
	referenced := map[string]bool{}
	searchCalls := map[string]bool{}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if isSearchToolID(call.ToolID) {
				searchCalls[string(call.ID)] = true
			}
		}
		if message.ToolResult != nil {
			for _, id := range message.ToolResult.Loaded {
				referenced[id] = true
			}
		}
	}
	var plain, deferred []layoutTool
	for _, spec := range request.Tools {
		if loaded[spec.ID] && referenced[spec.ID] {
			deferred = append(deferred, layoutTool{spec: spec, deferred: true})
			continue
		}
		plain = append(plain, layoutTool{spec: spec})
	}
	for _, spec := range request.UnloadedTools {
		deferred = append(deferred, layoutTool{spec: spec, deferred: true})
	}
	byID := func(list []layoutTool) {
		sort.SliceStable(list, func(i, j int) bool { return list[i].spec.ID < list[j].spec.ID })
	}
	byID(plain)
	byID(deferred)
	layout := &deferredLayout{deferred: make(map[string]bool, len(deferred)), searchCalls: searchCalls}
	for _, tool := range deferred {
		layout.deferred[tool.spec.ID] = true
	}
	layout.tools = append(plain, deferred...)
	return layout
}

// references returns the IDs of a Tool Result's loads that the request sends
// deferred, in the order the result lists them.
func (l *deferredLayout) references(result *gotato.ToolResult) []string {
	if l == nil || result == nil {
		return nil
	}
	var ids []string
	for _, id := range result.Loaded {
		if l.deferred[id] {
			ids = append(ids, id)
		}
	}
	return ids
}
