# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](../FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`Feed` has plumbing that is exported.** `Published`, `PublishLabeled`, `SetLabel`, `MirrorTo` and `NewInstanceFeed` are exported because `service` and `testkit` call them from other packages. Moving them behind a smaller exported surface changes the `service`/`gotato` split, so it needs a design, not a rename.

## Integration feedback

From the first real adoption (a Lark bot service running on several pods), each checked against the code on 2026-10-29. Evidence, verdicts and wording of the fixes are in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md); `D-NNN` is its entry number. Remove an item here in the commit that resolves it.

- [ ] **D-004 · An MCP server without annotations hides every tool under `WithMaxEffect`.** Add `WithDefaultEffect` or `WithToolEffectFunc`; state the consequence in FEATURES.md.
- [ ] **D-002 · `adapter/mcp` cannot filter or rename tools.** Add `WithToolFilter` and `WithToolRename`.
- [ ] **D-006 · `examples/larkbot` has no streaming card, and nothing says deltas are visible only on the writer Instance.** Add both; a writer switch needs the card's id in Session metadata.
- [ ] **D-007 · `larkbot` does not de-duplicate across pods.** Copy the README note into the two guides; pass `message_id` as `RequestID`.
- [ ] **D-018 · A folded tool called before it is loaded costs a round.** Reword the "also found, not loaded" line as an instruction; decide whether a read-only tool is loaded and run on the refusal; document `WithActiveToolSet` against fold.
- [ ] **D-019 · No cache gain on the chat wire through a self-hosted gateway.** Document how to get one (inject at the gateway, or `anthropic-messages` with `provider: anthropic`); parse `cache_read_input_tokens` and `cache_creation_input_tokens` on chat.
- [ ] **D-020 · Nothing warns that compaction is off when the context window is unknown.** Warn once at 0, show `ceiling=0` in `inspect`, an optional read of the window from a gateway, a loud line in FEATURES.md and the example config.
- [ ] **D-021 · `adapter/mcp` tools are not `Sequential`; raising `MaxParallelTools` runs write tools in parallel.** Document it with an example; an optional adapter switch.
- [ ] **D-022 · No guide for asking a person before a tool runs.** Add one to `guides/extension-points.md` with the limits of `Before`; add `SessionID` to `ToolUse`.
- [ ] **D-024 · No helper for a Turn or tool-call cap.** Add `MaxTurns` and `MaxToolCalls` as `TurnStopper` helpers; check whether a `TurnStopper` can send a Notice first.
- [ ] **D-025 · Tool arguments are base64 in a Record.** Keep `[]byte`; add an optional `arguments` JSON field to `tool_started`; document the form.
- [ ] **D-026 · A failure's reason is only readable from text.** Add `Code` to `ToolResult` and `ToolEnded`.

## Decisions

- [ ] **D-005 · Cost: documentation only, or a hook?** `Usage` has no price. Either state in FEATURES.md that a team converts `Usage` itself (with an example), or add a price table or an `Event` hook.
- [ ] **D-019 · Should the chat wire send `cache_control`?** Today it sends none, and `provider: anthropic` does not change that on chat. Either keep it a gateway matter and document it, or add the field.
- [ ] **D-010 · Reasoning effort through a self-hosted gateway.** The chat and anthropic wires refuse it. Either document `provider` for such a host, or add a pass-through extra body field.

## Open checks

Gotato runs no checks against real providers: a provider's behavior is played by fake gateways, and what only a real model shows is for a team to check on its own gateway.

- [ ] **Does a user who comes back after a compaction find the conversation they left?** GOALS.md G-E01 checks that the Recap keeps the facts, not that the conversation carries on: that the model answers a follow-up the way it would have before the compaction, in the same terms and without asking again what it was told. No goal measures it yet. A way to measure it is to be designed, as an evaluation next to G-E01.
- [ ] **Does `tool_choice: none` keep the prompt cache on Anthropic?** A Recap request that sends the tools but forbids calling them would stop a model from calling one. Until it is known, the Recap request is a default per wire API that the gateway configuration can change.
- [ ] **How long does a model-written Recap take?** It may take a minute or more, against `ModelCallDeadline` (2 minutes) and the Run's deadline. A team sees it on its own gateway; if it runs long, the Recap's call needs a deadline of its own.
- [ ] **Do the providers word a refusal as too long as the fake gateway does?** `internal/fakegateway` uses the wordings the providers are reported to return: `prompt is too long: N tokens > M maximum` on Claude, and on OpenAI `context_length_exceeded` with "maximum context length is N tokens" (Chat Completions) or "exceeds the context window" (Responses). The public documentation quotes none of them, and Gotato runs no check against a live provider. A team that finds another wording on its own gateway adds it with `overflow_phrases`.
