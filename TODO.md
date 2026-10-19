# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (one writer per Session across instances, the warm Session and its TTL, fault injection and the multi-instance harness) and the *Later* list (MCP, rate-limit info, examples and more) are tracked in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **An output-limit continuation starts a new Round.** With `MaxTruncatedContinuations` above 0 (it is 0 by default), a reply cut off at the output limit is followed by `UserMessage(TruncatedContinuationText)` (`loop.go`, `limits.go`). The gateway's `roundStart` (`gateway/provider.go`) takes any user Message as the start of a Round, so Claude and OpenAI are no longer sent the cut reply's reasoning, which the model needs to go on. Read from the code, not run. Fixed by FEATURES.md → G-F29, where the continuation becomes a `continue` notice.

## Decisions

None open. The compaction decisions were settled on 2026-10-19 and written into the project documents.
