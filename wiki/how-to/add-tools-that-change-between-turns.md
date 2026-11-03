# Add tools that change between Turns

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Add tools that change between Turns

By the end of this page your Agent gets tools that depend on who is asking, or arrive as a group, and finds them its own way. You need the [first page](../tutorials/getting-started.md).

## ToolSet and ToolSource

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

## ToolSearcher

Replaces the built-in BM25 search of `search_tools`. Gotato hands it a query and the specs of the tools that may load; it returns IDs, the first three load and the next five are listed. Install with `gotato.WithToolSearcher`.

## See also

- [How tools reach the model](../../specs/tools.md)
- [Tools, as the user sees them](../../FEATURES.md#f6-tools)
- [Write a tool and a model](write-a-tool-and-model.md)
