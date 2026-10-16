<!-- what-we-build: approved 2026-10-16 -->
# Gotato — Features

What Gotato does today, what it will do, and what it won't. Each feature is described from the side of the developer who embeds Gotato: the Go calls, the HTTP and gRPC routes, the CLI commands and the configuration keys. How it is built is in [DESIGNS.md](DESIGNS.md), when the planned parts arrive is in [ROADMAP.md](ROADMAP.md), and the terms are in [GLOSSARY.md](GLOSSARY.md).

## TL;DR

Every capability by group, with the feature it belongs to and its status: ✅ built, 🚧 built with a gap named, ⬜ planned. The details follow below, in the same order.

**Defining and running agents**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Define an Agent | from a model, an instruction, tools, a context builder, extensions and limits | G-F01 | ✅ |
| Run, continue and abort | `Prompt` returns when the Run settles; `Continue` resumes it; `Abort` stops it | G-F01 | ✅ |
| The Loop | calls the model and the tools it asks for, until it answers or a limit stops it | G-F01 | ✅ |
| Parallel tools | up to `MaxParallelTools` at once, results committed in the model's order | G-F01 | ✅ |
| Settled results | every Run ends `completed`, `failed`, `cancelled` or `deadline_exceeded`, with its usage | G-F01 | ✅ |
| Limits | Turns, tool calls, messages, bytes and deadlines, each with a default | G-F01 | ✅ |
| Continue a cut-off Turn | ask the model to go on after the output limit (`MaxTruncatedContinuations`) | G-F01 | ✅ |
| Steer | add a message the model reads on its next call | G-F26 | 🚧 |
| FollowUp | queue a message that keeps the Run going when it would settle | G-F26 | 🚧 |
| Runtime notices | tell the model something that is not the user's words, as a developer or system message after every tool result | G-F29 | ⬜ |
| A last Turn that answers | at `MaxTurns`, the last Turn goes without tools, so the Run ends with an answer | G-F30 | ⬜ |
| Sessions | one conversation: every Message, each Run's record, usage, recent Events and compactions | G-F02 | ✅ |
| Create a Session | over HTTP or the CLI, with your own ID, metadata, instruction and ceiling | G-F02 | ✅ |
| Fork | start a new Session from another one's history | G-F02 | ✅ |
| One Run per Session | a second Run on a busy Session is rejected or waits | G-F02 | ✅ |
| Per-Session settings | the Agent, instruction, compaction ceiling and tools, in metadata | G-F02 | ✅ |

**What the model sees**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Context | built every Turn in a fixed order, so the provider's cache keeps working | G-F04 | ✅ |
| Static blocks | reference text the Agent carries, such as a schema or a runbook | G-F04 | ✅ |
| Cache markers | after the system part, the tools and on the last message; `cache_control` on Anthropic | G-F04 | ✅ |
| Cache key | one key per Session on every request, so the provider serves a conversation from one prompt cache | G-F04 | ✅ |
| Your own `ContextBuilder` | retrieval, a window, or any other way to choose what the model sees | G-F04 | ✅ |
| Context inspection | the request the next Turn sends, with its prefix hash, without calling the model | G-F05 | ✅ |
| `context_built` Event | the prefix hash of the request each Turn sent | G-F05 | ✅ |
| Compaction on demand | from the CLI or over HTTP | G-F06 | ✅ |
| Automatic compaction | at the start of a Run, past the ceiling; off by default | G-F06 | ✅ |
| Summaries | by truncation or by a model | G-F06 | ✅ |
| Compaction records | what was replaced, with what, and the sizes before and after | G-F06 | ✅ |
| Changes that keep the cache | a changed instruction or tool set is appended, not rewritten into the prefix | G-F32 | ⬜ |

**Tools**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Go functions as tools | `gotato.NewFuncTool`, with the input schema taken from the Go type | G-F09 | ✅ |
| Schema check | every call is checked before the tool runs | G-F09 | ✅ |
| Effect | every tool declares read, write local, write external or destructive | G-F09 | ✅ |
| Bad calls go back to the model | an unknown tool or bad arguments fail as a tool result, and the Run goes on | G-F09 | ✅ |
| Effect cap per Agent | the model never sees a tool above the Agent's highest Effect | G-F09 | ✅ |
| Tool registry | register, remove, switch on and switch off tools at any time | G-F10 | ✅ |
| Tool sets | groups of tools the model switches on itself | G-F10 | ✅ |
| Tools per Session | switch a tool on or off for one Session | G-F10 | ✅ |

**Models**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Three wire APIs | OpenAI Chat Completions, OpenAI Responses and Anthropic Messages | G-F07 | ✅ |
| Your own model | implement `gotato.Model` | G-F07 | ✅ |
| Retries | on no response, 429 (not a hard quota), 500, 502 to 504, 529, or an overloaded provider; a provider's `Retry-After` wins | G-F07 | ✅ |
| Broken-stream restart | a stream that broke before any output is sent again | G-F07 | ✅ |
| Output-limit report | a reply cut off by the output limit says `max_tokens` | G-F07 | ✅ |
| Reasoning replay | chat `reasoning_content` is kept and sent back | G-F07 | ✅ |
| Cache reads | counted on every wire API | G-F07 | ✅ |
| Cache key on the wire | sent in a body field (`prompt_cache_key` on Responses) or a header, as configured | G-F07 | ✅ |
| Readable tool names | `demo.echo` goes out as `demo_echo` | G-F07 | ✅ |
| Per-request headers | `gateway.WithRequestHeaders`, for one Run | G-F07 | ✅ |
| Configuration | a provider is a few lines of YAML, or environment variables | G-F07 | ✅ |
| Structured output | an Agent can require its final reply to fit a JSON schema | G-F28 | ⬜ |
| Recovering a cut stream | a stream cut after some output goes on from where it stopped | G-F31 | ⬜ |

