<!-- what-we-build: drafted 2026-10-22 -->
# Gotato — Features

What Gotato does today, what it will do, and what it won't. Each feature is described from the side of the developer who embeds Gotato: the Go calls, the HTTP and gRPC routes, the CLI commands and the configuration keys. The principles and the broad decisions behind them are in [DESIGNS.md](DESIGNS.md); a decision that concerns one feature is kept with that feature here. When the planned parts arrive is in [ROADMAP.md](ROADMAP.md), and the terms are in [GLOSSARY.md](GLOSSARY.md). What happens when a part fails is collected in [When something fails](#when-something-fails).

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
| Interrupt | stop the step in flight and go on with a new message, in the same Run | G-F26 | ⬜ |
| Runtime notices | tell the model something that is not the user's words, in a Message of its own kind, from Gotato, your service or an extension | G-F29 | ⬜ |
| A last Turn that answers | at `MaxTurns`, the last Turn goes without tools, so the Run ends with an answer | G-F30 | ⬜ |
| Sessions | one conversation: every Message, each Run's record, usage, its Events and compactions | G-F02 | ✅ |
| Create a Session | over HTTP or the CLI, with your own ID, metadata, instruction and ceiling | G-F02 | ✅ |
| Fork | start a new Session from another one's Record; it writes the store and activates nothing | G-F02 | ✅ |
| One Run per Session | a second Run on a busy Session is rejected or waits | G-F02 | ✅ |
| Per-Session settings | the Agent, instruction, compaction ceiling and tools, in metadata | G-F02 | ✅ |

**What the model sees**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Context | built every Turn in a fixed order, so the provider's cache keeps working | G-F04 | ✅ |
| Static blocks | reference text the Agent carries, such as a schema or a runbook | G-F04 | ✅ |
| Cache markers | after the system part, the tools and on the last message, and on Claude where the Round started, or, right after a compaction, at the end of the last Segment; `cache_control` on Anthropic | G-F04 | ✅ |
| Cache key | one key per Session on every request, so the provider serves a conversation from one prompt cache | G-F04 | ✅ |
| Your own `ContextBuilder` | retrieval, a window, or any other way to choose what the model sees | G-F04 | ✅ |
| Context inspection | the request the next Turn sends, with its prefix hash, without calling the model | G-F05 | ✅ |
| `context_built` Event | the prefix hash of the request each Turn sent | G-F05 | ✅ |
| Compaction on demand | from the CLI or over HTTP | G-F06 | ✅ |
| Automatic compaction | at the start of a Run, past the ceiling; off by default | G-F06 | ✅ |
| Summaries | by truncation or by a model | G-F06 | ✅ |
| Compaction records | what was replaced, with what, and the sizes before and after | G-F06 | ✅ |
| Segments | each compaction closes the history since the last one into a Segment that is never rewritten: the conversation word for word, a line per tool call, and a Recap the Agent's model writes | G-F06 | ⬜ |
| Compaction before the cache goes cold | an idle Session is compacted shortly before its prompt cache expires | G-F06 | ⬜ |
| Compaction at the window | before any model call past the ceiling, mid-Run included, and once when the provider refuses a request as too long | G-F06 | ⬜ |
| Reading a Segment back | `read_segment` returns a Segment's Messages from the Record, tool results included | G-F06 | ⬜ |
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
| ToolSets that stay on | a ToolSet the model switched on is a fact, so the next Run, on any instance, offers it too | G-F10 | ⬜ |

**Models**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Three wire APIs | OpenAI Chat Completions, OpenAI Responses and Anthropic Messages | G-F07 | ✅ |
| Your own model | implement `gotato.Model` | G-F07 | ✅ |
| Retries | on no response, 429 (not a hard quota), 500, 502 to 504, 529, or an overloaded provider; a provider's `Retry-After` wins | G-F07 | ✅ |
| Broken-stream restart | a stream that broke before any output is sent again | G-F07 | ✅ |
| Output-limit report | a reply cut off by the output limit says `max_tokens` | G-F07 | ✅ |
| Reasoning replay | the current Round's reasoning goes back to Claude and OpenAI, and all of it to any other provider | G-F07 | ✅ |
| Provider rules | name the provider, or let the gateway recognize its own address, for its reasoning and cache rules | G-F07 | ✅ |
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
| File store | a directory per Session, one file per write, linked into place | G-F03 | ✅ |
| Your own store | an adapter with eight calls onto the database you run, each an ordinary conditional write | G-F03 | ✅ |
| Versioned shape | an Entry of an unknown kind, or a document from a newer `schema_version`, is refused, never misread | G-F03 | ✅ |
| Append-only Record | every step is appended as it happens, as facts and a log; compaction appends a Checkpoint; a load reads the facts from the last Checkpoint | G-F03 | ✅ |
| Store conformance suite | `testkit/storetest`: one suite that an adapter runs to prove it meets the store contract | G-F03 | ✅ |

**Many instances**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Busy policy | a second Run on a busy Session is rejected with 409, or waits | G-F27 | ✅ |
| Capacity | past `MaxActiveRuns`, a new Run is rejected with 429 | G-F27 | ✅ |
| Drain | on SIGTERM or an endpoint: mark the Liveness row draining, answer 503 on `/readyz`, take no new Sessions, let each Run finish, save and release, then close a stopped Run's stream with a reconnect code | G-F27 | ✅ |
| Save every step | each step is written as it happens, so a crash loses at most the step in flight | G-F27 | ✅ |
| One writer at a time | a Claim is an Entry with an Epoch, so a writer that lost it can never write; another instance stops before it calls the model | G-F27 | ✅ |
| Liveness rows | one row per instance in the store; when it expires, all of that instance's Claims end, and an instance that stops removes its own | G-F27 | ✅ |
| Any instance answers | an idle Session moves to whichever instance a request reaches; a busy one is waited for, then refused with 409 | G-F27 | ✅ |
| Load balancer setup | the endpoints read `session-id` and answer with it; hash on it, retry a 503 marked "not run" once, follow `/readyz` | G-F27 | ✅ |
| Activation limit | past a hard limit on its active Sessions, an instance refuses a new one with a 503 marked "not run" | G-F27 | ✅ |
| Warm Session and TTL | a Session stays in memory while in use, and leaves after 60 idle minutes unless its Agent sets another TTL; by default it is compacted before its cache goes cold | G-F27 | ⬜ |
| Prompt cache retention and keepalive | the provider's longest cache retention, set per model or for all of them; keeping a warm Session's cache from expiring | G-F33 | 🚧 |

**Endpoints**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| HTTP API | a handler you mount on your own mux | G-F14 | ✅ |
| Streaming | each Event as server-sent events, ending with the result | G-F14 | ✅ |
| Stream resume | reconnect on any instance and go on after the number of the last Event seen, with the Record's epoch; a stream that ends says whether and when to reconnect | G-F14 | ✅ |
| gRPC | `gotato.v2.SessionService`, in its own Go module | G-F14 | ✅ |
| Go API | call the Runner directly, and subscribe to a Run's, a Session's or the Instance's Events | G-F14 | ✅ |
| Contract version | reported on `GET /healthz` and through `Contract` | G-F14 | ✅ |
| Access hooks | the caller's identity reaches every extension and store call, and a "not allowed" error answers 403 | G-F14 | ⬜ |

**Watching and extending**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| Events | every step of a Session as a Go type of its own for each kind of Event, named for what happened | G-F13 | ✅ |
| The Record as the Event log | every stored Event is an Entry, numbered by its position, and written before it is sent | G-F13 | ✅ |
| Subscriptions | a Run's stream, a Session's from any number, and an Instance's, in the Go API; a Session never waits for any of them | G-F13 | ✅ |
| Claim Events | a Session claimed, released or fenced, and a Liveness row that lapsed | G-F13 | ✅ |
| Message Events | a message queued, committed, dropped, withdrawn or rejected, and an Abort requested; a live Session sends them, and the Runner and the endpoints with the warm Session | G-F13 | 🚧 |
| Streamed reasoning | the model's reasoning as it arrives, as `reasoning_delta` | G-F13 | ✅ |
| Extensions | six stages around the Loop, from `RunPreparer` to `TurnStopper` | G-F15 | ✅ |
| Advisory extensions | may fail without failing the Run, and say so in an `extension_failed` Event | G-F15 | ✅ |

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
| Fault injection and a multi-instance harness | a crash after every write of the reference workload, client disconnects, a stream cut at every Event, and several instances over one store behind a hashing proxy, with one killed | G-F22 | ✅ |
| API-compatibility check | CI flags a removed or changed exported identifier, so a break cannot slip in unwritten | G-F22 | ⬜ |

**Later**

| Capability | What it does | Feature | Status |
|---|---|---|---|
| MCP integration | MCP servers as a tool source | G-F12 | later |
| Telemetry adapters | an Instance's Events mapped onto OpenTelemetry | — | later |
| Model catalog | model names and prices | — | later |
| Provider rate-limit information | limits and reset time, reported with usage | — | later |
| Request IDs and idempotency keys | on the HTTP and gRPC endpoints | — | later |
| Examples | small programs for common uses, such as a persistent Session or forks | G-F24 | later |
| Token budget per Run | a cap on the tokens one Run may spend | — | later |
| Paging and filtering the Session list | for services with many Sessions | — | later |
| Loading only the recent Transcript | instead of the whole Record | — | later |

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

You define an Agent once, then run Sessions of it: a message goes in, the Loop works, and a settled result comes out.

```go
// error handling left out
s, _ := gotato.NewSession(gotato.WithModel(model), gotato.WithTool(lookup))
defer s.Close(ctx)
result, _ := s.Prompt(ctx, gotato.UserMessage("Where is order 42?"))
fmt.Println(gotato.TextOf(*result.FinalMessage))
```

- `gotato.NewSession(options…)` builds a live Session from an Agent's parts: a model, an instruction, tools, a context builder, extensions and limits. In a service, `service.Agent` names the same parts, and the Runner builds the Session.
- A `gotato.Session` is one concrete type. Its ID is the Session ID: it comes from its Transcript when that is a `session.Record`, and is generated otherwise. `Status` and `Done` report where it is in its life.
- `Prompt` sends a message and returns when the Run settles. `Continue` resumes a Run without a new message. `Abort` stops a Run, which still settles and returns its result.
- The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it.
- A Turn's tools run one at a time by default. With `MaxParallelTools` above 1, up to that many run at once, and their results are committed in the order the model asked for them.
- Every Run settles as `completed`, `failed`, `cancelled` or `deadline_exceeded`, with its final message and its token usage, including cache reads and writes. The final message carries the reason the model stopped.
- When an extension stops the Run, its reason is in the `run_ended` Event and in the Session's Record of the Run.

The limits are passed with `gotato.WithLimits`, and `gotato.DefaultLimits()` returns the defaults:

| Setting | Default | Why it exists |
|---|---|---|
| `MaxTurns` | 32 | stops a Loop that never reaches an answer |
| `MaxToolCalls` | 128 | stops a Run that calls tools without end |
| `MaxMessages`, `MaxTranscriptBytes` | 1,000 messages, 32 MiB | keeps what the model is sent from growing without bound. They count what a load reads, the last Checkpoint's Messages and what follows it, not every Message the Record holds ([G-F06](#g-f06--compaction-)). With Segments, the Messages a Segment keeps word for word count too, since a load reads them; so the count limit is raised or dropped in the change that builds Segments, and `MaxTranscriptBytes` stays. |
| `MaxMessageBytes`, `MaxToolResultBytes` | 4 MiB each | keeps one reply or one tool result from filling the context |
| `MaxParallelTools` | 1 | tools run in order, unless the team knows they are safe to run together |
| `RunDeadline` | 5 minutes | a Run always ends, even when a call hangs |
| `ModelCallDeadline`, `ToolCallDeadline` | 2 minutes each | one stuck call ends instead of holding the whole Run |
| `MaxTruncatedContinuations` | 0, off | a model can spend a Turn's whole output limit thinking and stop before it acts. Above 0, the Run asks it to go on, up to that many times, instead of settling on the cut-off reply. |

#### The live Session is `gotato.Session`, and its record is `session.Record` — 2026-10-16

The object a caller builds and talks to already has what the glossary gives a Session: an ID, a Status and its state machine, a queue for Steer and FollowUp, a Transcript, a `ContextBuilder` and its tools. It lacks only saving itself and a TTL, which Phase 6 adds. So it takes the name Session, and the stored record takes the name Record.

| Option | For | Against |
|---|---|---|
| **`gotato.Session` for the live unit, `session.Record` for what the store keeps** (chosen) | The type is named after the thing a caller holds. Phase 6 grows the same type, so the name does not change again. | The record's name is settled before Phase 6, so this rename is larger. |
| `gotato.Loop` | Matches the old plan for this rename. | The Loop is a concept, the cycle inside a Session, not a thing a caller holds. |
| A role name, such as `gotato.Executor` | The smallest change. The record keeps its name. | In Phase 6, the Session either wraps the Executor or the type is renamed again. |

Three choices come with it:

