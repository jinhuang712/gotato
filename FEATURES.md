<!-- what-we-build: drafted 2026-10-15 -->
# Gotato — Features

What Gotato does today, what it will do, and what it won't. Each feature is described from the side of the developer who embeds Gotato: the Go calls, the HTTP and gRPC routes, the CLI commands and the configuration keys. How it is built is in [DESIGNS.md](DESIGNS.md), and the terms are in [GLOSSARY.md](GLOSSARY.md).

✅ built · 🚧 built, with a gap named · ⬜ planned

Feature IDs are stable. When features merge, the lower ID stays and the others retire: G-F08 into G-F07, G-F11 into G-F10, G-F17 to G-F21 into G-F16, G-F23 into G-F22. G-F25 retired.

## Defining and running agents

### G-F01 — Agents and Runs ✅

You define an Agent once, then run it: a message goes in, the Loop works, and a settled result comes out.

- `gotato.NewAgent(options…)` builds an Agent from a model, an instruction, tools, a context builder, extensions and limits. In a service, `service.AgentSpec` names the same parts.
- `Prompt` sends a message and returns when the Run settles. `Continue` resumes a Run without a new message. `Abort` stops a Run, which still settles and returns its result.
- The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it.
- A Turn's tools run one at a time by default. With `MaxParallelTools` above 1, up to that many run at once, and their results are committed in the order the model asked for them.
- Every Run settles as `completed`, `failed`, `cancelled` or `deadline_exceeded`, with its final message and its token usage, including cache reads and writes. The final message carries the reason the model stopped. When an extension stops the Run, its reason is in the `agent_end` Event and in the Session's record of the Run.

Settings, passed with `gotato.WithLimits`. `gotato.DefaultLimits()` returns the defaults:

| Setting | Default | Why it exists |
|---|---|---|
| `MaxTurns` | 32 | stops a Loop that never reaches an answer |
| `MaxToolCalls` | 128 | stops a Run that calls tools without end |
| `MaxMessages`, `MaxTranscriptBytes` | 1,000 messages, 32 MiB | keeps one conversation from growing without bound |
| `MaxMessageBytes`, `MaxToolResultBytes` | 4 MiB each | keeps one reply or one tool result from filling the context |
| `MaxParallelTools` | 1 | tools run in order, unless the team knows they are safe to run together |
| `RunDeadline` | 5 minutes | a Run always ends, even when a call hangs |
| `ModelCallDeadline`, `ToolCallDeadline` | 2 minutes each | one stuck call ends instead of holding the whole Run |

### G-F26 — Steering a Run 🚧

While a Run works, you can change its course without starting over.

- `Steer` adds a message to the running Loop, and the model reads it on its next call (up to 8 waiting, `MaxSteerMessages`).
- `FollowUp` adds a message the Run picks up when it would otherwise settle, so the same Run keeps going. Its Turns count toward the same `MaxTurns` (up to 4 waiting, `MaxFollowUpMessages`).
- A message still waiting when the Run ends is dropped, not kept for the next Run.

