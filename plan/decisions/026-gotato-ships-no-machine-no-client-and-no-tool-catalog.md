# 026. Gotato ships no machine, no command-running tool, no tool catalog and no client

Decided: 2026-10-16    Status: current

## The problem
Gotato is built for agents whose work needs tools and data, not a computer ([PHILOSOPHY.md → 2](../../PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)). A shell, a marketplace or a front end would each turn it toward a cloud coding agent or an application.

## The choice
A Session has no shell, sandbox or filesystem, and no built-in tool runs commands, code or a browser. Tools are Go functions or MCP tools, which the team chooses and writes: general-purpose tools come from MCP servers and from the application. An MCP server is one ToolSet, brought in by `adapter/mcp`, a module of its own on the official Go SDK, so a service that uses no MCP carries none of it; the adapter maps a server's tools to Tools, takes their Effect from the server's annotations and builds none of the protocol's other features ([FEATURES.md → G-N20](../../FEATURES.md#g-n20--mcp-features-gotato-does-not-build)). Gotato has no clients and no user interface: callers write their own against the HTTP handler and the Go API.

## Not chosen
- A shell, a sandbox or a filesystem for each Session: work that needs a machine runs on a laptop, in CI or in a coding product.
- Built-in tools that run commands, code or a browser: Gotato is not a cloud coding agent, and a tool that runs anything is a bound nobody can read.
- A tool catalog or marketplace that ships with Gotato: the team chooses and writes its tools.
- Bundled bots, schedulers, front ends or terminal UIs: callers write their own.

## Consequences
Gotato stays small and its bounds stay readable. A query tool over a large result is another project ([041](041-a-read-only-bash-for-large-results-is-another-project.md)).
