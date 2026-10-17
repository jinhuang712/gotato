# Changelog

What changed in each Gotato release, from the user's side. Newest first. A breaking change carries a before/after note, so you can move your code in one pass. Additive changes may be listed, but breaking ones must be.

## Unreleased

Phases 2 and 3 of [ROADMAP.md](ROADMAP.md): the Effect cap, and the names. The Effect cap is an addition. The names ship as one planned break: the HTTP and gRPC contract version is now `"4"`, and the CLI contract version is now `"2"`.

### Names

- **The code now uses the names in [GLOSSARY.md](GLOSSARY.md).** The Agent is a definition, `service.Agent`. The object you build and talk to is the live Session, `gotato.Session`: one concrete type, with the interfaces the old `gotato.Agent` was split into removed, because nothing implemented them but the core. What the store keeps is the Session's Record, `session.Record`. The Loop stays a concept. No `// Deprecated:` aliases are kept. **Breaking:**

  | Before | After |
  |---|---|
  | `service.AgentSpec`, `service.Config.Specs`, `Runner.Spec(name)` | `service.Agent`, `service.Config.Agents`, `Runner.Agent(name)` |
  | `gotato.NewAgent(…) (RuntimeAgent, error)` | `gotato.NewSession(…) (*gotato.Session, error)` |
  | `gotato.Agent`, `RuntimeAgent`, `ControllableAgent`, `EventAgent`, `LifecycleAgent`, `AgentLifecycle`, `RunCanceler`, `IdleWaiter`, `ToolInspector`, `ContextInspector`, `EventSource`, `LifecycleSource` | removed; every method is on `*gotato.Session` |
  | `gotato.AgentID` | `gotato.SessionID` |
  | `gotato.AgentStatus`, `AgentCreated` … `AgentClosed` | `gotato.SessionStatus`, `SessionCreated` … `SessionClosed` |
  | `ErrAgentClosed`, `ErrAgentClosing` (`agent_closed`, `agent_closing`) | `ErrSessionClosed`, `ErrSessionClosing` (`session_closed`, `session_closing`) |
  | `EventAgentStart`, `EventAgentEnd` (`agent_start`, `agent_end`) | `EventRunStart`, `EventRunEnd` (`run_start`, `run_end`) |
  | `LifecycleAgentCreated`, `…Closing`, `…Closed` (`agent_created`, …) | `LifecycleSessionCreated`, `…Closing`, `…Closed` (`session_created`, …) |
  | `AgentID` (`agent_id`) on `Event`, `LifecycleEvent` and the extension inputs | `SessionID` (`session_id`) |
  | `session.Session` | `session.Record` |
  | `session.Record(s)`, the recorder | `session.NewRecorder(r)` |
  | `testkit.NewSession` | `testkit.NewRecord` |
  | `BenchmarkAgentFootprint`, `heap-B/agent` | `BenchmarkSessionFootprint`, `heap-B/session` |

  ```go
  // before
  agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTranscript(s), gotato.WithExtension(session.Record(s)))
  runner, err := service.New(service.Config{Store: store, Specs: []service.AgentSpec{{Name: "default", Model: model}}})
  // after
  live, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(r), gotato.WithExtension(session.NewRecorder(r)))
  runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "default", Model: model}}})
  ```
- **A live Session's ID is its Session ID, and every Event carries it.** `gotato.Session` takes its ID from its Transcript when the Transcript has an `ID() string` method, as a `session.Record` does, and generates one otherwise. The Runner's own Events carry it too, so `session_loaded` now has a Session, though still no Run. **Breaking:** the `session_id` payload key of `session_loaded`, `session_saved` and `session_compacted` is gone; read `Event.SessionID` instead.
- **A Run record no longer has `agent_id`.** It held a counter that meant nothing after a restart, and nothing read it. `Record.RecordRunStart` takes the Run's ID and time only. A Record saved before still loads; the old key is ignored, and its stored Events keep their old kinds and `agent_id`.
- Error messages say "Session is closed" and "Session is already processing a Run" where they said "Agent".

### Runtime

