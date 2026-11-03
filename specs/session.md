# Session

A conversation with an agent has to survive a crash, a deploy and a week of silence, and it has to accept a new message while a Run is still going. Gotato splits this into parts with one job each. An Agent says how to run. A Session is the conversation that runs it. A Run is one request to that Session, made of Turns, and everything it does is written to the Session's Record as it happens.

## TL;DR
- An Agent is a definition and holds nothing; a Session is one conversation, and its Record in the Store is the truth. The live Session is a goroutine built over that Record for each Run.
- A Run is made of Turns, and a Turn of Steps that are each written before the next one starts. Every Run ends exactly once, in one of five ways.
- Steer, FollowUp and Interrupt reach a Run in flight, each at its own point. What is only queued in memory is lost if the Instance dies; what is written is not.
- A host registers an AgentProfile (the Agent as code) and, for a job that runs again, a TaskSpec that only narrows it, inline or from a store of its own. A Session looks its AgentProfile up by name each time it is built.
- Each value has one owner and one typed way in. A way in that is not the owner's is refused with `invalid_argument` before anything is written; nothing is dropped or overridden.

## Who holds what

The Agent is on top and holds nothing; the Record at the bottom holds everything that must last.

```text
 Agent            model, instruction, tools, Effect cap, Sampling, limits   (a definition, shared)
   │ runs, many times
   ▼
 Session          one conversation, named by its ID = its Record's ID
   │ built over, for each Run
   ▼
 live Session     a goroutine: Loop + Runtime (Status, queues)    (in memory, cheap to drop)
   │ writes every Step through a Writer
   ▼
 Record           append-only log of Entries                      (in the Store: the truth)
```

Read it top to bottom as "what is cheap to lose". The Agent is shared by many Sessions, so it can hold no conversation. The live Session is built when a Run needs it and can be dropped at any time, because the Record holds everything it needs. Its ID is the Record's ID, so whichever Instance builds it, it is the same Session. Keeping the Agent separate from the Session is what lets one definition serve many conversations. In code the definition is a `profile.AgentProfile`.

## A Session is built from parts

The live Session is one package and one goroutine, built from parts that each do one job ([decision 048](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)).

```text
 Session
   def      what the Agent gave: Model, instruction, extensions, limits, Sampling, cache key     no lock
   life     commands, the token that admits one Run, status, close                              no lock
   run      the Run in flight: ID and Turn, ending, how to stop it, the interrupt cancel,       run.mu
            the count of abort writes (a sync.WaitGroup)
   events   the Journal, what is staged, the numbers, the Feed                                  events.mu
   history  the Transcript, its encoded size, the fallback for a result too large for a Message no lock
   tools    the registry of installed and loaded Tools                                         tools.mu
   queue    the messages that wait for a Run (Steer, FollowUp, Interrupt, notice)               its own lock
   host     what the host set: the Run-start call, the hand-off of leftovers, the request ID    no lock
```

Four parts have a lock, and they are taken in one order: events, then run, then queue. The tools lock is taken with none of them held, and no call into the host is made under any lock. A Turn commits a Tool Result in stages, each its own call so that each can be read and fails alone: what the extensions make of it, what is spilled, its Message in the history, its Events, and their write (`commitResult`). The parts are tested alone, and `TestFileLayers` orders the files of the package.

## Who may set what

A value that a layer owns is set only through that layer. The table is the whole rule: the owner, the one typed way in, and what any other way gets.