- **One concrete type, no interfaces.** `gotato.NewSession` returns a `*Session`. There is one implementation, and no caller implements the interfaces the old type was split into, so they go. A caller that wants a fake defines its own small interface, as Go code usually does.
- **One ID.** A live Session's ID is its Session ID: it comes from its record, or is generated when there is none. Every Event carries it as `session_id`.
- **Today a live Session lives for one Run.** The Runner builds one from the record, runs it and closes it, so it behaves like a Session whose TTL is zero, and the store stays the truth. Phase 6 keeps it warm.

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
- `Abort` stops the Run at once. It still settles, as cancelled, and keeps every Turn it committed.
- A message still waiting when the Run ends is dropped, not kept for the next Run.

**Gap:** Steer and FollowUp work only on a Session you build yourself with `gotato.NewSession`. The Runner builds a live Session for each Run and keeps it to itself, so its Go API, the HTTP and gRPC endpoints and the CLI cannot reach them. Stopping a Run already works, but only on the instance that runs it (`Runner.CancelSession`, `POST /v1/sessions/{id}/cancel`, gRPC `CancelRun`). Planned, as the decision below says:

- every request for a Session reaches its writer through the load balancer's hash, so the endpoints and the CLI can steer, interrupt and stop a Run in the writer's own process;
- **Interrupt** cancels a model call in flight, lets a running tool finish, and starts the next Turn of the same Run with the new message;
- a message that arrives after the Run has ended starts a new Run instead of being dropped.

Each kind takes effect at its own point:

| Kind | When it takes effect |
|---|---|
| Steer | at the next Turn boundary |
| Notice (G-F29) | right before the next model call, from the same queue as a Steer; it never starts a Run, and never keeps one going |
| FollowUp | where the Run would otherwise settle |
| Interrupt | at once: a model call in flight is cancelled, a tool in flight finishes, and the message starts the next Turn of the same Run |
| Stop | at once: everything in flight is cancelled through its `ctx`, and the Run settles as cancelled |

- An Interrupt lets a running tool finish, because a write cut in half can leave half a side effect. Stop cancels tools too, and each tool decides how to stop safely.
- A message the writer accepted but has not committed is lost if the writer dies. The caller sees its stream end and sends it again, as it would with a single instance.

#### Messages that arrive during a Run — 2026-10-16

A Session's messages reach its writer through the load balancer's hash, so each kind can take effect in the writer's own process.

| Option | For | Against |
|---|---|---|
| **Steer at a Turn boundary; Interrupt and Stop at once; a late message starts a new Run** (chosen) | The same behavior as one instance. Nothing waits in the store. | A message accepted but not yet committed is lost if the writer dies; the caller sends it again. |
| Queue every message in the store first | Survives the writer's death. | A write per message, and a writer that polls the store. |
| Reject a message while a Run is going | The simplest. | A caller cannot steer or stop a Run. |

### G-F29 — Runtime notices ⬜