**Storage**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Memory store | for tests and short-lived programs | G-F03 | ✅ |
| File store | one JSON file per Session, written atomically | G-F03 | ✅ |
| Your own store | an adapter with four calls onto the database you run | G-F03 | ✅ |
| Versioned shape | a document from a newer `schema_version` is refused, never misread | G-F03 | ✅ |
| Store conformance suite | one suite that an adapter runs to prove it meets the store contract | G-F03 | ⬜ |

**Many instances**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Busy policy | a second Run on a busy Session is rejected with 409, or waits | G-F27 | ✅ |
| Capacity | past `MaxActiveRuns`, a new Run is rejected with 429 | G-F27 | ✅ |
| Drain | on shutdown, stop taking Runs, wait for the active ones, then cancel them; `/readyz` answers 503 | G-F27 | ✅ |
| Save every Turn | a crash loses at most the Turn in flight | G-F27 | ⬜ |
| Conflict on a stale save | an old copy cannot overwrite a newer one | G-F27 | ⬜ |
| Ownership across instances | one instance runs a Session at a time | G-F27 | ⬜ |
| Warm Session and TTL | a Session stays in memory while in use, and leaves after 60 idle minutes, compacted first by default | G-F27 | ⬜ |
| Prompt cache retention and keepalive | longer provider cache retention, and keeping a warm Session's cache from expiring | G-F33 | ⬜ |

**Endpoints**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| HTTP API | a handler you mount on your own mux | G-F14 | ✅ |
| Streaming | each Event as server-sent events, ending with the result | G-F14 | ✅ |
| gRPC | `gotato.v2.SessionService`, in its own Go module | G-F14 | ✅ |
| Go API | call the Runner directly, and subscribe to a Run's Events | G-F14 | ✅ |
| Contract version | reported on `GET /healthz` and through `Contract` | G-F14 | ✅ |
| Access hooks | the caller's identity reaches every extension and store call, and a "not allowed" error answers 403 | G-F14 | ⬜ |

**Watching and extending**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Events | every step of a Run, with its Run, Turn, sequence number and time | G-F13 | 🚧 |
| Store Events | `session_loaded` and `session_saved` time each load and save a Run makes | G-F13 | ✅ |
| Extensions | seven stages around the Loop, from `RunPreparer` to `TurnStopper` | G-F15 | ✅ |
| Advisory extensions | may fail without failing the Run | G-F15 | ✅ |

**The CLI**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Commands | run, session, context, tools, events, serve, doctor and version | G-F16 | ✅ |
| Machine-readable output | `--json` and `--jsonl` on stdout, and fixed exit codes | G-F16 | ✅ |
| Built-in models | `echo` and `demo`, with no credentials | G-F16 | ✅ |

**Testing**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Fake models | scripted (`FakeModel`), replayed (`ReplayModel`), or answering from the request (`FuncModel`) | G-F22 | ✅ |
| Test helpers | a fake tool, an Event recorder and a ready-made Session | G-F22 | ✅ |
| CLI scenarios | in-process and against the built binary, asserting on JSON | G-F22 | ✅ |
| Reference workload | seven tasks with fake models, from a lookup to a multi-step support case, played through a Runner and measured | G-F22 | ✅ |
| Live run | the same tasks against your provider, with its price, on demand | G-F22 | ✅ |
| Reference service | the smallest service that embeds Gotato, timed from start to ready and measured for size | G-F22 | ✅ |
| Fault injection and a multi-instance harness | crashes, client disconnects, and several instances over one store | G-F22 | ⬜ |
| API-compatibility check | CI flags a removed or changed exported identifier, so a break cannot slip in unwritten | G-F22 | ⬜ |

**Later**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| MCP integration | MCP servers as a tool source | G-F12 | later |
| Telemetry adapters | Events mapped onto OpenTelemetry | — | later |
| Model catalog | model names, context sizes and prices | — | later |
| Provider rate-limit information | limits and reset time, reported with usage | — | later |
| Request IDs and idempotency keys | on the HTTP and gRPC endpoints | — | later |
| Streamed reasoning | the model's reasoning as it arrives | — | later |
| Typed Event payloads | payloads with a fixed, typed shape; how is still open | — | later |
| Examples | small programs for common uses, such as a persistent Session or forks | G-F24 | later |
| Token budget per Run | a cap on the tokens one Run may spend | — | later |
| Paging and filtering the Session list | for services with many Sessions | — | later |
| Loading only recent history | instead of the whole Session | — | later |

