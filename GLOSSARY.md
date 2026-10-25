# Glossary

One name for each thing in Gotato, with its meaning in one line. When the code still uses an older name, the last column says so, until the code catches up.

The two central terms match how Anthropic's and OpenAI's agent APIs use them: an **Agent** is a definition, and a **Session** is a live conversation that runs one.

## Core

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Agent** | A named, reusable definition: model, instruction, Static blocks, tools, the highest Effect its tools may have, limits, its compaction ceiling, and the TTL of its Sessions. It holds no conversation, no state and no status. One Agent runs many Sessions. | agent spec, agent config, template, sub-agent, child agent, worker, supervisor (roles belong to applications) | `service.Agent`. With the Go API alone, the options passed to `gotato.NewSession`. |
| **Session** | The minimal unit a system interacts with: one conversation running one Agent. It is a Loop and a Runtime. It has an ID, a Status, a message queue and a state machine, and it holds its Transcript, its Context and its tools, and uses the connections its Instance shares. It manages itself: it saves itself and checks its own health. While it idles, it is compacted shortly before its prompt cache expires or its time to live (TTL: 60 idle minutes unless its Agent sets another) ends, whichever comes first, unless that is turned off; at the end of its TTL it leaves memory. Its Record stays in the Store, and any Instance can load it again. It is bound to no Instance. Everything inside it is hidden behind its API, which can still show its Context and Transcript for inspection. | conversation, thread, actor, instance | `gotato.Session`, the live Session. Until Phase 7 keeps it warm, the Runner builds one from the Record for each Run. `service.Runner` holds the run handle, and the Session's `session.Writer` writes each Step. |
| **Record** | What the Store keeps of a Session: an append-only log of Entries. Its Facts (Messages, Tool calls that started, metadata changes, Claims and Checkpoints) rebuild the Session, and its Log entries (Runs, usage, model calls, retries, the parts of a streaming reply) record what happened. Every Entry is also an Event, so the Record is the Session's Event log. Nothing already written is rewritten. Any Instance loads a Session from its Facts, starting at its last Checkpoint. | the Session itself, snapshot | `session.Record`; a Store appends its `session.Entry` batches. |
| **Loop** | The cycle inside a Session that calls the model and runs the tools it asks for, until the Run ends. Also called the agentic loop. It is a concept, not a type. | | `executeRun` in `gotato.Session` |
| **Harness** | The industry's word for the code around a model: its loop, its tools and its context handling. In Gotato, the harness is a Session's Loop and Runtime. | framework | |
| **Runtime** | The part of a Session around its Loop: its Status, its queue, its state machine, its saving and its TTL. A Session is a Loop and a Runtime. | | In part: `gotato.Session` holds the Status, its state machine and the Steer and FollowUp queue. Saving is `session.Writer`, which the live Session writes every Step through (`gotato.Journal`). The TTL comes in Phase 7. |
| **Transcript** | A Session's stored history: every committed Message, in order. | history, log | `gotato.Transcript`, implemented by `session.Record` and `session.Writer` |
| **Context** | What the model sees on one Turn, built from the Transcript. | prompt, model view | `gotato.ModelContext`, built by a `ContextBuilder` |
| **Runner** | The part of a Go service that hosts Sessions: it starts them from Agents, routes messages to them, loads them from the Store and applies limits. | host, server | `service.Runner` |
| **Status** | The state a Session is in, such as idle, running or failed. The Session's state machine moves it. | state | `Session.Status` (created, idle, busy, closing, closed), and `RunResult.Status` for one Run. |

