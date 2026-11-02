# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](../FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`Feed` has plumbing that is exported.** `Published`, `PublishLabeled`, `SetLabel`, `MirrorTo` and `NewInstanceFeed` are exported because `service` and `testkit` call them from other packages. Moving them behind a smaller exported surface changes the `service`/`gotato` split, so it needs a design, not a rename.

## Integration feedback

Findings from real use, each checked against the code (first pass on 2026-10-29). Evidence, verdicts and wording of the fixes are in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md); `D-NNN` is its entry number. Remove an item here in the commit that resolves it.

- [ ] **D-018 · A folded tool called before it is loaded costs a round.** Still open: decide whether a read-only tool is loaded and run on the refusal (a round 3 decision, measured after the rewording).
- [ ] **D-020 · Nothing warns that compaction is off when the context window is unknown.** `inspect` shows the ceiling and the documentation is loud; the "warn once" is dropped and replaced by draft decision 042.
- [ ] **D-024 · No helper for a Turn or tool-call cap.** Add `MaxTurns` and `MaxToolCalls` as `TurnStopper` helpers; check whether a `TurnStopper` can send a Notice first.
- [ ] **D-025 · Tool arguments are base64 in a Record.** Keep `[]byte`; add an optional `arguments` JSON field to `tool_started`; document the form.
- [ ] **D-032 · Decide whether a per-Run value should reach `ToolUse`, besides the documented `ctx` route.** The `ctx` route is documented and pinned by a test.
- [ ] **D-033 · Follow-up to D-027.** Input length does add about +2 s of first-token time over 44k tokens, so D-019 stays a latency item until measured.
- [ ] **D-034 · A long Run never compacted mid-Run because the model window was left unset.** Confirmed. No core change; it is another reason for D-020's warning.
- [ ] **D-038 · A compaction ceiling is a hard failure line, and a low one ends Runs.** Document it in the compaction section: the ceiling is a hard line, not a cost knob, and must stay above the floor after a compaction (instruction and tool definitions, kept tail, the largest Tool Result of one Turn; mark the rule of thumb a guess). A cost cap is a team's own `TurnStopper`. Decided: no code.
- [ ] **D-039 · After a compaction a loaded tool the Session never called is unloaded, and its first call fails once.** Confirmed by measurement, expected. One sentence in the compaction and tool-search sections; running the call on the spot is the second half of D-018.
- [ ] **D-035 · `compaction_ended` shows no window and hides a ceiling of 0; `context_built` has no per-part size.** Needs a decision first: carrying the two numbers to the Checkpoint changes the arguments of three public `Record` methods. Then an estimate per part.
- [ ] **D-036 · Clearing old Tool Results in the middle of a Run is answered by the draft decision 043 (a reference instead of a loss).**

## Decisions

- [ ] **Compaction moves content out and never makes it unreadable (draft [043](decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md)).** A reference for every Tool Result and Message a compaction or merge removes from the Context; reasoning is not read back. Answers D-036.
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
