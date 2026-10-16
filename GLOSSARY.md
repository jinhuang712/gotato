# Glossary

One name for each thing in Gotato, with its meaning in one line. When the code still uses an older name, the last column says so, until the code catches up.

The two central terms match how Anthropic's and OpenAI's agent APIs use them: an **Agent** is a definition, and a **Session** is a live conversation that runs one.

## Core

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Agent** | A named, reusable definition: model, instruction, Static blocks, tools, the highest Effect its tools may have, limits, and the TTL of its Sessions. It holds no conversation, no state and no status. One Agent runs many Sessions. | agent spec, agent config, template, sub-agent, child agent, worker, supervisor (roles belong to applications) | `service.AgentSpec`. `gotato.Agent` is the Loop today. |
| **Session** | The minimal unit a system interacts with: one conversation running one Agent. It is a Loop and a Runtime. It has an ID, a Status, a message queue and a state machine, and it holds its Transcript, its Context and its environment (its tools and connections). It manages itself: it saves itself, checks its own health and holds its connections. After a time to live (TTL: 60 idle minutes unless its Agent sets another) it is compacted, unless that is turned off, and leaves memory; its record stays in the Store, and any Instance can load it again. Everything inside it is hidden behind its API, which can still show its Context and Transcript for inspection. | conversation, thread, actor, instance | Split across `session.Session` (the stored record), `gotato.Agent` (the Loop, created per Run) and `service.Runner` (the lock and the run handle). |
| **Loop** | The cycle inside a Session that calls the model and runs the tools it asks for, until the Run ends. Also called the agentic loop. | | `gotato.Agent` (`executeRun`) |
| **Harness** | The industry's word for the code around a model: its loop, its tools and its context handling. In Gotato, the harness is a Session's Loop and Runtime. | framework | |
| **Runtime** | The part of a Session around its Loop: its Status, its queue, its state machine, its saving and its TTL. A Session is a Loop and a Runtime. | | Not built as one piece. Today a Run's status lives in `RunResult`, and saving lives in `service.Runner`. |
| **Transcript** | A Session's stored history: every committed Message, in order. | history, log | `gotato.Transcript`, implemented by `session.Session` |
| **Context** | What the model sees on one Turn, built from the Transcript. | prompt, model view | `gotato.ModelContext`, built by a `ContextBuilder` |
| **Runner** | The part of a Go service that hosts Sessions: it starts them from Agents, routes messages to them, loads them from the Store and applies limits. | host, server | `service.Runner` |
| **Status** | The state a Session is in, such as idle, running or failed. The Session's state machine moves it. | state | `RunResult.Status` covers a Run only. |

## Execution

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Run** | One request to a Session, from the input to the settled result. It may take many Turns. | job, task | `gotato.RunResult`, `service.RunRequest` |
| **Turn** | One model call plus the tool calls it asked for. | step, iteration | `turn_start`, `turn_end` events |
| **Message** | One entry in the Transcript: user input, an assistant reply or a tool result. | | `gotato.Message` |
| **Limits** | Caps on a Run: Turns, bytes, tool calls and deadlines. | quotas | `gotato.CoreLimits` |
| **Continue** | Resume a Loop without new input. | | `Agent.Continue` |
| **Steer** | Insert a Message into a running Loop, to change its course. | | `Agent.Steer` |
| **FollowUp** | Queue a Message to handle after the current Run. | | `Agent.FollowUp` |
| **Abort** | Stop a Run. The Run still settles with a result. | kill | `Agent.Abort`, `Runner.CancelRun` |

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
| **Effect cap** | The highest Effect an Agent's Tools may have. The model never sees a Tool above it. Only the Agent sets it, and there is none by default. | permission, policy, approval | `gotato.WithMaxEffect`, `service.AgentSpec.MaxEffect` |

## State and history

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Store** | Where Sessions are saved: memory, a file, or your own database. The Store is the truth, and a Runner's memory is only a cache. | repository, persistence | `session.Store` |
| **Compaction** | Replacing an old part of the Transcript with one summary. | summarization, pruning | `modelctx.Compact`, `modelctx.AutoCompact` |
| **Fork** | Copying a Session's saved state to start a new Session. | branch, clone | `session.Fork`, `Runner.Fork` |
| **Static block** | Labeled reference content that an Agent places after its instruction, such as a schema or a runbook. It stays the same for every Turn, so the provider's prompt cache reuses it. | resource, reference | `modelctx.WithStatic`, set through `AgentSpec.ContextBuilder` |

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
| **Drain** | Finishing or stopping work before a pod shuts down. | | `Runner.Drain` |
| **Lease** | Planned: one possible way for one Instance to own a Session at a time. The mechanism is an open question in DESIGNS.md. | lock | Not built. The lock is local to one process. |
| **Cloud agent** | An Agent whose Sessions live in your own cluster and are called like any other service. | | |
| **Agent as a service** | Gotato's aim: callers start and talk to Sessions like any other API, inside your own infrastructure. | | |

## Open questions

- **Compaction** is planned for a later phase. It is not part of an Agent's definition until then, though the code already has it.