Gap: both work only on an Agent you build yourself with `gotato.NewAgent`. The Runner builds a private Agent for each Run, so its Go API, the HTTP and gRPC endpoints and the CLI cannot reach them. What should happen to a message that arrives mid-Run is still open ([DESIGNS.md → Open questions](DESIGNS.md#open-questions)).

### G-F02 — Sessions ✅

A Session is one conversation that runs an Agent. It keeps everything that happened, so the next message continues where the last one ended.

- You create a Session with `POST /v1/sessions` or `gotato session create`, optionally with your own ID, metadata, instruction, panel and compaction ceiling. `POST /v1/runs` creates a Session and runs it in one call.
- A Session keeps every Message, a record of each Run, its token usage, its 1,000 most recent Events and every compaction.
- Forking a Session starts a new one from its history and records the parent.
- One Session runs one Run at a time. A second Run on a busy Session is rejected, or waits, as the Runner's busy policy says (G-F27).

Per-Session settings live in the Session's metadata, so one Agent can serve Sessions that differ slightly:

| Key | Effect | Why it exists |
|---|---|---|
| `gotato.agent` | the Agent this Session runs | one Runner serves several Agents |
| `gotato.instruction` | replaces the Agent's instruction for this Session | a caller tailors one conversation without a new Agent |
| `gotato.panel` | panel items for this Session: `time`, `cwd` | some conversations need the time or the directory, others do not |
| `gotato.compact_ceiling` | this Session's compaction budget, in estimated tokens | a long-lived Session can compact sooner or later than the default |
| `gotato.tool.<id>` | a tool switched on or off for this Session | a caller narrows or widens one conversation's tools |
| `gotato.model` | the model last used; written by Gotato, for information | shows which model produced the history |

## What the model sees

### G-F04 — Context ✅

On every Turn, Gotato builds what the model sees from the Session, in a fixed order, so the provider can reuse the cached prefix from one Turn to the next:

```text
instruction · Static blocks  →  tools, sorted by ID  →  the history  →  the Panel, on the last message
```

- **Static blocks** are reference text the Agent carries, such as a schema or a runbook. You attach them with `modelctx.WithStatic` and the `Resource`, `Text` and `JSON` helpers.
- **The Panel** is small content that changes every Turn. It is never saved. `time` and `cwd` are built in, and `modelctx.WithPanel` adds your own.
- Gotato marks cache points after the system part, after the tools and before the last message. For Anthropic, they become `cache_control` (the last four are kept).
- Another way to choose what the model sees, such as retrieval or a window, is your own `ContextBuilder` ([G-N10](#g-n10--context-organization-beyond-full-history-belongs-to-applications)).

### G-F05 — Context inspection 🚧

You can see what the model would receive now, without calling it.

- `gotato context inspect <id>`, `gotato context build <id>`, `GET /v1/sessions/{id}/context` and `modelctx.InspectSession` return the request as the Context builder makes it.
- The report includes the prefix hash, the size of the system part and of the Panel, and an estimate of the tokens (bytes ÷ 4).
- Every Turn also emits a `context_built` Event with the prefix hash of the request it sent.

Gap: the report leaves out what extensions change afterwards (`ContextTransformer`, `MessageConverter`), an auto-compaction that has not run yet, and the model options. So a request can differ from its report ([GOALS.md](GOALS.md) G-G03).

### G-F06 — Compaction ✅

An old part of the history can be replaced by one summary, so a long conversation stays within the model's window.

- On demand: `gotato context compact <id> --keep N` or `POST /v1/sessions/{id}/compact`.
- Automatically: at the start of a Run, when the estimated tokens pass the ceiling.
- The summary comes from truncation or from a model (`--summarizer truncate|model`).
- Every compaction is recorded in the Session: what it replaced, with what, and the sizes before and after.

Settings: a ceiling per Agent in the service, and per Session with `gotato.compact_ceiling`. Off by default (a ceiling of 0), because compaction loses detail, and the team decides when that trade is worth it.

## Tools

### G-F09 — Tools and their Effect 🚧

A tool is one thing an agent can do, such as a lookup in your database.

- `gotato.NewFuncTool` turns a Go function into a tool. Its input schema comes from the Go type, and every call is checked against that schema before the tool runs. The check covers `type`, `enum`, `required`, `properties` and `additionalProperties`; other keywords pass through unchecked.
- Every tool declares its Effect: `read` < `write_local` < `write_external` < `destructive`. A tool that declares nothing counts as destructive. `gotato.ToolWithEffect` sets the Effect on any tool, and `gotato tools list --json` shows a declared Effect (a tool with none shows no `effect` field).
- A tool result carries a status. A failure carries a safe message for the model.

Gaps:

- **A bad tool call fails the whole Run.** When the model names a tool that does not exist, or sends arguments that are not valid JSON or fail the schema check, the Run fails instead of returning the reason to the model so it can correct the call ([TODO.md](TODO.md); [GOALS.md](GOALS.md) G-G04).
- **Effect cap per Agent** is not built. An Agent cannot yet limit its tools to a highest Effect, such as read only ([DESIGNS.md → Tools and safety](DESIGNS.md#tools-and-safety)).

### G-F10 — Tool sets and the registry ✅

The tools an agent has can change while it runs.

- A registry lets you register, remove, switch on and switch off tools at any time. The Agent reads its tool sources again at the start of every Turn.
- A tool set groups tools the model can switch on itself, by calling `activate_toolset` (up to 16 active, `MaxActiveToolSets`).
- For one Session: `gotato tools activate <id> --session ID` and `deactivate`, stored as `gotato.tool.<id>`.
- Tools come from your own Go code. The CLI ships `demo.echo` and `time.now` for diagnostics only.

## Models

### G-F07 — Models and providers ✅

A provider is configuration, not code. Any vendor that speaks a supported wire API works without new code, and you can always bring your own client by implementing `gotato.Model`.

| Wire API (`api:`) | Also accepted as |
|---|---|
| `openai-chat-completions` (the default) | `openai-completions` |
| `openai-responses` | `openai-codex-responses` |
| `anthropic-messages` | `anthropic` |

- Gotato retries a call that never got a response, or that failed with 429 (except a hard quota), 500, 502, 503, 504 or 529, or whose error says the provider is rate-limited or overloaded. A provider's `Retry-After` or `retry-after-ms` wins over Gotato's own backoff.
- Without a YAML file, `gateway.NewFromEnv()` reads `GOTATO_GATEWAY_API`, `GOTATO_GATEWAY_BASE_URL`, `GOTATO_GATEWAY_ENDPOINT`, `GOTATO_GATEWAY_API_KEY` and `GOTATO_GATEWAY_MODEL`, and falls back to `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL`.
- A reply cut off by the output limit reports `max_tokens`, even next to tool calls.

Settings, in the gateway's YAML file (`--gateway-config`, or `GOTATO_GATEWAY_CONFIG`, else `gateway.yaml`):

| Key | Default | Why it exists |
|---|---|---|
| `api` | `openai-chat-completions` | picks the wire API the provider speaks |
| `endpoint` or `base_url` | — | where the provider listens |
| `api_key` | — | the provider's key. Write `${NAME}` to read it from the environment, so the file can be committed. |
| `auth.type` | `api_key`, the only scheme | the gateway never logs in interactively or reads another program's credentials |
| `model` | — | the model to call |
| `headers` | none | headers some providers or proxies require |
| `max_retries`, `no_retries` | 2 retries | providers shed load briefly; a retry usually succeeds |
| `retry_backoff` | 200 ms, growing per attempt, with jitter | spreads retries out so they do not land at once |

## Storage

### G-F03 — Stores 🚧

Every Session is saved to a store, and you choose which.

- **In memory:** for tests and short-lived programs.
- **One JSON file per Session,** written atomically. The CLI uses this store, in `--store DIR` (default `$GOTATO_HOME/sessions`, else `~/.gotato/sessions`).
- **Your own adapter:** four calls (save, get, list, delete) onto the database you run ([G-N08](#g-n08--storage-engines-belong-to-applications)).
- A saved Session has a versioned shape (`schema_version`, now 1). A document from a newer version is refused, never misread.

Gap: a save replaces the whole Session, once per Run, with no check for a newer copy. A crash mid-Run loses the Run, and two instances can overwrite each other. The fixes are planned in G-F27.

## Many instances

### G-F27 — Service: many instances 🚧

A Runner hosts Sessions inside your service, on as many instances as you run.

| Capability | Status | What happens |
|---|---|---|
| Busy policy | ✅ | a second Run on a busy Session is rejected with 409 (`reject`, the default), or waits (`wait`) |
| Capacity | ✅ | past `MaxActiveRuns`, a new Run is rejected with 429; off by default |
| Drain | ✅ | on shutdown, the Runner stops taking Runs, waits for the active ones, then cancels them. `/readyz` answers 503 while it drains, so a rolling deploy stops sending traffic. |
| Save every Turn | ⬜ | a crash loses at most the Turn in flight ([GOALS.md](GOALS.md) G-G01) |
| Conflict on a stale save | ⬜ | a save from an old copy fails instead of overwriting a newer one |
| Session ownership across instances | ⬜ | one instance runs a Session at a time, and a second instance stops before it calls the model (G-G08). Cancel and stream requests reach the owner from any instance. |
| Warm Session and TTL | ⬜ | a Session stays in memory while in use, and leaves it after 60 idle minutes; the store stays the truth |

Gap: today the lock on a Session works within one instance only, and cancel and stream requests work only on the instance that runs the Session.

Settings, on `service.Config` or `gotato serve`:

| Setting | Default | Why it exists |
|---|---|---|
| busy policy (`--queue`) | `reject` | a caller learns at once that the Session is busy, instead of waiting without knowing |
| `MaxActiveRuns` (`--max-runs`) | 0, no bound | the team sizes its own instances |
| drain timeout (`--drain-timeout`) | 10 seconds | long enough for most Runs to finish during a rolling deploy |

`gotato-grpc` takes `--max-runs` but not `--queue` or `--drain-timeout`, so its busy policy is always `reject`.

## Endpoints

### G-F14 — HTTP, gRPC and streaming ✅

A bot, a job or another service talks to Sessions like any other API.

- **HTTP:** mount the handler from `service/httpapi` on your own mux. The routes:

  ```text
  GET    /healthz  /readyz  /v1/agents
  POST   /v1/sessions      GET /v1/sessions      GET|DELETE /v1/sessions/{id}
  POST   /v1/sessions/{id}/fork    GET /v1/sessions/{id}/events    GET /v1/sessions/{id}/context
  POST   /v1/sessions/{id}/compact
  POST   /v1/sessions/{id}/runs    POST /v1/sessions/{id}/runs/stream
  POST   /v1/sessions/{id}/cancel  POST /v1/runs/{run_id}/cancel
  POST   /v1/runs                  POST /v1/runs/stream
  ```

- A run request carries `prompt`, or `continue: true`, and may set its own deadline with `timeout_ms`. `GET …/events` filters with `?kind=`.
- A Run that settles as failed, cancelled or past its deadline still answers 200, with the outcome in the body. Only request-level problems use error codes:

  | Code | When |
  |---|---|
  | 400 | the request is invalid |
  | 404 | the Session does not exist |
  | 408 | the caller cancelled the request |
  | 409 | the Session is busy, the Run is not active, or the Runner is draining |
  | 413 | the body is too large |
  | 429 | the Runner is at capacity |
  | 500 | a settled Run could not be saved |
  | 504 | the request's deadline passed |

- **Streaming:** the `/stream` routes send each Event as server-sent events (`event: <kind>`), and end with `event: result`. The stream answers 200 before the Run is admitted. So only a bad or oversized body gets a status code, and every other failure (busy, capacity, not found, draining) arrives in-band as `event: error`.
- **gRPC:** `gotato.v2.SessionService`, in its own Go module, so gRPC never enters a service that does not use it. Methods: `Contract`, `Agents`, `CreateSession`, `GetSession`, `ListSessions`, `DeleteSession`, `ForkSession`, `Run`, `StreamRun`, `CancelRun`, `Events`, `Context`, `Compact`. The `gotato-grpc` binary serves it on `127.0.0.1:8788` by default, with `--store`, `--gateway-config` and `--max-runs`.
- **Go API:** call the Runner directly, and subscribe to a Run's Events as they happen.
- Both contracts report their version, now `"2"`, on `GET /healthz` and through `Contract`.
- Authentication and logging are yours: wrap the HTTP handler, or add gRPC interceptors.

## Watching and extending

### G-F13 — Events 🚧

Every step of a Run is reported as an Event, so you can watch, log or measure it.

| Kinds | When |
|---|---|
| `agent_start`, `agent_end` | a Run starts, and settles with its status and stop reason |
| `turn_start`, `turn_end` | a Turn starts, and ends with its usage |
| `context_built` | the Context for a Turn is built, with its prefix hash and sizes |
| `message_start`, `message_update`, `message_end` | the model's reply arrives |
| `tool_execution_start`, `tool_execution_update`, `tool_execution_end`, `tool_result_committed` | a tool runs and its result is kept |
| `toolset_activated` | the model switches on a tool set |
| `session_compacted` | a compaction replaced part of the history; recorded in the Session only, never streamed |

Every Event of a Run carries its Run, its Turn, a sequence number and a timestamp, and the payload keys of each kind are documented with the kind. `session_compacted` is the exception: compaction writes it into the Session outside any Run, so it has no Run, Turn or sequence number.

Gap: payloads are plain key-value maps, not typed ([TODO.md](TODO.md) → Decisions), and the model's reasoning is not streamed as it arrives.

### G-F15 — Extensions ✅

Extensions add behavior at fixed points of the Loop, without forking it. You install them with `gotato.WithExtension`.

| Stage | Runs |
|---|---|
| `RunPreparer` | before a Run, and may rewrite the history first |
| `ContextTransformer` | after the Context is built, before it is sent |
| `MessageConverter` | when Messages are converted for the model |
| `PreToolUse` | before a tool runs, and may refuse the call |
| `PostToolUse` | after a tool runs |
| `EventObserver` | for every Event |
| `TurnStopper` | at the end of a Turn, and may stop the Run |

Each stage runs on the Run's own goroutine, so a slow extension slows the Run. An extension marked advisory may fail without failing the Run.

## The CLI

### G-F16 — The `gotato` CLI ✅

The CLI runs the same Runner as the service, so anything you try in a terminal behaves the same behind HTTP. The full contract, with every flag and JSON field, is in [cmd/gotato/README.md](cmd/gotato/README.md).

| Command | Does |
|---|---|
| `run [--session ID] [--model M] "prompt"` | runs a prompt, in a new or an existing Session. `--continue` resumes without a prompt, `--no-save` leaves the store untouched, `--events jsonl` streams the Events, and `--panel` and `--compact-ceiling` set this Session's panel and ceiling. |
| `session create \| list \| show \| fork \| events \| resume \| delete` | manages Sessions |
| `context inspect \| build \| compact <id>` | shows or compacts what the model sees |
| `tools list \| describe \| active \| activate \| deactivate` | shows and switches tools |
| `events --session ID [--kind K]` | prints a Session's Events |
| `serve [--addr] [--max-runs] [--queue] [--drain-timeout]` | serves the HTTP API, on `127.0.0.1:8787` by default |
| `doctor` | reports the Go version, and checks the store, the models and the gateway configuration |
| `version` | prints the version |

- `--json` and `--jsonl` print data on stdout, and diagnostics go to stderr. `--timeout` sets a command's deadline.
- Exit codes: `0` success, `1` runtime error, `2` usage error, `3` not found, `4` the Run did not complete.
- Built-in models need no credentials: `echo` (the default) and `demo`. `gateway` uses the YAML provider.
- Environment: `GOTATO_HOME`, `GOTATO_MODEL`, `GOTATO_GATEWAY_CONFIG`.

## Testing

### G-F22 — Test kit 🚧

You test agents without a network or a paid model.

- `testkit` ships a scripted model (`FakeModel`), a model that replays recorded replies (`ReplayModel`), a fake tool, an Event recorder, a ready-made Session, and the `echo` and `demo` models.
- CLI scenarios run in-process and against the built binary, and assert on JSON.

Gap: the tools that [GOALS.md](GOALS.md) needs are planned and not built: a reference workload, fault injection (crashes, client disconnects), and a harness that runs several instances over one store.

## Later

Ideas worth building once they are needed. None has a milestone yet.

| Idea | Note |
|---|---|
| G-F12 — MCP integration | MCP servers as a tool source, with their annotations mapped to an Effect. Reaches tools a team already runs as MCP servers. |
| Telemetry adapters | an observer package that maps Events onto OpenTelemetry. Teams can write their own today ([G-N11](#g-n11--telemetry-backends-belong-to-applications)). |
| Model catalog | model names, context sizes and prices next to the providers |
| Provider rate-limit information | limits, remaining calls and reset time reported with usage, so a service can slow itself down |
| Request IDs and idempotency keys | on the HTTP and gRPC endpoints |
| Streamed reasoning | the model's reasoning as it arrives (`reasoning_update`) |
| Typed Event payloads | a decision still open ([TODO.md](TODO.md) → Decisions) |
| G-F24 — Examples | one-shot use, a persistent Session, compaction, forks, dynamic tools, CLI automation |
| Token budget per Run | the turn and byte limits already bound a runaway loop |
| Paging and filtering the Session list | matters at population scale, after ownership across instances |
| Loading only recent history | decided together with how a Turn is saved ([DESIGNS.md → Open questions](DESIGNS.md#open-questions)) |
| Store conformance suite | lets an adapter prove it meets the store contract ([DESIGNS.md → Open questions](DESIGNS.md#open-questions)) |

## Not planned

What Gotato will not build, and why. Look here before proposing a feature. Each item belongs to the application, an MCP server, or the tools themselves.

### G-N01 — Agent Organization Belongs to Applications

Gotato provides peer agents. Applications compose roles, teams, and hierarchies.

### G-N02 — Task Graphs, Scheduling, and Orchestration Belong to Applications

Durable application tasks, task dependency graphs, task scheduling, multi-agent orchestration, integration pipelines, and project-level planning live in the application.

### G-N03 — Global Resource Scheduling Belongs to Applications

Applications that run many agents own global concurrency, CPU and memory protection, build and test limits, provider quotas, and other system-level resource policy. Gotato provides cancellable, lightweight Runs that those schedulers control. The Runner only bounds concurrent Runs and keeps one Run per Session (G-F27).

### G-N04 — Product UI Belongs to Applications

IDEs and desktop products build on Gotato. Its official CLI is a runtime interface and a diagnostic tool.

### G-N05 — Service Mode Is Optional

`gotato serve` and `gotato-grpc` are first-class ways to run Gotato, and library and CLI use stand on their own. All three drive the same Runner, and the core packages never depend on the service.

### G-N06 — Memory Architecture Belongs to Applications

Gotato provides Sessions, Context, the store contract and compaction. Applications choose their own long-term memory.

### G-N07 — Ordinary Go Is the Workflow Language

Ordinary Go composition is the default. A workflow package, if one ever exists, is optional and leaves the Loop unchanged.

### G-N08 — Storage Engines Belong to Applications

Gotato defines the store contract and the stable shape of a saved Session, and ships the in-memory and file stores. An application backs the store with the database it already runs, MySQL, Postgres, Mongo or another, through an adapter that owns the database's schema and migrations.

### G-N09 — Tool Catalogs Belong to MCP Servers and Applications

Gotato provides the tool contract, tool sets and the registry. Filesystem, Git, HTTP and other tools, and adapters for skill formats such as SKILL.md (Agent Skills), come from MCP servers or the application.

### G-N10 — Context Organization Beyond Full History Belongs to Applications

Gotato ships full history, Static blocks, the Panel and compaction. Retrieval, windowing, summarization pipelines and other ways to choose what the model sees are the application's own `ContextBuilder`.

### G-N11 — Telemetry Backends Belong to Applications

Gotato emits Events with a stable shape. Mapping them onto OpenTelemetry or another telemetry framework is an observer in the application or in an extension package.

### G-N12 — Tool Approval Belongs to the Tools

An Agent's tools are written by the team and allowed by name, and an Agent caps the Effect its tools may have. A tool that is risky for some arguments refuses those arguments in its own code. A team that wants an extra check before a call installs a `PreToolUse` extension. Gotato has no built-in approval system.

### G-N13 — No Cloud Coding Agent

Gotato leaves coding to developers' machines and to coding products. Sessions in the cluster investigate, decide and answer ([PHILOSOPHY.md, question 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)).

### G-N14 — No Remote Dev Environment, Shell or Sandbox in the Pods

A developer's environment does not move into the cluster. A Session acts only through the tools its Agent allows, and work that needs a machine runs on a laptop, in CI, or in a coding product.

### G-N15 — No General-Purpose Assistant

Gotato is for purpose-built agents. A general agent needs people to step in, and a purpose-built agent that needs people needs fixing ([PHILOSOPHY.md, question 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

### G-N16 — No Separate Platform

A platform beside your services is a second system to deploy and operate. Gotato is a library inside the services you already run.

### G-N17 — No Infrastructure

Discovery, routing, autoscaling and deployment already exist where your services run. Gotato behaves correctly when they scale it, and leaves them alone.

### G-N18 — No Ready-Made Clients

Bots, schedulers and front ends are the callers' own code. Gotato provides the HTTP handler, the gRPC service and the Go API they call.