**Not planned**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Agent organization | roles, teams and hierarchies belong to applications | G-N01 | not planned |
| Task graphs and orchestration | tasks, scheduling and pipelines belong to applications | G-N02 | not planned |
| Global resource scheduling | concurrency, quotas and resource policy belong to applications | G-N03 | not planned |
| Product UI | IDEs and desktop products build on Gotato | G-N04 | not planned |
| A required service | the service is optional; library and CLI use stand on their own | G-N05 | not planned |
| Memory architecture | long-term memory belongs to applications | G-N06 | not planned |
| A workflow language | ordinary Go is the workflow language | G-N07 | not planned |
| Storage engines | adapters onto your database belong to applications | G-N08 | not planned |
| Tool catalogs | tools come from MCP servers and applications | G-N09 | not planned |
| Context organization beyond full history | retrieval and windowing are your own `ContextBuilder` | G-N10 | not planned |
| Telemetry backends | mapping Events onto a backend belongs to applications | G-N11 | not planned |
| Tool approval | tools refuse unsafe arguments themselves | G-N12 | not planned |
| Cloud coding agent | coding stays on developers' machines and in coding products | G-N13 | not planned |
| Remote dev environment, shell or sandbox | work that needs a machine runs elsewhere | G-N14 | not planned |
| General-purpose assistant | Gotato is for purpose-built agents | G-N15 | not planned |
| Separate platform | Gotato is a library inside your services | G-N16 | not planned |
| Infrastructure | discovery, routing and autoscaling already exist | G-N17 | not planned |
| Ready-made clients | bots, schedulers and front ends are the callers' own code | G-N18 | not planned |

Feature IDs are stable. When features merge, the lower ID stays and the others retire: G-F08 into G-F07, G-F11 into G-F10, G-F17 to G-F21 into G-F16, G-F23 into G-F22. G-F25 retired.

## Defining and running agents

### G-F01 — Agents and Runs ✅

You define an Agent once, then run it: a message goes in, the Loop works, and a settled result comes out.

```go
// error handling left out
agent, _ := gotato.NewAgent(gotato.WithModel(model), gotato.WithTool(lookup))
result, _ := agent.Prompt(ctx, gotato.UserMessage("Where is order 42?"))
fmt.Println(gotato.TextOf(*result.FinalMessage))
```

- `gotato.NewAgent(options…)` builds an Agent from a model, an instruction, tools, a context builder, extensions and limits. In a service, `service.AgentSpec` names the same parts.
- `Prompt` sends a message and returns when the Run settles. `Continue` resumes a Run without a new message. `Abort` stops a Run, which still settles and returns its result.
- The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it.
- A Turn's tools run one at a time by default. With `MaxParallelTools` above 1, up to that many run at once, and their results are committed in the order the model asked for them.
- Every Run settles as `completed`, `failed`, `cancelled` or `deadline_exceeded`, with its final message and its token usage, including cache reads and writes. The final message carries the reason the model stopped.
- When an extension stops the Run, its reason is in the `agent_end` Event and in the Session's record of the Run.

The limits are passed with `gotato.WithLimits`, and `gotato.DefaultLimits()` returns the defaults:

| Setting | Default | Why it exists |
|---|---|---|
| `MaxTurns` | 32 | stops a Loop that never reaches an answer |
| `MaxToolCalls` | 128 | stops a Run that calls tools without end |
| `MaxMessages`, `MaxTranscriptBytes` | 1,000 messages, 32 MiB | keeps one conversation from growing without bound |
| `MaxMessageBytes`, `MaxToolResultBytes` | 4 MiB each | keeps one reply or one tool result from filling the context |
| `MaxParallelTools` | 1 | tools run in order, unless the team knows they are safe to run together |
| `RunDeadline` | 5 minutes | a Run always ends, even when a call hangs |
| `ModelCallDeadline`, `ToolCallDeadline` | 2 minutes each | one stuck call ends instead of holding the whole Run |
| `MaxTruncatedContinuations` | 0, off | a model can spend a Turn's whole output limit thinking and stop before it acts. Above 0, the Run asks it to go on, up to that many times, instead of settling on the cut-off reply. |

### G-F26 — Steering a Run 🚧

While a Run works, you can change its course without starting over. This is where each kind of message enters a Run:

```text
Turn 1 ──► Turn 2 ──► Turn 3 ──► (would settle) ──► Turn 4 …
             ▲                         ▲
   Steer: read on the next       FollowUp: picked up here,
   model call                    so the same Run goes on
```

- `Steer` adds a message to the running Loop, and the model reads it on its next call. Up to 8 can wait (`MaxSteerMessages`).
- `FollowUp` adds a message the Run picks up when it would otherwise settle, so the same Run keeps going. Its Turns count toward the same `MaxTurns`. Up to 4 can wait (`MaxFollowUpMessages`).
- A message still waiting when the Run ends is dropped, not kept for the next Run.

