# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (one writer per Session across instances, the warm Session and its TTL, fault injection and the multi-instance harness) and the *Later* list (MCP, rate-limit info, examples and more) are tracked in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **An output-limit continuation starts a new Round.** With `MaxTruncatedContinuations` above 0 (it is 0 by default), a reply cut off at the output limit is followed by `UserMessage(TruncatedContinuationText)` (`loop.go`, `limits.go`). The gateway's `roundStart` (`gateway/provider.go`) takes any user Message as the start of a Round, so Claude and OpenAI are no longer sent the cut reply's reasoning, which the model needs to go on. Read from the code, not run. Fixed by FEATURES.md → G-F29, where the continuation becomes a `continue` notice.

## Decisions

- [ ] **A Session stops at 1,000 messages by default.** `CoreLimits.MaxMessages` (default 1,000) bounds the whole Transcript, not one Run. Once a Session holds 1,000 messages, every later Run fails with `limit_exceeded` when it commits the prompt, unless the Session is compacted or the service raises the limit. Automatic compaction is off by default, so a long-lived Session reaches this without warning. Options: count only the Run's own messages, raise the default, or document the limit next to compaction (`limits.go`, `commitMessage` in `session.go`). The reference workload raises the limit for `history_1000` (`testkit.HistoryTask`). The maintainer's view (2026-10-18): the count of messages is not what matters, the size of the Context is, and a Context that grows too large should be compacted; so this is settled with how a Session is compacted (G-F06, ROADMAP.md → Waiting for design).
