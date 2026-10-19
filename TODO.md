# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (one writer per Session across instances, the warm Session and its TTL, fault injection and the multi-instance harness) and the *Later* list (MCP, rate-limit info, examples and more) are tracked in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **An output-limit continuation starts a new Round.** With `MaxTruncatedContinuations` above 0 (it is 0 by default), a reply cut off at the output limit is followed by `UserMessage(TruncatedContinuationText)` (`loop.go`, `limits.go`). The gateway's `roundStart` (`gateway/provider.go`) takes any user Message as the start of a Round, so Claude and OpenAI are no longer sent the cut reply's reasoning, which the model needs to go on. Read from the code, not run. Fixed by FEATURES.md → G-F29, where the continuation becomes a `continue` notice.

- [ ] **An open or a create can cut a valid stream of a newer Record.** `open` and `create` (`service/liveness.go`) write their claim, then call `cutStreams`, and `markStreams` (`service/streams.go`) cuts every started stream whose epoch is not the opener's. If the Record is deleted and created again on another instance between the claim and `cutStreams`, and a client starts a stream of the new Record on this instance in that gap, the stream ends with `cannot_resume`. The window is microseconds and needs that delete and create inside it; the client recovers by starting over. Verified by a scratch test that holds the claim's `Append`, not kept. Fix: cut only the streams registered before the open began its `Load` (a generation taken then), and mark the others for one more `Load` instead of cutting them.

## Decisions

None open. The compaction decisions were settled on 2026-10-19 and written into the project documents.
