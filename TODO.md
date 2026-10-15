# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (MCP, rate-limit info, examples, testkit fixtures, Session ownership across instances, the Effect cap, the warm Session and its TTL) are tracked by status marker in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`ToolUse.Executed` and `ToolUse.Result` are reserved and never assigned.** A Tool or `PreToolUse` extension always sees `false`/`nil`. Wire them into a `PostToolUse` view or remove them; removal changes the JSON shape and needs a breaking-change entry in `CHANGELOG.md` (`tool.go`).
- [ ] **Provider identity leaks into `ToolCall.ID`.** The Responses adapter packs the provider item ID into the call ID and splits it back with `splitResponsesCallID`, so a provider-private ID is persisted in the Session. Add an opaque, adapter-owned field to `ToolCall` (core clones and serializes it, like `ContentPart.Signature`) and keep `ToolCall.ID` to the call ID (`gateway/responses.go`, `types.go`).

- [ ] **A crash in the middle of a Run loses the whole Run.** `service.Runner` saves the Session once, after the Run settles (`service/service.go`, the `store.Save` after `agent.Prompt`). A pod that is killed or runs out of memory mid-Run loses every Turn the Run committed. `Drain` covers only a graceful shutdown. Save after each committed Turn.
- [ ] **Two pods can run one Session at once, and the last save wins.** The Session lock is local to one process (`withSessionLock`), and `Store.Save` replaces the whole document with no revision check. Two instances that share a Store can both run a Session and silently drop each other's Turns. A save must detect a concurrent write, and only one instance may run a Session at a time; a second instance must stop before it calls the model. The mechanism (a lease, a revision compared on save, or both) is an open question in DESIGNS.md. See DESIGNS.md → State and the store, GOALS.md G-G08 and FEATURES.md §Service.

## Decisions

- [ ] **Typed event payloads (G-F13).** `Event.Payload` is `map[string]any` with documented keys, and the gRPC adapter carries it as `payload_json`. Choose between per-kind payload structs (a breaking wire change with a `ContractVersion` bump) and keys locked by tests. Add `reasoning_update` for streaming reasoning deltas either way; today only text deltas emit `message_update`.
- [ ] **Blocking `EventObserver` by default.** Every extension stage runs synchronously on the agent goroutine; `AdvisoryExtension` only makes a failure non-fatal. An observer doing I/O slows every event. Options: run observers asynchronously by default, add a per-call extension deadline, or document the cost and ship an async observer wrapper (`extensions.go`).
