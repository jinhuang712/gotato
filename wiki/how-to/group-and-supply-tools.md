# Group tools and supply them per Turn

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Group tools and supply them per Turn

By the end of this page your Agent gets tools as a named group, tools that depend on who is asking, and a search of your own. You need [Write a tool](write-a-tool.md).

## Group tools into a ToolSet

A ToolSet is a named group of Tools from one source, such as an MCP server or one internal system. It has a `Spec` (a name and a description) and a `Tools` method that returns its Tools.

```go
// TicketTools is a ToolSet: a named group of Tools from one source.
type TicketTools struct{ Close *CloseTicket }

func (TicketTools) Spec() gotato.ToolSetSpec {
	return gotato.ToolSetSpec{Name: "tickets", Description: "Ticket system tools."}
}

func (s TicketTools) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{s.Close}, nil
}
```

- **A Tool's ID in the Session is `<set>.<tool>`,** here `tickets.close_ticket`. That is the name the model calls, and the name in the Record.
- **Three ways to install it,** by how much the model sees:

  | Option | The model sees | Use it for |
  |---|---|---|
  | `gotato.WithActiveToolSet(set)` | every Tool, always, as if they were base Tools | a few Tools every Run needs |
  | `gotato.WithToolSet(set)` (the default) | the set's description and its Tool names; it loads the ones it needs with `search_tools` | a set of a dozen Tools |
  | `gotato.WithFoldedToolSet(set)` | the description and a count; it lists the Tools, then loads them | a large set, such as a big MCP server |

- **`search_tools` with `toolset` is the dear form.** It lists every tool of a folded set with its description. The list is not capped, and a result under one page is not spilled, so it stays in every later request. `queries` and `names` are the cheap forms. `loaded_tools` in the result is for the wires with native deferred loading and is only stored in the Record on the chat wire.
- **`Tools` runs on `ctx`,** may fail, and must be deterministic. A failure marks the set unavailable and does not stop the Session.
- **A set whose Tools change between Turns** is a `ToolSource`, installed with `gotato.WithToolSource`; see [the extension points](../reference/extension-points.md).
- **For an MCP server** you do not write the set: `adapter/mcp` supplies it. See [Install](install.md).

## Supply tools that change between Turns

A `ToolSet` is a named group with a description and one source; the model loads its tools one by one with `search_tools`. A `ToolSource` is lower level: one method, `Tools(ctx)`, called at the start of each Turn. Reach for a ToolSource when the set of tools depends on who is asking. Through the Runner, set `profile.AgentProfile.ToolSources`:

```go
memory := store.NewMemoryStore()
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{
	Name:        "default",
	Model:       m,
	ToolSources: []gotato.ToolSource{source{}}, // source has Tools(ctx) ([]gotato.Tool, error)
})
runner, err := host.New(host.Config{
	Store:        store.Store{Records: memory, Liveness: memory},
	Profiles:     profiles,
	DefaultAgent: "default",
})
```

A ToolSet is attached with `AgentProfile.ToolSets` (a `profile.ToolSet{Set, Mode}`), not `ToolSources`.

## Search tools your own way

Replaces the built-in BM25 search of `search_tools`. Gotato hands it a query and the specs of the tools that may load; it returns IDs, the first three load and the next five are listed. Install with `gotato.WithToolSearcher`.

## See also

- [How tools reach the model](../../specs/tools.md)
- [Tools, as the user sees them](../../FEATURES.md#f6-tools)
- [Write a tool](write-a-tool.md)