Gotato, your service or an extension sometimes has to tell the model something that is not the user's words. Today the only place for it is a user message: the output-limit continuation is a user message, and so is the compaction summary, so the model takes them as the user speaking, and a new Round begins, which drops the reasoning in flight on Claude and OpenAI. A notice is a Message of its own kind for this ([DESIGNS.md → What is not the user's words goes to the model as a notice](DESIGNS.md#what-is-not-the-users-words-goes-to-the-model-as-a-notice--2026-10-18)), and a compacted stretch of history becomes a Segment, with tags of its own ([G-F06](#g-f06--compaction-)). Designed on 2026-10-18; built with the warm Session.

**The notices.** Gotato sends five kinds itself, each after the Event that causes it; a service or an extension sends its own:

| Kind | What it tells the model | Caused by |
|---|---|---|
| `continue` | the last reply did not finish, because it hit the output limit or came back empty, so go on | `reply_ended` |
| `last_turn` | this is the last Turn, so answer without tools: at `MaxTurns`, or when the Run's deadline is near ([G-F30](#g-f30--a-last-turn-that-answers-)) | the Loop's own count |
| `stream_cut` | the reply was cut off mid-stream, so go on from where it stopped without repeating it ([G-F31](#g-f31--recovering-a-cut-stream-)) | `reply_cut_off` |
| `settings_changed` | the instruction changed, or a tool came or went, told here instead of rewritten into the prefix ([G-F32](#g-f32--changes-that-keep-the-cache-)) | `metadata_changed` |
| `run_interrupted` | the last Run was stopped before it finished, so a tool may have done only part of its work | `run_ended` by a drain, or a Run found unfinished when the Session is loaded |
| your own | something that happened outside the conversation, such as an order that was delivered | your service or extension |

**How a notice is kept.** It is a Message with the role `notice`, a `kind` and a `source` (`gotato` or `service`), and, for Gotato's own, the number of the Event that caused it:

```json
{ "id": "m_60", "role": "notice",
  "notice": { "kind": "order_update", "source": "service" },
  "parts": [{ "kind": "text", "text": "Order 42 was delivered at 14:05 today." }] }
```

- It is not the user's words: it never starts a Round, and it is not counted as a user message.
- It is appended once and never changed or removed, so the cached prefix holds, and so does Claude's check of the thinking blocks after it.
- Its words state a fact. What the model should do about it belongs in the instruction.

**When it goes in.** A notice waits in the queue that holds a Steer ([G-F26](#g-f26--steering-a-run-)), in the order they arrived. The queue is emptied right before every model call, so a notice follows a user message or a Turn's Tool Results and never sits between a tool call and its result.

- A notice never starts a Run and never keeps one going. With no Run, it waits for the next one and goes in after its prompt; a notice still waiting when a Run settles waits for the next Run. A service that wants the model to react at once calls `Continue` after it.
- `notice_queued` and `notice_committed` report both steps ([G-F13](#g-f13--events-)); `notice_committed` carries the number of the `notice_queued` it came from.

**How each wire API receives it.**

| Model | Form |
|---|---|
| Claude models that take a system message mid-conversation: Fable 5.1, Mythos 5.1, Fable 5, Mythos 5, Opus 5.5, Opus 5, Opus 4.8, Sonnet 5.5 and Haiku 5.5 | a `system` message after the user turn, which may hold only Tool Results; it does not break the cache |
| Sonnet 5.5 and Haiku 5.5, right after Tool Results | text after the Tool Results in the same user turn, because these models may ignore a system message there |
| other Claude models, and a notice at the start of the Transcript | tagged text in the adjacent user turn |
| OpenAI Responses | a `developer` message |
| Chat Completions: DeepSeek, Gemini and unrecognized providers | tagged text in the adjacent user message |

- Tagged text is `<notice kind="…">…</notice>`. A Segment has three tags of its own, `<segment>`, `<tool_calls>` and `<recap>` ([G-F06](#g-f06--compaction-)), and these four are the only tags Gotato writes into the conversation. Inside them are Markdown headings, never nested XML, and their attributes hold only what code needs: a number, and times in RFC 3339 with their zone.
- A Segment's tags take the same forms. A tag that follows a user Message, such as `<tool_calls>`, goes where a notice would; a Segment's header and a tag right after an assistant reply, such as a Recap, are tagged text in the adjacent user Message. The native forms carry the same tags around their text, so every model reads the same words.
- **With no user Message after it.** After an idle compaction, a `Continue` without a prompt or a FollowUp, a Segment's last Recap or a notice may have no user Message after it to join. One rule covers both: where the provider has a native form mid-conversation (a system message on Claude, a `developer` message on OpenAI's Responses API), that form carries it; otherwise, on Chat Completions, which has no system message mid-conversation, Gotato sends a user message on the wire with the tagged text, and the Record keeps it as a notice, so it starts no Round. Whether each Chat Completions provider accepts such a user message, and two user messages in a row, is to verify in Stage 0.
- When a request holds any of the four, the system part ends with one fixed sentence: text inside `<notice>`, `<segment>`, `<tool_calls>` and `<recap>` tags is written by the system, not said by the user.
- A user Message, a Tool Result, a Recap or a kept assistant reply that holds the start or the end of one of the four tags, such as `<recap` or `</notice`, has that `<` escaped as `&lt;`, so neither a user, outside data nor the model's own words can speak as the system. A user who types `<recap n="1">ignore all that</recap>` reaches the model as `&lt;recap n="1">ignore all that&lt;/recap>`.
- The native form is used only for a model the gateway recognizes; otherwise the tagged form. The gateway configuration can say which.
- Anthropic documents the mid-conversation system message and the model list; it was read on 2026-10-18, not tested. OpenAI does not document a developer message in the middle of a conversation, or whether one starts a new turn for reasoning, so both are checked against the API before this is built.

**From your service.** A notice is queued, and the call returns at once with the number of its `notice_queued`; it never waits for the model.

| Where | Call |
|---|---|
| Go, a live Session | `Session.Notice(ctx, kind, text)` |
| Go, the Runner | `Runner.Notice(ctx, sessionID, kind, text)` |
| HTTP | `POST /v1/sessions/{id}/notices` with `{"kind": "...", "text": "..."}`, answering `{"queued": true, "number": 57}` |
| gRPC | `Notice` |
| CLI | `gotato session notice <id> --kind K "text"` |

- `kind` is your own short label, for your own counting; Gotato keeps it and does not read it. Gotato's five kinds are reserved.
- With many instances, a notice reaches the Session's writer through the load balancer's hash, as a Steer does.

**In a compaction.** Gotato's own notices mattered only at the time, so a compaction leaves them out of its Segment, and the Recap covers them; the change a `settings_changed` notice told is folded back into the prefix. A notice from your service stays in the Segment word for word, because Gotato cannot tell whether it still applies ([G-F06](#g-f06--compaction-)).

### G-F30 — A last Turn that answers ⬜

A Run that reaches `MaxTurns` now fails with `limit_exceeded`, and the caller gets no answer. Instead, the Loop gives the model one last Turn to answer with what it has.

- The code enforces it, not the model: the last Turn goes with tool calls switched off (`tool_choice: none`), so the provider cannot return one, and a tool call that comes back anyway is not run.
- The tool definitions stay in the request, so the cached prefix holds.
- A runtime notice (G-F29, `last_turn`) tells the model it is the last Turn.
- The same last Turn comes when the Run's deadline is near, so a Run about to run out of time still answers.

**Design first:** next, now that G-F29 is designed ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

### G-F02 — Sessions ✅

A Session is one conversation that runs an Agent. It keeps everything that happened, so the next message continues where the last one ended.

- You create a Session with `POST /v1/sessions` or `gotato session create`, optionally with your own ID, metadata, instruction and compaction ceiling. `POST /v1/runs` creates a Session and runs it in one call.
- A Session keeps every Message, a record of each Run, its token usage, its Events and every compaction. The Store keeps them as the Session's Record, `session.Record`, which holds every stored Event as an Entry and keeps the last 1,000 of them in memory.
- Forking a Session starts a new one from its Record and records the parent, and the position of the parent's log it branches at. A fork writes the store and activates nothing, so its first request activates it on whichever instance that request reaches. The fork keeps its parent's prompt cache key, so its first Turn reads the parent's cached prefix there too.
- A fork's Record starts with a Checkpoint of its parent's facts. Referring to the parent's entries instead is a later optimization, and it would pin them against deletion.
- One Session runs one Run at a time. A second Run on a busy Session is rejected, or waits, as the Runner's busy policy says (G-F27).

Per-Session settings live in the Session's metadata, so one Agent can serve Sessions that differ slightly:

| Key | Effect | Why it exists |
|---|---|---|
| `gotato.agent` | the Agent this Session runs | one Runner serves several Agents |
| `gotato.instruction` | replaces the Agent's instruction for this Session | a caller tailors one conversation without a new Agent |
| `gotato.compact_ceiling` | this Session's compaction budget, in estimated tokens | a long-lived Session can compact sooner or later than the default |
| `gotato.tool.<id>` | a tool switched on or off for this Session | a caller narrows or widens one conversation's tools |
| `gotato.model` | the model the Agent last asked for; written by Gotato, for information | shows which model the Session was run with; each reply also records the model that actually answered (`Message.Model`, G-F07) |
| `gotato.cache_key` | the prompt cache key this Session's requests carry, in place of the hash of its ID; written by a fork, from its parent | a fork shares its parent's prefix, so it should share its cache |

## What the model sees

### G-F04 — Context ✅

On every Turn, Gotato builds what the model sees from the Session, in a fixed order, so the provider can reuse the cached prefix from one Turn to the next:

```text
instruction · Static blocks  →  tools, sorted by ID  →  the Transcript
```

- **Static blocks** are reference text the Agent carries, such as a schema or a runbook. You attach them with `modelctx.WithStatic` and the `Resource`, `Text` and `JSON` helpers.
- **Nothing that changes every Turn is added.** Such content is never cached, so it would cost its full price on every Turn. A fact the model needs now, such as the current time, comes from a tool it calls.
- Gotato marks cache points after the system part, after the tools and on the last message, so each request is cached whole for the next Turn. For Anthropic, they become `cache_control`, and the last four are kept. For a recognized Claude, the gateway adds one where the last Round started, because a new Round leaves the last Round's reasoning out ([G-F07](#g-f07--models-and-providers-)). Claude takes at most four, so right after a compaction that one moves to the end of the last Segment ([G-F06](#g-f06--compaction-)).
- Every request also carries a cache key, so a provider that keys its prompt cache serves one conversation from one cache. The Runner gives each Session its own key, a hash of its Session ID; with `gotato.NewSession`, you set one with `gotato.WithCacheKey`. How the key travels is the gateway's configuration ([G-F07](#g-f07--models-and-providers-)).
- Another way to choose what the model sees, such as retrieval or a window, is your own `ContextBuilder` ([G-N10](#g-n10--context-organization-beyond-full-history-belongs-to-applications)).

### G-F05 — Context inspection ✅

You can see what the model would receive now, without calling it.

- `gotato context inspect <id>`, `gotato context build <id>`, `GET /v1/sessions/{id}/context` and the gRPC `Context` method return the request the Session's next Turn would send.
- The Runner builds the live Session a Run would build and asks it, so the request includes the Agent's `ContextTransformer` and `MessageConverter` extensions, its tools and its model options.
- The report includes the prefix hash, the size of the system part, and an estimate of the tokens (bytes ÷ 4).
- `compaction_pending` says the next Run compacts the Session first, because it is past `compact_ceiling`. That Run then sends a shorter request than the report shows.
- Every Turn emits a `context_built` Event with the prefix hash of the request it sent. The reference workload checks that it equals an inspection of that Turn's Transcript ([GOALS.md](GOALS.md) G-G03).

In Go, the same request comes from the Session itself:

```go
inspection, _ := s.InspectContext(ctx) // fails with ErrBusy during a Run
report := modelctx.ReportOf(inspection)
```

**Limits:** inspection builds as a Run's first Turn does. It cannot know a tool set the model switches on later in a Run, or a `ContextTransformer` whose output depends on the Run or the Turn. The extensions and tool sources run during inspection too, so they must not have side effects.

### G-F06 — Compaction 🚧

A Session can live for weeks, its user often comes back long after the provider's cache has expired, and a long Run can outgrow the model's window. Compaction keeps what the model is sent within its window, keeps as much of the conversation as it can, and makes the first request after a long pause cheap. It appends a Checkpoint (G-F03) and never rewrites the Record, so nothing a Session said is lost, unless the team prunes the entries before a Checkpoint ([DESIGNS.md → Compaction closes a Segment, before the cache goes cold](DESIGNS.md#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19)).

**Built today.** An old part of the Transcript is replaced by one summary:

- **On demand:** `gotato context compact <id> --keep N`, `POST /v1/sessions/{id}/compact`, gRPC `Compact` and `Runner.Compact`.
- **Automatically:** at the start of a Run, when the estimated tokens pass the ceiling: a ceiling per Agent in the service, and per Session with `gotato.compact_ceiling`. It is off by default (a ceiling of 0).
- The summary comes from truncation or from a model (`--summarizer truncate|model`), and reaches the model as a user message.
- Every compaction is recorded in the Session: what it replaced, with what, and the sizes before and after.
- A compaction appends a Checkpoint (G-F03) instead of rewriting the Record, so the old part stays in the store and is no longer loaded. A Checkpoint holds the whole state of the facts, the summary and the kept tail included, so a load never mixes a summary with the entries it summarized.
- When the Agent's model is sent only the current Round's reasoning (a recognized Claude or OpenAI, see [G-F07](#g-f07--models-and-providers-)), the kept tail is kept without its reasoning, because the summary changes the prefix that reasoning was written under. The Runner asks the model (`modelctx.DropsReasoning`); with `modelctx.Compact` alone, set `CompactOptions.DropReasoning`. For DeepSeek and unrecognized providers, the reasoning stays.

**Gap:** each compaction summarizes the summary before it, it waits for the start of a Run, and nothing compacts an idle Session. Everything below was designed on 2026-10-19, and is built with the warm Session ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)).

**Segments.** Each compaction closes what came since the last Checkpoint into a Segment, a stretch of history that is never rewritten, and appends it after the Segments before it:

```text
before   [Segment 1][Segment 2] the Messages since the last compaction …
after    [Segment 1][Segment 2][Segment 3] the kept tail
```

- There is no summary of a summary until the oldest Segments merge, so nothing drifts. The Segments before keep their bytes, so a provider that caches any prefix reads them from the cache while the tool list stays the same. On Claude that needs a cache marker at the end of the last Segment, because Claude reads a cached prefix only up to a marker an earlier request placed. Right after a compaction, the gateway puts there the marker it otherwise puts where the last Round started, since Claude takes at most four and G-F04 places four; the Recap that ends the Segment is then its own content block, so the marker falls between blocks and not inside a Message. The marker goes back to where the last Round started once a new Round begins. OpenAI reads any prefix without markers. Whether Claude reads the Segments before from the cache at that marker, and what the next compaction's request then costs, is to verify in Stage 0.
- Each Segment has a number, and the times its stretch began and ended.
- The Record keeps every Message a Segment covers, unless the team prunes the entries before a Checkpoint ([G-F03](#g-f03--stores-)). A load starts at the last Checkpoint, and the model reads a Segment back with `read_segment` (below).

After two compactions, the model is sent this, from the top:

```text
system     the Agent's instruction, as it is now, and the fixed sentence about the tags (G-F29)
tools      the base tools, the ToolSets that stayed active (G-F10), and read_segment
Segment 1  <segment> · user · <tool_calls> · assistant · … · <recap>    never rewritten
Segment 2  <segment> · user · <tool_calls> · assistant · … · <recap>    never rewritten
tail       the Messages since the last compaction, as they are
```

**What a Segment keeps.** Code builds it, and only the Recap at its end comes from a model:

| Part | In the Segment | By |
|---|---|---|
| the user's messages | word for word | code |
| each Run's final reply | word for word | code |
| the text of a Run's other Turns, and reasoning | left out | code |
| each tool call | one line inside its Run: the time, the tool, its arguments cut at 200 characters, and `ok` or the error code | code |
| tool results | left out | code |
| a notice from your service | word for word | code |
| Gotato's own notices | left out: the Recap covers them, and a changed setting is folded into the prefix | code |
| the Recap | what happened, the key facts the tools returned, the decisions, what is still open, and where a Run stands | the Agent's model |

- The tool results are left out, so the facts they held survive only in the Recap. That matters most for an investigation, whose Runs have little conversation.
- A call repeated with the same arguments is one line with its count, and a failed call keeps its error code. Past 100 lines, a Segment counts the rest of its calls by tool name.
- Once the tool calls are lines, the assistant Messages next to each other become one, so user and assistant still take turns.

**What a Segment looks like.** The parts code builds come first, and the Recap last, right before the Messages that follow it:

```text
<segment>     its number, first and last time, and what was left out     code
user          word for word                                              code
<tool_calls>  this Run's tool calls, one line each                       code
assistant     this Run's final reply, word for word                      code
user · <tool_calls> · assistant …
<recap>       What happened · Key facts · Decisions · Open items · Where it stands     model
```

Here is one Segment of an operations assistant that reads payments, orders and logs, shortened, as tagged text. `[user]` and `[assistant]` stand for real user and assistant Messages:

```text
<segment n="1" from="2026-10-19T10:02:00+08:00" to="2026-10-19T10:41:00+08:00">
Segment 1. The messages up to recap 1 are word for word; reasoning,
intermediate steps and tool results were left out. read_segment(1) returns
the segment in full.
</segment>
[user]       Some charges in last night's batch B-20261018-07 failed. Why?
<tool_calls>
10:02 payments.get_batch {"batch_id":"B-20261018-07"} → ok
10:03 logs.query {"service":"payment-gateway","order":"O-88502"} → ok
10:04 orders.search {"ids":["O-88213","O-88347","O-88502"]} → ok
</tool_calls>
[assistant]  3 of 412 failed. O-88213: the card expired, so a retry will not help.
             O-88347: insufficient funds; retry in a few days. O-88502: a gateway
             timeout; safe to retry now. Shall I retry O-88502?
[user]       Retry O-88502, leave the other two, and retry O-88347 on Friday.
<tool_calls>
10:40 payments.retry {"order":"O-88502"} → ok
</tool_calls>
[assistant]  O-88502 went through, charge C-5521. O-88347 waits for Friday, October 23.
<recap n="1">
## What happened
- Found why 3 charges in batch B-20261018-07 failed; retried O-88502 at the user's request.
## Key facts
- B-20261018-07: 412 charges, 409 succeeded, 3 failed.
- O-88213: card_expired. O-88347: insufficient_funds.
- O-88502: gateway_timeout at 2026-10-18 23:41:07; retried at 10:40, charge_id C-5521.
## Decisions
- O-88213 and O-88347 are left alone for now (the user's request).
## Open items
- Retry O-88347 on 2026-10-23 (the user's request).
</recap>
```

- In this example the stretch held about 55K tokens, most of them tool results, and the Segment holds about 1.3K; both are estimates.
- When the user comes back the next day, after the cache has expired, the request writes those 1.3K tokens and the new message at full price, and the model still knows which charge is O-88213 and why it failed.
- What the Recap left out, such as the largest of the 412 charges, the model reads with `read_segment(1)`, or asks the tool again.

**The Recap.** The Agent's own model writes it:

- The request is the Context the next model call would send, with the same instruction, tools and Messages, and one plain user message after it, so the whole prefix is read from the cache. That message carries no tag: it is never stored, and it asks for work rather than stating a fact. Neither the request nor its reply becomes a Message; only the Recap is kept, in the Checkpoint.
- Its output limit is 8K tokens. A Recap cut off fails, so the limit is high; output not used costs nothing.
- A Recap fails when the reply calls a tool, is cut off at the output limit, does not start with one of the five `## ` headings, uses any other `## ` heading, or has no `## What happened`. A heading with nothing under it may be left out. The compaction still goes on: the Segment is kept without a Recap, with its conversation and tool call lines, and its Event says `recap: none`.
- `modelctx.Summarizer` becomes what writes a Recap, so a library user can bring their own; its new name is settled when it is built. `TruncateSummarizer` stays, for tests and for use without a model.

The message, for the Segment above:

```text
Write a recap of the messages after the last segment, from 2026-10-19 10:02
until now. It closes this stretch of the conversation as segment 1. The
user's messages, your final replies and one line per tool call are kept next
to it; your reasoning, your intermediate steps and all tool results are not.

Write in the conversation's language, as short bullet points, under these
headings. Leave out a heading that has nothing under it.

## What happened     what the user asked for, and what was done
## Key facts         what the tools returned that still matters; copy IDs,
                     numbers, names, times and error codes exactly
## Decisions         what the user or you decided, and why
## Open items        what was promised, asked for, or left unfinished
## Where it stands   only while a task is still running: the step it is on
                     and the next one

Leave out the instructions, the tool list and the environment; they are
sent separately. Do not call tools. Do not address the user. Start with the
first heading.
```

- It says that the conversation and the tool call lines are kept, so the model spends its words on what the tools found, not on repeating the conversation.
- It asks for IDs, numbers and error codes exactly, because the tool results that held them are gone.
- When the oldest Segments merge, another message asks for one Recap of their Recaps, under the same rules, and says which numbers `read_segment` still reads.

**When it runs.** Four triggers, each recorded as why the compaction ran:

| Trigger | When | Why it ran |
|---|---|---|
| Idle | a warm Session has been idle for min(TTL, cache retention) less 5 minutes, or for the TTL less 5 minutes when the retention is unknown or not well above 5 minutes, and the stretch since the last Checkpoint holds at least 20K tokens | `before_cache_expires`, or `before_unload` when the TTL comes first, both come at once, or the retention is not used |
| Past the ceiling | before any model call, mid-Run included, when the Context is past the ceiling | `past_ceiling` |
| Overflow | the provider refuses a request as too long, before any output | `overflow` |
| On demand | `gotato context compact`, `POST /v1/sessions/{id}/compact`, gRPC `Compact` and `Runner.Compact` | `on_demand` |

- **Idle.** The cache is still warm, so the Recap's request reads it, and a user who comes back later pays full price only for the smaller Context. The cache retention is the one the gateway configuration sets ([G-F33](#g-f33--prompt-cache-retention-and-keepalive-)). When it is unknown, as for an unrecognized provider, which is sent none, or not well above the 5-minute lead, as with `cache_retention: 5m`, the trigger uses the TTL alone: a retention of 5 minutes less the lead is 0, and the Session would be compacted the moment a Run ends, so a user who answers a minute later would pay to write a new prefix instead of reading a warm one. The compaction then runs before the Session leaves memory (`before_unload`). How much above the lead counts as well above is set with the lead when it is built. Five minutes ahead, because a compaction can take a minute or more. How a keepalive moves this trigger is settled with G-F33's keepalive design.
- **The smallest stretch.** Below 20K tokens an idle Session is not compacted, which saves a model call: if a user comes back 30 to 50 percent of the time, a compaction pays for itself from 10K to 17K tokens. That return rate is an assumption, tuned once real numbers are in.
- **Who compacts.** The instance that holds the warm Session, on its TTL timer ([G-F27](#g-f27--service-many-instances-)). A message that arrives meanwhile cancels the compaction and is served first, while the cache is still warm. If that instance goes away first, as in a deploy, the stretch waits for the next idle time or the ceiling.
- **Past the ceiling.** The size is the input and output tokens the last model call reported, plus an estimate of what came after it (bytes ÷ 4). After a compaction, the old usage no longer counts and the size is estimated afresh, so a compaction does not set off another at once. The ceiling is the window less max(10% of it, 16K tokens): 180K for a 200K window, and always at least 16K for a reply and a Recap. The window is the model's `context_window` in the gateway configuration ([G-F07](#g-f07--models-and-providers-)); without it there is no default ceiling, and this trigger is off unless the Agent or the Session sets one. Segments are still merged at their default budget. A ceiling set without a window is used as it is, since the room left for a reply and a Recap cannot be known.
- **Mid-Run.** The cut falls between two Turns, so a tool call never leaves its result. The Run's latest Turns stay in the kept tail, tool results included, with reasoning as [DESIGNS.md → Reasoning](DESIGNS.md#reasoning-goes-back-for-the-current-round-where-the-provider-allows-it--2026-10-18) says, checked against Claude and OpenAI (below), up to 20K tokens and no more than a quarter of the window, or of the ceiling when no window is known. The Run's prompt stays word for word in the Segment, which has no final reply yet, and the Recap says where the Run stands: the step it is on and the next one.
- **Overflow.** When the provider refuses a request as too long before any output, the Session is compacted and the call sent again, unless the Turn has compacted already. The oldest Segments are merged too when the Segments alone are past the limit (see *Many Segments*).
- **Once per Turn.** A Turn compacts at most once, whatever the trigger. Still past the ceiling after it, the Run ends with `limit_exceeded`, and the Event says why. When the provider still refuses the request as too long after it, the Run ends with an error code of its own that says the request was too long; the code is named when it is built, and added to the contract. Either usually means one tool result is too large, so `MaxToolResultBytes` is the first guard.

**A Segment closed mid-Run.** A Run that investigates an alert nears the ceiling at its 30th Turn, after four earlier compactions. The cut falls between Turns 26 and 27, and the model is sent this, from the top:

```text
system        the Agent's instruction, and the fixed sentence about the tags
tools         the base tools, the ToolSets Segment 5 used and those Turns 27–30 call, and read_segment
Segments 1–4  never rewritten
Segment 5     <segment n="5"> · user: the alert, word for word · <tool_calls>: Turns 1–26 · <recap n="5">
tail          Turns 27–30 as they were, tool calls and results included
```

- The Run has no final reply yet, so the Segment holds only its prompt and its tool calls.
- Its Recap ends with *Where it stands*, such as: the network is ruled out; next, the slow queries on the orders database.

**Many Segments.**

- **Reading one back.** Once a Session has a Segment, the model has a built-in tool, `read_segment`, that reads a Segment's Messages from the Record by its number, tool results included. Its result is cut at `MaxToolResultBytes` and read page by page. It declares `read`, so every Effect cap keeps it. Its result is a tool result, so it carries none of the four tags: a prefix on each line says whose it is, such as `[user]`, `[assistant]` or `[tool payments.get_batch]`, and a line of the text that itself starts with such a prefix is escaped, so a user cannot pass their words off as the assistant's. When the team has pruned the entries a Segment covers, `read_segment` says that the Segment's Messages were removed.
- **Merging the oldest.** Gotato is safe by default, so Segments have a token budget and a long Session cannot outgrow its window on Segments alone. When the Segments together pass the budget, the oldest are merged into one Segment, with one Recap the model writes for them, and the merge is appended as a new Checkpoint. A merged Segment keeps its header and that Recap, and the Segments it merged stay readable with `read_segment` by their own numbers. It is named by the range it covers, `<segment n="1-3">` and `<recap n="1-3">`. A merge rewrites the prefix once, so it is done at the idle trigger when it can be. A merge whose Recap fails leaves the Segments unmerged.
- **The Segments' budget.** It is half the ceiling when there is one. With no ceiling, which is the case without a `context_window` and without a ceiling from the Agent or the Session, a default budget applies, so Segments are merged even then. The default's number is not chosen yet; it is set when this is built, from the measurements of the Recap.
- **Merging on an overflow.** When the provider refuses a request as too long and the Segments alone take more than the limit it names, the oldest Segments are merged on that overflow too, sized from the limit in the refusal, or to the budget when the refusal names none. What each provider's refusal says is to verify in Stage 0 ([HANDOFF.md](HANDOFF.md)). Without this, a Segment closed on an overflow would not help, since it closes only what came since the last Checkpoint.

**After a compaction.**

- **The instruction and the tool definitions** are built from the current Agent on every request, outside the Transcript, so a compaction carries neither. A change that [G-F32](#g-f32--changes-that-keep-the-cache-) appends as a `settings_changed` notice is folded back into the prefix, as the current version, and the notice is no longer carried.
- **The ToolSets** used in the Segment just closed stay active, and so do those a kept tail still calls; the others are switched off, and the model can switch them on again ([G-F10](#g-f10--tool-sets-and-the-registry-)).
- **Notices.** Gotato's own (`continue`, `last_turn`, `stream_cut`, `run_interrupted`) mattered only at the time, so they are left out, and the Recap covers them. A notice from your service stays in the Segment word for word, because Gotato cannot tell whether it still applies ([G-F29](#g-f29--runtime-notices-)).
- **The tags** reach each wire API in a notice's forms, and a user Message or Tool Result that imitates one is escaped ([G-F29](#g-f29--runtime-notices-)).
- The model is not told how much of its window is left.

**How it is kept.**

- The Checkpoint is the `compaction_ended` Event, as today: the whole state after the compaction, which a load starts from. It holds every Segment, the kept tail and the active ToolSets, so every instance that loads the Session from it offers the same tools.  Switching a ToolSet on is a fact too ([G-F10](#g-f10--tool-sets-and-the-registry-)), so between two compactions an instance that loads the Session offers the same ToolSets as the warm one.
- A Segment is stored as data: its number, its first and last time, the Messages it keeps word for word, its tool call lines, its Recap, and the range of the Record it covers. The rules that turn it into a request are fixed, so every instance sends the same bytes.
- The compaction's record adds the Segment's number, the estimated tokens before and after, the usage of the model call that wrote the Recap, and the ToolSets kept. It records usage only, not cost: Gotato keeps no prices, and a cost is added once the model catalog ([Later](#later)) has them.
- **The kept tail.** With Segments, a compaction closes everything since the last Checkpoint and keeps only a tail. `--keep N`, `{"keep":N}` on `POST /v1/sessions/{id}/compact` and `CompactPolicy.Floor` stay, as the size of that tail: `keep` in Messages and `Floor` in tokens, cut at a Turn boundary so a tool call keeps its result. Their meaning, "how much recent history stays", does not change, so the versioned CLI and HTTP contract does not break. Only the default of an automatic compaction changes, to the kept tail mid-Run in the settings below.
- **The limits.** `MaxMessages` and `MaxTranscriptBytes` count only the Messages a load reads, as they do today, and a Segment's Messages are among them. It is the size of the Context that matters, not the number of messages, and a Context that grows too large is compacted by the ceiling and the Segments' budget. So once Segments exist, the default of 1,000 messages is raised or the count dropped; a Session of short messages must not reach it before it reaches a ceiling counted in tokens.
- An idle compaction runs outside any Run, so its usage is on its compaction Event, not on a Run.

**Settings.** Each can be configured, and their names are settled when built; these are the defaults:

| Setting | Default | Why |
|---|---|---|
| idle compaction | on | a Session that comes back after a long pause starts cold anyway |
| compaction past the ceiling | on when the window is known | a Run that outgrows the window cannot go on |
| ceiling | the window less max(10% of it, 16K tokens); the Agent's ceiling and `gotato.compact_ceiling` override it | room for a reply and a Recap |
| idle lead | 5 minutes before min(TTL, cache retention); before the TTL alone when the retention is unknown or not well above the lead | a compaction can take a minute or more, and its Recap should read a warm cache |
| smallest stretch to compact when idle | 20K tokens | below it, the model call costs more than it saves |
| kept tail mid-Run | 20K tokens, and at most a quarter of the window, or of the ceiling when no window is known | the Run goes on from its latest Turns as they were |
| Recap output limit | 8K tokens | a Recap cut off fails, and output not used costs nothing |
| Segments' budget | half the ceiling; with no ceiling, a default budget whose number is not chosen yet | the oldest Segments are merged when the Segments pass it, and on an overflow, so a long Session is safe by default and leaves room for the conversation that follows |
| tool call lines | 100 per Segment, arguments cut at 200 characters | one busy Run does not fill its Segment |

**Checked with real provider requests before it is built:**

- the Recap's quality: whether the Agent's model, writing on the cached prefix, keeps the IDs, numbers and open items a later question needs;
- how the model reads a `<tool_calls>` block between a user message and the reply: as a record of calls already made, not as calls to make again or text to copy;
- what Claude and OpenAI need of the reasoning in a tail kept mid-Run, whose tool loop is still going while the Segment changed the prefix that reasoning was written under;
- whether Claude reads the Segments before from the cache at a marker at the end of the last Segment ([G-F04](#g-f04--context-)).

The checks can each change what is built, so building waits for them ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)). A history that holds a call to a tool no longer defined is not checked, because compaction makes none: a kept tail keeps the ToolSets it calls, and a Segment holds its calls as text. It is checked if a case is found.

### G-F32 — Changes that keep the cache ⬜

The instruction and the tools come before the Transcript, so changing them mid-Session makes the provider read the whole Transcript again at full price. Three things do that today: a Session's own instruction (`gotato.instruction`), a tool set the model switches on, and a tool switched on or off for one Session.

- Such a change is appended instead: a runtime notice (G-F29) for a new instruction, and the new tool's definition added after the Transcript, while the tool list sent at the start stays as it was.
- A compaction folds such changes back into the prefix, where the request changes anyway ([G-F06](#g-f06--compaction-)).
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
s, _ := gotato.NewSession(gotato.WithModel(model), gotato.WithTools(lookup, refund),
	gotato.WithMaxEffect(gotato.EffectRead))
```

- A tool above the cap is hidden: it is in no model request, no inspected Context and no tool listing. That includes the tools of a ToolSet the model activates later. The tool that activates a ToolSet declares `read`, so every cap keeps it.
- A model that calls a hidden tool by a guessed name gets the failed result of an unknown tool, which lists only the visible tools, and the Run goes on.
- A tool that declares no Effect counts as destructive, so any cap below `destructive` hides it.
- No cap is the default. In a service, `service.Agent.MaxEffect` sets the cap, and a Session can neither raise it nor switch on a hidden tool. Work that needs another cap is another Agent (see the decision below).
- The cap is shown by `gotato tools list` (`max_effect` in `--json`), and by `GET /v1/agents` and the gRPC `Agents` call, as `max_effect` from Agent name to cap. An Agent without a cap has no entry.
- The check is G-G05: every cap against tools of every Effect ([GOALS.md](GOALS.md)).

#### The Effect cap hides tools, and only an Agent sets it — 2026-10-16

The cap decides which tools a model may use, so two questions came first: what happens to a tool above the cap, and who may set the cap.

| Option | For | Against |
|---|---|---|
| **Hide the tools above the cap** (chosen) | The model cannot plan around a tool it never sees, and spends no Turn on a call that fails. Fewer tool definitions in the Context. | A model that guesses a hidden name gets "unknown tool", not the reason. |
| Offer every tool, and refuse a call above the cap | The model learns why a call failed. | The model keeps choosing tools it may not use, and each refusal costs a Turn. |

| Option | For | Against |
|---|---|---|
| **Only the Agent sets the cap** (chosen) | The bounds are in one place, the Agent's definition, and a reviewer reads them there. | Two jobs with different bounds need two Agents. |
| A Session can lower the cap too | One Agent for several jobs. | We found no case where the same purpose-built Agent needs different bounds per conversation. A different bound means a different job, so it is a different Agent. |

There is no cap by default: an Agent that names none behaves as before.

### G-F10 — Tool sets and the registry ✅

The tools a Session offers can change while it runs.

- A registry lets you register, remove, switch on and switch off tools at any time. The Session reads its tool sources again at the start of every Turn.
- A tool set groups tools the model can switch on itself, by calling `activate_toolset`. Up to 16 can be active (`MaxActiveToolSets`).
- **After a compaction** (planned with [G-F06](#g-f06--compaction-)'s Segments), only the ToolSets called in the Segment it closes stay active, with those a kept tail still calls. The others are switched off, including one that was switched on and never called, and the model can switch them on again. A compaction switches off none by count, and `MaxActiveToolSets` stays as it is. The active ToolSets are kept in the Checkpoint, so every instance that loads the Session from it offers the same ones. Between two compactions, activation is a fact too (below). The tool list comes before the Transcript, so changing it changes the request; a compaction changes the request anyway, which makes it the cheapest moment.
- **Gap, settled:** a ToolSet the model switches on lasts one Run in the service today. `toolset_activated` is a log entry that nothing reads back, and the Runner builds a live Session for every Run, so the next Run starts with the Agent's ToolSets only. It becomes a fact, which a rebuild reads, so a ToolSet the model switched on stays on in the next Run, on any instance, and the Context depends only on the Record and the Agent ([DESIGNS.md → The Context depends only on the Record and the Agent](DESIGNS.md#the-context-depends-only-on-the-record-and-the-agent)). It is built before Segments ([HANDOFF.md](HANDOFF.md), Stage 3), since a compaction relies on the active set.
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
- A reply records the model that answered it (`Message.Model`), as the provider reports it: `model` in each chat chunk, in Responses' `response.created` and `response.completed`, and in Anthropic's `message_start`. Behind a gateway that falls back to another model, it names the fallback, not the model requested. It is a record only, and never sent back.
- A thinking model's reasoning is kept in the Transcript, and sent back as each wire API carries it: `reasoning_content` on the chat API, as DeepSeek requires; encrypted reasoning items on Responses; signed `thinking` and `redacted_thinking` blocks on Anthropic. How much of it goes back depends on the provider (below).
- A tool call's `extra_content` on the chat API is kept on the call and sent back as it came. Gemini 3 and later put the thought signature there, on the first of parallel calls, and refuse a call of the current Round that comes back without it. Google documents the field on a whole response; streams captured from Vertex AI's chat API carry it on the call's first delta. Gotato has not yet run this against Gemini.
- Parallel tool calls on the chat API stay apart when the provider sends them without an index, with a null one, or all at index 0, as Gemini's chat API does: a delta that names a new call ID starts a new call.
- Cache reads are counted on every wire API, from `cached_tokens` and DeepSeek's `prompt_cache_hit_tokens` on the chat API.
- A tool goes to the provider under its own ID when the provider accepts that name, and otherwise with each other character replaced by `_` (`demo.echo` becomes `demo_echo`), so the model reads and repeats a real name.

**Provider rules**

A few providers have rules of their own for reasoning and the cache. The gateway follows them for the provider that `provider` names, or, without it, for the provider whose own address it calls: `api.anthropic.com`, `api.openai.com` or `api.deepseek.com`. It never guesses from a model name, because behind a gateway a model name may be an alias. `provider: other` turns this off.

| Provider | Reasoning sent back | Added to the request |
|---|---|---|
| `anthropic` | only the current Round's | `cache_control` with `"ttl": "1h"`; on Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5, `thinking.block_binding` with `drop_block`, its beta header, and `thinking.type: "adaptive"`, their default |
| `openai` | only the current Round's | on Responses from GPT-5.6, `prompt_cache_options.ttl: "30m"` |
| `deepseek`, and any provider not recognized | all of it | nothing |

- **A Round** is a user Message and every reply and Tool Result after it, so the reasoning that goes back is the reasoning of the replies after the last user Message. A Steer starts a new Round. A Continue without a new prompt stays in the Round it resumes. On Responses, an earlier Round's function call goes without its item ID too.
- **DeepSeek** refuses a request that leaves out earlier `reasoning_content` once tools are in play, on its chat API and on its Anthropic one, and an unknown provider may do the same. Both get all the reasoning, and no field or header beyond the request.
- **A new model** gets the reasoning as it is. The provider drops what its model cannot read.
- **Block binding.** Claude's newest models check that a thinking block comes after the same system prompt, tools and Messages it was written under. Leaving out every earlier Round's reasoning keeps that true; `drop_block` asks Claude to drop a block whose prefix changed anyway, instead of refusing the request. `no_drop_block: true` turns it off. Claude reports what it dropped in `input_transformations`, which Gotato does not read yet.
- **Cache retention.** `cache_retention` sets how long the provider keeps the cache. Without it, a recognized provider gets its longest; a shorter value than the provider's longer option keeps its default. OpenAI models before GPT-5.6 keep their own default, which is 24 hours for an organization without Zero Data Retention. `GOTATO_GATEWAY_CACHE_RETENTION` sets it for every model in the process and wins over each model's own.

The reasoning rule and the retention were checked against the providers' documentation on 2026-10-18, not yet against the providers themselves.

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
| `context_window` | none | the model's context window, in tokens, from which compaction takes its ceiling ([G-F06](#g-f06--compaction-)). Gotato keeps no table of windows, so without it there is no default ceiling, and Segments merge at their default budget. Planned with G-F06's Segments. |
| `headers` | none | headers some providers or proxies require |
| `cache_key_field` | `prompt_cache_key` on `openai-responses`, none on chat; `none` turns it off | the body field that carries the prompt cache key |
| `cache_key_header` | none | the header that carries the prompt cache key, such as `session-id`, for a provider that keys its cache by a session header |
| `max_retries`, `no_retries` | 2 retries | providers shed load briefly; a retry usually succeeds |
| `retry_backoff` | 200 ms, growing per attempt, with jitter | spreads retries out so they do not land at once |
| `provider` | recognized from the provider's own address, else none | `anthropic`, `openai`, `deepseek` or `other`: whose rules for reasoning and the cache apply |
| `cache_retention` | the recognized provider's longest | how long the provider keeps the prompt cache, such as `5m` to keep Claude's default and its lower write price. `GOTATO_GATEWAY_CACHE_RETENTION` wins over it. |
| `no_drop_block` | false | stops asking Claude to drop a thinking block whose prefix changed, so such a request is refused instead |

Without a YAML file, `gateway.NewFromEnv()` reads `GOTATO_GATEWAY_API`, `GOTATO_GATEWAY_BASE_URL`, `GOTATO_GATEWAY_ENDPOINT`, `GOTATO_GATEWAY_API_KEY`, `GOTATO_GATEWAY_MODEL` and `GOTATO_GATEWAY_PROVIDER`, and falls back to `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL`.

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

A Gotato service runs far from its callers, on networks and providers it does not control, so a reply cut off partway must not fail the Run. Today a stream cut before any output is sent again, and a stream cut after some output fails the Run with `model_failure`. The text received before the cut is kept in the Transcript as a cut-off reply (`StopCutOff`), and it is never sent to a model again, so a later Run or `Continue` asks the model afresh.

- The Run goes on instead, one of two ways, chosen when this is designed: keep the output so far and ask the model, in a runtime notice (G-F29), to continue without repeating it; or send the request again and tell stream readers, with an Event, to drop what they received.
- The retries stay bounded, so a provider that keeps cutting off still fails the Run.

**Design first:** next, now that G-F29 is designed ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

## Storage

### G-F03 — Stores ✅

Every Session is saved to a store as its Record (`session.Record`), and you choose which store:

| Store | For |
|---|---|
| In memory | tests and short-lived programs |
| A directory per Session, with one file per write | the CLI, in `--store DIR` (default `$GOTATO_HOME/sessions`, else `~/.gotato/sessions`) |
| Your own adapter | the database you run, through the store contract below ([G-N08](#g-n08--storage-engines-belong-to-applications)) |

The Record is an append-only log ([DESIGNS.md → The Record is an append-only log](DESIGNS.md#the-record-is-an-append-only-log--2026-10-16) and [The Record keeps every step](DESIGNS.md#the-record-keeps-every-step-as-facts-and-a-log--2026-10-17)):

- Every step of a Run is appended as it happens (G-F27), at a position the store never gives twice. Nothing already written is rewritten.
- Each Entry is a fact, which a rebuild reads, or a log entry, which records what happened and is never read to rebuild. A metadata change, such as a new instruction or a switched-off tool, is a fact.
- A compaction appends a Checkpoint, and a load reads the facts from the last one.
- A Claim is a fact at the position after the head its instance read, so a writer that lost its Claim can never write (G-F27).
- An Entry of an unknown kind is refused, never misread. A session file of the earlier format (`schema_version` 1) is read as a Checkpoint, and moved into the new layout by its first write.
- The Record grows with every step. A team that wants to bound its storage deletes the entries before a Checkpoint itself.

The file store writes each batch of Entries to a file of its own, named by its first position, and links it into place. A link fails when the name exists, so two processes on one directory can never both write a position, and a crash never leaves a torn write. One window is left: a writer compares its epoch with the Record's before it writes its file and links it, so a Record that another process, or another goroutine, deletes and creates again under the same ID in those few milliseconds still takes the old writer's batch. The memory store checks and writes under one lock and has no such window.

**The store contract** is eight calls, each one ordinary conditional write or read, so a plain SQL or document database can do it:

| Call | What it does |
|---|---|
| `Create` | writes a new Record, starting with a Checkpoint; fails when the ID is taken |
| `Append` | writes a batch of Entries after a position, all or nothing, and only when that position is still the last and the Summary's epoch is the Record's; a writer of a Record deleted and created again gets `ErrNotFound` |
| `Load` | returns the facts from the last Checkpoint, the last position, and the Summary |
| `Log` | returns every Entry after a position, facts and log entries |
| `List`, `Delete` | list the Summaries, and delete a Record; an append to a deleted Record fails |
| `Beat`, `Instance` | renew, read and remove one Liveness row per instance, timed by the store's clock |

A writer passes the Session's Summary with every write, so `List` never reads a log. `session.Writer` makes the writes, `session.Open` reads the facts a Run needs, and `session.View` reads everything, Runs and Events included.

#### Adapters prove the store contract with a conformance suite — 2026-10-16

The store is the truth, and each team backs it with its own adapter, so a team needs a way to know its adapter keeps what Gotato relies on.

| Option | For | Against |
|---|---|---|
| **A conformance suite in `testkit`, shipped with the store contract** (chosen) | An adapter runs one set of checks and learns where it falls short. The memory and file stores run the same suite. | The suite is part of the contract, so it changes with it. |
| The contract in prose only | Nothing to maintain. | Each adapter reads the prose its own way, and a wrong one shows up as a lost Turn. |
| Ship a database adapter in the core | Works on day one for one database. | Rejected with "Gotato owns the shapes, and adapters own storage and telemetry": drivers, migrations and one schema for everyone. |

The suite is `testkit/storetest`, a package of its own so that importing `testkit` does not link the `testing` package into a program. `storetest.Run(t, backing)` runs every check, each on a new backing store, with several handles on one store as several instances use it:

- a Record created, loaded and listed, and a second create of one ID refused;
- appends that continue the log, and a load that starts at the last Checkpoint;
- a stale writer refused, an append that does not continue the head refused, and a race of eight writers with exactly one winner;
- a writer of a deleted Record refused with `ErrNotFound` when the Record was created again, at the same length or longer;
- deletes, unknown Records and a cancelled context;
- Liveness rows that are alive, and that expire by the store's clock and never come back;
- a row its instance ended, which is removed and reads as not alive from every handle.

The suite's own tests check that it fails an adapter that moves a stale write onto the head, one that overwrites a position, one that compares only the length, one that keeps ended rows, and one that still reads a removed row as alive.

## Many instances

### G-F27 — Service: many instances 🚧

A Runner hosts Sessions inside your service, on as many instances as you run. Any instance answers a request as if it were the only one ([DESIGNS.md → Many instances from day one](DESIGNS.md#4-many-instances-from-day-one)).

| Capability | Status | What happens |
|---|---|---|
| Busy policy | ✅ | a second Run on a busy Session is rejected with 409 (`reject`, the default), or waits (`wait`) |
| Capacity | ✅ | past `MaxActiveRuns`, a new Run is rejected with 429; off by default |
| Drain | ✅ | on SIGTERM, or when your own endpoint calls `Runner.Drain`, the instance marks its Liveness row draining, `/readyz` answers 503, and it admits no new Run and takes no Session over from another instance. Every Run in flight finishes, saves and releases its Session. At the drain timeout the Runs still going are stopped, saved and released, and only then does each of their streams end with a reconnect code. `Runner.Close` then removes the row. |
| Save every step | ✅ | each step is written as it happens: the prompt, a model call before it is made, a streaming reply every second or 4 KiB, the reply, tools before they run, each tool result, each continuation. A crash loses at most the step in flight ([GOALS.md](GOALS.md) G-G01). |
| One writer at a time | ✅ | the writer holds a Claim: a fact naming its instance and an Epoch, a number that grows with every new Claim, written at the position after the head it read. A writer that lost its Claim always writes to a taken position, so the store refuses it, and a second instance stops before it calls the model (G-G08). |
| Liveness rows | ✅ | each instance renews one row in the store every third of its TTL, timed by the store's clock. When the row expires, every Claim of that instance ends at once. An instance that cannot show its row was renewed in time writes nothing, so it never calls the model. |
| Any instance answers | ✅ | an instance that is not the writer takes over a Session that is idle, or whose writer is gone, and loads its facts from the last Checkpoint. For a Session its live writer is using, draining or not, it waits, up to a bound, then takes it over; past the bound it answers 409 with `Retry-After`. No Session is bound to an instance. |
| Activation limit | ✅ | past `MaxActiveSessions`, an instance refuses a Run that would activate another Session with a 503 marked "not run" (gRPC: `UNAVAILABLE` with a trailer), emits `run_rejected` with the reason `activation_limit`, and loads, claims and creates nothing, so the load balancer can retry it elsewhere. Off by default. Spreading load stays the load balancer's and the autoscaler's job. |
| Warm Session and TTL | ⬜ | a Session stays in memory while in use, and leaves it after 60 idle minutes unless its Agent sets another TTL; the store stays the truth. While it idles, it is compacted shortly before its prompt cache expires or it leaves, whichever comes first, unless a setting turns this off ([G-F06](#g-f06--compaction-)). |

**One writer.** Only the writer may append to a Session's Record, and it holds a Claim with an Epoch.

- A Claim is a fact written at the position after the head its instance read, with the next Epoch. Its write also marks the Session busy until the writer releases it, so another instance waits instead of claiming a Session whose Run has not started.
- A writer that holds the Claim already writes it again, as a `session_claimed` Event with the same Epoch, when it opens the Session again. The write takes the next position too, so when the writer and another instance open an idle Session at the same moment, exactly one write lands, and the other instance waits for that Run instead of failing.
- When a write fails because its position is taken, the writer drops its copy at once.
- When a write fails for another reason, its outcome is unknown: the writer stops, and the Session is opened again from the store, which shows whether the write landed. A late write can only take a free position, so it never overwrites anything.
- A second instance learns that the Session has a writer before it calls the model, so it never pays for a Turn it cannot commit. One case is beyond any check: an instance paused for longer than its row lives may send one request when it wakes, before it notices. Its result can never be written.

**Liveness rows.** Each instance writes one row and renews it on a heartbeat. A Claim points at that row, and it is valid while the row is alive.

- The row's expiry is set and compared with the store's own clock, never an instance's.
- An instance gets a new identity each time it starts, and a row that has expired never comes back to life.
- When one instance dies, its row expires, and every Claim it held ends with it, with no write.
- An instance that cannot show its row was renewed within its TTL, less a margin, writes nothing. Draining itself then is planned.
- `Runner.Close` removes the row when an instance stops, so its Sessions move at once instead of after the TTL, and the store keeps nothing of the instance. A closed Runner writes nothing more, so its row never comes back. `gotato`, `gotato serve` and `gotato-grpc` call it.
- The row of an instance that died is not removed: it stays in the store, expired, so it can never be renewed.
- Planned: the row carries the instance's count of live Sessions, as a load signal.

**A request that reaches an instance that is not the writer.** The instance looks at the Claim:

| The Session is | The instance |
|---|---|
| idle, or its writer is gone | claims it, loads it from the last Checkpoint and serves the request |
| in use by its live writer, also while that writer drains | waits for that writer to release it, up to a bound, then claims it; past the bound it answers 409 with `Retry-After` |

This covers every case where a Session's writer is not where the load balancer points: a fork, a retry after a refusal, a Session activated before the load balancer's hash changed. A takeover drops the old writer's warm copy, and the new writer loads from the store.

**Where many instances differ from one.** A caller cannot tell which instance served it, apart from these places, which are part of the contract:

| Place | What differs from a single instance |
|---|---|
| The writer dies mid-Run | the step in flight is lost, and a tool that was running is answered with an unknown outcome; the stream ends, and the Session waits for the dead instance's row to expire before another instance takes it over |
| Two requests for one Session reach two instances at once | whichever opens it first runs, and the other waits for it, up to `Liveness.Wait` whatever the busy policy, then answers 409 |
| The load balancer's hash changes | for a few seconds, a message may wait for a running Run, and a Steer then starts a new Run |
| Several instances | `MaxActiveRuns` and `MaxActiveSessions` count per instance, not across the service |
| A Session's stream on an instance that is not its writer | the stored Events reach it when its next look at the store finds them (`StreamPoll`, one second by default), not as they are written, and it sees none of the deltas; the look keeps the stream live, so the deltas this instance does send are not lost |
| A rolling deploy | an instance on an older contract version refuses what it does not know, until it is replaced |

**Drain.** On SIGTERM, or when your own endpoint calls `Runner.Drain`, an instance drains in this order:

```text
mark draining in its Liveness row ──► /readyz answers 503 ──► refuse new Runs and takeovers
  ──► every Run finishes, saves, and releases its Session
  ──► at the drain timeout: stop the Runs still going, then save and release them
  ──► end each stopped Run's stream with a reconnect code ──► Close removes the row
```

- The mark lets every instance see that this one is leaving. Another instance still waits for a Session the draining instance is using, because its Run is about to be saved; taking it over would fence that Run.
- A request the draining instance refuses gets `ErrDraining`, 409 over HTTP. A Run admitted before the drain still runs, also one queued behind a busy Session; one that would take a Session over from another instance is refused. Creating or forking a Session still works, since it writes the store and activates nothing.
- A Run that finishes in time ends its stream with its result, as always. A Run the drain stops ends its stream with its result and then a reconnect code, once every Run of the drain is saved and released, so the caller reconnects to a Session that no instance is using. In the Go API, the stopped Run's `RunResult.Reconnect` carries the wait.
- A release is a write like any other, so it lands only while the Claim still names this instance. What a drain cannot save by its deadline is fenced by the Epoch anyway.
- Today each Run has its own stream. With the warm Session ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)), the signal reaches every live Session through one closed channel, and each Session handles it in its own turn, after the messages before it.

**Activation limit.** An instance counts the Sessions it has activated: those that a Run holds or waits for, each once. `Admission.MaxActiveSessions` (`--max-sessions`) is the hard limit on that count; zero, the default, has none.

- **What is refused.** A Run that would activate one Session more than the limit, whether it creates a Session or wakes a stored one. It is refused before anything is loaded, claimed or created: `ErrOverloaded`, code `overloaded`, HTTP 503 with the header `Gotato-Not-Run: true` and `"not_run": true` in the body, gRPC `UNAVAILABLE` with the trailer `gotato-not-run: true`, and a `run_rejected` Instance Event with the reason `activation_limit`. A streaming request is refused the same way, before the stream starts, so a load balancer can retry it too. The mark tells this 503 from the other one, a store failure, which may have run.
- **What is not refused.** A Run on a Session that is active here already, which reaches the busy policy instead; creating or forking a Session, which activates nothing; and the short changes that open a Session under its lock (compact, tool activation, delete).
- **Why it counts Sessions in use.** Live Sessions are what an instance's memory and its Claims grow with, the Runner knows the count exactly, and it costs nothing to keep. Memory itself depends on how long the Sessions are and on the Agent, so a service that wants a byte budget sizes the count to it. With the warm Session ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)) the same setting counts the Sessions held in memory.
- **Why `/readyz` does not fail at the limit.** A full instance still serves the Sessions it holds, and a consistent hash sends their requests here. `/readyz` answers 503 for a drain only, and reports `active_sessions` beside `active_runs`, so an operator sees how close the instance is.
- While an instance keeps refusing new Sessions, the Sessions routed to it run elsewhere, and each of their requests takes one extra retry, until load moves.

**Gap:** cancel requests work only on the instance that runs the Session.

**Your load balancer** routes; Gotato never forwards a request between instances. It needs three settings:

| Setting | Why |
|---|---|
| a consistent hash on the `session-id` header | every request for one Session reaches one instance, its writer |
| one retry on another instance after a 503 marked "not run" | a refusal never ran, so a retry cannot run it twice |
| readiness from `/readyz` | an instance that is draining receives no new requests |

A request that creates a Session, such as `POST /v1/runs`, needs no header, and goes to any instance; its response carries the new ID in a `session-id` header, to send next time.

- **What reading the header means.** The endpoints read `session-id` as a check. It must agree with the Session ID in the path or in the body (`POST /v1/sessions` with an `id`); otherwise the request is refused with 400 on HTTP, or `INVALID_ARGUMENT` on gRPC, because the load balancer would have routed it for another Session. A missing or blank header agrees with everything, and on a request that creates a Session without naming an ID the header is ignored. The key is `session-id` in HTTP headers and in gRPC metadata. Gotato does not pick the Session from the header: the path or the body says which Session a request is about.
- **Where the new ID is set.** `POST /v1/sessions`, `POST /v1/runs`, a fork, and the gRPC `CreateSession`, `Run` and `ForkSession` answer with it. A streaming request that creates a Session cannot: its headers go out first, and its first Event, `session_created`, names the Session.
- **The "not run" mark.** A 503 with `Gotato-Not-Run: true` (gRPC: `UNAVAILABLE` with `gotato-not-run: true` in the trailers) never ran, so the load balancer may send it once to another instance. A connection that broke after the request was sent is different: it may have run, and is not retried.

The test harness has such a load balancer: `TestProxyRetriesAFullInstancesRefusalElsewhere` and `TestRandomInstancesGiveWhatOneInstanceGives` in `service/workload` hash on `session-id` over several instances, retry a refusal marked "not run" once, and drop an instance whose connection is refused. Examples of these settings for common load balancers come later.

Settings, on `service.Config` or `gotato serve`:

| Setting | Default | Why it exists |
|---|---|---|
| busy policy (`--queue`) | `reject` | a caller learns at once that the Session is busy, instead of waiting without knowing |
| `MaxActiveRuns` (`--max-runs`) | 0, no bound | the team sizes its own instances |
| drain timeout (`--drain-timeout`) | 10 seconds | long enough for most Runs to finish during a rolling deploy |
| Liveness TTL (`Config.Liveness.TTL`, service only) | 10 seconds | long enough to ride out a slow store; a dead instance's Sessions wait this long before another instance takes them over |
| Liveness wait (`Config.Liveness.Wait`, service only) | 5 seconds | a request for a Session that runs elsewhere waits this long for it before a 409 |
| activation limit (`Admission.MaxActiveSessions`, `--max-sessions`) | 0, no limit | the team sizes its own instances; past the limit a new Session goes to another instance |
| stream look (`Config.StreamPoll`, service only) | 1 second | how often an idle Session stream looks in the store for Events that another instance wrote, at the cost of one `Log` read per stream per look; negative turns it off |

`gotato-grpc` takes `--max-runs` and `--max-sessions` but not `--queue` or `--drain-timeout`, so its busy policy is always `reject`.

### G-F33 — Prompt cache retention and keepalive 🚧

A provider keeps a cached prefix for minutes, and a conversation that pauses longer pays full price to start again.

- **Retention:** the gateway asks a recognized provider for its longest cache retention: an hour on Claude, and 30 minutes on OpenAI's Responses API from GPT-5.6. It is set where the provider is configured, not per Agent: `cache_retention` for one model, and `GOTATO_GATEWAY_CACHE_RETENTION` for every model, winning over each model's own ([G-F07](#g-f07--models-and-providers-)). An hour on Claude doubles the price of a cache write; `5m` keeps its default.
- **Keepalive:** while a warm Session is in memory (G-F27), refresh its cache shortly before it expires. Each refresh costs a cache read of the whole prefix, so it pays only when the Session is likely to come back soon; it is a setting per Agent, with a limit.

**Gap:** the keepalive is not built. **Design first:** with the warm Session ([ROADMAP.md → Phase 6](ROADMAP.md#phase-6--warm-session)).

## Endpoints

### G-F14 — HTTP, gRPC and streaming 🚧

A bot, a job or another service talks to Sessions like any other API.

**HTTP.** Mount the handler from `service/httpapi` on your own mux. It serves these routes:

```text
GET    /healthz  /readyz  /v1/agents
POST   /v1/sessions      GET /v1/sessions      GET|DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/fork    GET /v1/sessions/{id}/events    GET /v1/sessions/{id}/context
GET    /v1/sessions/{id}/stream?after=N&record_epoch=E
POST   /v1/sessions/{id}/compact
POST   /v1/sessions/{id}/runs    POST /v1/sessions/{id}/runs/stream
POST   /v1/sessions/{id}/cancel  POST /v1/runs/{run_id}/cancel
POST   /v1/runs                  POST /v1/runs/stream
```

- A run request carries `prompt`, or `continue: true`, and may set its own deadline with `timeout_ms`.
- `GET /v1/agents` answers `{"agents": [...], "max_effect": {name: cap}}`; an Agent without an Effect cap has no `max_effect` entry.
- `GET …/events` answers every stored Event of the Session, read from its Record, as JSON Lines, and filters with `?kind=`.
- `GET /readyz` answers 200 with `active_runs` and `active_sessions`, and 503 with `"draining": true` while the instance drains.
- A Run that settles as failed, cancelled or past its deadline still answers 200, with the outcome in the body. Only request-level problems use error codes:

| Code | When |
|---|---|
| 400 | the request is invalid |
| 404 | the Session does not exist |
| 408 | the caller cancelled the request |
| 409 | the Session is busy, the Run is not active, the Runner is draining, or a stream cannot resume (`cannot_resume`, with the current `record_epoch`) |
| 413 | the body is too large |
| 429 | the Runner is at capacity |
| 500 | a settled Run could not be saved |
| 503 | the instance is past its activation limit (`overloaded`, marked not run), or the store failed |
| 504 | the request's deadline passed |

**Streaming.** The `/stream` routes send each Event of the Run's stream as server-sent events (`event: <kind>`, `data:` one flat JSON object), and end with `event: result`. The handler reads a Subscription, so a client that reads slowly never slows the Run.

- The stream answers 200 once the Run is admitted.
- So a bad or oversized body gets a status code, and so does a request refused at admission, which is known before any byte is written: draining answers 409, capacity 429, and past the activation limit 503 with `Gotato-Not-Run: true`, the same as `/v1/runs`. Every failure after admission (busy, not found, an unknown Agent, a store failure) arrives in-band as `event: error`.
- A Run the drain stopped ends its stream with `event: result` and then `event: reconnect`. Its `retry:` field and `retry_after_ms` give how long to wait before the Session is continued on any instance; when the result has no `run_id`, nothing ran, and the request is sent again.
- **Stream resume.** `GET /v1/sessions/{id}/stream` sends a Session's stored Events after a number, read from the Record, and then the live ones across Runs, as server-sent events, on any instance. A client that lost a stream reconnects through the load balancer, with the number of the last Event it saw, and the stream goes on without gaps or repeats ([DESIGNS.md → A stream resumes from a position](DESIGNS.md#a-stream-resumes-from-a-position)).
  - **The number.** Every stored Event on a Session's stream and on a Run's stream has its number as the SSE `id:`, which is its Entry's position in the Record ([G-F13](#g-f13--events-)). A browser's `EventSource` sends the last one back by itself as `Last-Event-ID`; the `after` parameter says the same, and wins. 0, or neither, starts at the first Event. A delta has no `id:`: it is not a position, the finished Message holds its text, and deltas sent before a reconnect are not sent again.
  - **The Record's epoch.** It is an identity chosen when the Record is created, which changes only if the Record is deleted and created again under the same ID. Without it, a client holding number 40 of a deleted Session would read the 41st Event of another. It is the `record_epoch` of `session_created`, of every Session summary, of a Run's result, and of the first frame of a Session's stream, `event: stream` with `{session_id, record_epoch, after, head}`, also sent as the `Record-Epoch` response header. A Record written before epochs names one from its creation time (`created-<time>`), the same on every surface: the summary, the list, a Run's result, `session_created` of a schema v1 document, and the stream. A store adapter keeps `Summary.RecordEpoch` with the rest of the Summary; the store contract test checks it.
  - **When the position cannot be resumed.** A client may send the epoch it saw, as `record_epoch` or the `Record-Epoch` header. If it is not the Record's, or `after` is past the Record's last Event, the request is refused before the stream starts: 409, code `cannot_resume`, and the current `record_epoch` in the body. The client starts over with `after` 0. A browser's `EventSource` cannot send the header, so it compares the epoch in the first frame with the one it kept. An unknown Session is 404.
  - **A Run's stream that is lost.** A Run's stream carries the same ids, and its first Event names the Session (`session_created` has the epoch). A client that loses it reconnects through the Session's stream after the last number it saw, and reads the rest of the Run from there. For an existing Session, the client learns the epoch from the response that created it, or from the first frame of the resumed stream.
  - **When a stream ends.** A drain ends a Session's stream, once every Run is saved and released, after the stored Events, with `event: reconnect`: `{"retry_after_ms", "why": "draining"}` and the SSE `retry:` field, the wait spread at random from half a second to one and a half. A Run's stream the drain stopped ends the same way. The first frame also sets `retry:`, a different wait for each client, which a browser uses after a connection that dropped without a word, so a lost instance does not make every client reconnect at the same instant. Any other end is an `event: error` and no reconnect. An idle stream sends a comment every 15 seconds (`httpapi.WithKeepAlive`).
  - **A Session deleted, or created again, under the stream.** A stream follows the Record it opened, the one its epoch names, and never delivers an Event of another Record under the same ID. It ends instead, with `event: error`. Over HTTP, a Session that is gone gives `{"error":"Not Found","message":"session: not found"}`, with no `code`, and one created again gives `cannot_resume`; over gRPC, `StreamSession` returns `NOT_FOUND` or `FAILED_PRECONDITION`. What was queued for it is dropped.
    - **On the instance that deletes the Session, opens or creates the new Record** (a Run, a tool change, a compaction, a create), **or opens a stream on it**, the old stream ends at once. A stream that read the old epoch while that happened reads the epoch again before it starts, and is refused: with 404 if the Session is gone, with 409 `cannot_resume` if it was created again. The open or create cuts only the streams that started before it read the Record, or wrote it, and that are of another Record. A stream that started after may be of a newer Record, since another instance can delete and create the Session again after the claim was written; the open reads the Record once more, and cuts such a stream only if its epoch is not the Record's now.
    - **On any other instance**, it ends at the first look that finds the Session gone, or at the first read that returns Events of the new Record. Until then it stays open and delivers nothing: a new Record that is no longer than what the stream has seen is noticed only once it grows past that, because an idle look reads only the Events after the stream's number.
    - **What makes it so.** Each Record has a Feed of its own on an instance, named by its Session ID and epoch, so a stream is never handed the live Events of another Record, and the stream of a new Record never waits for the old Record's numbers. Every read of the Record that returns Events is checked against the stream's epoch before they are delivered. A stream is registered before it reads the Record, so what the instance does to the Record meanwhile reaches it. `Runner.SubscribeSession` is the same stream.
  - **Which instance.** A stream on the Session's writer receives its Events as they are written. On any other instance it also receives them, from the Record, when its next look at the store finds them (`Config.StreamPoll`, one second by default), so a load balancer that sends a reconnect elsewhere still works, a little later. An idle stream costs one read of the Record after its number per look (`Log`, which is empty when nothing was written), on every instance, because between Runs no instance is the writer; the Record's epoch is read again (`Load`) only after a look that returned Events.

**gRPC.** `gotato.v2.SessionService` lives in its own Go module, so gRPC never enters a service that does not use it.

- Methods: `Contract`, `Agents`, `CreateSession`, `GetSession`, `ListSessions`, `DeleteSession`, `ForkSession`, `Run`, `StreamRun`, `StreamSession`, `CancelRun`, `Events`, `Context`, `Compact`.
- `StreamSession(session_id, after, record_epoch)` is the same stream as the HTTP route, as a server stream of `SessionUpdate`: a `start` message with the epoch, then the Events. The epoch is also in the header metadata `record-epoch`. A position that cannot be resumed fails with `FAILED_PRECONDITION` and the current epoch in a `record-epoch` trailer; an unknown Session with `NOT_FOUND`. The drain ends the stream with `UNAVAILABLE` and a `retry-after-ms` trailer, like `StreamRun`.
- The `session-id` metadata is checked and answered as the HTTP header is; a call refused as not run is `UNAVAILABLE` with the trailer `gotato-not-run: true`.
- An Event is a message with a header and a `oneof` that holds one message for each type of Event, so a client reads checked fields, not a JSON payload. A Message that an Event carries travels as JSON bytes.
- The `gotato-grpc` binary serves it on `127.0.0.1:8788` by default, with `--store`, `--gateway-config` and `--max-runs`.
- A Run the drain stopped sends its result, then ends `StreamRun` with `UNAVAILABLE` and a `retry-after-ms` trailer.

**Go API.** Call the Runner directly, and subscribe to Events as they happen: `Runner.StreamRun` for one Run, `Runner.SubscribeSession` for a Session from any number, and `Runner.SubscribeInstance` for the Instance ([G-F13](#g-f13--events-)).

**For all of them:**

- Both contracts report their version, now `"4"`, on `GET /healthz` and through `Contract`.
- Authentication and logging are yours: wrap the HTTP handler, or add gRPC interceptors.
- **Access hooks (planned, not built).** Wrapping the handler shows only the URL, not who owns a Session, and `StatusFor` has no code for "not allowed", so a refusal from a store adapter would answer 500. Planned: the caller's identity travels in `context.Context` to every extension and every store call, and a new "not allowed" error from either answers 403 on HTTP and the matching code on gRPC. Who may touch which Session stays the application's rule; Gotato builds the places to enforce it, not the rule.

**Gap:** the caller's identity does not reach extensions and stores. It is planned above.

## Watching and extending

### G-F13 — Events 🚧

Every step of a Session is reported as an Event, so you can watch, log or measure it. Each kind of Event is a Go type of its own, a stored Event is an Entry of the Session's Record, and a Session never waits for anyone who reads its Events ([DESIGNS.md → Every step is an Event, and a stored Event is an Entry](DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18)).

- **The Record is the Event log.** Every stored Event is an Entry of the Session's Record, and its number is that Entry's position, so every instance reads the same number. The number counts the whole Record, not one Run.
- **Written, then sent.** A stored Event reaches its subscribers only after its write. Most ride the write of their step; the few between steps, such as a retry, an Abort request or a queued message, get a write of their own. Deltas are sent at once, marked with the number of the Event they follow. A Session without a store, in library use, still numbers and sends its Events.
- **A Go type for each kind.** A caller switches on the type and reads checked fields. Every type embeds a `gotato.Header` with its time and the Session's ID, and the number, the Run and the Turn where it has them; an Instance Event has no number. In JSON an Event is one flat object whose `kind` names its type (`gotato.MarshalEvent`, `gotato.UnmarshalEvent`); in gRPC it is a `oneof`. A kind a later version adds comes back as a `gotato.UnknownEvent`, so a consumer tolerates it.

```go
sub, _ := live.SubscribeRun(ctx) // before Prompt
for {
	event, err := sub.Next(ctx)
	if err != nil { break }
	switch e := event.(type) {
	case gotato.TextDelta:
		fmt.Print(e.Text)
	case gotato.RunEnded:
		fmt.Println(e.Status, e.Number)
	}
}
```

Each Event is kept in one of three places: in the **Record**, which a subscriber can read again from any number; **sent only**, for deltas; or on the **Instance**, for an Instance Event, from an Instance that holds no Claim on the Session.

*Runs and replies*

| Event | When | Kept |
|---|---|---|
| `run_started`, `run_ended` | a Run starts, and settles with its status, its cause and its stop reason | Record |
| `turn_started`, `turn_ended` | a Turn starts, and ends with its usage | Record |
| `context_built` | a Turn's Context is built, with its prefix hash and sizes | Record |
| `reply_started`, `reply_ended` | the model's reply starts, and ends with the Message, the model that answered and why it stopped | Record |
| `text_delta`, `reasoning_delta` | a piece of the reply's text or reasoning | sent only |
| `reply_part_kept` | the text and reasoning streamed since the last part, kept every second or few KiB so that a crash loses at most the last part | Record |
| `reply_cut_off` | the reply's stream broke, and the text so far is kept as a Cut-off reply | Record |
| `model_retried` | the gateway calls the model again, with the attempt, the wait and why | Record |

*Tools*

| Event | When | Kept |
|---|---|---|
| `tool_started`, `tool_ended` | a Tool starts, and ends with its status and how long it ran; a call blocked before it ran has a `tool_ended` and no `tool_started`. A `tool_ended` rides the write that commits its group's results, so it is sent when the group is done: in a parallel group it can wait behind the slowest sibling, though its `elapsed_ms` and time are those of its own Tool | Record |
| `tool_progress` | a Tool reports its own progress | sent only |
| `tool_result_committed` | a Tool Result enters the Transcript, in the order the model asked | Record |
| `toolset_activated` | the model switches on a ToolSet | Record |

*Messages into a Session*

| Event | When | Kept |
|---|---|---|
| `run_requested`, `run_waiting`, `run_rejected` | a request for a new Run arrives, waits for a busy Session, or is refused, with why: busy, at capacity, draining or past the activation limit (`activation_limit`) | Instance |
| `message_queued` | a Steer or FollowUp waits for its point in the Run | Record |
| `message_committed` | a user Message enters the Transcript, with where it came from: the prompt, a Steer, a FollowUp, or the Loop's own continuation, which becomes a notice with G-F29 | Record |
| `message_dropped`, `message_withdrawn`, `message_rejected` | a waiting message is dropped because the Run ended first, withdrawn by its caller, or refused because the queue is full | Record |
| `notice_queued`, `notice_committed` | a notice ([G-F29](#g-f29--runtime-notices-)) waits in the queue, and goes into the Transcript right before a model call; planned with G-F29 | Record |
| `abort_requested` | an Abort arrives; the Run's `run_ended` follows, and the time between them is how long the Abort took | Record |

*Claims*

| Event | When | Kept |
|---|---|---|
| `session_claimed` | an Instance claims the Session, with why (a new Session, an idle one, a Writer that is gone, or a Writer it waited for) and which Instance held it before. An Instance that opens a Session it holds already writes it again, with the same Epoch | Record |
| `session_released` | the Writer lets the Session go | Record |
| `session_fenced` | a write is refused because another Instance holds the Claim | Instance |
| `liveness_lapsed` | this Instance's Liveness row was not renewed in time, so it writes nothing more | Instance |

*The Session*

| Event | When | Kept |
|---|---|---|
| `session_created` | a Session is created, with the Record's epoch (`record_epoch`); a fork names the Session and the position it came from (`forked_from`). It is the first Entry of every Record | Record |
| `metadata_changed` | a setting in the Session's metadata changes, such as its instruction, its model or its Agent | Record |
| `compaction_started`, `compaction_ended`, `compaction_failed` | a compaction starts, appends its Checkpoint, or fails, with why it ran: on demand or past the ceiling. With G-F06's Segments, also before the cache expires (`before_cache_expires`), before the Session leaves memory (`before_unload`), or after the provider refused a request as too long (`overflow`) | Record |
| `session_unloaded` | the Session leaves this Instance's memory, with why. A live Session in the Go API sends it when it closes | Instance |
| `session_deleted` | the Session's Record is deleted | Instance |

*The store and extensions*

| Event | When | Kept |
|---|---|---|
| `session_loaded`, `session_saved` | how long the store took to load the Session, and to write a Run's steps | Instance |
| `store_write_failed` | a write failed, for another reason than a taken position | Instance |
| `extension_failed` | an advisory extension failed, and the Run went on | Record |
| `events_skipped` | an Instance's stream fell behind and skipped Events; it says how many | Instance |

**Facts and log entries.** The Entries a rebuild reads are facts: `session_created`, `compaction_ended` (the two Checkpoints), `toolset_activated` (planned, [G-F10](#g-f10--tool-sets-and-the-registry-)), `message_committed`, `reply_ended`, `reply_cut_off`, `tool_result_committed`, `tool_started`, `metadata_changed` and `session_claimed`. Every other stored Event is a log entry, which a rebuild never reads. A load starts at the last Checkpoint and reads only the facts after it, with one exception: when the last Run of a Session never ended, the load also reads the log since that Checkpoint, rebuilds the Cut-off reply from the `reply_part_kept` Entries, and the Instance that takes the Session over stores it as a `reply_cut_off`. A crash loses at most the last part of a reply. A Record written before the Event design is still read: its Entries keep their old kinds, and new Events are appended after them.

**Subscriptions.** A Session puts each Event in its subscribers' queues and never waits for one:

| Subscription | For | What it carries |
|---|---|---|
| A Run's stream | the caller of a Run: `Session.SubscribeRun`, `Runner.StreamRun`, `/runs/stream`, gRPC `StreamRun` | the Run's Events and deltas; it is the Session's stream from the Run's first Event to its last, with the same numbers. For a Run the Runner starts, the first Event is the claim, or the creation of the Session, that the Run caused |
| A Session's stream | an application that shows a conversation, and a client that reconnects | every Event of the Session across its Runs, after any number: first from the Record, then live. `Session.SubscribeFrom`, `Runner.SubscribeSession` |
| An Instance's stream | the service's own telemetry | every Event of every Session on the Instance, and its Instance Events; a subscriber can keep only some Agents (`service.ForAgents`). `Runner.SubscribeInstance` |

- Each subscriber has one goroutine that forwards its Events in order, so a slow subscriber slows only itself.
- A subscriber that falls too far behind is no longer buffered in memory. It catches up from the Record by number and then rejoins the live Events, so it never misses a stored Event; only deltas can be skipped.
- An Instance's stream mixes Sessions and has no single number. When its subscriber falls behind, it skips Events and says how many in an `events_skipped` Instance Event; each stored Event names its Session and number, so the subscriber can read what it missed from the Record.
- With the Go API alone, a caller subscribes to the Session it built, before its first `Prompt`.
- `Runner.StreamRun` runs the Run on its own goroutine and calls its sink from the caller's, reading a Subscription, so a slow HTTP or gRPC client slows its own stream and never the Run.
- An Instance Event is not in a Run's stream: `session_loaded` and `session_saved` are read from the Instance's stream.
- A Run that is not saved (`SkipSave`) has only its Run's stream; its Events are in no Record, so they are numbered from 1 on that stream and are not Record positions.
- A stream that needs Events its source cannot read again, as a Session over a Record that is not stored can after the Record's window (the last 1000 Events), ends with `gotato.ErrEventsLost`.
- `Runner.StreamSession` opens a Session's stream for a client that reconnects: it checks the Record's epoch and the number first, and follows the Record when another instance writes it. HTTP (`GET /v1/sessions/{id}/stream`) and gRPC (`StreamSession`) serve it ([G-F14](#g-f14--http-grpc-and-streaming-)).

**What it replaced.** It was one break, with a before-and-after entry in [CHANGELOG.md](CHANGELOG.md):

- the map of fields in each Event, and `payload_json` on gRPC;
- the sequence number that counts within one Run;
- the `events` Log entry that kept a copy of the Events, `session.Recorder`, which wrote it, and `session_compacted`, which `compaction_ended` replaced;
- `EventObserver` ([G-F15](#g-f15--extensions-)), and the lifecycle stream of the Go API (`SubscribeLifecycle`), which `session_created` and `session_unloaded` replaced;
- today's names, such as `run_start`, renamed `run_started`, `message_update`, renamed `text_delta`, and `tool_execution_end`, renamed `tool_ended`.

**Gap:** the Runner and the endpoints do not take a Steer, a FollowUp or an Interrupt yet, so the message Events come only from a live Session in the Go API until the warm Session (Phase 6). `message_withdrawn` has no way to happen before that, and `session_unloaded` comes only from a live Session that closes.

### G-F15 — Extensions ✅

Extensions add behavior at fixed points of the Loop, without forking it. You install them with `gotato.WithExtension`.

| Stage | Runs |
|---|---|
| `RunPreparer` | before a Run, and may rewrite the Transcript first |
| `ContextTransformer` | after the Context is built, before it is sent |
| `MessageConverter` | when Messages are converted for the model |
| `PreToolUse` | before a tool runs, and may refuse the call |
| `PostToolUse` | after a tool runs |
| `TurnStopper` | at the end of a Turn, and may stop the Run |

- Each stage runs on the Run's own goroutine, so a slow extension slows the Run.
- An extension marked advisory may fail without failing the Run. The failure is stored as an `extension_failed` Event, with the stage.
- There is no observer stage: a Session never waits for a subscriber ([G-F13](#g-f13--events-)). A service reads Events from a Subscription instead.

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
| `conversation` | ten prompts on one Session, three lookups, and a last question only the earlier Turns answer |
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

**Fault injection.** These tests check that nothing is lost when an instance or a client goes away, and that many instances behave like one:

| Test | What it does |
|---|---|
| `TestCrashAtEveryWriteRebuildsTheLastStep` | plays each reference task with an instance that dies after its n-th write, for every n (126 crash points). Another instance must open exactly what that write left, with every tool call answered and a Context whose prefix hash matches a clean play's, and its next Run must complete. Each write holds at most one more stored Message than the one before. |
| `TestStreamDisconnectSettlesAndSavesTheRun` | over HTTP and over gRPC: a client that leaves mid-stream still gets its Run settled as cancelled and saved. |
| `TestManyInstancesOneWriter` | three Runners over one memory store, and over one file store, take concurrent prompts sent to random instances, and one instance is killed partway. Every completed Run's prompt lands once, followed by its reply, and no instance calls the model for a Session it does not hold. |
| `TestStreamCutAtEveryEventResumesEqualToAnUncutOne` | over HTTP and over gRPC: for each Event of each reference task, a Session's stream is cut after that Event and resumed after its number, by `Last-Event-ID` or `after` and with the Record's epoch. The two parts joined are the same stored Events, in the same order, numbered with no gap and no repeat, as a stream that was never cut read; no resumed delta follows an Event before the cut. |
| `TestRandomInstancesGiveWhatOneInstanceGives` | the hashing proxy: three instances over one store, each behind its own HTTP handler, and a proxy that hashes on `session-id`. Reference tasks run on Sessions created through the proxy, with prompts sent through it or straight to random instances, Sessions running concurrently, and in one variant an instance killed between two prompts. Each Session ends with the Transcript and the Events, under the same numbers and in the same order, that one instance gives. A stream read on an instance that is not the writer reads the same Events. |
| `TestConcurrentMessagesToOneSessionAreNeverInterleaved`, `TestKilledInstanceMidTrafficLosesOnlyTheStepInFlight` | concurrent messages to one Session through the proxy and straight to instances land once each, never interleaved; an instance killed in the middle of the traffic loses only the step in flight, and the proxy does not retry a request that broke after it was sent. |
| `TestProxyRetriesAFullInstancesRefusalElsewhere` | a full instance refuses new Sessions as not run, the proxy retries each once on another instance, where it runs, and the full instance activates none. |

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

**Gap:** an API-compatibility check in CI, which flags a removed or changed exported identifier, is scheduled for when mass testing begins (ROADMAP Phase 9).

## When something fails

Each feature above says what happens when its part fails. This table collects them in one place. Rows for parts not built yet describe the design; each feature's status says what is built.

| Case | What happens | Feature |
|---|---|---|
| A tool fails, or the model gets a tool call wrong | a failed tool result tells the model why, and the Run goes on; the Turn and tool-call limits bound a model that keeps getting it wrong | G-F09 |
| The model fails | the Run fails, and the Record keeps every step written before it, the text of a reply cut off mid-stream included | G-F01 |
| The provider refuses a request as too long | the Session is compacted and the call sent again, unless the Turn has compacted already, and the oldest Segments are merged when they alone are past the limit; a refusal after that ends the Run with an error code of its own, named when it is built | G-F06 |
| A Recap fails: the reply calls a tool, is cut off at the output limit, does not start with one of the five `## ` headings, uses any other `## ` heading, or has no `## What happened` | the compaction goes on, and the Segment is kept without a Recap, with its conversation and tool call lines; the Event says `recap: none`. A merge whose Recap fails leaves the Segments unmerged | G-F06 |
| A limit is reached | the Run settles with a status that names the limit | G-F01 |
| A Session is busy on its writer, or the instance is at `MaxActiveRuns` | the new Run waits or is rejected, as the admission policy says | G-F27 |
| A write fails | the Run stops at that step with `store_failure` or `conflict`, and a failed model-call write stops it before the model is called; the caller retries or reports it | G-F03 |
| An instance stops gently | it drains: every Run finishes, saves and releases before the instance disconnects; a Run still going at the drain timeout is stopped, saved and released, and its stream says to reconnect | G-F27 |
| An instance dies mid-Run | at most the step in flight is lost, and a tool that was running is answered with an unknown outcome; another instance takes the Session over once the dead instance's row expires | G-F27 |
| The writer loses its liveness | it writes nothing more, so it never calls the model; draining itself is planned | G-F27 |
| A write's outcome is unknown | the writer stops, and the Session is opened again from the store, which shows whether the write landed | G-F27 |
| A request reaches an instance that is not the writer | an idle Session is taken over; a busy one is waited for, then refused with 409 | G-F27 |
| An instance is full | it refuses to activate a new Session, with a 503 marked "not run" | G-F27 |
| A stream is cut | the client reconnects through the load balancer with the number of the last Event it saw; the stream goes on from the Record. If the Record is not the one it saw, or the number is past its end, the answer is 409 `cannot_resume` and the client starts over | G-F14 |

## Later

Ideas worth building once they are needed. None has a milestone yet.

| Idea | Note |
|---|---|
| G-F12 — MCP integration | MCP servers as a tool source, each server one tool set the model switches on (G-F10), with their annotations mapped to an Effect. Reaches tools a team already runs as MCP servers. |
| Telemetry adapters | a package that subscribes to an Instance's Events and maps them onto OpenTelemetry. Teams can write their own today ([G-N11](#g-n11--telemetry-backends-belong-to-applications)). |
| Model catalog | model names and prices next to the providers. A model's window is never looked up: it comes from `context_window` in its gateway configuration (G-F07). |
| Provider rate-limit information | limits, remaining calls and reset time reported with usage, so a service can slow itself down |
| Request IDs and idempotency keys | on the HTTP and gRPC endpoints |
| G-F24 — Examples | one-shot use, a persistent Session, compaction, forks, dynamic tools, CLI automation |
| Token budget per Run | the turn and byte limits already bound a runaway loop |
| Paging and filtering the Session list | matters at population scale, after one writer per Session across instances |
| Loading only the recent Transcript | once the Record is append-only, a load already starts from the last Checkpoint (G-F03); loading less than that waits until it is needed |

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

Gotato emits Events with a stable shape. Mapping them onto OpenTelemetry or another telemetry framework is a subscriber in the application or in a package of its own.

### G-N12 — Tool Approval Belongs to the Tools

An Agent's tools are written by the team and allowed by name, and an Agent can cap the Effect its tools may have, which hides every tool above it from the model. A tool that is risky for some arguments refuses those arguments in its own code. A team that wants an extra check before a call installs a `PreToolUse` extension. Gotato has no built-in approval system.

### G-N13 — No Cloud Coding Agent

Gotato leaves coding to developers' machines and to coding products. Sessions in the cluster investigate, decide and answer ([PHILOSOPHY.md, question 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)).

### G-N14 — No Remote Dev Environment, Shell or Sandbox on the Instances

A developer's environment does not move into the cluster. A Session acts only through the tools its Agent allows, and work that needs a machine runs on a laptop, in CI, or in a coding product.

### G-N15 — No General-Purpose Assistant

Gotato is for purpose-built agents. A general agent needs people to step in, and a purpose-built agent that needs people needs fixing ([PHILOSOPHY.md, question 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

### G-N16 — No Separate Platform

A platform beside your services is a second system to deploy and operate. Gotato is a library inside the services you already run.

### G-N17 — No Infrastructure

Discovery, routing, autoscaling and deployment already exist where your services run. Gotato behaves correctly when they scale it, and leaves them alone.

### G-N18 — No Ready-Made Clients

Bots, schedulers and front ends are the callers' own code. Gotato provides the HTTP handler, the gRPC service and the Go API they call.