## Execution

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Run** | One request to a Session, from the input to the settled result. It may take many Turns. | job, task | `gotato.RunResult`, `service.RunRequest` |
| **Turn** | One model call plus the tool calls it asked for. | step, iteration | `turn_started`, `turn_ended` Events |
| **Round** | A user Message and every reply and Tool Result after it, up to the next user Message. A Steer starts a new Round; a Continue without a new prompt stays in its Round. Claude and OpenAI are sent only the current Round's reasoning. | turn (a provider's word for it, and a Turn is something else) | no type; the `gateway` finds it from the last user Message |
| **Step** | One thing a Run does that is written as it happens: the prompt, a model call starting, part of a streaming reply, a reply, Tools starting, a Tool Result, a continuation. A Turn has several. A crash loses at most the Step in flight. | | no type: the Events a Step stages are written together by `gotato.Journal.Write`, which `session.Writer` implements |
| **Message** | One entry in the Transcript: user input, an assistant reply, a tool result or a notice. It carries the Run that committed it, if any. | | `gotato.Message`, with `RunID` |
| **Notice** | A Message that tells the model a runtime fact the user did not say, from Gotato, the service or an extension: this is the last Turn, a reply was cut off, something happened outside the conversation. It waits in the same queue as a Steer, goes in right before the next model call, never starts a Round, and never starts or extends a Run. A compacted stretch of history is a Segment, not a notice. | system reminder, user message | none yet (G-F29). Today the output-limit continuation is a user Message. |
| **Cut-off reply** | An assistant reply whose stream broke before the model finished. The Transcript keeps the text received so far, and the reply is never sent to a model again. | partial message | a `gotato.Message` with `StopCutOff` |
| **Limits** | Caps on a Run: Turns, bytes, tool calls and deadlines. | quotas | `gotato.CoreLimits` |
| **Continue** | Resume a Loop without new input. | | `Session.Continue` |
| **Steer** | Insert a Message into a running Loop, to change its course. | | `Session.Steer` |
| **FollowUp** | Queue a Message that the Run picks up when it would otherwise settle, so the same Run goes on. | | `Session.FollowUp` |
| **Abort** | Stop a Run. The Run still settles with a result. | kill | `Session.Abort`, `Runner.CancelRun`; it is an `abort_requested` Event, and the Run's `run_ended` follows |

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
| **Store** | Where Sessions are saved, as Records: memory, a file, or your own database. The Store is the truth, and a Runner's memory is only a cache. | repository, persistence | `session.Store`, with memory and file stores, and the conformance suite `testkit/storetest` for your own |
| **Entry** | One line of a Record, at a position no other Entry ever takes: a Fact or a Log entry. Every Entry is a stored Event, and its position is that Event's number. | row | `session.Entry`: its `Kind` is the Event's kind, and its `Seq` is the Event's number. |
| **Fact** | An Entry a rebuild reads: a Message, Tool calls that started, a metadata change, a Claim or a Checkpoint. The Context is built from the Facts on every Turn and is never stored. | | `session.LayerFact` |
| **Log entry** | An Entry that records what happened, for people and telemetry: a Run's start and end, usage, a model call, a retry, a release, part of a streaming reply. A rebuild does not read it, except the reply parts of a Run that never ended. | | `session.LayerLog` |
| **Compaction** | Closing what came since the last Checkpoint into a Segment, and appending a Checkpoint that lists the Segments it uses and holds the kept tail. The Messages it covers stay in the Record but are no longer loaded. | summarization, pruning | `modelctx.Compact`, `modelctx.AutoCompact`, through `Record.CloseSegmentAndCheckpoint`. Today it closes a Segment built by code; the Recap written by the model, `read_segment` and merging are not built (G-F06). |
| **Checkpoint** | A Fact that holds the whole state of a Session's Facts at one point, so it stands in for everything before it. Every Record starts with one, and Compaction appends one with every Segment, the kept tail and the active ToolSets. A load starts at the last one. | snapshot | the `session_created` and `compaction_ended` Entries (`Entry.IsCheckpoint`), and `Record.Checkpoint`. A Checkpoint keeps the active ToolSets; it lists Segments by number with `Record.CheckpointSegments`, and a compaction writes one with every Segment it closes (G-F06). |
| **Segment** | One stretch of a Session's history that a Compaction closed: the user's messages and each Run's final reply word for word, one line for each tool call inside its Run, and a Recap at its end. It has a number and is never rewritten, except that the oldest are merged at last. The model can read one back in full with `read_segment`. A Segment is written once, as its own Entry, and a Checkpoint lists the Segments it uses by number. | epoch (the Record epoch and a Claim's Epoch are other things), summary | `gotato.Segment`, stored as a `segment_closed` Entry and closed by `modelctx.Compact`; `read_segment` and merging are not built (G-F06) |
| **Recap** | The end of a Segment, which the Agent's own model writes on the cached prefix: what happened, the key facts the tools returned, the decisions, what is still open, and, for a Run compacted mid-Run, where it stands. A Recap fails when the reply calls a tool, is cut off at the output limit, does not start with one of the five `## ` headings, uses any other `## ` heading, or has no `## What happened`; a heading with nothing under it may be left out. A Segment whose Recap failed is kept without one. What writes it is a `modelctx.RecapWriter`, which is `ModelRecap` for the Agent's model, `NoRecap` for none, and `TruncateRecap` for tests. | summary | `gotato.Segment.Recap` holds the text; `modelctx.RecapWriter` is built with the new names (G-F06); its request type and `NoRecap` are not, and until the model writes the Recap a Segment has one only from a writer that is set |
| **Fork** | Starting a new Session from another Session's Record, as a branch at one point of its log. It writes the Store and activates nothing. | clone | `session.Fork`, `Runner.Fork`: the new Record starts with a Checkpoint of its parent, and names the position it branches at. |
| **Static block** | Labeled reference content that an Agent places after its instruction, such as a schema or a runbook. It stays the same for every Turn, so the provider's prompt cache reuses it. | resource, reference | `modelctx.WithStatic`, set through `service.Agent.ContextBuilder` |