| Value | Owner | The one way in | Any other way |
|---|---|---|---|
| instruction, model, tools, Effect cap, Sampling, Extensions, ceiling | the Agent (the definition) | `profile.AgentProfile` fields; the Runner builds every `SessionOption` | there is none: `AgentProfile` has no `Options`, and a Session has no instruction or ceiling of its own, so another one is another Agent. The one hole is the read-back tool: `AgentProfile.ReadBack` replaces the Runner's default |
| the AgentProfile a Session runs | the Runner | `RunRequest.Agent`, `CreateSessionRequest.Agent` | a `gotato.agent` key in metadata: `ErrReservedKey` |
| the TaskSpec a Session was opened for | the Runner | `RunRequest.Task`, `RunRequest.TaskSpec`, `CreateSessionRequest.Task`, `CreateSessionRequest.TaskSpec` | a `gotato.task_spec` key: `ErrReservedKey` |
| a tool switched off for a Session | the Runner | `Runner.SetToolActive` | a `gotato.tool.<id>` key: `ErrReservedKey` |
| the prompt cache key | the Runner | `Runner.Fork` | a `gotato.cache_key` key: `ErrReservedKey` |
| a single-valued option of a live Session | `gotato.Session` | one `WithTranscript`, `WithInstruction`, `WithMaxEffect`, `WithToolFilter`, `WithSampling`, ... | the option given twice: `NewSession` fails, naming it |
| what a Transcript stores | the Transcript | `Transcript.Capabilities()` | a wrapper that hides the Journal while it says `Durable`: `NewSession` fails |
| the stored Record | the Runner and its Writer | `CreateSession`, `Run`, `Fork`; they answer values (`record.Info`, `record.Document`) | `Runner.Store()` is gone, and a value returned has no `Set` |

Every metadata key that starts with `gotato.` (`host.ReservedPrefix`) is the Runner's. `CreateSession`, `RunRequest.Metadata`, `RunRequest.Update` and `POST /v1/sessions` refuse it, so a remote caller cannot replace an instruction, move a ceiling or switch an Agent through metadata. The application's own keys are free. A Transcript declares what it can do in `Capabilities` (an ID, a Journal, whether its Events are durable, whether a large Tool Result is spilled, an EventSource and loaded Tools); one that stores must declare all of the parts a store needs, and `NewSession` refuses a Transcript that does not, so a wrapper that loses durability fails at construction and not weeks later.

## Agent profiles and task specs

The Agent in the picture above is, in code, a `profile.AgentProfile`, held by a `profile.Registry` that the host gives the Runner. This diagram shows where a Session's AgentProfile comes from ([decision 044](../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)). The package `profile` is optional and sits between `host` and `store`: it imports no `host`, and the core runs without it.

```text
 host.Config{Profiles, DefaultAgent, Warm, TimeAwayAfter}
   │ asks the Registry by name, each time a Session is built
   ▼
 profile.Registry
   ├─ inline:     a Go value
   └─ from store: store.Definitions ─▶ document ─▶ resolved through the Catalog
   ▼
 AgentProfile  ◀── narrowed by ── TaskSpec (checked by TaskSpec.Narrow)
```

- **Two definitions.** An AgentProfile is a kind of Agent: name, instruction, model, Tools and ToolSets, Effect cap, Sampling, compaction parts, limits, a Run timeout and, optionally, its own ReadBack. A TaskSpec is a job that runs again: a name, its AgentProfile, a Background and bounds that only narrow it. How the host keeps Sessions (`Config.Warm`, `Config.TimeAwayAfter`) is the host's setting and not a profile's.
- **Four registrations.** `RegisterAgentProfileInline(p)` and `RegisterTaskSpecInline(t)` register a value; `RegisterAgentProfileWithStore(name, defs)` and `RegisterTaskSpecWithStore(name, defs)` register a name whose document is read from a `store.Definitions` at each lookup. Each name is registered once and a later registration replaces. There is no fallback store: a row that is not registered by name is not a definition.
- **Stored documents are data only.** A document names a model, Tools and ToolSets, and holds numbers and text; the `profile.Catalog` the service fills with its Go models, Tools and ToolSets resolves the names. A stored definition selects what the code offers and never adds to it. Values that are Go code (Extensions, ToolSources, the parts of `Compact`) are set inline.
- **Narrowing.** After a value is resolved, inline or stored, `TaskSpec.Narrow` refuses a spec that widens its profile (a Tool or ToolSet the profile lacks, a higher Effect cap, a longer timeout, a higher `MaxTokens`) and a name the Catalog lacks. Its other Sampling fields may differ.
- **Lookup per build.** A Session looks its AgentProfile up by name each time it is built, so a replaced profile reaches new and rebuilt Sessions and never a Run in flight. `Config.DefaultAgent` names the one a request that names none runs; an unknown name is `ErrUnknownAgent`.
- **A Session of a TaskSpec** is opened by `CreateSessionRequest.Task` or `.TaskSpec`, or by a Run with no Session: both go through one function, so they write the same Record. A Run of a TaskSpec is `RunRequest.Task` (a registered name) or `RunRequest.TaskSpec` (a value), never both. It opens a new Session, so it takes no `SessionID` and no `Agent`. The Background is appended once, after the instruction, and the TaskSpec is kept in the Session's metadata under `gotato.task_spec` as a JSON document, so every Instance builds the Session the same after the registered spec changes. The Session's `gotato.agent` is the TaskSpec's profile, and a later `RunRequest.Agent` on it is refused. `Admission.Task` names the TaskSpec to the Admitter.

