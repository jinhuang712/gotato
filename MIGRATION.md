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

### Request layout and `ModelRequest.CacheBreakpoints`

The agent now assembles every request through `gotato.AssembleRequest`: tools sorted by ID, messages reduced by `gotato.ForModel` (no `ID`, `Usage`, `StopReason`, or `ContentPart.Metadata`), and `CacheBreakpoints` hints added. Adapters that read those runtime fields from `ModelRequest.Messages` must take them from the Session instead. `ModelContext` gained `System` and `Panel` block lists; a builder that only sets `Messages` keeps working.

### New event kind `context_built`

Every Turn now emits one protected `context_built` event before the model request (payload: `messages`, `source_messages`, `prefix_hash`, `prefix_messages`, `panel_bytes`, `system_bytes`, `tools`, `strategy`, `selected_messages`, `dropped_messages`, plus builder metadata). Consumers that assert exact event sequences must include it; consumers must in general tolerate unknown kinds.

### Prompt validation

`Prompt`, `Steer`, and `FollowUp` now reject a message whose parts are all blank text (`UserMessage("")`, `UserMessage("  ")`) with `invalid_argument`. Previously an empty text part was accepted. Messages with binary or JSON parts are accepted as before.

### CLI

`run --context SPEC` and the `window:N` / `summary:N` strategies do not exist. Within a session the model sees the whole history; use `--compact-ceiling N` for automatic compaction and `--panel time,cwd` for per-turn dynamic content. The run outcome field `context` is replaced by `compacted`.

### Gateway: API-key only, Responses instead of Codex

The `pi_oauth` auth type, the `chatgpt.com/backend-api` default, the `originator: pi` / `chatgpt-account-id` headers, and all reading or writing of `~/.pi/agent/auth.json` are removed. The gateway is a service-level adapter and authenticates with `api_key` only. `api: openai-codex-responses` is accepted as an alias for `api: openai-responses`, which now targets `https://api.openai.com/v1/responses` by default; `api: openai-completions` is an alias for `openai-chat-completions`. A YAML file with `auth.type: pi_oauth` fails to load with an explicit message.

### Deprecations (removal planned with the next wire `ContractVersion`)

- `gotato.SpawnID`, `Event.SpawnID`, `Event.OriginRunID` — orchestration provenance is application metadata, not a runtime type. Store lineage in `Session.Metadata` or in your own records.

### Unchanged

The `Agent` interface, `NewAgent` and every existing `With*` option, message/tool/model/event/extension types, `CoreLimits` semantics (`DefaultLimits()` is newly exported so partial overrides are possible), the `gateway` YAML schema, and the HTTP/gRPC wire contract `ContractVersion "1"`.
