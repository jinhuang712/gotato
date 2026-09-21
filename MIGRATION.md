# Migration Notes

Breaking or behavior-visible changes, newest first. Additive changes are not listed.

## Review remediation (2026-09)

### `toolregistry.New` returns an error

`toolregistry.New(tools...)` now returns `(*Registry, error)` and fails on the first invalid or duplicate Tool instead of silently dropping it. Call `New` and check the error, or use `MustNew(tools...)` for Tools known to be valid.

```go
// before
reg := toolregistry.New(fsRead, shell)
// after
reg, err := toolregistry.New(fsRead, shell)
if err != nil { /* ... */ }
// or, when the Tools are already validated
reg := toolregistry.MustNew(fsRead, shell)
```

`Registry.Register` and `Registry.Unregister`/`Activate`/`Deactivate` now wrap `ErrDuplicate`/`ErrNotFound` with the offending ID (`errors.Is` still matches), and `List`/`Describe`/`Active` return copied specs. Registering a Tool with a whitespace-padded ID stores the trimmed ID, so `Describe(" spaced ")` no longer resolves a Tool registered as `"spaced"`. A zero-value `Registry` is now usable for `Register`.

### FileStore durability and listing

`session.FileStore.Save` fsyncs the temp file and the store directory before returning, so a successful `Save` is durable across a crash or power loss. `FileStore.List` now returns an error when any session file is unreadable instead of silently omitting it; a single corrupt file fails the listing.

### HTTP body limit

`service/httpapi` rejects a JSON request body larger than 1 MiB with `413 Request Entity Too Large`. `POST /v1/sessions` with an explicit ID is now atomic and returns `409` instead of racing a concurrent create.

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

### New `RuntimeAgent` return from `NewAgent`

`gotato.NewAgent` now returns `gotato.RuntimeAgent` (an `Agent` plus the control, lifecycle, event, and inspection interfaces Core always implemented) instead of the bare `Agent`. Code that assigns the result to an `Agent` variable or passes it where an `Agent` is expected is unchanged; code that discovered `ControllableAgent`, `EventSource`, `ToolInspector`, `IdleWaiter`, `RunCanceler`, or `LifecycleAgent` by type assertion can call them directly.

### `Service.Runner.Fork` and `Inspect` signatures

`Runner.Fork(ctx, sessionID, newID string)` takes an explicit new ID (empty for a random one) and rejects an existing ID instead of overwriting it. `Runner.Inspect(ctx, sessionID, options ...InspectOptions)` accepts per-inspection `Instruction`/`Panel` overrides. `service.RunRequest` gained `SkipSave`.

### `Transcript` gained `Len()`

An Agent now enforces `MaxMessages` through `Transcript.Len()` instead of materializing `Messages()`, so a `Session`-backed history is not copied on every commit. The interface is extended:

```go
type Transcript interface {
    Len() int
    Messages() []Message
    Append(Message) error
}
```

`session.Session` and the default in-memory transcript already implement it. A custom Transcript must add `Len() int`.

### Compaction result shape

`modelctx.Compact` now sets `MessagesBefore` on the no-op path too (it was 0), and `Result.Compaction` is a `*session.Compaction` present only when `replaced` is true. A no-op compaction no longer emits an all-zero `compaction` object in the CLI, HTTP, or gRPC JSON.

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

### Service: `orchestration` + `host` → `service`; wire contract 2

`orchestration`, `host`, and `cmd/gotato-agent` are removed. The service is `service.Runner` (a Session store plus Agents created per Run) with `service/httpapi` over HTTP (`ContractVersion "2"`) and `adapter/grpc` over gRPC (`gotato.v2.SessionService`, replacing `gotato.v1.AgentService`). The unit of identity is the Session ID; there are no Conversation keys, agent generations, retirement, or spawn groups. Derived work is `session.Fork` plus another Run.

| Before | After |
|---|---|
| `POST /v1/runs {"agent_name","conversation_key","prompt"}` | `POST /v1/sessions {"agent"}` then `POST /v1/sessions/{id}/runs {"prompt"}`, or `POST /v1/runs {"prompt","agent"}` for create-and-run |
| `POST /v1/runs/stream` (SSE) | `POST /v1/sessions/{id}/runs/stream` or `POST /v1/runs/stream` (SSE: `event: <kind>` … `event: result`) |
| `POST /v1/runs/async`, `GET /v1/runs/{id}`, `POST /v1/runs/progress` | removed; use the stream or a client-side job |
| `POST /v1/runs/{run_id}/cancel` | same, plus `POST /v1/sessions/{id}/cancel` |
| `GET /v1/conversations/{id}` | `GET /v1/sessions/{id}` (full document) |
| `POST /v1/agents/{id}/close`, `/admin/drain` | removed; Agents live for one Run; drain happens on process shutdown |
| `go run ./cmd/gotato-agent` | `gotato serve` (HTTP) or `go run ./adapter/grpc/cmd/gotato-grpc` |

Removed from the root package: `SpawnID`, `Event.SpawnID`, `Event.OriginRunID`, `AgentName`, `ConversationID`, `ConversationKey`, `AgentGeneration`, `LifecycleEvent.ConversationID`, `LifecycleEvent.Generation`. Store lineage and tenancy in `Session.Metadata`.

### Gateway: API-key only, Responses instead of Codex

The `pi_oauth` auth type, the `chatgpt.com/backend-api` default, the `originator: pi` / `chatgpt-account-id` headers, and all reading or writing of `~/.pi/agent/auth.json` are removed. The gateway is a service-level adapter and authenticates with `api_key` only. `api: openai-codex-responses` is accepted as an alias for `api: openai-responses`, which now targets `https://api.openai.com/v1/responses` by default; `api: openai-completions` is an alias for `openai-chat-completions`. A YAML file with `auth.type: pi_oauth` fails to load with an explicit message.

### Unchanged

The `Agent` interface, `NewAgent` and every existing `With*` option, message/tool/model/extension types, `CoreLimits` semantics (`DefaultLimits()` is newly exported so partial overrides are possible), and the `gateway` YAML schema apart from the auth change above. `NewAgent` returns the larger `RuntimeAgent` interface; see the entry above.