- **An Agent can cap the Effect of its tools.** `gotato.WithMaxEffect(effect)` and `service.Agent.MaxEffect` name the highest Effect a tool may have. A tool above the cap is never shown to the model, including the tools of a ToolSet activated later, and a call to it fails as a call to an unknown tool. A tool that declares no Effect counts as destructive, so any lower cap hides it. A Session cannot switch on a hidden tool: `Runner.Tools` does not list it, and `Runner.SetToolActive` reports it as unknown. There is no cap by default, so an Agent that names none behaves as before. An unknown Effect fails `NewSession` and `service.New`.
- **The tool that activates a ToolSet declares `read`.** It changes only which tools the model sees, so every cap keeps it. Its `ToolSpec`, and the `ToolUse` a `PreToolUse` extension gets for it, now carry `EffectRead` instead of no Effect.
- **`context_built` counts are numbers, and the ContextBuilder's metadata has its own key.** The Session merged the builder's `Metadata` into the payload under the same keys, so `FullHistoryContext`'s `"source_messages": "3"` replaced the Session's count, and `selected_messages` and `dropped_messages` always arrived as strings. The payload now has the shape of the `context inspect` report: `strategy`, `messages`, `source_messages`, `selected_messages`, `prefix_hash`, `prefix_messages`, `system_bytes` and `tools` are the Session's own, each count a number, and the builder's `Metadata` sits under `metadata`, as its strings. The same payload reaches HTTP, gRPC (`payload_json`) and `gotato events`. **Breaking:** read `dropped_messages`, and any key your own builder sets, from `metadata`.

  ```text
  before  {"strategy":"full_history","source_messages":"3","selected_messages":"3","dropped_messages":"0","messages":3,…}
  after   {"strategy":"full_history","source_messages":3,"selected_messages":3,"messages":3,…,
           "metadata":{"strategy":"full_history","source_messages":"3","selected_messages":"3","dropped_messages":"0"}}
  ```

### Providers

- **A Responses tool call keeps the provider's item ID out of its call ID.** The Responses adapter packed the provider's item ID into `ToolCall.ID` as `call_…|fc_…`, so a provider-private ID was stored in the Session and reached `Event.ToolCallID`. `ToolCall` now has `Signature`, an opaque provider artifact that the adapter sets and reads back on replay, like `ContentPart.Signature`. `ToolCall.ID` is the call ID alone. A Session saved before still replays: the adapter reads a packed call ID as it did.

### Contracts

- HTTP: the contract version is now `"4"`. Every Event in a stream or in `GET …/events` has `session_id` where it had `agent_id`, and the kinds `run_start` and `run_end`. `GET /v1/agents` adds `max_effect`, an object from Agent name to cap; an Agent without a cap has no entry.
- gRPC: the contract version is now `"4"`. `Event.agent_id = 1` is now `session_id = 1`, with the same field number. `AgentsResponse` adds `map<string, string> max_effect = 2`, with the same meaning as on HTTP. The generated code also picks up comments that `service.proto` had gained since the last generation.
- CLI: the contract version that `doctor` reports is now `"2"`, because `events` and `run --events jsonl` print `run_start`, `run_end` and `session_id`. `tools list --json` and `tools active --json` add `max_effect` when the agent has a cap, and `tools` is now `[]` rather than `null` when no tool is listed.

## v0.1.2 — 2026-10-16

Phase 1 of [ROADMAP.md](ROADMAP.md), Measure: the reference workload, the live run, and a number for every benchmark in [GOALS.md](GOALS.md) but G-B13. The HTTP and gRPC contract version is now `"3"`.

### Runtime

- **The panel is gone.** It re-rendered small content that changes every Turn, such as the time, onto the last message of every request. Such content is never cached, so it cost its full price on every Turn, and after a tool call it was appended to the tool's output. A request is now the system prompt, the tools and the history. A fact the model needs now, such as the current time, comes from a tool it calls. A Session that still carries `gotato.panel` in its metadata runs as before, without a panel. **Breaking:**
  - Go: `ModelContext.Panel`, `ModelContext.RenderPanel`, `gotato.PanelTag`, `modelctx.WithPanel`, `modelctx.PanelFunc`, `modelctx.Time`, `modelctx.Report.PanelBytes`, `service.MetaPanel`, `service.PanelFromSpec`, `service.InspectOptions.Panel` and `service.Config.Now`, which only the time panel read, are removed.
  - CLI: `--panel` is gone from `run`, `session create` and `context inspect` (exit 2 if given).
  - HTTP and gRPC: the contract version is now `"3"`. The context report (`GET /v1/sessions/{id}/context`, the gRPC `report_json`) has no `panel_bytes`, and the `context_built` payload has no `panel_bytes`.

  ```go
  // before: the time on every request
  builder := modelctx.WithPanel(modelctx.FullHistory(), func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
      return []gotato.Block{modelctx.Time(time.Now())}, nil
  })
  // after: a tool the model calls when it needs the time
  clock, _ := gotato.NewFuncTool("clock.now", "Returns the current time in RFC 3339 UTC.",
      func(context.Context, struct{}) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
  ```
