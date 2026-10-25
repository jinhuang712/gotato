# Gotato TODO

Open defects, design decisions and open checks, verified against the code. Planned capabilities (the warm Session and its TTL, compaction with Segments, runtime notices, steering a Run through the Runner, tool search, the MCP adapter, large Tool Results) and the *Later* list (rate-limit info, examples and more) are tracked in [FEATURES.md](FEATURES.md), and their order in [ROADMAP.md](ROADMAP.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **An output-limit continuation starts a new Round.** With `MaxTruncatedContinuations` above 0 (it is 0 by default), a reply cut off at the output limit is followed by `UserMessage(TruncatedContinuationText)` (`loop.go`, `limits.go`). The gateway's `roundStart` (`gateway/provider.go`) takes any user Message as the start of a Round, so Claude and OpenAI are no longer sent the cut reply's reasoning, which the model needs to go on. Read from the code, not run. Fixed by FEATURES.md → G-F29, where the continuation becomes a `continue` notice.
- [ ] **Each provider's refusal as too long is not yet played on a fake gateway.** The six phrases in `gateway/overflow.go` are unchecked against live providers; the gateway reads `message`, `code` and `type`, and a team adds its own phrases with `overflow_phrases`. The four wordings by which `Error.ContextLimit()` reads the limit a refusal names (`limitPatterns`) are unchecked in the same way, and a refusal in other words names none, so a merge on an overflow is then sized to the Segments' budget. What is left is to test each provider's form of the refusal on a fake gateway (ROADMAP.md Phase 11).

## Decisions

- [ ] **How does a model read part of a spilled result inside a pod with no machine?** Typed reader parameters (`lines`, `grep`, a `json` path), or a terminating pipeline language: a subset of bash syntax with a parser of Gotato's own. Eight other ways are ruled out ([DESIGNS.md → Reading a large result needs no machine](DESIGNS.md#reading-a-large-result-needs-no-machine--2026-10-25)). Decided in [ROADMAP.md → Phase 9](ROADMAP.md#phase-9--reading-large-results), after real MCP results show what a model needs; the features are in [FEATURES.md → G-F36](FEATURES.md#g-f36--reading-large-results-).

## Open checks

Gotato runs no checks against real providers: a provider's behavior is played by fake gateways (ROADMAP.md Phase 11), and what only a real model shows is for a team to check on its own gateway.

- [ ] **Does a user who comes back after a compaction find the conversation they left?** GOALS.md G-E01 checks that the Recap keeps the facts, not that the conversation carries on: that the model answers a follow-up the way it would have before the compaction, in the same terms and without asking again what it was told. No goal measures it yet. A way to measure it is to be designed, as an evaluation next to G-E01.
- [ ] **Does `tool_choice: none` keep the prompt cache on Anthropic?** A Recap request that sends the tools but forbids calling them would stop a model from calling one ([FEATURES.md → G-F30](FEATURES.md#g-f30--a-last-turn-that-answers-)). Until it is known, the Recap request is a default per wire API that the gateway configuration can change.
- [ ] **How long does a model-written Recap take?** It may take a minute or more, against `ModelCallDeadline` (2 minutes) and the Run's deadline. A team sees it on its own gateway; if it runs long, the Recap's call needs a deadline of its own.
- [ ] **Does every refusal as too long reach the Loop before any output?** An Anthropic stream that ends with `model_context_window_exceeded` does so in the middle of a reply (`gateway/anthropic.go`), after some text, so a compact-and-send-again cannot assume no output. Each wire API's path is tested on a fake gateway, with the refusal before any output and after some.
