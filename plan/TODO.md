# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](../FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`Feed` has plumbing that is exported.** `Published`, `PublishLabeled`, `SetLabel`, `MirrorTo` and `NewInstanceFeed` are exported because `service` and `testkit` call them from other packages. Moving them behind a smaller exported surface changes the `service`/`gotato` split, so it needs a design, not a rename.

## Integration feedback

Findings from real use, each checked against the code (first pass on 2026-10-29). Evidence, verdicts and wording of the fixes are in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md); `D-NNN` is its entry number. Remove an item here in the commit that resolves it.

- [ ] **D-004 · An MCP server without annotations hides every tool under `WithMaxEffect`.** Add `WithDefaultEffect` or `WithToolEffectFunc`; state the consequence in FEATURES.md.
- [ ] **D-002 · `adapter/mcp` cannot filter or rename tools.** Add `WithToolFilter` and `WithToolRename`.
- [ ] **D-006 · `examples/larkbot` has no streaming card, and nothing says deltas are visible only on the writer Instance.** Add both; a writer switch needs the card's id in Session metadata.
- [ ] **D-007 · `larkbot` does not de-duplicate across pods.** Copy the README note into the two guides; pass `message_id` as `RequestID`.
- [ ] **D-018 · A folded tool called before it is loaded costs a round.** Reword the "also found, not loaded" line as an instruction; decide whether a read-only tool is loaded and run on the refusal; document `WithActiveToolSet` against fold.
- [ ] **D-019 · No cache gain on the chat wire on an endpoint that is not a provider's own address.** Document how to get one (add it at the endpoint, or `anthropic-messages` with `provider: anthropic`); parse `cache_read_input_tokens` and `cache_creation_input_tokens` on chat.
- [ ] **D-020 · Nothing warns that compaction is off when the context window is unknown.** Warn once at 0, show `ceiling=0` in `inspect`, an optional read of the window from a gateway, a loud line in FEATURES.md and the example config.
- [ ] **D-021 · `adapter/mcp` tools are not `Sequential`; raising `MaxParallelTools` runs write tools in parallel.** Document it with an example; an optional adapter switch.
- [ ] **D-022 · No guide for asking a person before a tool runs.** Add one to `guides/extension-points.md` with the limits of `Before`; add `SessionID` to `ToolUse`.
- [ ] **D-024 · No helper for a Turn or tool-call cap.** Add `MaxTurns` and `MaxToolCalls` as `TurnStopper` helpers; check whether a `TurnStopper` can send a Notice first.
- [ ] **D-025 · Tool arguments are base64 in a Record.** Keep `[]byte`; add an optional `arguments` JSON field to `tool_started`; document the form.
- [ ] **D-026 · A failure's reason is only readable from text.** Add `Code` to `ToolResult` and `ToolEnded`.
- [ ] **D-027 · The time of a Run is the model's (81% to 99%), not the tools'.** Measured on 8 Runs of a real service built on Gotato. Say in FEATURES.md that parallel tools save about 5% and a cache is a matter of cost; reword the "not loaded" line first (D-018); reopen the Turn cap decision (D-024).
- [ ] **D-029 · A name prefix is a guess about Effect.** Say so in the MCP section of FEATURES.md; recommend `destructive` by default plus an exact list; use an endpoint that shows several servers as a test of filter and rename.
- [ ] **D-030 · Retrying a `NotRun` after a back-off is a plain answer to `not_writer` for one replica.** One paragraph and its limit in the multi-instance guide.
- [ ] **D-032 · A tool cannot learn who asked, except through a `ctx` value set before `Runner.Run` (works, undocumented).** Document the route and pin it with a test; decide whether a per-Run field should reach `ToolUse`.
- [ ] **D-033 · Follow-up to D-027.** Input length does add about +2 s of first-token time over 44k tokens, so D-019 stays a latency item until measured.
- [ ] **D-034 · A long Run never compacted mid-Run because the model window was left unset.** Confirmed. No core change; it is another reason for D-020's warning.
- [ ] **D-035 · `compaction_ended` carries no ceiling or window; `context_built` has no per-part size.** Add the ceiling and window first, then an estimate per part. Compatible.
- [ ] **D-036 · Decide whether to clear old Tool Results in the middle of a Run.** Write the decision first; check whether the compaction ceiling already answers it.
- [ ] **D-037 · `search_tools` with `toolset` returns a whole ToolSet (up to 30 KB measured), uncapped and not spilled.** By design; say so in FEATURES.md and the tool-search guide, and name `queries` and `names` as the cheap forms. `loaded_tools` in the result is Record-only on the chat wire.

## Decisions

- [ ] **Model profile: describe a model apart from its connection, with the window stated (draft [042](decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md)).** Answers D-020 and D-034 at the root; no pool, no routing.
- [ ] **D-005 · Cost: documentation only, or a hook?** `Usage` has no price. Either state in FEATURES.md that a team converts `Usage` itself (with an example), or add a price table or an `Event` hook.
- [ ] **D-019 · Should the chat wire send `cache_control`?** Today it sends none, and `provider: anthropic` does not change that on chat. Either keep it a gateway matter and document it, or add the field.
- [ ] **D-010 · Reasoning effort on an endpoint that is not a provider's own address.** The chat and anthropic wires refuse it. Either document `provider` for such an endpoint, or add a pass-through extra body field.

## Open checks

Gotato runs no checks against real providers: a provider's behavior is played by fake gateways, and what only a real model shows is for a team to check on its own gateway.

- [ ] **Does a user who comes back after a compaction find the conversation they left?** GOALS.md G-E01 checks that the Recap keeps the facts, not that the conversation carries on: that the model answers a follow-up the way it would have before the compaction, in the same terms and without asking again what it was told. No goal measures it yet. A way to measure it is to be designed, as an evaluation next to G-E01.
- [ ] **Does `tool_choice: none` keep the prompt cache on Anthropic?** A Recap request that sends the tools but forbids calling them would stop a model from calling one. Until it is known, the Recap request is a default per wire API that the gateway configuration can change.
- [ ] **How long does a model-written Recap take?** It may take a minute or more, against `ModelCallDeadline` (2 minutes) and the Run's deadline. A team sees it on its own gateway; if it runs long, the Recap's call needs a deadline of its own.
- [ ] **Do the providers word a refusal as too long as the fake gateway does?** `internal/fakegateway` uses the wordings the providers are reported to return: `prompt is too long: N tokens > M maximum` on Claude, and on OpenAI `context_length_exceeded` with "maximum context length is N tokens" (Chat Completions) or "exceeds the context window" (Responses). The public documentation quotes none of them, and Gotato runs no check against a live provider. A team that finds another wording on its own gateway adds it with `overflow_phrases`.
