# Glossary

One name for each thing in Gotato, with its meaning in one line. When the code still uses an older name, the last column says so, until the code catches up.

The two central terms match how Anthropic's and OpenAI's agent APIs use them: an **Agent** is a definition, and a **Session** is a live conversation that runs one.

## Core

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Agent** | A named, reusable definition: model, instruction, Static blocks, tools, the highest Effect its tools may have, limits, and the TTL of its Sessions. It holds no conversation, no state and no status. One Agent runs many Sessions. | agent spec, agent config, template, sub-agent, child agent, worker, supervisor (roles belong to applications) | `service.Agent`. With the Go API alone, the options passed to `gotato.NewSession`. |
| **Session** | The minimal unit a system interacts with: one conversation running one Agent. It is a Loop and a Runtime. It has an ID, a Status, a message queue and a state machine, and it holds its Transcript, its Context and its tools, and uses the connections its Instance shares. It manages itself: it saves itself and checks its own health. After a time to live (TTL: 60 idle minutes unless its Agent sets another) it is compacted, unless that is turned off, and leaves memory; its Record stays in the Store, and any Instance can load it again. It is bound to no Instance. Everything inside it is hidden behind its API, which can still show its Context and Transcript for inspection. | conversation, thread, actor, instance | `gotato.Session`, the live Session. Until Phase 6 keeps it warm, the Runner builds one from the Record for each Run. `service.Runner` holds the lock and the run handle. |
| **Record** | What the Store keeps of a Session: an append-only log of its Messages, metadata changes, Runs, usage, recent Events and Checkpoints. Nothing already written is rewritten. Any Instance loads a Session from it, starting at its last Checkpoint. | the Session itself, snapshot | `session.Record`. Today the Store rewrites it whole once per Run; appending is planned. |
| **Loop** | The cycle inside a Session that calls the model and runs the tools it asks for, until the Run ends. Also called the agentic loop. It is a concept, not a type. | | `executeRun` in `gotato.Session` |
| **Harness** | The industry's word for the code around a model: its loop, its tools and its context handling. In Gotato, the harness is a Session's Loop and Runtime. | framework | |
| **Runtime** | The part of a Session around its Loop: its Status, its queue, its state machine, its saving and its TTL. A Session is a Loop and a Runtime. | | In part: `gotato.Session` holds the Status, its state machine and the Steer and FollowUp queue. Saving lives in `service.Runner`, and the TTL comes in Phase 6. |
| **Transcript** | A Session's stored history: every committed Message, in order. | history, log | `gotato.Transcript`, implemented by `session.Record` |
| **Context** | What the model sees on one Turn, built from the Transcript. | prompt, model view | `gotato.ModelContext`, built by a `ContextBuilder` |
| **Runner** | The part of a Go service that hosts Sessions: it starts them from Agents, routes messages to them, loads them from the Store and applies limits. | host, server | `service.Runner` |
| **Status** | The state a Session is in, such as idle, running or failed. The Session's state machine moves it. | state | `Session.Status` (created, idle, busy, closing, closed), and `RunResult.Status` for one Run. |

## Execution

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Run** | One request to a Session, from the input to the settled result. It may take many Turns. | job, task | `gotato.RunResult`, `service.RunRequest` |
| **Turn** | One model call plus the tool calls it asked for. | step, iteration | `turn_start`, `turn_end` events |
| **Message** | One entry in the Transcript: user input, an assistant reply or a tool result. | | `gotato.Message` |
| **Limits** | Caps on a Run: Turns, bytes, tool calls and deadlines. | quotas | `gotato.CoreLimits` |
| **Continue** | Resume a Loop without new input. | | `Session.Continue` |
| **Steer** | Insert a Message into a running Loop, to change its course. | | `Session.Steer` |
| **FollowUp** | Queue a Message to handle after the current Run. | | `Session.FollowUp` |
| **Abort** | Stop a Run. The Run still settles with a result. | kill | `Session.Abort`, `Runner.CancelRun` |

