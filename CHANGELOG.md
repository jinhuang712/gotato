# Changelog

What changed in Gotato, from the user's side. Newest first. A breaking change carries a before/after note, so you can move your code in one pass. Additive changes may be listed, but breaking ones must be.

## Unreleased

### Breaking

- **A Tool of your own cannot take a name under a ToolSet's namespace.** The ID and the `Name` of a Tool from `WithTools` or a `ToolSource` may not start with the name of an installed ToolSet and a dot. A dotted name outside every namespace, like `payments.get_batch`, is still yours.
  - Before: `WithTools` with a Tool `files.read` and a ToolSet `files` started, then failed every Run once the ToolSet, installed active, carried `read`; a root Tool with `Name: "files.read"` ran in place of the ToolSet's `files.read` when the Model called it.
  - After: `NewSession` fails with `invalid_argument`; a `ToolSource` that supplies such a Tool keeps its last set and stores `extension_failed` (stage `toolsource`), and the Run goes on. Rename the Tool, for example `local_read`.
- **A service Agent whose tools repeat a Tool ID is refused.** `Agent.Tools`, `Agent.ToolSources`, `read_result` and `read_segment` are one tool source to the Session, and a repeated ID fails it like any bad Tool.
  - Before: the first Tool with the ID was kept and the other was dropped without a word, so an Agent's own `read_result` hid the built-in one.
  - After: a Run of the Agent fails with `invalid_argument` (or, when a `ToolSource` starts repeating an ID mid-Session, the Session keeps its last set and stores `extension_failed`, stage `toolsource`), and `Runner.Tools` fails with `invalid_argument`. Give each Tool its own ID.
- **`search_tools` is a reserved Tool name.** A Tool of your own may not have it as its ID or its `Name`.
  - Before: a root Tool `search_tools` worked while no ToolSet offered a catalog, then failed every Run once one did.
  - After: `NewSession` fails with `invalid_argument`; a `ToolSource` that supplies one keeps its last set, as above.

### Fixed

- `session.Writer.Write` numbers and stores its Events in one step. Before, a `Flush` from another goroutine could write them in between, and `Write` returned no Events and no error; now it always returns the Events it was given, numbered and stored.
- `adapter/mcp`: with `WithListCache`, a tool listing that fails (a canceled context, a server error) no longer loses the server's `list_changed`, so the next resolution lists again instead of serving the old list for the whole TTL. A slow listing that began before a change no longer replaces a list read after it, and a new connection to the server makes the kept list stale, since a change while no connection was open sent no `list_changed`.
- `modelctx`: a compaction in the middle of a Run keeps the Run's last reply and everything after it. Before, a notice or a steer committed after a Turn's tool results was taken as the start of the Turn, so the next model call saw none of those results; after an interrupt the cut-off reply was closed into the Segment and its text lost; and on a Run's first Turn a prompt followed by a notice could be cut into the Segment. Now the prompt is kept whole, and a prompt past the ceiling ends the Run with `limit_exceeded`.
- `modelctx`: a Turn counts as compacted only when a compaction or a merge started in it. Before, a merge at a Run's start that found nothing to merge used up the Turn's compaction, so an overflow after it ended the Run with `context_overflow` instead of compacting and sending the call again. A compaction that started and was dropped still counts.

### Changed

- **A Tool's `Spec` is read once, when the Tool is resolved.** The Session keeps it: a root Tool's from `NewSession`, a `ToolSource`'s Tools' from each Turn, a `ToolSet`'s Tools' from each resolution. A Tool whose `Spec` panics, or a typed nil, now fails its whole source like any other bad Tool, instead of the process.
  - Before: `Spec` was read again on every rebuild of the Tool list, so a Spec that changed showed at once, and a Spec that panicked ended the process.
  - After: a Spec that changes shows at the next resolution of its source. Return a stable value, and supply Tools that change through a `ToolSource` or a `ToolSet`.
- **`ToolSource`s are asked at once, with no lock held, and `WithToolFilter` is asked when the Tools are resolved.** A slow `ToolSource` no longer holds `Session.Tools` or `Session.ToolSets`.
  - Before: the ToolSources were asked one after another under the registry's locks; the filter ran on every rebuild of the Tool list, under the locks, and a filter that panicked ended the process.
  - After: the filter is asked at `NewSession`, at the start of every Run and at every Turn of a Session with a `ToolSource`; a change to its answer shows at the next of them. A filter that panics fails the Run (or `NewSession`) with `extension_failure`.

## 0.2.0 — 2026-10-29

Gotato 0.2.0 is the first release: a minimal, synchronous, Go-native agent runtime that runs on its own in a small `main` and embeds into running, multi-instance Go services. It needs Go 1.24 (the MCP adapter needs Go 1.25).