## Watching and extending

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Event** | Something that happened to a Session or an Instance, as a value of its own type, named for what happened, such as `run_started`. A stored Event is an Entry, numbered by its position in the Record. A delta, such as `text_delta`, is only sent. An Instance Event happens where no Claim is held, and goes only to that Instance's Subscriptions. | log line | `gotato.Event`, an interface with one Go type for each kind, such as `gotato.RunStarted`, each with a `gotato.Header`. |
| **Subscription** | A queue of Events, in order: a Run's, a Session's from any number, or an Instance's. A Session never waits for one; a subscriber that falls behind catches up from the Record. | observer, listener | `gotato.Subscription`, from `Session.Subscribe`, `SubscribeRun` and `SubscribeFrom`, and from `Runner.StreamRun`, `SubscribeSession`, `StreamSession` (a Session's stream for a client that reconnects, with the Record's epoch) and `SubscribeInstance`. A `gotato.Feed` fills them. |
| **Extension** | Code that wraps the Loop at a defined point, such as before a Tool runs or at the end of a Turn. | hook, middleware, plugin | `gotato.WithExtension` and its stage interfaces |

## Cluster and positioning

| Term | Meaning | Not | In code today |
|---|---|---|---|
| **Instance** | One running copy of your service. On Kubernetes it is a pod, and the proposal says "pod" for that reason. | replica, node | |
| **Admission** | The rule for how many Runs may run at once, and what happens when a Session is busy. | | `service.Admission` |
| **Drain** | Finishing work before an Instance shuts down: it takes no new Sessions, lets every Run finish, saves, releases its Claims, and only then disconnects. | | `Runner.Drain`: it marks the Liveness row draining, admits no new Run and takes no Session over, waits for the Runs in flight, and at its deadline stops, saves and releases the rest; `Runner.Close` then removes the Liveness row. |
| **Writer** | The one Instance allowed to append to a Session's Record at a time. It holds the Claim, and it is wherever the Load balancer sends the Session's requests. | owner, leader, master | `service.Runner` as its Instance, writing through a `session.Writer` |
| **Claim** | A Writer's hold on a Record: a Fact naming the Instance and an Epoch, written at the position after the head that Instance read. It is valid while that Instance's Liveness row is alive. | lease, lock | `session.Claim` |
| **Epoch** | A number in a Claim that grows with every new Claim. A Claim takes a position in the Record, so an Instance that lost its Claim always writes to a taken position, and the Store refuses it. | version, generation | `session.Claim.Epoch` |
| **Record epoch** | An identity a Record gets when it is created. It changes only if the Record is deleted and created again under the same ID, so a number a client holds means the same Event only within one epoch. A stream that resumes is refused when the client's epoch is not the Record's, and an open stream follows one epoch and ends when its Record is deleted or created again. It is not the Epoch of a Claim. | version, generation | `session.Record.RecordEpoch`, `session.Summary.RecordEpoch` and `Epoch()`; `record_epoch` in `session_created`, in a Session summary and in a Run's result. |
| **Activation limit** | The hard limit on the Sessions an Instance has active. Past it, the Instance refuses a Run that would activate another Session, as not run, so the Load balancer sends it to another Instance. | quota, rate limit | `service.Admission.MaxActiveSessions`, `service.ErrOverloaded`, `gotato.ErrOverloaded` |
| **Liveness row** | One row per running Instance in the Store, renewed on a heartbeat and timed by the Store's clock. When it expires, every Claim of that Instance ends at once, and an Instance that stops removes it, with the same effect. | heartbeat (the act of renewing it) | `Store.Beat` and `Store.Instance`; `service.Runner` renews its row and removes it with `Runner.Close`. |
| **Load balancer** | The router in front of your Instances. It sends every request for one Session to one Instance by hashing its `session-id`. It belongs to your infrastructure, not to Gotato. | gateway (Gotato's `gateway` package talks to model providers) | |
| **Cloud agent** | An Agent whose Sessions live in your own cluster and are called like any other service. | | |
| **Agent as a service** | Gotato's aim: callers start and talk to Sessions like any other API, inside your own infrastructure. | | |