- **A bad Tool call no longer ends the Run.** A call that names an unknown Tool, or carries arguments that are not valid JSON or fail the Tool's schema, settles as a failed Tool Result that never ran. Its text names the problem and, for an unknown Tool, lists the Tools the model was shown, so the model can correct the call on its next Turn. On that Turn, every gateway sends the call back with `{}` in place of arguments that are not a JSON object, so the provider accepts the request. `MaxTurns` and `MaxToolCalls` still bound a model that keeps getting it wrong. **Breaking:** `Prompt` no longer returns `ErrToolResolutionFailure` or `ErrToolArgumentFailure` for these calls. Read the Tool Result instead: `Status == ToolResultFailed`, `Executed == false`, and a `SafeError` that starts with the error code.
- **A streamed reply is stored as its blocks, not its fragments.** The Agent kept one `ContentPart` per streamed text or reasoning delta, so a reply was saved and replayed as hundreds of fragments. It now merges contiguous text deltas, and contiguous reasoning deltas, into one Part each; a reasoning block ends when its signature arrives. `message_update` Events still carry each text delta. The Responses gateway also joins adjacent text Parts of Sessions saved before this change.
- **A reasoning block that streams no text keeps its own Part.** A `ModelReasoningDone` signed the last reasoning Part even when an earlier block had already signed it. So an Anthropic `redacted_thinking` block, a thinking block with its display omitted, or a Responses reasoning item without a summary overwrote the block before it, and that block was missing when the Session was replayed. Each such block is now stored as a reasoning Part of its own, with an empty `Text` and its artifact in `Signature`.
- **`TextOf` returns text only; `ReasoningOf` returns reasoning.** `gotato.TextOf` used to join text and reasoning Parts, so a model's thinking leaked into final answers (`FinalText`), compaction summaries, CLI output and the `content` the chat gateway sends back. **Breaking:** code that wanted the thinking must call `gotato.ReasoningOf`.

  ```go
  // before: text and reasoning together
  all := gotato.TextOf(message)
  // after
  answer := gotato.TextOf(message)
  thinking := gotato.ReasoningOf(message)
  ```
- **A Turn that the output limit cut off can continue.** With `CoreLimits.MaxTruncatedContinuations` above 0, a Turn that stopped at the output limit before any Tool call goes on with `TruncatedContinuationText`, up to that many times per Run, instead of settling the Run on the cut-off reply. Off by default.

- **Context inspection shows what the next Turn sends.** `Runner.Inspect`, and with it `gotato context inspect` and `build`, `GET /v1/sessions/{id}/context` and the gRPC `Context` method, now builds the Agent a Run would build and asks it. The request it reports includes the Agent's `ContextTransformer` and `MessageConverter` extensions, its tools and its model options (`request.options`), so its `prefix_hash` equals the one the next Turn's `context_built` Event reports. The report gains `compaction_pending` and `compact_ceiling`. In Go, the core Agent implements `ContextInspector` (`InspectContext`, which fails with `ErrBusy` during a Run), and `modelctx.ReportOf` turns its `ContextInspection` into a report; `AutoCompactor` gains `Pending` and `Ceiling`. Inspection runs the context extensions and the tool sources, so they must not have side effects. **Breaking:** `RuntimeAgent` now includes `ContextInspector`, so a type of your own that implements `RuntimeAgent` must add `InspectContext`.

- **The Runner times the store with Events.** It emits `session_loaded` before `agent_start` and `session_saved` after `agent_end`, for each load and save a Run makes, with `session_id`, `messages` and the store's time in `elapsed_ms` (fractional milliseconds). A one-shot Run that creates its Session also emits a `session_saved` before `agent_start`. They reach the `StreamRun` sink, the HTTP and gRPC streams, `gotato run --events jsonl` and the `EventObserver`s in `AgentSpec.Extensions`, and are not recorded in the Session. A failed load or save is marked `failed: true`, without the store's error text. A stream's first Event is now one of these, not `agent_start`; consumers already have to tolerate unknown kinds.
- **A Run loads its Session once.** The Runner loaded an existing Session before taking its lock and again after, so every Run read the store twice. It now reads it once, under the lock. A Run on a Session that does not exist still fails with `session.ErrNotFound`, now after the lock is taken.
- **Every request carries the Session's prompt cache key.** `ModelRequest.CacheKey` groups the requests that share a cacheable prefix; like the cache breakpoints, it never changes the prompt or the prefix hash. `gotato.WithCacheKey` sets it on an Agent, and the Runner gives each Session its own, `service.CacheKey(sessionID)`, a hash of the ID so a caller's ID never reaches the provider. A Spec's `Options` may set another. Context inspection shows it as `request.cache_key`. `Runner.Fork` gives the child its parent's key in `gotato.cache_key` (`service.MetaCacheKey`), so a fork reads its parent's cached prefix, and the Runner sends that key for any Session that has one.
- **The message cache breakpoint is on the last message.** `AssembleRequest` marked the message before the last one, so on Anthropic each Turn left its newest message out of the cache. The marker is now on the last message, a one-message request included, so the whole request is cached for the next Turn. **Breaking** only for code that read `CacheBreakpoints` and expected `Index` to be `len(Messages)-2`.

