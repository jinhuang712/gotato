# Tools

An agent acts through tools, and a service may have hundreds of them: your own functions, and every tool of every MCP server it connects. Each tool definition is read on every request, and each can do harm. Gotato decides which tools the model sees, loads them one by one so the request stays small and the cache stays warm, and runs each call so that its result is written whole or not at all.

## TL;DR
- A Tool declares an Effect, and the Agent's cap hides every Tool above it, so the model never plans around a tool it may not use.
- A ToolSet is a source; a Tool is the unit of loading. The model finds tools in a catalog and loads only what it uses, and every load is a fact of the Record.
- A call is checked, run and committed in a fixed order. A big result is spilled to the Record and read back in pages.

## What the model is shown

Every tool passes the same gates before it reaches a request; a tool that fails one is not hidden by a refusal later, it is simply absent.

```text
 WithTools, ToolSource ───────────────────────────┐
 ToolSet, installed active ──┐                    ├─▶ Effect cap ─▶ visible tools ─▶ request
 ToolSet, listed or folded ──┴─▶ Effect cap ─▶ filter ─▶ index of search_tools
                                                         (loaded ones join the visible tools)

 Effect:  read  <  write_local  <  write_external  <  destructive      (no Effect counts as destructive)
```

An Effect says what a tool changes. The Agent names the highest Effect its tools may have, and a Session can lower what it offers with a filter but can never raise the cap. A tool above the cap is in no request, no inspected Context and no search index. A model that calls its name anyway gets a failed result that says the Session does not allow it, and the Run goes on. `search_tools` and `read_result` declare `read`, so every cap keeps them. Watch the default: with no cap, every tool runs.

## The catalog and the three installs

A ToolSet is installed in one of three ways, and the mode sets how much of it the model reads.

```text
 install            description of search_tools shows              tools in the request
 active             (nothing)                                       all of them, always
 listed (default)   crm — Customer records                          only the loaded ones
                      crm.find, crm.update, crm.export
 folded             crm — Customer records (214 tools; pass         only the loaded ones
                      toolset="crm" to list them)
```

The catalog is the description of the one discovery tool, `search_tools`. It lists a loaded tool like any other, so its bytes do not change while tools load. A team folds a big ToolSet itself, since the catalog has no size budget. ToolSets resolve at the start of every Run, never inside one, so the catalog is the same on every request of a Run. A ToolSet that fails is shown as unavailable and skipped; the Run goes on.

## A load

A tool becomes visible at the end of the batch that asked for it, and the Record says so in the same write.

```text
 Model                    Loop                         Record
   │ search_tools(names|queries|toolset)                    │
   │ ───────────────────▶ │ stage the load (top 3 of a query load,
   │                      │   next 5 are listed only; a name loads exactly)
   │                      │ result: "Loaded: crm.find ..."  │
   │                      │ ── Tool Result + tool_loaded ─▶ │  one write
   │                      │ batch ends: the tool is visible │
   │ next Turn sees crm.find in its tools                   │
   │ crm.export called while not loaded: the call does NOT run,
   │   it is loaded now, the result says "call it again"    │
```

Search is BM25 over each tool's ID and description, and a team can plug in its own `ToolSearcher`. A load is a fact (`tool_loaded`), so any Instance that loads the Record offers the same tools, and a Fork carries them. A loaded tool stays until a compaction, which keeps the tools called in the Segment it closes or in the kept tail and unloads the rest (`tool_unloaded`). A loaded tool its ToolSet no longer carries is unloaded with `why: gone`, once, by the Instance that ran.

## Loading and the cache

The tool list comes before the Transcript, so a change to it makes the provider read everything after it again.

```text
 list form (chat completions)          native form (Responses, Anthropic)
 ┌ base tools ──────────┐              ┌ base tools ──────────┐   same bytes
 │ search_tools         │              │ search_tools         │   on every
 │ crm.find   (loaded)  │ ◀ grows      │ crm.export  deferred │   request
 └──────────────────────┘ at a load    │ crm.find    deferred │
                                       │ crm.update  deferred │
                                       └──────────────────────┘
                                       a load = a reference in the Tool Result
```

In the list form a load adds a tool, so the next request pays one cache read after the tools. In the native form every catalog tool is sent deferred, in ID order, whether loaded or not, and a load only shows up as a reference in the history. The core stays wire-agnostic: it hands the gateway the visible tools, the loaded IDs and the tools still to load, and the gateway lays them out. A loaded tool that no Tool Result references any more, because a compaction folded the result away, is sent plain, once, at that compaction.

