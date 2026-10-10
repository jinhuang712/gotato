# Gotato TODO

Open defects and design decisions, verified against the code. Planned capabilities (MCP, the tool effect field, the Anthropic adapter, rate-limit info, examples, testkit fixtures, a Session lease) are tracked by status marker in [FEATURES.md](FEATURES.md). Remove an item in the commit that resolves it.

## Defects

- [ ] **`ToolUse.Executed` and `ToolUse.Result` are reserved and never assigned.** A Tool or `PreToolUse` extension always sees `false`/`nil`. Wire them into a `PostToolUse` view or remove them; removal changes the JSON shape and needs a `MIGRATION.md` entry (`tool.go`).
- [ ] **Provider identity leaks into `ToolCall.ID`.** The Responses adapter packs the provider item ID into the call ID and splits it back with `splitResponsesCallID`, so a provider-private ID is persisted in the Session. Add an opaque, adapter-owned field to `ToolCall` (core clones and serializes it, like `ContentPart.Signature`) and keep `ToolCall.ID` to the call ID (`gateway/responses.go`, `types.go`).
- [ ] **`ModelOptions` has no runtime path.** `ModelRequest.Options` exists, but core never fills it and there is no `WithModelOptions`; temperature, max tokens, and reasoning effort are configurable only by hand-built requests (`model.go`, `agent.go`).

## Decisions

- [ ] **Typed event payloads (G-F13).** `Event.Payload` is `map[string]any` with documented keys, and the gRPC adapter carries it as `payload_json`. Choose between per-kind payload structs (a breaking wire change with a `ContractVersion` bump) and keys locked by tests. Add `reasoning_update` for streaming reasoning deltas either way; today only text deltas emit `message_update`.
- [ ] **Blocking `EventObserver` by default.** Every extension stage runs synchronously on the agent goroutine; `AdvisoryExtension` only makes a failure non-fatal. An observer doing I/O slows every event. Options: run observers asynchronously by default, add a per-call extension deadline, or document the cost and ship an async observer wrapper (`extensions.go`).
