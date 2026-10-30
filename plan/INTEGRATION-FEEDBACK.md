# Integration feedback: first real adoption (a Lark bot service running on several pods)

Source: the downstream team's append-only log (entries `D-001` to `D-012`; the team calls them G-NNN, renamed here so they do not clash with GOALS.md goals). Each entry was checked against this repository on 2026-10-29. The log is still growing: add new entries below, do not rewrite old ones.

Verdict: **confirmed** (the claim holds), **partly** (part of it holds, see the note), **wrong** (the repository already covers it).

Remove an item from *Backlog* in the commit that resolves it, as in [TODO.md](TODO.md).

## Backlog

### P0


### P1

- [ ] **D-004 · An MCP server without annotations hides every tool under `WithMaxEffect`.** Verdict: confirmed (docs only; the core's `WithMaxEffect` code was not read). `WithToolEffect` matches the exact MCP name only. Fix: add `WithDefaultEffect(ToolEffect)` for tools with no annotations, or `WithToolEffectFunc`; add the consequence to the MCP section of FEATURES.md.
- [ ] **D-002 · `adapter/mcp` cannot filter or rename tools.** Verdict: confirmed. The only cleaning is empty and repeated names, recorded in `Skipped()`. Fix: `WithToolFilter(func(*sdk.Tool) bool)` (rejected tools go to `Skipped` as "filtered") and `WithToolRename(func(string) string)`; add both to the options table.
- [ ] **D-006 · A streamed card across pods needs the card's id, and nothing says so.** Verdict: partly. Deltas are sent, not stored; a slow subscriber skips only deltas; `StreamSession` on another instance sees no deltas. Wrong in the log: the card is updated by the writer's own `SubscribeInstance`, so the *message* id need not be shared; what a writer switch needs is the **card's** own id. `examples/larkbot` stores only the thread root. Fix: one sentence in FEATURES.md "deltas are visible only on the writer Instance"; a streaming card in the example (buffer by RunID, throttle about 500 ms, store the card id in Session metadata, final text from `RunEnded` when `events_skipped` fires).
- [ ] **D-007 · `larkbot` does not de-duplicate across pods.** Verdict: confirmed. Wrong in the log: `examples/larkbot/README.md` and a test already call it a known gap; the two guides do not mention it. Fix: copy the README note into `guides/example-chat-bot.md` and `guides/embed-in-a-service.md`; pass Lark's `message_id` as `MessageRequest.RequestID` so a repeat can be found in `run_started`. Gotato itself does not de-duplicate `RequestID` in 0.2.0 (roadmap item).

### P2

- [ ] **D-010 · Reasoning effort cannot be set through a self-hosted gateway on chat/anthropic wires.** Verdict: partly. `refuseReasoningConfig` rejects it on both; only Responses sends it. Wrong in the log: an unrecognised host already returns every reasoning artifact, so no `provider: deepseek` is needed. Fix: document how to set `provider` for a LiteLLM host (cache TTL optimizations need it); consider a pass-through extra body field.
- [ ] **D-005 · No cost field or price table.** Verdict: confirmed, and a gap in the docs, not a stated non-goal. Fix: say in FEATURES.md that cost is the team's own conversion of `Usage`; add a worked example. No core change.

### New since the first pass


- [ ] **D-018 · A folded tool called before it is loaded costs the model a round.** Verdict: partly. Measured downstream in a staging run of 16 rounds: 2 of 36 tool calls hit `X was not loaded, so this call did not run. It is loaded now; call it again.` The message and the counts are documented (`search_tools` loads the top 3 and lists 5 more; both fixed; only `WithToolSearcher` changes the ranking). Wrong in the log: `search_tools` already tells loaded from listed (`Loaded: a, b.` and `Also found, not loaded: c, d.`); the model calls the listed names anyway. No decision records why a call is not run on the spot; a likely reason (a guess) is that a load takes effect with the Tool Result commit and the tool list must stay byte-stable for the cache. Their relief, `WithActiveToolSet`, is documented. Fix, one of: reword the listed line as an instruction ("not loaded yet: call `search_tools` with these names first"); for read-only tools, load and run on the refusal (changes the commit contract, needs a decision); add the `WithActiveToolSet` versus fold trade-off to FEATURES.md.
- [ ] **D-019 · No prompt-cache gain through a self-hosted gateway on the chat wire.** Verdict: mostly confirmed. Measured downstream: 16 rounds, 436k input tokens, no `cache_read_tokens` or `cache_write_tokens` in `usage`. The chat wire sends no `cache_control`; only `cache_key_field` and `cache_key_header` exist. `provider: anthropic` on the chat wire does **not** add `cache_control` (it only sets `anthropicCacheTTL` on the `anthropic-messages` wire). Usage parsing on chat reads `prompt_tokens_details.cached_tokens` and DeepSeek's `prompt_cache_hit_tokens`; it does not read the top-level `cache_read_input_tokens` or `cache_creation_input_tokens`, and `CacheWriteTokens` is never filled. Both fields are `omitempty`, so a missing field cannot tell "no hit" from "not reported" from "never cached". Whether the gateway in between injects `cache_control` cannot be checked here. Related to D-010. Fix: a FEATURES.md section on caching through a self-hosted gateway (inject at the gateway, or use the `anthropic-messages` wire with `provider: anthropic`); read the Anthropic-style usage fields on chat; sending `cache_control` on chat is a feature and needs a decision first.

- [ ] **D-020 · No warning that compaction is off when the context window is unknown.** Verdict: confirmed, overstated. A window of 0 means unknown, the gateway keeps no table of windows, and with no window and no `CeilingTokens` the ceiling trigger is off (code and FEATURES.md agree). No log, Event or start-up check says so; `inspect` drops the ceiling field when unknown; `gateway.example.yaml` fills in `context_window_tokens`, which reads as a required key. Wrong in the log: history does not grow until it fails; a refusal as too long still compacts once and retries, if `OverflowPhrases` recognises the wording (the built-in list is short on purpose, 400 and 413 only). Decision 038 argues against a conservative default ceiling ("a number that depends on the model"). Fix: warn once when the window is 0; show `ceiling=0` in `inspect`; an optional way to read the window from a gateway's model info (does not exist today); a loud line in FEATURES.md and the example config that no window means compaction only after a refusal.
- [ ] **D-021 · Parallel tools: a default that is easy to misread, and a hazard.** Verdict: partly. `MaxParallelTools` defaults to 1. Wrong in the log: at 1, `Sequential` changes nothing; it only matters above 1. Not in the log: `adapter/mcp` tools are not `Sequential`, so raising `MaxParallelTools` runs the server's **write** tools in parallel too, and grouping ignores `Effect`. The measured gain was small (about 7.6 s of 161.7 s). Fix: a FEATURES.md example (`MaxParallelTools: N`, write tools `Sequential`); say in the MCP section that adapter tools are not `Sequential`; an optional adapter switch that makes non-read tools `Sequential` (not a new default).
- [ ] **D-022 · No guide for asking a person before a tool runs.** Verdict: partly, the details hold. Decision 005 says no built-in approval and points to `PreToolUse` or a tool that hands the step to a person. `ToolUse` has `RunID`, `Turn`, `CallID`, `ToolID`, `Arguments`, `SourceIndex`, `Effect`, no Session id. `Before` runs for every call of the Turn before any executor starts. A wait inside `Before` is bounded by the Run's context and `RunTimeout`, not `ToolCallDeadline`. A `Before` that returns an error ends the Run (`CodeExtensionFailure`); to fail closed without that, return `Block:true`. A blocked call has `tool_ended` and no `tool_started` (kept on purpose: `tool_started` is a fact used on recovery). Fix: a "needs a person" section in `guides/extension-points.md` with those limits; add `SessionID` to `ToolUse` (new field, compatible). Keep the missing `tool_started`; tell blocked from failed through D-026.
- [ ] **D-025 · Tool arguments are base64 in a Record.** Verdict: partly. `ToolUse.Arguments` and `ToolCall.Arguments` are `[]byte`, so JSON shows base64; no spec fixes that form. Changing to `json.RawMessage` is a break: arguments that are not valid JSON are stored on purpose (preflight checks `json.Valid`) and `RawMessage` would fail to commit them, and old Records hold base64 strings that would read as a JSON string. Fix without a break: keep `[]byte`; add an optional `arguments` JSON field (or a short summary) to `tool_started`; say in FEATURES.md that arguments are bytes, base64 in JSON.
- [ ] **D-026 · A failure's reason is only readable from text.** Verdict: partly. Blocked is already apart (`status: blocked`), and `Executed` tells "did not run" from "ran". `ToolResult` has no code; the reason is a `SafeError` prefix (`tool_resolution_failure:`, `tool_argument_failure:`, `tool_execution_failure:`, `deadline_exceeded:`), and a call to an unloaded tool is told by its `LoadedTools` and text. `tool_ended` does not carry `SafeError`, so a subscriber must read `tool_result_committed`. Fix: add `Code` (existing codes plus one for "not loaded") to `ToolResult` and `ToolEnded`, set where `rejectedPlan` builds the result. New field, compatible. Pairs with D-018.
- [ ] **D-024 · No default cap on Turns, tool calls or a Run's time in the core.** Verdict: confirmed, and by design (decision 038: a cap exists only for a named failure; one number does not fit a lookup and a long investigation). The 30-minute `RunTimeout` is in `service.Agent`, not in `CoreLimits`. `TurnStopper` is `Stop(ctx, TurnSnapshot) (StopDecision, error)` and `StopDecision` can only stop, not tell the model to wrap up. Fix, without a default: an optional helper `MaxTurns(n, ...)` and `MaxToolCalls(n)` built as a `TurnStopper`; "wrap up" before the cap needs a Notice, and whether a `TurnStopper` can send one was not checked.

### P3 (docs)


## Corrections to send back to the downstream log

New entries that cite the old ones; do not edit them.

| Entry | Correction |
|---|---|
| D-006 | The card id, not the message id, is what a writer switch needs. |
| D-007 | The README documents the gap; the guides do not. |
| D-009 | Docs warn and the default is safe; the example's prefix check is the weak point. |
| D-010 | An unrecognised host returns reasoning by default; no `provider` needed. |
| D-006 | A streamed card needs `StreamRun`, not `SubscribeInstance` (see D-013). |
| D-018 | `search_tools` already separates loaded from listed names. |
| D-020 | A refusal as too long still compacts once; the gap is the missing warning. |
| D-021 | Raising `MaxParallelTools` also runs adapter write tools in parallel; mark them `Sequential` first. |
| D-011 | `install.md` already states the Go 1.25 need for the adapter. |

## Open checks for the downstream team

- D-004: does their MCP gateway send annotations? (They planned to test it in a staging environment.) That decides whether D-004 hurts them at all.
- D-014: their Store now passes the suite on a single node; failover retry and concurrent writers to one Session are still unchecked.