## MCP as a ToolSet

One MCP server is one ToolSet, and its tools are `<server>.<tool>`.

```text
 Sessions of this Instance           mcp.ToolSet                      MCP server
   resolve (start of a Run) ───────▶ list tools  ───────────────────▶ tools/list
   ◀── Tools, Effect from annotations                                       │
   call crm.find (Run's ctx) ──────▶ tools/call + progress token ───▶       │
   ◀── Tool Result, progress as tool_progress ◀─────────────────────────────┘
        one shared connection; 404 on the session = reconnect, send the call once more
```

The adapter is a Go module of its own, so a service without MCP carries none of it. It connects at the first resolution and shares that one connection among all Sessions, so no state of the server belongs to a Session and any Instance can make any call. Effect comes from the annotations, and a tool with none is destructive. The server's own text never reaches the model as an error: a failed call says only that the server did not take it. A tool that asks for input in the middle of a call gets no answer, because there is no person to ask.

## One call, start to commit

Calls are checked in source order, run in groups, and committed in source order.

```text
 preflight (source order)      run                        commit (source order)
 1 find the tool               5 ToolStarted, flush       8 Post-tool extensions
 2 arguments valid JSON        6 run the group; a            9 spill if the text is over a page
 3 arguments fit the schema      Sequential tool is alone;  10 Tool Result Message
 4 Pre-tool extension may        the rest run in parallel   11 tool_result_committed (+ tool_loaded)
   block                         up to a bound; ToolEnded      in one flush
 a failure at 1-4 = a failed     as each ends               loads become visible at the batch end
 result the model can read
```

A call the model got wrong, whether an unknown tool, bad JSON or arguments that fail the schema, becomes a failed result with the reason, and the next Turn can correct it. A tool's own error becomes "tool failed" with a code, unless the error is a `PublicError`, since a foreign error may carry a host or a secret. A panic is a failed call, not a dead process. On cancel, a call that had started is recorded as canceled with `executed: true`, because it may have done its work; one that had not is never launched. Every call gets a result, even when the Run ends first, because a provider refuses a request that has a call without one.

## A big result

A result over one page is written to the Record as an Entry of its own, and the Context keeps a preview.

```text
 Tool returns 412 KiB ─▶ Record:  tool_result_spilled (full body)   ◀── written first
                      └▶ Message: preview (8,000 bytes) + [Result cut: 13 pages, read_result(ref="result:call_9a2", page=1)]
 Turn N+1: model ─ read_result(ref, page=2) ─▶ one page (32 KiB) of the body, "More: ... page=3"
```

The body is written before the Message that points to it, so a crash between the two leaves an Entry nobody points to and nothing worse. The Message is small and written once, so the bytes already sent do not change, and a load does not read the body until it is asked for. Only text is spilled; an image stays in the Message. A Session spills only over a Record that keeps Entries, because the body needs somewhere to be read from.

## What this buys you
- The model never sees, plans around or searches its way to a tool above the cap.
- Hundreds of tools cost a catalog, not hundreds of definitions, and a load does not break the cache where the provider can defer tools.
- Any Instance rebuilds the same tool list and the same history from the Record.

## See also
[FEATURES.md → G-F09](../FEATURES.md#g-f09--tools-and-their-effect-), [G-F10](../FEATURES.md#g-f10--tool-sets-), [G-F34](../FEATURES.md#g-f34--tool-search-), [G-F12](../FEATURES.md#g-f12--the-mcp-adapter-) and [G-F35](../FEATURES.md#g-f35--large-tool-results-) for defaults and status, decisions [005](../plan/decisions/005-no-approval-system.md), [007](../plan/decisions/007-the-effect-cap-hides-tools-above-it.md), [008](../plan/decisions/008-only-the-agent-sets-the-effect-cap.md), [024](../plan/decisions/024-tools-load-one-by-one-from-a-catalog.md) [025](../plan/decisions/025-a-large-tool-result-is-spilled-and-read-in-pages.md) and [026](../plan/decisions/026-a-read-only-bash-for-large-results-is-another-project.md), [GLOSSARY.md](../GLOSSARY.md). Related specs: [record.md](record.md) and [context.md](context.md).
