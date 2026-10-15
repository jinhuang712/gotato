# Changelog

What changed in each Gotato release, from the user's side. Newest first. A breaking change carries a before/after note, so you can move your code in one pass. Additive changes may be listed, but breaking ones must be.

## Unreleased

### Runtime

- **A reply that the output limit cut off keeps `StopMaxTokens`, even when it holds Tool calls.** The Tools that finished still run, and a `TurnStopper` sees the cut-off in `TurnSnapshot.Assistant.StopReason`. `TurnSnapshot.StopReason` stays the Turn's own reason, which is `StopToolCalls` once Tools ran. **Breaking:** code that read `Message.StopReason == StopToolCalls` to find Tool calls must read `len(Message.ToolCalls) > 0` instead.

### Providers

- **The Responses gateway no longer delivers a Tool call that the output limit cut short,** because its arguments are incomplete. It used to deliver the call with those arguments and report `StopToolCalls`.

## v0.1.0 — 2026-10-11

The first tagged release. The notes below list the breaking changes from the development builds before it.

### Tool Registry

- **`toolregistry.New` returns an error.** `toolregistry.New(tools...)` now returns `(*Registry, error)` and fails on the first invalid or duplicate Tool. It used to drop that Tool silently. Call `New` and check the error, or use `MustNew(tools...)` for Tools known to be valid.

  ```go
  // before
  reg := toolregistry.New(fsRead, shell)
  // after
  reg, err := toolregistry.New(fsRead, shell)
  if err != nil { /* ... */ }
  // or, when the Tools are already validated
  reg := toolregistry.MustNew(fsRead, shell)
  ```

- **Registry errors name the Tool.** `Registry.Register`, `Unregister`, `Activate` and `Deactivate` wrap `ErrDuplicate` and `ErrNotFound` with the offending ID, and `errors.Is` still matches. `List`, `Describe` and `Active` return copied specs. A Tool registered with a whitespace-padded ID is stored under the trimmed ID, so `Describe(" spaced ")` no longer finds a Tool registered as `"spaced"`. A zero-value `Registry` now accepts `Register`.

### Sessions and stores

- **`FileStore.Save` is durable.** It fsyncs the temp file and the store directory before it returns, so a successful `Save` survives a crash or a power loss.
- **`FileStore.List` reports unreadable files.** It returns an error when any session file is unreadable. It used to omit that file silently. One corrupt file now fails the listing.

### Runtime

- **`internal/testmodel` became `testkit`.** `EchoModel` and `DemoModel` are now exported from `testkit`, together with `FakeModel`, `ReplayModel`, `FakeTool`, `EventRecorder` and session fixtures. The old package was internal, so no external code could import it.

  ```go
  // before
  import "github.com/jinhuang712/gotato/internal/testmodel"
  model := testmodel.DemoModel{}
  // after
  import "github.com/jinhuang712/gotato/testkit"
  model := testkit.DemoModel{}
  ```

- **`NewAgent` returns `RuntimeAgent`.** It is an `Agent` plus the control, lifecycle, event and inspection interfaces that the core always implemented. Code that assigns the result to an `Agent` variable is unchanged. Code that found `ControllableAgent`, `EventSource`, `ToolInspector`, `IdleWaiter`, `RunCanceler` or `LifecycleAgent` by type assertion can call them directly.
- **`Transcript` gained `Len()`.** An Agent enforces `MaxMessages` through `Transcript.Len()`, so it no longer copies a `Session`-backed history on every commit. `session.Session` and the default in-memory transcript already implement it. **Breaking:** a custom Transcript must add `Len() int`.

  ```go
  type Transcript interface {
      Len() int
      Messages() []Message
      Append(Message) error
  }
  ```

- **Identifiers changed format.** `AgentID`, `RunID` and `MessageID` values are no longer `agent-1`, `run-7` or `message-12`. They are `<prefix>-<8 hex process nonce><counter>`, for example `run-3fa9c1e02`, so they stay unique across restarts and across processes that share one Session store. Treat IDs as opaque strings, and stop parsing the numeric suffix. The types and JSON field names are unchanged.
- **Every request is assembled for prompt caching.** The agent builds each request through `gotato.AssembleRequest`. Tools are sorted by ID. `gotato.ForModel` strips `ID`, `Usage`, `StopReason` and `ContentPart.Metadata` from messages. `CacheBreakpoints` hints are added. **Breaking:** an adapter that read those runtime fields from `ModelRequest.Messages` must take them from the Session instead. `ModelContext` gained `System` and `Panel` block lists, and a builder that sets only `Messages` keeps working.
- **New event kind `context_built`.** Every Turn emits one protected `context_built` event before the model request. Its payload holds `messages`, `source_messages`, `prefix_hash`, `prefix_messages`, `panel_bytes`, `system_bytes`, `tools`, `strategy`, `selected_messages`, `dropped_messages` and the builder's metadata. Consumers that assert exact event sequences must include it. Every consumer must tolerate unknown kinds.
- **Blank prompts are rejected.** `Prompt`, `Steer` and `FollowUp` reject a message whose parts are all blank text, such as `UserMessage("")` or `UserMessage("  ")`, with `invalid_argument`. Messages with binary or JSON parts are accepted as before.
- **What stayed the same:** the `Agent` interface, `NewAgent` and every existing `With*` option, the message, tool, model and extension types, and the meaning of `CoreLimits`. `DefaultLimits()` is newly exported, so you can override part of the limits.