## Run, Turn and Round

A Run is one request; a Turn is one model call and its Tool calls; a Round is one user Message and all that follows it.

```text
Messages  user         reply+call   Tool Result  reply        Steer        reply        user         reply
Turns                  ├─────────Turn 1─────────┤├───Turn 2──┤             ├───Turn 3──┤             ├───Turn 1──┤
Rounds    ├─────────────────────Round 1──────────────────────┤├────────Round 2─────────┤├────────Round 3─────────┤
Runs      ├───────────────────────────────────Run A────────────────────────────────────┤├─────────Run B──────────┤
```

Run A starts with a Prompt. Turn 1 asks for a Tool, Turn 2 answers, and a Steer that arrives as the Run would settle starts Round 2 and Turn 3 inside the same Run. A Round matters to the model's view: Claude and OpenAI are sent only the current Round's reasoning, and a Steer is a user Message, so it starts a new Round. A Turn is not a Round, and a Run is neither: the Run is the unit a caller waits for and the Record counts.

## One Turn, written Step by Step

Each arrow to the Store is a Step, and a Step is written before the next one begins.

```text
 Loop                                   Store              Model         Tool
  │ 1 build the Context from the Record    │                  │             │
  │ 2 context_built, reply_started ─ write ▶│  ◀ before the call              │
  │ 3 ─────────────── request ─────────────────────────────▶ │             │
  │   ◀── reply streams (parts kept) ─────────────────────── │             │
  │ 4 reply_ended ─────────────── write ───▶│                  │             │
  │ 5 tool_started, per group ─── write ───▶│                  │             │
  │ 6 ─────────── run the group ─────────────────────────────────────────▶ │
  │ 7 each Tool Result, in order ─ write ──▶│                  │             │
```

The model call is written before it is made, so a Writer that has lost its Claim stops at step 2, before it spends a model call. Tool calls are written as started before they run, so a crash leaves calls that are known to have started. Results commit in the order the model asked for them, even when the Tools ran in parallel. What a crash leaves is answered when the Session is read: a Tool call that started has an unknown outcome, one that never started was not run, and a reply cut off mid-stream is kept as a Cut-off reply that the model never sees again. A crash loses at most the Step in flight: the next Instance finds the Run started and never ended, and ends it as `failed` with the cause `interrupted`.

## How a Run ends

After every Turn the Loop decides whether to go on; the Run ends in one of five ways, and each writes exactly one `run_ended`.

```text
 Turn ends ─▶ TurnStopper says stop? ─ yes ─▶ completed (stopped by an extension)
                  │ no
                  ▼
        reply asked for Tools? ─ yes ─▶ next Turn (a waiting Steer goes in first)
                  │ no
                  ▼
        Steer or FollowUp waiting? ─ yes ─▶ next Turn, same Run
                  │ no
                  ▼
        completed (the model's final reply)

 at any point:  Abort or ctx canceled ─▶ canceled         ctx deadline ─▶ deadline_exceeded
                a failure, or the compaction ceiling ─▶ failed (the cause says which)
```

