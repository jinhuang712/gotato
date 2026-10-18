# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (one writer per Session across instances, the warm Session and its TTL, fault injection and the multi-instance harness) and the *Later* list (MCP, rate-limit info, examples and more) are tracked in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

None open.

## Decisions

- [ ] **Typed event payloads (G-F13).** `Event.Payload` is `map[string]any` with documented keys, and the gRPC adapter carries it as `payload_json`. Choose between per-kind payload structs (a breaking wire change with a `ContractVersion` bump) and keys locked by tests. Add `reasoning_update` for streaming reasoning deltas either way; today only text deltas emit `message_update`.
- [ ] **A Session stops at 1,000 messages by default.** `CoreLimits.MaxMessages` (default 1,000) bounds the whole Transcript, not one Run. Once a Session holds 1,000 messages, every later Run fails with `limit_exceeded` when it commits the prompt, unless the Session is compacted or the service raises the limit. Automatic compaction is off by default, so a long-lived Session reaches this without warning. Options: count only the Run's own messages, raise the default, or document the limit next to compaction (`limits.go`, `commitMessage` in `session.go`). The reference workload raises the limit for `history_1000` (`testkit.HistoryTask`).
- [ ] **A message's way into a Session has no Events (G-F13, G-F26).** Only the commit of a Steer or FollowUp is reported (`message_start`/`message_end` with `source`). Its arrival, a full buffer, a drop when the Run ends first (`discardControl` in `session.go`), a Run rejected or waiting on a busy Session, a full Runner, and a cancel request emit nothing, so a team cannot see a lost message or measure how long a cancel takes. What happens to a message mid-Run is now decided (FEATURES.md → G-F26 → Messages that arrive during a Run), so the names are the open question, designed at the start of ROADMAP.md → Phase 6.
- [ ] **Blocking `EventObserver` by default.** Every extension stage runs synchronously on the agent goroutine; `AdvisoryExtension` only makes a failure non-fatal. An observer doing I/O slows every event. Options: run observers asynchronously by default, add a per-call extension deadline, or document the cost and ship an async observer wrapper (`extensions.go`).
