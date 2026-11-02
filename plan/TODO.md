# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](../FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

None open.

## Integration feedback

Findings from real use, each checked against the code (first pass on 2026-10-29). Evidence, verdicts and wording of the fixes are in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md); `D-NNN` is its entry number. Remove an item here in the commit that resolves it.


## Decisions

- [ ] **Compaction moves content out and never makes it unreadable ([043](decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md)).** A reference for every Tool Result and Message a compaction or merge removes from the Context; reasoning is not read back. Answers D-036.
- [ ] **Model profile: describe a model apart from its connection, with the window stated ([042](decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md)).** Answers D-020 and D-034 at the root; no pool, no routing.

## Open checks

Gotato runs no checks against real providers: a provider's behavior is played by fake gateways, and what only a real model shows is for a team to check on its own gateway.

- [ ] **Does a user who comes back after a compaction find the conversation they left?** GOALS.md G-E01 checks that the Recap keeps the facts, not that the conversation carries on: that the model answers a follow-up the way it would have before the compaction, in the same terms and without asking again what it was told. No goal measures it yet. A way to measure it is to be designed, as an evaluation next to G-E01.
- [ ] **Does `tool_choice: none` keep the prompt cache on Anthropic?** A Recap request that sends the tools but forbids calling them would stop a model from calling one. Until it is known, the Recap request is a default per wire API that the gateway configuration can change.
- [ ] **How long does a model-written Recap take?** It may take a minute or more, against `ModelCallDeadline` (2 minutes) and the Run's deadline. A team sees it on its own gateway; if it runs long, the Recap's call needs a deadline of its own.
- [ ] **Do the providers word a refusal as too long as the fake gateway does?** `internal/fakegateway` uses the wordings the providers are reported to return: `prompt is too long: N tokens > M maximum` on Claude, and on OpenAI `context_length_exceeded` with "maximum context length is N tokens" (Chat Completions) or "exceeds the context window" (Responses). The public documentation quotes none of them, and Gotato runs no check against a live provider. A team that finds another wording on its own gateway adds it with `overflow_phrases`.
