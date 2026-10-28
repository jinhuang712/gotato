# 039. A Tool is the unit of loading, found through a catalog

Decided: 2026-10-25    Status: current

## The problem
The tool list comes before the Transcript, so every tool definition in it is read on every request, and a change to it makes the provider read everything after it again. A service that connects MCP servers can offer hundreds of tools.

## The choice
A Tool is the unit of loading. The model finds tools through a catalog in the description of one discovery tool, `search_tools`, and loads only what it uses. A ToolSet is a grouping and a source, and a team chooses how much of it the catalog shows.

## Not chosen
- A ToolSet is the unit, switched on whole: few concepts, but a big ToolSet such as one MCP server fills the tool list the moment it is switched on, and a count limit on active ToolSets is a guess.
- Every tool always visible: nothing to discover, but hundreds of definitions in every request and a model that chooses badly among them.

## Consequences
The model loads only what it uses. The catalog is the same bytes while tools load, and a provider's deferred loading keeps the tool list the same bytes too. It costs a search tool to call before the first use, and a fallback for the wire API that has no deferred loading.