The five endings are the final reply, the `ctx`, a `TurnStopper`, the compaction ceiling and a failure. The normal one is the first: the model answers with no Tool call and nothing waits, so the Run is `completed`. The core counts no Turns and no Tool calls; a team that wants a cap writes a `TurnStopper`. The bottom lines can happen at any point, and `context.Context` is the only way to stop a Run, so an Abort reaches the model call, the Tool and the Store call together. A canceled Run still writes what it had committed, and an Abort writes `abort_requested` before `run_ended`. A Run whose last write fails is `failed` with `store_failure`, never `completed`.

## Steer, FollowUp and Interrupt

Three ways to reach a Run in flight; they differ in where the Message lands.

```text
 time ─▶   [ model call ][ Tool ][ model call ][ Tool ][ model call: final answer ]
                │           │          │          │               │
 Interrupt ─────┘ cuts the call, keeps the text as a Cut-off reply; next Turn starts
 Steer      ────────────── lands at the next Turn boundary (┴), never cuts work in flight
 FollowUp   ─────────────────────────────────────────── waits for where the Run would settle
                                                         │
 too late (after the last boundary) ─▶ kept for a following Run, or dropped (see below)
```

A Steer lands after the Tool Results of the Turn that is running, or at settlement, where it keeps the Run going for one more Turn. An Interrupt cuts a model call short; a Tool in flight finishes first, and then it lands like a Steer. A FollowUp is used only where the Run would end, so it never changes a Run that is making progress. The queues are bounded, and each message is written as queued before it can be committed, so the Record never shows a commit with no queueing. A message the Run took and could not commit goes back to the front of its queue, except the one whose commit failed, which is dropped (`commit_failed`). In the core, a message that reaches a Run already writing its end is refused with `invalid_state`; a Runner keeps it for the following Run instead.

## What a Session holds, and what a crash loses

The Record keeps the facts; the live Session keeps only what is cheap to rebuild.

```text
 in the Record (survives)             in memory only (lost with the Instance)
 ┌─────────────────────────────┐      ┌──────────────────────────────────┐
 │ Messages, Tool calls started│      │ Status: created idle busy        │
 │ Runs, usage, every Event    │      │         closing closed           │
 │ loaded Tools, metadata      │      │ Steer / FollowUp / Notice queues │
 │ Claims, Checkpoints         │      │ Subscriptions and their Feed     │
 └─────────────────────────────┘      └──────────────────────────────────┘
        a crash:   a Step in flight is lost;  a queued message is lost
        (its message_queued stays in the Record with no closing Event)
```

The Session has one Status at a time and takes one Run at a time: a second Prompt gets `busy` at once, and one on a closing or closed Session gets `session_closing` or `session_closed`. `Close` lets the Run in flight finish and drops what is still queued, each with a `message_dropped`. The Session never waits for a Subscription: a slow subscriber catches up from the Record. When a Run settles with messages still queued, the Runner starts a following Run with them on the same Instance. It drops them instead when the Run was stopped, the Instance is draining, or the Writer failed.

## What this buys you
- You can drop a Session from memory at any time and lose nothing but a queued message and the Step in flight.
- A caller can read any Run as one settled result: a status, a cause, and every Turn that was committed, also when it was canceled.
- Steer, FollowUp and Interrupt let you change a long Run's course without a second Run or a lock of your own.

## See also
[FEATURES.md → G-F02](../FEATURES.md#g-f02--sessions-), [G-F01](../FEATURES.md#g-f01--agents-and-runs-) and [G-F26](../FEATURES.md#g-f26--steering-a-run-), decisions [047](../plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md),  [004](../plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [016](../plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [021](../plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md), [028](../plan/decisions/028-every-step-is-written-as-facts-and-a-log.md) and [038](../plan/decisions/038-a-limit-guards-a-real-failure.md), decision [044](../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md), [GLOSSARY.md](../GLOSSARY.md). The Record, the Events and the Context have specs of their own: [record.md](record.md), [events.md](events.md) and [context.md](context.md).
