# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](../FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`Feed` has plumbing that is exported.** `Published`, `PublishLabeled`, `SetLabel`, `MirrorTo` and `NewInstanceFeed` are exported because `service` and `testkit` call them from other packages. Moving them behind a smaller exported surface changes the `service`/`gotato` split, so it needs a design, not a rename.

## Integration feedback

From the first real adoption (a Lark bot service running on several pods), each checked against the code on 2026-10-29. Evidence, verdicts and wording of the fixes are in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md); `D-NNN` is its entry number. Remove an item here in the commit that resolves it.

- [ ] **D-009 · The `WithForwarding` example's allow check is bypassed by `http://10.0.evil.com`.** Check the host IP against a CIDR in the example and in `guides/http-in-five-minutes.md`; mark `serve --forward` "tests only" in its help.
- [ ] **D-001 · A nested module's tag can lag behind the root's on the Go proxy.** Release notes: push `adapter/mcp/vX.Y.Z` first (or together), warm the proxy, name `GOPROXY=direct` as the fallback.
- [ ] **D-004 · An MCP server without annotations hides every tool under `WithMaxEffect`.** Add `WithDefaultEffect` or `WithToolEffectFunc`; state the consequence in FEATURES.md.
- [ ] **D-002 · `adapter/mcp` cannot filter or rename tools.** Add `WithToolFilter` and `WithToolRename`.
- [ ] **D-006 · `examples/larkbot` has no streaming card, and nothing says deltas are visible only on the writer Instance.** Add both; a writer switch needs the card's id in Session metadata.
- [ ] **D-007 · `larkbot` does not de-duplicate across pods.** Copy the README note into the two guides; pass `message_id` as `RequestID`.
- [ ] **D-003 · Nothing warns against `http.Client.Timeout` on the MCP transport.** One sentence in the option comment and FEATURES.md.
- [ ] **D-008 · No Mongo section in `guides/write-a-store.md`.**
- [ ] **D-011 · `guides/install.md` opens with Go 1.24 only.** Add "1.25 with `adapter/mcp`".
- [ ] **D-012 · No recommended `WithListCache` TTL.** Docs only; the default stays 0.

## Decisions

- [ ] **D-005 · Cost: documentation only, or a hook?** `Usage` has no price. Either state in FEATURES.md that a team converts `Usage` itself (with an example), or add a price table or an `Event` hook.
- [ ] **D-010 · Reasoning effort through a self-hosted gateway.** The chat and anthropic wires refuse it. Either document `provider` for such a host, or add a pass-through extra body field.

## Open checks

Gotato runs no checks against real providers: a provider's behavior is played by fake gateways, and what only a real model shows is for a team to check on its own gateway.

- [ ] **Does a user who comes back after a compaction find the conversation they left?** GOALS.md G-E01 checks that the Recap keeps the facts, not that the conversation carries on: that the model answers a follow-up the way it would have before the compaction, in the same terms and without asking again what it was told. No goal measures it yet. A way to measure it is to be designed, as an evaluation next to G-E01.
- [ ] **Does `tool_choice: none` keep the prompt cache on Anthropic?** A Recap request that sends the tools but forbids calling them would stop a model from calling one. Until it is known, the Recap request is a default per wire API that the gateway configuration can change.
- [ ] **How long does a model-written Recap take?** It may take a minute or more, against `ModelCallDeadline` (2 minutes) and the Run's deadline. A team sees it on its own gateway; if it runs long, the Recap's call needs a deadline of its own.
- [ ] **Do the providers word a refusal as too long as the fake gateway does?** `internal/fakegateway` uses the wordings the providers are reported to return: `prompt is too long: N tokens > M maximum` on Claude, and on OpenAI `context_length_exceeded` with "maximum context length is N tokens" (Chat Completions) or "exceeds the context window" (Responses). The public documentation quotes none of them, and Gotato runs no check against a live provider. A team that finds another wording on its own gateway adds it with `overflow_phrases`.