## Configuration and capabilities

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Model** | The interface Gotato calls for a reply, as a stream of events. | LLM | `gotato.Model` |
| **Provider** | A configured model endpoint: its URL, its key, its Wire API and its models. | vendor, adapter | `gateway` YAML config |
| **Wire API** | A request and response format that providers speak, such as OpenAI Chat Completions, OpenAI Responses or Anthropic Messages. Gotato implements each Wire API once, and any provider that speaks it works. | API format, protocol | `api:` in the `gateway` config |
| **Tool** | One capability a Session can call, with a name, a schema and an Effect. | function, action | `gotato.Tool` |
| **ToolSet** | A group of Tools that the model can activate as one. | toolkit | `gotato.ToolSet` |
| **Tool Registry** | The catalog of Tools, with which of them are active. | | `toolregistry.Registry` |
| **ToolSource** | Anything that supplies Tools at the start of each Turn. | | `gotato.ToolSource` |
| **Effect** | How much a Tool can change: read, write local, write external or destructive. | side effect, risk level | `gotato.ToolEffect` |
| **Effect cap** | The highest Effect an Agent's Tools may have. The model never sees a Tool above it. Only the Agent sets it, and there is none by default. | permission, policy, approval | `gotato.WithMaxEffect`, `service.Agent.MaxEffect` |

## State and history

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Store** | Where Sessions are saved, as Records: memory, a file, or your own database. The Store is the truth, and a Runner's memory is only a cache. | repository, persistence | `session.Store` |
| **Compaction** | Appending a Checkpoint whose summary stands in for an old part of the Transcript. The old part stays in the Record but is no longer loaded. | summarization, pruning | `modelctx.Compact`, `modelctx.AutoCompact`. Today they replace the old part instead of appending. |
| **Checkpoint** | An entry in a Record that stands in for everything before it: the summary Compaction wrote, and the point from which the original entries are loaded again. | snapshot | Not built. |
| **Fork** | Starting a new Session from another Session's Record, as a branch at one point of its log. It writes the Store and activates nothing. | clone | `session.Fork`, `Runner.Fork`. Today it copies the parent's entries. |
| **Static block** | Labeled reference content that an Agent places after its instruction, such as a schema or a runbook. It stays the same for every Turn, so the provider's prompt cache reuses it. | resource, reference | `modelctx.WithStatic`, set through `service.Agent.ContextBuilder` |

## Watching and extending

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Event** | A structured fact emitted while a Session works, such as a Turn starting or a Tool ending. | log line | `gotato.Event` |
| **Extension** | Code that wraps the Loop at a defined point, such as before a Tool runs or on each Event. | hook, middleware, plugin | `gotato.WithExtension` and its stage interfaces |

## Cluster and positioning

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Instance** | One running copy of your service. On Kubernetes it is a pod, and the proposal says "pod" for that reason. | replica, node | |
| **Admission** | The rule for how many Runs may run at once, and what happens when a Session is busy. | | `service.Admission` |
| **Drain** | Finishing work before an Instance shuts down: it takes no new Sessions, lets every Run finish, saves, releases its Claims, and only then disconnects. | | `Runner.Drain`. Today it stops new Runs, waits for the Runs in flight, and cancels them at its deadline. |
| **Writer** | The one Instance allowed to append to a Session's Record at a time. It holds the Claim, and it is wherever the Load balancer sends the Session's requests. | owner, leader, master | Not built. The lock is local to one process. |
| **Claim** | A Writer's hold on a Record: the Record names the Instance and an Epoch. It is valid while that Instance's Liveness row is alive. | lease, lock | Not built. |
| **Epoch** | A number in a Claim that grows with every new Claim. Every append checks it, so an Instance that lost its Claim can no longer append. | version, generation | Not built. |
| **Liveness row** | One row per running Instance in the Store, renewed on a heartbeat and timed by the Store's clock. When it expires, every Claim of that Instance ends at once. | heartbeat (the act of renewing it) | Not built. |
| **Load balancer** | The router in front of your Instances. It sends every request for one Session to one Instance by hashing its `session-id`. It belongs to your infrastructure, not to Gotato. | gateway (Gotato's `gateway` package talks to model providers) | |
| **Cloud agent** | An Agent whose Sessions live in your own cluster and are called like any other service. | | |
| **Agent as a service** | Gotato's aim: callers start and talk to Sessions like any other API, inside your own infrastructure. | | |

## Open questions

- **Compaction** is planned for a later phase. It is not part of an Agent's definition until then, though the code already has it.
