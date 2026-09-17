# Migration Notes

Breaking or behavior-visible changes, newest first. Additive changes are not listed.

## Runtime foundation (2026-09)

### `internal/testmodel` → `testkit`

`internal/testmodel.EchoModel` and `DemoModel` are now `testkit.EchoModel` and `testkit.DemoModel` (exported, plus `FakeModel`, `ReplayModel`, `FakeTool`, `EventRecorder`, fixtures). The package was internal, so no external code could import it.

```go
// before
import "github.com/jinhuang712/gotato/internal/testmodel"
model := testmodel.DemoModel{}
// after
import "github.com/jinhuang712/gotato/testkit"
model := testkit.DemoModel{}
```

### Identifier format

`AgentID`, `RunID`, and `MessageID` values are no longer `agent-1`, `run-7`, `message-12`. They are `<prefix>-<8 hex process nonce><counter>` (for example `run-3fa9c1e02`) so they stay unique across restarts and across processes that share one Session store. Anything that parsed the numeric suffix must stop; treat IDs as opaque strings. The types and JSON field names are unchanged.

### New event kind `context_built`

Every Turn now emits one protected `context_built` event before the model request (payload: `messages`, `source_messages`, `strategy`, `selected_messages`, `dropped_messages`, plus builder metadata). Consumers that assert exact event sequences must include it; consumers must in general tolerate unknown kinds.

### Prompt validation

`Prompt`, `Steer`, and `FollowUp` now reject a message whose parts are all blank text (`UserMessage("")`, `UserMessage("  ")`) with `invalid_argument`. Previously an empty text part was accepted. Messages with binary or JSON parts are accepted as before.

### Deprecations (removal planned with the next wire `ContractVersion`)

- `gotato.SpawnID`, `Event.SpawnID`, `Event.OriginRunID` — orchestration provenance is application metadata, not a runtime type. Store lineage in `Session.Metadata` or in your own records.

### Unchanged

The `Agent` interface, `NewAgent` and every existing `With*` option, message/tool/model/event/extension types, `CoreLimits` semantics (`DefaultLimits()` is newly exported so partial overrides are possible), the `gateway` YAML schema, and the HTTP/gRPC wire contract `ContractVersion "1"`.