### Context

- **Compaction results changed shape.** `modelctx.Compact` sets `MessagesBefore` on the no-op path too, where it used to be 0. `Result.Compaction` is a `*session.Compaction`, present only when `replaced` is true. A no-op compaction no longer emits an all-zero `compaction` object in the CLI, HTTP or gRPC JSON.

### Service

- **`orchestration` and `host` became `service`, with wire contract 2.** `orchestration`, `host` and `cmd/gotato-agent` are removed. The service is `service.Runner`: a Session store plus an Agent created for each Run. `service/httpapi` serves it over HTTP (`ContractVersion "2"`), and `adapter/grpc` serves it over gRPC (`gotato.v2.SessionService`, replacing `gotato.v1.AgentService`). The Session ID is the unit of identity. Conversation keys, agent generations, retirement and spawn groups are gone. Derived work is `session.Fork` plus another Run.

  | Before | After |
  |---|---|
  | `POST /v1/runs {"agent_name","conversation_key","prompt"}` | `POST /v1/sessions {"agent"}` then `POST /v1/sessions/{id}/runs {"prompt"}`, or `POST /v1/runs {"prompt","agent"}` to create and run in one call |
  | `POST /v1/runs/stream` (SSE) | `POST /v1/sessions/{id}/runs/stream` or `POST /v1/runs/stream` (SSE: `event: <kind>` … `event: result`) |
  | `POST /v1/runs/async`, `GET /v1/runs/{id}`, `POST /v1/runs/progress` | removed: use the stream or a client-side job |
  | `POST /v1/runs/{run_id}/cancel` | the same, plus `POST /v1/sessions/{id}/cancel` |
  | `GET /v1/conversations/{id}` | `GET /v1/sessions/{id}` (the full document) |
  | `POST /v1/agents/{id}/close`, `/admin/drain` | removed: an Agent lives for one Run, and drain happens on process shutdown |
  | `go run ./cmd/gotato-agent` | `gotato serve` (HTTP) or `(cd adapter/grpc && go run ./cmd/gotato-grpc)` |

  Removed from the root package: `SpawnID`, `Event.SpawnID`, `Event.OriginRunID`, `AgentName`, `ConversationID`, `ConversationKey`, `AgentGeneration`, `LifecycleEvent.ConversationID` and `LifecycleEvent.Generation`. Store lineage and tenancy in `Session.Metadata`.

- **HTTP body limit.** `service/httpapi` rejects a JSON request body larger than 1 MiB with `413 Request Entity Too Large`.
- **Creating a session with an explicit ID is atomic.** `POST /v1/sessions` with an ID returns `409` when the ID exists, instead of racing a concurrent create.
- **`Runner.Fork` and `Runner.Inspect` signatures changed.** `Runner.Fork(ctx, sessionID, newID string)` takes an explicit new ID, empty for a random one, and rejects an existing ID instead of overwriting it. `Runner.Inspect(ctx, sessionID, options ...InspectOptions)` accepts `Instruction` and `Panel` overrides for one inspection. `service.RunRequest` gained `SkipSave`.

### Providers

- **The gateway authenticates with an API key only, and Responses replaces Codex.** The `pi_oauth` auth type, the `chatgpt.com/backend-api` default, the `originator: pi` and `chatgpt-account-id` headers, and every read or write of `~/.pi/agent/auth.json` are removed. `api: openai-codex-responses` is accepted as an alias for `api: openai-responses`, which now targets `https://api.openai.com/v1/responses` by default. `api: openai-completions` is an alias for `openai-chat-completions`. A YAML file with `auth.type: pi_oauth` fails to load with an explicit message.

### CLI

- **`run --context SPEC` is gone,** together with the `window:N` and `summary:N` strategies. Within a session the model sees the whole history. Use `--compact-ceiling N` for automatic compaction and `--panel time,cwd` for content that changes every turn. The run outcome field `context` is replaced by `compacted`.