**Gap:** both work only on an Agent you build yourself with `gotato.NewAgent`. The Runner builds a private Agent for each Run, so its Go API, the HTTP and gRPC endpoints and the CLI cannot reach them. What should happen to a message that arrives mid-Run is still open ([DESIGNS.md → Open questions](DESIGNS.md#open-questions)).

### G-F29 — Runtime notices ⬜

Gotato, or your service, sometimes has to tell the model something that is not the user's words: that the history was compacted, that this is the last Turn, or that the previous reply was cut off. Today the only place for it is a user message, so the model takes it as the user speaking, and on the Responses API a new user message hides the reasoning the Run built so far.

- A notice is a message of its own kind. Each wire API sends it the way it supports: a `developer` message on Responses, a `system` message on Anthropic models that accept one mid-conversation, and a tagged user message elsewhere.
- A notice waits until every tool call of the Turn has its result, because a provider refuses anything between a call and its result.
- G-F30 and G-F31 build on it.

**Design first:** with what happens to a message that arrives mid-Run ([ROADMAP.md → Design discussion](ROADMAP.md#design-discussion)).

### G-F30 — A last Turn that answers ⬜

A Run that reaches `MaxTurns` now fails with `limit_exceeded`, and the caller gets no answer. Instead, the Loop gives the model one last Turn to answer with what it has.

- The code enforces it, not the model: the last Turn goes with tool calls switched off (`tool_choice: none`), so the provider cannot return one, and a tool call that comes back anyway is not run.
- The tool definitions stay in the request, so the cached prefix holds.
- A runtime notice (G-F29) tells the model it is the last Turn.

**Design first:** after G-F29 ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

### G-F02 — Sessions ✅

A Session is one conversation that runs an Agent. It keeps everything that happened, so the next message continues where the last one ended.

- You create a Session with `POST /v1/sessions` or `gotato session create`, optionally with your own ID, metadata, instruction and compaction ceiling. `POST /v1/runs` creates a Session and runs it in one call.
- A Session keeps every Message, a record of each Run, its token usage, its 1,000 most recent Events and every compaction.
- Forking a Session starts a new one from its history and records the parent. The fork keeps its parent's prompt cache key, so its first Turn reads the parent's cached prefix.
- One Session runs one Run at a time. A second Run on a busy Session is rejected, or waits, as the Runner's busy policy says (G-F27).

Per-Session settings live in the Session's metadata, so one Agent can serve Sessions that differ slightly:

| Key | Effect | Why it exists |
|---|---|---|
| `gotato.agent` | the Agent this Session runs | one Runner serves several Agents |
| `gotato.instruction` | replaces the Agent's instruction for this Session | a caller tailors one conversation without a new Agent |
| `gotato.compact_ceiling` | this Session's compaction budget, in estimated tokens | a long-lived Session can compact sooner or later than the default |
| `gotato.tool.<id>` | a tool switched on or off for this Session | a caller narrows or widens one conversation's tools |
| `gotato.model` | the model last used; written by Gotato, for information | shows which model produced the history |
| `gotato.cache_key` | the prompt cache key this Session's requests carry, in place of the hash of its ID; written by a fork, from its parent | a fork shares its parent's prefix, so it should share its cache |

## What the model sees

### G-F04 — Context ✅

On every Turn, Gotato builds what the model sees from the Session, in a fixed order, so the provider can reuse the cached prefix from one Turn to the next:

```text
instruction · Static blocks  →  tools, sorted by ID  →  the history
```

- **Static blocks** are reference text the Agent carries, such as a schema or a runbook. You attach them with `modelctx.WithStatic` and the `Resource`, `Text` and `JSON` helpers.
- **Nothing that changes every Turn is added.** Such content is never cached, so it would cost its full price on every Turn. A fact the model needs now, such as the current time, comes from a tool it calls.
- Gotato marks cache points after the system part, after the tools and on the last message, so each request is cached whole for the next Turn. For Anthropic, they become `cache_control`, and the last four are kept.
- Every request also carries a cache key, so a provider that keys its prompt cache serves one conversation from one cache. The Runner gives each Session its own key, a hash of its Session ID; with `gotato.NewAgent`, you set one with `gotato.WithCacheKey`. How the key travels is the gateway's configuration ([G-F07](#g-f07--models-and-providers-)).
- Another way to choose what the model sees, such as retrieval or a window, is your own `ContextBuilder` ([G-N10](#g-n10--context-organization-beyond-full-history-belongs-to-applications)).

### G-F05 — Context inspection ✅

You can see what the model would receive now, without calling it.

- `gotato context inspect <id>`, `gotato context build <id>`, `GET /v1/sessions/{id}/context` and the gRPC `Context` method return the request the Session's next Turn would send.
- The Runner builds the Agent a Run would build and asks it, so the request includes the Agent's `ContextTransformer` and `MessageConverter` extensions, its tools and its model options.
- The report includes the prefix hash, the size of the system part, and an estimate of the tokens (bytes ÷ 4).
- `compaction_pending` says the next Run compacts the Session first, because it is past `compact_ceiling`. That Run then sends a shorter request than the report shows.
- Every Turn emits a `context_built` Event with the prefix hash of the request it sent. The reference workload checks that it equals an inspection of that Turn's Transcript ([GOALS.md](GOALS.md) G-G03).

In Go, the same request comes from the Agent itself:

```go
inspection, _ := agent.InspectContext(ctx) // fails with ErrBusy during a Run
report := modelctx.ReportOf(inspection)
```

**Limits:** inspection builds as a Run's first Turn does. It cannot know a tool set the model switches on later in a Run, or a `ContextTransformer` whose output depends on the Run or the Turn. The extensions and tool sources run during inspection too, so they must not have side effects.

### G-F06 — Compaction ✅

An old part of the history can be replaced by one summary, so a long conversation stays within the model's window.

- **On demand:** `gotato context compact <id> --keep N` or `POST /v1/sessions/{id}/compact`.
- **Automatically:** at the start of a Run, when the estimated tokens pass the ceiling.
- The summary comes from truncation or from a model (`--summarizer truncate|model`).
- Every compaction is recorded in the Session: what it replaced, with what, and the sizes before and after.

**Setting:** a ceiling per Agent in the service, and per Session with `gotato.compact_ceiling`. It is off by default (a ceiling of 0), because compaction loses detail, and the team decides when that trade is worth it. The planned compaction before a warm Session leaves memory (G-F27) is the other way round: on by default, because a Session that comes back after its TTL starts cold anyway.

### G-F32 — Changes that keep the cache ⬜

The instruction and the tools come before the history, so changing them mid-Session makes the provider send the whole history again at full price. Three things do that today: a Session's own instruction (`gotato.instruction`), a tool set the model switches on, and a tool switched on or off for one Session.

- Such a change is appended instead: a runtime notice (G-F29) for a new instruction, and the new tool's definition added after the history, while the tool list sent at the start stays as it was.
- Whether it pays depends on how often a service makes such changes, so the reference workload measures a Session that switches a tool set on before this is designed.

**Design first:** after a measurement ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

## Tools

### G-F09 — Tools and their Effect ✅

A tool is one thing an agent can do, such as a lookup in your database:

```go
lookup, _ := gotato.NewFuncTool("orders.lookup", "Finds an order by its ID", findOrder)
lookup = gotato.ToolWithEffect(lookup, gotato.EffectRead)
```

- `gotato.NewFuncTool` turns a Go function into a tool. Its input schema comes from the Go type, and every call is checked against that schema before the tool runs. The check covers `type`, `enum`, `required`, `properties` and `additionalProperties`; other keywords pass through unchecked.
- Every tool declares its Effect: `read` < `write_local` < `write_external` < `destructive`. A tool that declares nothing counts as destructive. `gotato.ToolWithEffect` sets the Effect on any tool, and `gotato tools list --json` shows a declared Effect (a tool with none shows no `effect` field).
- A tool result carries a status. A failure carries a safe message for the model.
- A call the model gets wrong fails as a tool result, and the Run goes on: an unknown tool, or arguments that are not valid JSON or fail the schema check. The result says what was wrong, and for an unknown tool it lists the tools the model was shown, so the model can correct the call on its next Turn ([GOALS.md](GOALS.md) G-G04).

**The Effect cap.** An Agent can name the highest Effect its tools may have. Here the refund tool is never offered, because the Agent only reads:

```go
agent, _ := gotato.NewAgent(gotato.WithModel(model), gotato.WithTools(lookup, refund),
	gotato.WithMaxEffect(gotato.EffectRead))
```

- A tool above the cap is hidden: it is in no model request, no inspected Context and no tool listing. That includes the tools of a ToolSet the model activates later. The tool that activates a ToolSet declares `read`, so every cap keeps it.
- A model that calls a hidden tool by a guessed name gets the failed result of an unknown tool, which lists only the visible tools, and the Run goes on.
- A tool that declares no Effect counts as destructive, so any cap below `destructive` hides it.
- No cap is the default. In a service, `service.AgentSpec.MaxEffect` sets the cap, and a Session can neither raise it nor switch on a hidden tool. Work that needs another cap is another Agent ([DESIGNS.md → The Effect cap hides tools](DESIGNS.md#the-effect-cap-hides-tools-and-only-an-agent-sets-it--2026-10-16)).
- The cap is shown by `gotato tools list` (`max_effect` in `--json`), and by `GET /v1/agents` and the gRPC `Agents` call, as `max_effect` from Agent name to cap. An Agent without a cap has no entry.
- The check is G-G05: every cap against tools of every Effect ([GOALS.md](GOALS.md)).

### G-F10 — Tool sets and the registry ✅

The tools an agent has can change while it runs.

- A registry lets you register, remove, switch on and switch off tools at any time. The Agent reads its tool sources again at the start of every Turn.
- A tool set groups tools the model can switch on itself, by calling `activate_toolset`. Up to 16 can be active (`MaxActiveToolSets`).
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

A provider is a few lines in the gateway's YAML file (`--gateway-config`, or `GOTATO_GATEWAY_CONFIG`, else `gateway.yaml`):

```yaml
api: openai-chat-completions
base_url: https://gateway.example.com/v1
api_key: ${GOTATO_GATEWAY_API_KEY}
model: your-model
```

**Retries and broken streams**

- Gotato retries a call that never got a response, or that failed with 429 (except a hard quota), 500, 502, 503, 504 or 529, or whose error says the provider is rate-limited or overloaded. A provider's `Retry-After` or `retry-after-ms` wins over Gotato's own backoff.
- On every wire API, a stream that breaks before any output reached the caller is sent again, within the retries. A break after output fails as a retryable error, because sending again would repeat that output.

**What comes back from the wire**

- A reply cut off by the output limit reports `max_tokens`, even next to tool calls.
- On the chat API, a thinking model's `reasoning_content` is kept as reasoning and sent back on the next call, as DeepSeek requires.
- Cache reads are counted on every wire API, from `cached_tokens` and DeepSeek's `prompt_cache_hit_tokens` on the chat API.
- A tool goes to the provider under its own ID when the provider accepts that name, and otherwise with each other character replaced by `_` (`demo.echo` becomes `demo_echo`), so the model reads and repeats a real name.

**The prompt cache key**

- Each request's cache key goes in the body field that `cache_key_field` names, and in the header that `cache_key_header` names, so a provider can take it in either place.
- On `openai-responses`, the field defaults to `prompt_cache_key`, the Responses API's own field. On `openai-chat-completions` no field is sent unless you name one, because some compatible servers refuse unknown fields. `anthropic-messages` takes no field and caches by `cache_control`; a header still works there.
- A provider that keys its cache by a session header needs `cache_key_header`, such as `session-id`. Without it, such a provider serves every Turn cold.

**Headers**

- `gateway.WithRequestHeaders` adds headers to one Run's requests through its context, for a provider that routes each conversation by a header.
- Neither these nor the configured `headers` can replace the authentication, content-type or streaming headers. `User-Agent` defaults to `gotato/0.1`.

All the settings in the YAML file:

| Key | Default | Why it exists |
|---|---|---|
| `api` | `openai-chat-completions` | picks the wire API the provider speaks |
| `endpoint` or `base_url` | — | where the provider listens |
| `api_key` | — | the provider's key. Write `${NAME}` to read it from the environment, so the file can be committed. |
| `auth.type` | `api_key`, the only scheme | the gateway never logs in interactively or reads another program's credentials |
| `model` | — | the model to call |
| `headers` | none | headers some providers or proxies require |
| `cache_key_field` | `prompt_cache_key` on `openai-responses`, none on chat; `none` turns it off | the body field that carries the prompt cache key |
| `cache_key_header` | none | the header that carries the prompt cache key, such as `session-id`, for a provider that keys its cache by a session header |
| `max_retries`, `no_retries` | 2 retries | providers shed load briefly; a retry usually succeeds |
| `retry_backoff` | 200 ms, growing per attempt, with jitter | spreads retries out so they do not land at once |

Without a YAML file, `gateway.NewFromEnv()` reads `GOTATO_GATEWAY_API`, `GOTATO_GATEWAY_BASE_URL`, `GOTATO_GATEWAY_ENDPOINT`, `GOTATO_GATEWAY_API_KEY` and `GOTATO_GATEWAY_MODEL`, and falls back to `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL`.

### G-F28 — Structured output ⬜

A Run can return a value, not only text: an Agent can require its final reply to fit a JSON schema.

- A pipeline step, an investigation or a bot hands its result to code, so it wants a reply in a fixed shape, not prose to parse.
- **The schema belongs to the Agent.** An Agent is purpose-built for one use. A bot that must answer in the shape of a chat card carries that card's schema, and an Agent that writes a report carries none and answers in Markdown. An Agent without a schema behaves as it does today.
- Each wire API has its own way to ask for a schema, and the gateway maps one schema onto it.
- **A reply that does not fit fails the Run.** It settles `failed`, with a reason that names the schema, and the reply stays in the Transcript, so the caller can read it or run again. Gotato does not ask the model to try again: when the model gets a case wrong, the fix is a better instruction, schema or model ([PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).
- **HTTP and gRPC change nothing.** The Agent's definition carries the schema, so a request picks the Agent as it does today, and the final message is the JSON text. A Go caller decodes it into its own type.
- **Streaming works as it does today.** A reply arrives in pieces, and it can only be checked when it is complete. For an Agent with a schema, the pieces are provisional: a reply that does not fit settles the Run as `failed` at the end, after the client has already seen the pieces.
- Gotato already checks tool arguments against a schema ([G-F09](#g-f09--tools-and-their-effect-)); that check is the likely starting point for checking a reply.

**Gap:** not built. A schema for one Run alone, rather than for an Agent, is not planned; adding it later would be an addition, not a break.

### G-F31 — Recovering a cut stream ⬜

A Gotato service runs far from its callers, on networks and providers it does not control, so a reply cut off partway must not fail the Run. Today a stream cut before any output is sent again, and a stream cut after some output fails the Run with `model_failure`.

- The Run goes on instead, one of two ways, chosen when this is designed: keep the output so far and ask the model, in a runtime notice (G-F29), to continue without repeating it; or send the request again and tell stream readers, with an Event, to drop what they received.
- The retries stay bounded, so a provider that keeps cutting off still fails the Run.

**Design first:** after G-F29 ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

## Storage

### G-F03 — Stores 🚧

Every Session is saved to a store, and you choose which:

| Store | For |
|---|---|
| In memory | tests and short-lived programs |
| One JSON file per Session, written atomically | the CLI, in `--store DIR` (default `$GOTATO_HOME/sessions`, else `~/.gotato/sessions`) |
| Your own adapter | the database you run, through four calls: save, get, list and delete ([G-N08](#g-n08--storage-engines-belong-to-applications)) |

- A saved Session has a versioned shape (`schema_version`, now 1).
- A document from a newer version is refused, never misread.
- A conformance suite in `testkit` is planned (ROADMAP Phase 4). An adapter runs one set of checks against the store contract, so a team that writes its own adapter can prove it keeps what Gotato relies on, such as a saved Session loading back unchanged and a stale save failing.

**Gap:** a save replaces the whole Session, once per Run, with no check for a newer copy. A crash mid-Run loses the Run, and two instances can overwrite each other. The fixes are planned in G-F27.

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
| Warm Session and TTL | ⬜ | a Session stays in memory while in use, and leaves it after 60 idle minutes; the store stays the truth. Before it leaves, it is compacted, unless a setting turns this off; how it is compacted is decided in a later phase. |

**Gap:** today the lock on a Session works within one instance only, and cancel and stream requests work only on the instance that runs the Session.

Settings, on `service.Config` or `gotato serve`:

| Setting | Default | Why it exists |
|---|---|---|
| busy policy (`--queue`) | `reject` | a caller learns at once that the Session is busy, instead of waiting without knowing |
| `MaxActiveRuns` (`--max-runs`) | 0, no bound | the team sizes its own instances |
| drain timeout (`--drain-timeout`) | 10 seconds | long enough for most Runs to finish during a rolling deploy |

`gotato-grpc` takes `--max-runs` but not `--queue` or `--drain-timeout`, so its busy policy is always `reject`.

### G-F33 — Prompt cache retention and keepalive ⬜

A provider keeps a cached prefix for minutes, and a conversation that pauses longer pays full price to start again.

- **Retention:** ask for the provider's longer cache retention where it offers one, such as an hour on Anthropic, per Agent.
- **Keepalive:** while a warm Session is in memory (G-F27), refresh its cache shortly before it expires. Each refresh costs a cache read of the whole prefix, so it pays only when the Session is likely to come back soon; it is a setting per Agent, with a limit.

**Design first:** with the warm Session ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)).

## Endpoints

### G-F14 — HTTP, gRPC and streaming ✅

A bot, a job or another service talks to Sessions like any other API.

**HTTP.** Mount the handler from `service/httpapi` on your own mux. It serves these routes:

```text
GET    /healthz  /readyz  /v1/agents
POST   /v1/sessions      GET /v1/sessions      GET|DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/fork    GET /v1/sessions/{id}/events    GET /v1/sessions/{id}/context
POST   /v1/sessions/{id}/compact
POST   /v1/sessions/{id}/runs    POST /v1/sessions/{id}/runs/stream
POST   /v1/sessions/{id}/cancel  POST /v1/runs/{run_id}/cancel
POST   /v1/runs                  POST /v1/runs/stream
```

- A run request carries `prompt`, or `continue: true`, and may set its own deadline with `timeout_ms`.
- `GET /v1/agents` answers `{"agents": [...], "max_effect": {name: cap}}`; an Agent without an Effect cap has no `max_effect` entry.
- `GET …/events` filters with `?kind=`.
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

**Streaming.** The `/stream` routes send each Event as server-sent events (`event: <kind>`), and end with `event: result`.

- The stream answers 200 before the Run is admitted.
- So only a bad or oversized body gets a status code. Every other failure (busy, capacity, not found, draining) arrives in-band as `event: error`.

**gRPC.** `gotato.v2.SessionService` lives in its own Go module, so gRPC never enters a service that does not use it.

- Methods: `Contract`, `Agents`, `CreateSession`, `GetSession`, `ListSessions`, `DeleteSession`, `ForkSession`, `Run`, `StreamRun`, `CancelRun`, `Events`, `Context`, `Compact`.
- The `gotato-grpc` binary serves it on `127.0.0.1:8788` by default, with `--store`, `--gateway-config` and `--max-runs`.

**Go API.** Call the Runner directly, and subscribe to a Run's Events as they happen.

**For all of them:**

- Both contracts report their version, now `"3"`, on `GET /healthz` and through `Contract`.
- Authentication and logging are yours: wrap the HTTP handler, or add gRPC interceptors.
- **Access hooks (planned, not built).** Wrapping the handler shows only the URL, not who owns a Session, and `StatusFor` has no code for "not allowed", so a refusal from a store adapter would answer 500. Planned: the caller's identity travels in `context.Context` to every extension and every store call, and a new "not allowed" error from either answers 403 on HTTP and the matching code on gRPC. Who may touch which Session stays the application's rule; Gotato builds the places to enforce it, not the rule.

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
| `session_loaded`, `session_saved` | the Runner loaded the Session before a Run, or saved it, with the message count and the store's time in `elapsed_ms`; streamed, never recorded in the Session |

- Every Event of a Run carries its Run, its Turn, a sequence number and a timestamp.
- The payload keys of each kind are documented with the kind.
- `session_compacted` is the exception: compaction writes it into the Session outside any Run, so it has no Run, Turn or sequence number.
- The Runner emits the store Events around the Loop's, to the same stream and to the `EventObserver`s in the Agent's extensions. `session_loaded` comes before the Run starts, so it has no Run and sequence 0. `session_saved` after the Run takes the Run and the next sequence number. A one-shot Run that creates its Session also emits a `session_saved` before it starts. A failed load or save is marked `failed`, without the store's error text.

**Gap:** payloads are plain key-value maps, not typed ([TODO.md](TODO.md) → Decisions), and the model's reasoning is not streamed as it arrives. A message's way into a Session (a Steer arriving or dropped, a Run waiting on a busy Session, a cancel request) has no Events yet; they are decided with what happens to a message mid-Run ([DESIGNS.md → Open questions](DESIGNS.md#open-questions)).

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

- Each stage runs on the Run's own goroutine, so a slow extension slows the Run.
- An extension marked advisory may fail without failing the Run.

## The CLI

### G-F16 — The `gotato` CLI ✅

The CLI runs the same Runner as the service, so anything you try in a terminal behaves the same behind HTTP. The full contract, with every flag and JSON field, is in [cmd/gotato/README.md](cmd/gotato/README.md).

```bash
id=$(gotato session create --json | jq -r .id)
gotato run --session "$id" --model demo --json "use-tool"
```

| Command | Does |
|---|---|
| `run [--session ID] [--model M] "prompt"` | runs a prompt, in a new or an existing Session. `--continue` resumes without a prompt, `--no-save` leaves the store untouched, `--events jsonl` streams the Events, and `--compact-ceiling` sets this Session's ceiling. |
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

### G-F22 — Test kit and measurement 🚧

You test agents without a network or a paid model, and measure Gotato on a fixed workload.

- `testkit` ships a scripted model (`FakeModel`), a model that replays recorded replies (`ReplayModel`), and a model that answers from the request it gets (`FuncModel`, for agents that share one model and send in any order).
- It also ships a fake tool, an Event recorder, a ready-made Session, and the `echo` and `demo` models.
- CLI scenarios run in-process and against the built binary, and assert on JSON.

**The reference workload.** `testkit.ReferenceTasks` returns the tasks [GOALS.md](GOALS.md) measures, each with a fake model and a check of its answers:

| Task | What it does |
|---|---|
| `lookup` | one question, one `orders.lookup` call, one answer |
| `conversation` | ten prompts on one Session, three lookups, and a last question only the history answers |
| `tool_error` | a call the schema check refuses, a lookup that fails, and a recovery |
| `support_case` | an agentic loop in one Run: the model lists a customer's orders, reads the most recent one and the refund policy, refunds the damaged item, and reports the refund ID; an older order with the same item is past the refund window |
| `history_10`, `history_100`, `history_1000` | a lookup in a Session that already holds that many messages |

`service/workload` plays the tasks through a Runner, one at a time or under load, and reports what it saw:

- how many plays passed their check;
- every failed Run, tool call and check, with its cause: the model, the provider, the tool, the store, a limit, an extension, a cancel, or Gotato;
- token usage, the cost at your price, and the cache hit rate;
- Gotato's time per Turn next to the model's, and the store's time to load and save a Session;
- the time Gotato adds before the first request, next to the model's own time to its first token;
- whether each Turn's Context matches an inspection of its Transcript.

The numbers about a Run come from its Events, the store's time included, as a team would compute them from its own telemetry.

`go test ./service/workload` runs the workload in CI, and `BenchmarkReferenceTasks` measures it.

**The live run.** `gotato-workload` runs the same workload and prints the report as JSON. With `-live`, it plays the tasks against the provider in your gateway configuration:

```bash
go run ./cmd/gotato-workload -live -gateway-config gateway.yaml -price 3,15,0.3,3.75
```

- `-price` is the model's price in US dollars per million tokens: input, output, cache read and cache write.
- `-tasks`, `-plays`, `-concurrency` and `-store DIR` shape the run, with or without `-live`.
- It exits 3 when a failure was caused by Gotato or the store, or a Turn's Context differed from its inspection.
- The live run calls a paid model, so it runs on demand, never in CI.

**The reference service.** `bench/refservice` is the smallest service a team would write around Gotato: the agent, the Runner, a file store, the HTTP handler and a gateway model, in a Go module of its own. Two tools measure it:

```bash
(cd bench/refservice && go test -run '^$' -bench Startup -benchtime 20x .)   # start to the first 200 from /healthz
bench/refservice/footprint.sh                                               # modules and stripped binary, as JSON
```

**Gap:** fault injection (crashes, client disconnects) and a harness that runs several instances over one store are planned and not built. So is an API-compatibility check in CI, which flags a removed or changed exported identifier; it is scheduled for when mass testing begins (ROADMAP Phase 9).

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

Gotato ships full history, Static blocks and compaction. Retrieval, windowing, summarization pipelines and other ways to choose what the model sees are the application's own `ContextBuilder`.

### G-N11 — Telemetry Backends Belong to Applications

Gotato emits Events with a stable shape. Mapping them onto OpenTelemetry or another telemetry framework is an observer in the application or in an extension package.

### G-N12 — Tool Approval Belongs to the Tools

An Agent's tools are written by the team and allowed by name, and an Agent can cap the Effect its tools may have, which hides every tool above it from the model. A tool that is risky for some arguments refuses those arguments in its own code. A team that wants an extra check before a call installs a `PreToolUse` extension. Gotato has no built-in approval system.

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