### Measurement

- **The reference workload has an agentic task.** `testkit.SupportCaseTask` (`support_case`) is one Run of five Turns of the model's own choosing: it lists a customer's orders, reads the most recent one and the refund policy, refunds the damaged item with `orders.refund` (Effect `write_external`), and reports the refund ID. Its instruction is a support playbook long enough for a provider's prompt cache, and an older order with the same item is past the refund window, so a wrong choice fails and must be corrected. `ReferenceTasks` now returns seven tasks.
- **Startup and footprint are measured.** `bench/refservice` is the smallest service a team would write around Gotato, in a Go module of its own and checked in CI. `BenchmarkStartup` there times it from process start to the first 200 from `/healthz` (GOALS.md G-B10), and `footprint.sh` prints its modules and stripped binary size as JSON (G-B12).
- **The store's time and the time to the first token come from Events.** `service/workload` reads the store's time from `session_loaded` and `session_saved` instead of a timer around the store, and reports `before_request_per_run_ms` (from a Run's start to its first request) next to `model_first_token_ms` (the model's own time to its first text token), for G-B06.
- **The reference workload and the live run.** `testkit.ReferenceTasks` returns the tasks GOALS.md measures, each with a fake model and a check of its answers: a lookup, a 10-prompt conversation, a tool error and its recovery, and lookups in Sessions of 10, 100 and 1,000 messages. `service/workload` plays them through a Runner, one at a time or under load, and reports failures by cause, token usage and cost, the cache hit rate, Gotato's time per Turn next to the model's, the time to load a Session, and whether each Turn's Context matches its inspection. `gotato-workload` prints that report as JSON, and with `-live` plays the tasks against the provider in your gateway configuration, at the price you pass with `-price`.

### Providers

- **The gateway sends the prompt cache key.** `cache_key_field` names the body field that carries `ModelRequest.CacheKey`, and `cache_key_header` the header (`Config.CacheKeyField`, `Config.CacheKeyHeader`). On `openai-responses` the field defaults to `prompt_cache_key`; set `cache_key_field: none` to send none. Chat completions send no field unless you name one, and `anthropic-messages` refuses a field, because it caches by `cache_control`. A provider that keys its cache by a session header needs `cache_key_header`, such as `session-id`: on such an endpoint, the reference workload's `history_100` went from no cached input to 93% from the second Turn on, over five plays. A restarted stream keeps the header. Requests to `openai-responses` now carry `prompt_cache_key` whenever the Agent has a cache key, which every Run through a Runner has.
- **Tool names on the wire stay readable.** Every adapter sends a Tool ID that providers accept (`^[a-zA-Z0-9_-]{1,64}$`) unchanged, and replaces each other character with `_`, so `demo.echo` goes out as `demo_echo`. When two IDs end up with the same name, an ID that providers accept keeps it, and the other gets a readable prefix plus a short hash; so does an ID longer than 64 characters. Unsafe IDs used to go out as `gotato_` plus their base64 encoding, which models copied back wrong. Mapping a name back to its Tool ID is unchanged.
- **The chat, Responses and Anthropic Messages gateways restart a stream that breaks before any output.** A stream that broke after its headers (a connection reset, or a close before `[DONE]`, `response.completed` or `message_stop`) used to fail with a raw `unexpected EOF`, or as `model_protocol_failure`. While nothing has reached the caller, the gateway now sends the request again, within `max_retries`. Once output was delivered, it returns a `*gateway.Error` with `Retryable: true` and a message that contains `stream interrupted:`, which the Loop reports as `model_failure`. A chat stream that ends after a `finish_reason` without `[DONE]` now completes. **Breaking:** code that matched `model_protocol_failure` for a cut stream must match `model_failure`, and can read the cause with `errors.As(err, &gatewayErr)`.
- **The chat gateway keeps and sends back `reasoning_content`.** It dropped `delta.reasoning_content`, so a thinking model that requires it back failed mid-Run (DeepSeek answers HTTP 400). Reasoning deltas are now stored as reasoning Parts, and an assistant message's reasoning goes back as `reasoning_content`, left out when empty.
- **Per-request headers.** `gateway.WithRequestHeaders(ctx, headers)` adds headers to the requests of one Run, for a provider that routes each conversation by a header. Configured and per-request headers can set `User-Agent` (default `gotato/0.1`) but never the authentication, content-type or streaming headers.
- **The chat gateway counts cache reads.** `Usage.CacheReadTokens` is filled from `prompt_tokens_details.cached_tokens` and DeepSeek's `prompt_cache_hit_tokens`, as the Responses and Anthropic adapters already did.

## v0.1.1 — 2026-10-15

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