The first release is 0.2.0 because versions 0.1.0 and 0.1.1 of the module path were published by mistake before it, hold other code, and are retracted.

**In this release**

- **What ships:** the core and the packages around it, and one adapter module.
- **What it promises:** from this release on, a break is written down.
- **What was never checked:** everything that only a real provider or a real model can show.

### What ships

| Package | What it is |
|---|---|
| `gotato` | The core: Agent, Session, Tool, Model, Event. Standard library only; it runs by itself, with the conversation in memory. |
| `session`, `session/storetest` | The Record and its Store (memory and file), and the suite that proves a Store adapter keeps the store contract. |
| `modelctx` | The ContextBuilder and compaction: the model receives a built, bounded view, not the full history. |
| `gateway` | A provider is YAML, not code: the OpenAI Chat Completions, OpenAI Responses and Anthropic Messages wire APIs. |
| `service`, `service/httpapi` | The Runner that runs Sessions on many instances over one Store, and its HTTP and streaming surface under `/v1/sessions`. |
| `testkit` | Fake and replay models and tools, and Event recorders, for deterministic tests. |
| `adapter/mcp` | A module of its own: an MCP server's tools as a `ToolSet`. Tagged `adapter/mcp/v0.2.0`. |

The test CLI (`internal/cmd/gotato`), the workload tools, `examples/larkbot` and `bench/refservice` are in the repository but are not part of the release's promise. The CLI is our test tool and keeps no contract.

### What it promises from now on

- A breaking change to the Go API, the HTTP contract or the Event kinds has a CHANGELOG entry with a before and after.
- Where the cost is low, the old symbol stays for at least one release with a `// Deprecated:` comment.

### What was never checked against a real provider

Gotato runs no tests against real providers, and CI has no provider credentials. A provider's behavior is played by a fake gateway (`internal/fakegateway`) built from the providers' public wire formats. One live run exists (2026-10-16, one model, before later changes, G-F22); it was not repeated for this release. The list below is for a team to check on its own gateway.

- **The wire APIs ([G-F07](FEATURES.md#g-f07--models-and-providers-)).** `openai-chat-completions`, `openai-responses` and `anthropic-messages` are tested on request bytes and against the fake gateway, which refuses a request shaped otherwise than the documentation. Its quirks (a refusal as too long, a cut stream, cached tokens, a reasoning replay, a mid-conversation message, a slow reply) follow the documented shapes, not a provider's live answers.
- **The wording of a refusal as too long ([G-F07](FEATURES.md#g-f07--models-and-providers-), TODO "Do the providers word a refusal as too long as the fake gateway does?").** The wordings are the ones the providers are reported to return, and the public documentation quotes none. A team adds another with `overflow_phrases`.
- **Native tool search ([G-F34](FEATURES.md#g-f34--tool-search-)).** How a provider expands a `tool_reference`, and whether it takes a `cache_control` mark on a tool list that ends in deferred tools. A team whose gateway refuses `native` sets `list`.
- **Runtime notices ([G-F29](FEATURES.md#g-f29--runtime-notices-)).** Whether a provider answers a user turn that holds only a notice.
- **Compaction ([G-F06](FEATURES.md#g-f06--compaction-)).** How a model reads a `<tool_calls>` block between a user message and the reply; what Claude and OpenAI need of the reasoning in a tail kept mid-Run; whether Claude reads the Segments from the cache with Gotato's marks; whether `tool_choice: none` keeps the cache on Anthropic; how long a model-written Recap takes against `ModelCallDeadline`.
- **The Recap's quality ([G-E01](plan/GOALS.md#g-e01--the-recap-keeps-the-facts-), not measured).** The payments check ships and a team runs it on its own gateway: `go run ./internal/cmd/gotato-workload -recap-check -live -gateway-config gateway.yaml`. No gateway has run it yet, so the model-written Recap is off by default.
- **Cache and cost numbers.** [G-B14](plan/GOALS.md#g-b14--context-size-and-cost-per-turn-by-the-sessions-age-) (cost per Turn by the Session's age), [G-B15](plan/GOALS.md#g-b15--the-first-request-after-an-idle-return-) (the first request after an idle return, not measured) and [G-B16](plan/GOALS.md#g-b16--cost-of-a-compaction) (the cost and time of a compaction) need a provider's cache and prices; the sizes and tokens come from a fake model. G-B01 (cache hit rate) has its number from the one live run only.
- **MCP servers ([G-F12](FEATURES.md#g-f12--the-mcp-adapter-)).** The tests use the MCP SDK's own server; no other server was tried, and none of the specification's multi round-trip revision.
- **Time to a first agent (G-B13).** Not measured; it needs developers new to Gotato.
