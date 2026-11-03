# Gotato — Features

What Gotato does today, what it will do, and what it will not. Each feature is told from the side of the developer who embeds Gotato in a Go service: what they call, what happens, and what they see when it fails. How a part works inside is in [specs/](specs/), why it is built that way is in [plan/decisions/](plan/decisions/README.md), and the principle each feature rests on is in [DESIGNS.md](DESIGNS.md).

Status marks: ✅ built · 🚧 built, with the gap named beside it · ⬜ planned. A ✅ says the feature is built, not that it has been proved in real use.

## TL;DR

Every feature by group, with its status. *Later* holds ideas with no milestone, and *Not planned* holds what Gotato will not build.

**Defining and running agents**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F01 Agents and Runs](#g-f01--agents-and-runs) | define an Agent, run it, get a settled result | ✅ |
| [G-F26 Steering a Run](#g-f26--steering-a-run) | Steer, FollowUp, Interrupt and Stop a Run in flight, from any instance | ✅ |
| [G-F29 Runtime notices](#g-f29--runtime-notices) | tell the model what is not the user's words | ✅ |
| [G-F02 Sessions](#g-f02--sessions) | create, fork, keep your own metadata | ✅ |
| [G-F36 Agent profiles and task specs](#g-f36--agent-profiles-and-task-specs) | register Agents and repeatable jobs, inline or from a store | ✅ |

**What the model sees**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F04 Context](#g-f04--context) | built every Turn, in an order the provider's cache can use | ✅ |
| [G-F05 Context inspection](#g-f05--context-inspection) | the next request, without calling the model | ✅ |
| [G-F06 Compaction](#g-f06--compaction) | long Sessions fit the window, and nothing becomes unreadable | ✅ |

**Tools**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F09 Tools and their Effect](#g-f09--tools-and-their-effect) | a Go function as a tool, its Effect, and the cap | ✅ |
| [G-F10 Tool sets](#g-f10--tool-sets) | tools that arrive as a group, in three install modes | ✅ |
| [G-F34 Tool search](#g-f34--tool-search) | the model reads a catalog and loads tools one by one | ✅ |
| [G-F12 The MCP adapter](#g-f12--the-mcp-adapter) | an MCP server's tools as a ToolSet | ✅ |
| [G-F35 Large Tool Results](#g-f35--large-tool-results) | a big result is stored whole and read in pages | ✅ |

**Models**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F07 Models and providers](#g-f07--models-and-providers) | three wire APIs, four replaceable layers, the facts you state about a model | ✅ |
| [G-F31 Recovering a cut stream](#g-f31--recovering-a-cut-stream) | a reply cut off partway is asked for again, not failed | ✅ |
| [G-F33 Prompt cache retention](#g-f33--prompt-cache-retention) | state how long your provider keeps the cache | ✅ |

**Storage and instances**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F03 Stores](#g-f03--stores) | memory, file, or your own database; an append-only Record | ✅ |
| [G-F27 Service: many instances](#g-f27--service-many-instances) | one writer, takeover, warm Sessions, drain, admission | ✅ |

**Endpoints, watching and extending**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F14 HTTP and streaming](#g-f14--http-and-streaming) | routes, status codes, server-sent events, stream resume | ✅ |
| [G-F13 Events](#g-f13--events) | every step is a typed Event you can read, log or measure | ✅ |
| [G-F15 Extensions](#g-f15--extensions) | six stages around the Loop | ✅ |

**Library and testing**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F37 A light library with a versioned contract](#g-f37--a-light-library-with-a-versioned-contract) | standard library only in the core, modules you opt into, breaks that are written down | ✅ |
| [G-F22 Test kit and measurement](#g-f22--test-kit-and-measurement) | fake models, a reference workload, fault injection | ✅ |

**Later** and **Not planned**

| Part | What it holds |
|---|---|
| [Later](#later) | twelve ideas with no milestone |
| [Not planned](#not-planned) | G-N01 to G-N28, things Gotato will not build |

Feature IDs never change. When features merge, the lower ID stays: G-F08 into G-F07, G-F11 into G-F10, G-F23 into G-F22. G-F16 (the CLI, which G-F17 to G-F21 had merged into), G-F25, G-F28 and G-F30 retired, and G-F30 became G-N21. G-F24 and G-F32 are ideas under *Later*.

## Defining and running agents

### G-F01 — Agents and Runs

**Status:** ✅

You define an Agent once and run Sessions of it. A message goes in, the Loop calls the model and the tools it asks for until the model answers, and a settled result comes out. In a small program that is `gotato.NewSession` and `Prompt`; in a service it is `Runner.Run` or an HTTP request.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local), [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md), [specs/errors.md](specs/errors.md)
- **Why:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md), [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md), [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md), [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A developer builds an agent from a model, an instruction and a few Go tools, and runs it in a small `main` with no server and no store.
- A service runs the same Agent for each user message and reads one settled result.
- A team caps Turns or tool calls with a `TurnStopper` of its own, since the core counts neither.

| Item | What happens |
|---|---|
| `Prompt`, `Continue`, `Abort` | `Prompt` sends a message and returns when the Run settles; `Continue` resumes with no new message; `Abort` stops a Run, which still settles |
| Tools in one Turn | one at a time by default; with `MaxParallelTools` above 1, up to that many at once, results kept in the order the model asked; a tool marked `Sequential` always runs alone |
| A settled result | every Run that starts ends `completed`, `failed`, `canceled` or `deadline_exceeded`, with its final message, stop reason and token usage, cache reads and writes included; `completed` never carries an error and every other status always does |
| How a Run ends | the model's final reply, its context, a `TurnStopper`, the compaction ceiling (`limit_exceeded`, or `context_overflow` when the provider still refuses), or a failure |
| The Run's deadline | its context; in a service `AgentProfile.RunTimeout`, 30 minutes by default (negative means none, and a TaskSpec may shorten it); a request's own timeout wins |
| A Run refused before it started | an empty result and an error that says it did not run |
| A write that fails | the Run stops at that step with `store_failure` or `conflict`; a Run whose reply or end could not be saved settles `failed`, never `completed`, and the next Run starts without that reply; a Run whose only failed write is the release stays `completed` and the instance writes the release again until it lands |
| Your code panics | no call into your code can end the process or hold a lock of ours; a tool's panic fails that call and the Run goes on, and a model, store or extension panic fails the Run with a code |
| A request's own ID | `RunRequest.RequestID` and `MessageRequest.RequestID` are recorded in `run_started`, and echoed in the response; Gotato does not deduplicate, so a repeated ID runs again |
| `Runner.Run` returns both | a response and an error together: the error says whether the request succeeded as a whole, and a non-empty `SessionID` means a Run started and settled |

**Limits.** They guard a real failure, are set with `gotato.WithLimits`, and a zero field means its default.

| Setting | Default | Guards |
|---|---|---|
| `MaxMessages`, `MaxTranscriptBytes` | 10,000 messages, 32 MiB | what a load reads and the model is sent |
| `MaxMessageBytes` | 4 MiB | one reply filling memory |
| `MaxSteerMessages`, `MaxFollowUpMessages`, `MaxNotices` | 8, 4, 16 | what waits for the Run's next boundary; one past the bound is refused with `limit_exceeded` |
| `MaxParallelTools` | 1 | tools running together that were never meant to |
| `ModelCallDeadline`, `ToolCallDeadline` | 2 minutes each | one stuck call holding the Run, even a tool that ignores its context; the call fails with `deadline_exceeded` and the Run goes on; negative turns a deadline off |
| `MaxTruncatedContinuations` | 0, off | above 0, the Run asks the model to go on after the output limit, up to that many times |

**Cost is your own conversion of `Usage`.** Gotato keeps no price table and reports no money. `InputTokens` counts every prompt token processed, cached or not, and the cache reads and writes are parts of it, so you price the uncached part at the input rate and the cache parts at their own rates. A provider that reports no cache field leaves that part 0.

### G-F26 — Steering a Run

**Status:** ✅

While a Run works you can change its course without starting over, from any instance. A message either joins the Run in flight or, when none is going, starts a new one.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen), [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/session.md](specs/session.md), [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md), [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md)

**Stories:**

- A user adds a correction while the agent is still working, and the agent takes it at its next Turn.
- A user presses stop, and the Run settles `canceled` with every Turn it committed kept.
- A bot sends a follow-up question that the Run answers before it settles.

| Kind | When it takes effect | Limit |
|---|---|---|
| Steer | at the next Turn boundary | 8 waiting |
| FollowUp | where the Run would otherwise settle; its Turns count as Turns of the same Run | 4 waiting |
| Interrupt | at once: the model call in flight is canceled and its text kept as a Cut-off reply, a tool in flight finishes (a write cut in half can leave half a side effect), and the message starts the next Turn of the same Run | shares the Steer queue |
| Notice ([G-F29](#g-f29--runtime-notices)) | right before the next model call; never starts a Run | 16 waiting |
| Abort, Stop | at once: everything in flight is canceled, tools included, and each tool decides how to stop safely; the Run settles `canceled` | |

- Over Go it is `Runner.Send` with a kind and `Runner.AbortSession`; over HTTP it is `POST /v1/sessions/{id}/messages` and `POST /v1/sessions/{id}/abort`.
- A Run in flight takes the message and the call answers queued with its ID (HTTP 202).
- With no Run going the message is the prompt of a new Run, and the call holds until that Run settles (HTTP 200 with the Run).
- A message that arrives while a Run is ending waits for the release and starts the next Run; it is never answered `busy`.
- A message the instance had accepted is not lost because its Run ended: it becomes a following Run on the same Session, in the order accepted, and the first caller gets the first Run's result.
- A message is dropped, with its reason in a `message_dropped` Event, only for a cause that ends the work: the Run was stopped or past its deadline, the instance drains, the writer failed, or the queues are full.
- A message the writer accepted but had not saved is lost if the writer dies; the caller sees the stream end and sends it again.
- A Stop with no Run anywhere fails with `invalid_state`; naming the Run (`host.AbortRun`) makes a retry safe.

**A message for a Run another instance writes.** Routing by your load balancer's hash comes first. An instance that is not the writer answers `not_writer`, not run and retryable, with the writer's address when it has one; if no Run is going it takes the Session over and starts a new Run with the message.

- The address is `host.Config.Address`, optional, and Gotato never connects to it.
- HTTP answers 409 `not_writer` with the `Gotato-Writer` header.
- The HTTP endpoints forward it themselves when you opt in (`httpapi.WithForwarding`); a Go embedder reads `host.WriterOf(err)` and forwards with its own client.
- A forwarded request carries `Gotato-Forwarded: true`, so a second instance answers `not_writer` and never forwards again.
- Only a message for a Run is answered this way; a new prompt for a busy Session follows the busy policy ([G-F27](#g-f27--service-many-instances)).

### G-F29 — Runtime notices

**Status:** ✅

Gotato, your service or an extension sometimes has to tell the model something that is not the user's words, such as that the last reply hit the output limit or that the user has been away for three days. A notice is a Message of its own kind, so the model does not take it for the user and no new Round begins.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md)

**Stories:**

- A service tells the model that a deadline is near, without pretending the user said it.
- A user comes back after a weekend, and the model is told how long they were away and what time it is.

| Kind | What it tells the model | Caused by |
|---|---|---|
| `continue` | the last reply hit the output limit, go on | `MaxTruncatedContinuations` |
| `time_away` | how long the user was away and what time it is now | a message that starts a Run after 30 minutes idle (`host.Config.TimeAwayAfter`; negative turns it off) |
| your own | something outside the conversation | your service or extension |

- It goes in right before the next model call, in arrival order, and never between a tool call and its result.
- It never starts or extends a Run; a Run that settles with one waiting leaves it for the next Run.
- It is appended once and never changed, so the cached prefix holds.
- A service's own notice survives its writer: the next Run on any instance queues it again.
- Gotato's own notices are not durable: a leftover is dropped with its Run, and `time_away` is worked out again from the last Run's end.
- Sending is `Session.Notice`, `Runner.Notice` or `POST /v1/sessions/{id}/notices`, which answers 202 with the notice's number at once and never waits for the model.
- `kind` is your label; the two reserved kinds, an empty kind and an empty text are refused with `invalid_argument`.
- A full queue or a notice over `MaxMessageBytes` is refused with `limit_exceeded`.
- A notice for a Run on another instance is answered `not_writer`; with no Run going the instance takes the Session over for one write.
- Every wire API receives a notice as tagged text in a user turn ([G-N25](#g-n25--a-notice-has-no-native-form)).
- Not yet run against a real provider: whether one answers a user turn that holds only a notice.

### G-F02 — Sessions

**Status:** ✅

A Session is one conversation that runs an Agent. It keeps every Message, a record of each Run, its token usage, its Events and every compaction, as its Record ([G-F03](#g-f03--stores)).

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md), [specs/record.md](specs/record.md)
- **Why:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md), [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md)

**Stories:**

- A service creates a Session for a chat with its own ID and a starting history, and runs it later by that ID.
- A user tries another answer from the same point, and the service forks the Session.
- A service keeps its own fields, such as a chat ID or an owner, in the Session's metadata.

- **Create** with `POST /v1/sessions` or `Runner.CreateSession`, optionally with your own ID, metadata and a starting history; it answers a summary, and an ID that is taken fails with `already_exists`. Instead of an Agent it may name a TaskSpec ([G-F36](#g-f36--agent-profiles-and-task-specs)).
- **Fork** with `Runner.Fork` starts a new Session from another's Record and records the parent and the position. It activates nothing, so the first request activates it on whichever instance receives it, and it keeps its parent's prompt cache key so its first Turn reads the parent's cached prefix.
- **One Run at a time:** a second is rejected or waits as the busy policy says ([G-F27](#g-f27--service-many-instances)).
- **The instruction and the compaction ceiling are the Agent's.** A Session has neither of its own, so another instruction is another Agent.
- **Metadata** holds your fields, and the keys the Runner owns start with `gotato.`: the Agent, the TaskSpec the Session was opened for, a tool switched on or off, the model last asked for, and the cache key of a fork.
- **A caller that sets a `gotato.` key is refused**, not ignored: `invalid_argument` (HTTP 400, not run) names the key before anything is written.
- **Changing metadata** is a Go call, `RunRequest.Update`, applied under the Run's claim as its first write; an empty value deletes the key.
- **Over HTTP** metadata is set at creation only, and there is no call that changes metadata alone. A value that changes often belongs in the application's own store, keyed by the Session ID.

### G-F36 — Agent profiles and task specs

**Status:** ✅

A host registers the Agents it runs as AgentProfiles, and the jobs that run again as TaskSpecs, in one Registry, from code or from a store of its own. The core runs without them; they are in the optional package `profile`.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md), [specs/record.md](specs/record.md)
- **Why:** [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md), [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md), [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md), [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md)

**Stories:**

- A service registers a support Agent and a morning report job in code, and runs the job by name every day.
- A team keeps its definitions in its own database, and the service reads them by name when a Session is built.
- A job narrows its Agent, so the report can read orders but never refund one.

- **An AgentProfile** is the Agent as a host registers it: name, model, instruction, tools and ToolSets, Effect cap, Sampling, extensions, context builder, limits, compaction parts, the read-back tool and a Run timeout. How a host keeps its Sessions, such as the warm time and the time-away notice, is the host's own setting.
- **A TaskSpec** is a job that runs again: a name, its AgentProfile, a Background text, and bounds that only narrow it (a subset of tools and ToolSets, an Effect cap no higher, a timeout no longer, an output limit no higher).
- **A TaskSpec that widens its profile is refused** with the reason named; an unknown tool or ToolSet name is refused the same way, and a name that no TaskSpec holds is `invalid_argument`, not run.
- **Four registrations** on a Registry: an AgentProfile or a TaskSpec, each inline as a value or from a store by name. Each name is registered once and a later registration replaces it. A name that is not registered is not a definition, whatever a store holds.
- **A stored definition is data only:** a model name, tool and ToolSet names, numbers and text. It selects from the name catalog of what your code offers and never adds to it. Extensions and the other Go values are set inline.
- **A Session looks its AgentProfile up by name each time it is built,** so a replaced profile reaches new and rebuilt Sessions and never a Run in flight.
- **Running a TaskSpec opens a new Session.** The Background is added once after the instruction, and the Record keeps the TaskSpec as it was, so every instance builds the Session the same after the registered spec changes. `RunRequest.Task` and `CreateSessionRequest.Task` name a registered one, and `TaskSpec` carries one as a value; the two are exclusive and neither takes an Agent.
- **A caller-chosen ID is the retry point** of a create: a second create of it is `already_exists` and makes no second Session.
- **Listing:** `Runner.AgentProfiles()`, `Runner.AgentProfile(ctx, name)` and `Runner.Tasks(ctx)`; over HTTP `GET /v1/agents` and `GET /v1/tasks`. `POST /v1/sessions` takes `task`, a registered name and never a value, and not together with `agent` (400 `invalid_argument`).
- **`host.Config.DefaultAgent`** is the AgentProfile a request that names none runs; empty means none, and such a request fails with an unknown-agent error.
- **A hosted AgentProfile may replace the read-back tool** the Runner installs ([G-F35](#g-f35--large-tool-results)); nil keeps the default.

## What the model sees

### G-F04 — Context

**Status:** ✅

On every Turn Gotato builds what the model sees from the Session, in a fixed order so the provider can reuse its cached prefix: the instruction, then the tools sorted by ID, then the Transcript. You can inspect it exactly as it is sent ([G-F05](#g-f05--context-inspection)).

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end), [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [006](plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md), [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md), [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md)

**Stories:**

- A developer puts a schema or a runbook in the Agent's instruction and it is the same bytes on every Turn.
- A team replaces the Context builder to choose what the model sees.

- **Reference text** the Agent carries goes in its instruction. Nothing that changes every Turn is added; a fact the model needs now, such as the time, comes from a tool.
- **Cache markers** go after the system part, after the tools and on the last message (Anthropic `cache_control`, the last four kept).
- **A cache key** rides on every request so a provider serves one conversation from one cache: a hash of the Session ID in a service, `gotato.WithCacheKey` otherwise.
- **Retrieval, a window or any other way to choose what the model sees** is your own `ContextBuilder` ([G-N10](#not-planned)).

### G-F05 — Context inspection

**Status:** ✅

You can see what the model would receive now, without calling it.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md)

**Stories:**

- A developer asks why a Session got expensive and reads the size of each part of the next request.

- `GET /v1/sessions/{id}/context` returns the request the next Turn would send, built as a Run would build it, extensions and tools included; in Go it is `Session.InspectContext`, which fails with `busy` during a Run.
- The report gives the prefix hash, the system part's size, a token estimate (bytes divided by 4) for each part of the request (instruction, tool definitions, Segments, history, Tool Results), and whether the next Run compacts first because the size is past the ceiling. `gotato context inspect` prints each part with its share.
- Every Turn emits `context_built` with its prefix hash and the same part estimates, which the reference workload checks against an inspection of that Turn's Transcript.
- **Limits:** inspection builds as a Run's first Turn does, so it cannot know a tool loaded later in a Run. Extensions and tool sources run during inspection, so they must not have side effects.

### G-F06 — Compaction

**Status:** ✅

A Session can live for weeks and a long Run can outgrow the window. A compaction closes what came since the last Checkpoint, less a kept tail, into a Segment, and never rewrites the Record. Nothing moved out of the model's view is unreadable: the model can read any original back by its reference.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view), [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [033](plan/decisions/033-code-builds-a-segment-and-the-model-writes-only-the-recap.md), [034](plan/decisions/034-a-segment-is-written-once.md), [043](plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md), [046](plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md)

**Stories:**

- A support Session lives for a month, and its requests keep fitting the model's window with no setting beyond the window.
- A developer compacts one Session by hand, keeping the last few Messages.
- A team swaps one part of the compaction, such as when to compact or where to cut, without copying the rest.

**What the model keeps.** Code builds a Segment, and only the Recap comes from a model.

| In the Segment | How |
|---|---|
| the user's messages, each Run's final reply | word for word, cut at 2K tokens each with a line saying `read_segment` returns the rest; each carries a reference the model can read back |
| each tool call | one line inside its Run with time, tool, arguments cut at 200 characters, `ok` or its error code, and the reference of its Tool Result; repeats counted |
| a service's notice | word for word; Gotato's own notices are left out and the Recap covers them |
| the rest of a Run's text, reasoning and tool results | left out of the Context; a Tool Result and a kept message are read back by reference with `read_result`, the Segment in full with `read_segment` |
| the Recap | what happened, key facts, decisions, open items and where it stands |

- **The Recap is off by default.** The Agent's own model writes it when a team turns it on, and it should be turned on only after the team's own check on its model endpoint passes. A Segment with no Recap, or whose Recap failed the rules, is kept without one and its compaction says why. Not yet run against a real model.
- **The kept tail** is the latest Run's Turns up to 20K tokens and at most a quarter of the window, cut at a Turn boundary so a tool call keeps its result. It always holds at least the latest Turn.
- **Loaded tools** the closed Segment or the kept tail called stay loaded; the others unload ([G-F34](#g-f34--tool-search)).
- **Reasoning:** when the model's facts say the provider takes back only the current Round's reasoning, the kept tail loses its reasoning.

**When it runs.** Each trigger is recorded as the compaction's `why`.

| Trigger | When |
|---|---|
| Past the ceiling | before any model call, mid-Run included |
| Overflow | the provider refuses a request as too long, before any output; the call is sent again after the compaction, once per Turn |
| Idle | a warm Session has been idle until 5 minutes before its prompt cache or its TTL would end, and the stretch since the last Checkpoint holds at least 20K tokens |
| On demand | `POST /v1/sessions/{id}/compact` or `Runner.Compact`, with `keep_messages` or `min_tokens` |
| Past budget | at the start of a Run, when the Segments pass their budget and there is nothing to close (a merge only) |

- **The ceiling** is the model's window less the larger of 10% of it and 16K tokens, unless the Agent sets `CeilingTokens`; a negative value turns the trigger off.
- **The ceiling is a hard line, not a cost knob.** A Run still past it after the Turn's compaction ends `limit_exceeded`, whoever set the ceiling. A team that wants a cost cap writes its own `TurnStopper`.
- **No window and no ceiling means no automatic compaction by size.** Compaction then runs only after the provider refuses a request as too long, and only if the refusal is recognized. State the window to compact before a refusal.
- **Merging:** Segments have a token budget, half the ceiling or 32K tokens with none. Past it, the oldest merge into one Segment that always opens the Context and is named by its range. The originals stay readable by their own numbers. The merged Segment's Recap is written from the Recaps alone, or by code as a digest when no model writes it.
- **The idle compaction** runs on the warm Session's own timer, outside any Run, and a message that arrives cancels it. It is on by default and turned off with `WarmPolicy.NoIdleCompaction`. A service with its own timer calls the compact endpoint shortly before the cache expires.
- **What a team can replace:** when to compact, where to cut, how a Segment reads, the Recap writer and how many Segments merge are parts behind small interfaces; the frame that writes and keeps the guarantees is fixed.
- **Settings** are numbers only: the ceiling, the window, the Segment budget, the reasoning drop and the time zone of rendered times (UTC by default) on the compaction policy, and the tail size on the default cutter.

| A compaction that fails | What happens |
|---|---|
| the Recap fails | the Segment is kept without one; the call's usage is still recorded |
| a part fails, panics or breaks a rule | the compaction is dropped with `compaction_failed`, nothing is written and the Run goes on |
| the Record cannot be written | the compaction is dropped and the Record is as it was; an on-demand call returns the error and the Run goes on |
| the Run is stopped during it | the Run settles `canceled` |

## Tools

### G-F09 — Tools and their Effect

**Status:** ✅

A tool is one thing an agent can do. A Go function becomes a tool, and each tool declares how much it can change: its Effect.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [005](plan/decisions/005-no-approval-system.md), [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md), [014](plan/decisions/014-the-effect-cap-hides-tools-above-it.md), [015](plan/decisions/015-only-the-agent-sets-the-effect-cap.md), [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md)

**Stories:**

- A developer turns an order lookup function into a tool, and the model's arguments are checked against its schema before it runs.
- A team gives a read-only Agent a cap, and a refund tool in the same service is never offered to it.

| Item | What happens |
|---|---|
| A Go function as a tool | `gotato.NewFuncTool`; the input schema comes from the Go type, and every call is checked against it before the tool runs |
| Effect | `read` < `write_local` < `write_external` < `destructive`; a tool that declares none counts as destructive |
| Effect cap | `WithMaxEffect` or `AgentProfile.MaxEffect`, none by default; a tool above it is in no request, no inspected Context, no tool listing and no search index; a guessed call of one gets a failed result saying the Session does not allow it |
| Who sets the cap | only the Agent; a Session can neither raise it nor switch on a hidden tool |
| A Tool Result | carries a status and a `Code` that says why a call failed (`tool_resolution_failure`, `tool_argument_failure`, `tool_execution_failure`, `deadline_exceeded`, `tool_not_loaded`); no tool or extension can set or change it |
| A tool's error | becomes a failed result; its text reaches the model only if it is a `RuntimeError` or implements `PublicError`, otherwise it reads `tool failed` |
| A bad call | an unknown tool, or arguments that are not valid JSON or fail the schema, come back as a result that says what was wrong, and the Run goes on |
| `ToolUse.SessionID` | the Session a call belongs to, so an extension that asks a person knows which conversation to ask in |
| Arguments | are bytes, so a Record's JSON shows them as base64; the call is in the Message that holds it |

### G-F10 — Tool sets

**Status:** ✅

The tools a Session offers can change while it runs. A ToolSet is a group of tools with a name, a description and one source, such as an MCP server; a Tool is the unit of loading.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md), [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md), [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)

**Stories:**

- A service installs an MCP server's tools as a group, and the model sees only the group's description until it needs one.
- An operator switches one tool off for one Session without touching the Agent.

| Install mode | What the model sees |
|---|---|
| active (`WithActiveToolSet`) | every tool of the set, always, as a base tool |
| listed (`WithToolSet`, the default) | the set's description and all its tool names, in the catalog |
| folded (`WithFoldedToolSet`) | the set's description and a count of its tools |

- **A tool's ID is `<set>.<tool>`,** and that namespace is the ToolSet's: a tool of your own cannot take an ID that starts with an installed set's name and a dot, and `search_tools` is reserved. A dotted name outside every namespace is yours.
- **Tools come from your own code or an MCP server,** through `WithTools` or a `ToolSource` that is asked again at the start of every Turn. A source that fails keeps its last set and stores an `extension_failed` Event.
- **A ToolSet is resolved when a Run starts,** never inside one, so the catalog is the same on every request of a Run and an MCP server's changed tool list takes effect at the next Run.
- **A ToolSet that fails** is skipped whole: the catalog keeps its description with `(unavailable now; no tools)`, the other tools work, and a warning Event comes once per Run while it stays down.
- **Loaded tools** stay until the next compaction ([G-F34](#g-f34--tool-search)), and a loaded tool its ToolSet no longer carries is unloaded before the Run's first Turn.
- **Per Session,** `Runner.SetToolActive` switches a tool on or off; a tool switched off is never in the catalog, found or loaded.
- **Listing:** `Runner.Tools` names every tool of every ToolSet with its Effect and whether it is loaded and active. It is a Go call; over HTTP there is no tool listing or switch.
- **In a service** the Agent's own tools, its sources and the two read-back tools are one source, so an ID that two of them supply is refused with `invalid_argument`.

### G-F34 — Tool search

**Status:** ✅

A service that connects MCP servers can offer hundreds of tools. The model reads a catalog and loads the tools it needs, one by one, with `search_tools`.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md), [006](plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md)

**Stories:**

- A model facing 300 tools reads one catalog, loads the three it needs, and the request grows only by those.

| Item | What happens |
|---|---|
| The catalog | the description of `search_tools`; lists each ToolSet by its install mode and a loaded tool like any other, so its bytes do not change while tools load |
| `search_tools` | declares `read`, so every cap keeps it; `names` loads those tools; `queries` searches, loads the top 3 and lists 5 more; `toolset` lists a folded set's tools and loads none |
| Search | BM25 over each tool's ID and description; `ToolSearcher` plugs in your own |
| The index | only tools the Session may use: not above the Effect cap, not switched off |
| A call to an unloaded tool | does not run; the tool is loaded and the failed result says to call it again; it costs one round |
| How long a tool stays | until the next compaction, which keeps the tools the closed Segment or the kept tail called |
| The cache | `native` on `anthropic-messages` and `openai-responses`: every tool is in the request from the start, deferred, so the list has the same bytes on every request; `list` on chat completions, which has no native form: a loaded tool is added to the list, which costs one cache read per load. Chosen with `Deferred` (`deferred_tools` in the file; `native` on chat is refused) |

- **Not yet run against a real provider.** The `tool_search` reference task passes through both native forms on the fake provider. How a provider expands a deferred tool reference is not known yet. A team whose endpoint refuses `native` sets the list form.
- A change a service makes in the middle of a Session, such as a tool switched on for one Session, rewrites the tool list: see *Later*, *Changes that keep the cache*.

### G-F12 — The MCP adapter

**Status:** ✅

Tools a team already runs as MCP servers reach a Session as a ToolSet. The adapter is `adapter/mcp`, a Go module of its own; the core and runtime packages never import it. A server is one ToolSet, and its tools are `<server>.<tool>`.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md), [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md)

**Stories:**

- A team points a Session at a CRM server over HTTP and the model finds its tools through the catalog.
- A team renames a server's tool and hides another before the model sees either.

| Option | What it does |
|---|---|
| `mcp.NewToolSet(name, transport, ...)` | the ToolSet of one server; connects to nothing until first used, so a server that is down never fails it |
| `mcp.StreamableHTTP`, `mcp.StdioCommand` | the transports: HTTP used statelessly so any instance can make any call, or a subprocess shared by the instance's Sessions |
| `WithDescription` | the catalog description; without it the first paragraph of the server's instructions |
| `WithConnectTimeout` | bounds one connect, 10 seconds by default |
| `WithToolEffect`, `WithDefaultEffect` | the Effect of one tool, or of every tool of a server that sends no annotations |
| `WithToolFilter`, `WithToolRename` | leave a tool out, or change only the name the model sees |
| `WithSequentialWrites` | marks every tool above `read` as `Sequential`; off by default |
| `WithListCache(ttl)` | keep the tool list for a time unless the server says it changed; 0 by default, so a change is seen at once |
| `Skipped()`, `Close()` | the tools left out and why; end the connection |

- **Effect comes from the server's annotations,** with the specification's defaults: a read-only hint is `read`, a non-destructive closed-world tool is `write_local`, a non-destructive open-world tool is `write_external`, and anything else is `destructive`. A tool with no annotations is `destructive`, so a cap hides it. A tool's name is not an Effect: name the read-only tools exactly with `WithToolEffect`.
- **Authentication** is your `http.Client`: a fixed header, a refreshing token or mutual TLS. A round tripper can send the end user's token on each call though the connection is shared. The MCP authorization flow is not offered ([G-N20](#g-n20--mcp-features-gotato-does-not-build)).
- **Adapter tools are not `Sequential`,** so with `MaxParallelTools` above 1 a server's write tools run beside its read tools; mark them first or leave the limit at 1.
- **Results become the model's parts:** text, images, structured content as JSON, and one line for what cannot be shown. A server's error is a failed Tool Result the model reads.
- **Progress** reaches `tool_progress` Events. A large result is spilled ([G-F35](#g-f35--large-tool-results)).
- **A tool that asks for input mid-call** ends with a failed result, since the client supports neither elicitation nor sampling. Nothing waits.
- Tested against the SDK's in-process server, streamable HTTP on loopback and stdio. Not yet checked against a server of the specification's multi round-trip revision.

### G-F35 — Large Tool Results

**Status:** ✅

A tool can return far more than a Context should hold. A text result over one page is stored whole in the Record, and the model gets a preview and a reference and reads the rest in pages with `read_result`.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/tools.md](specs/tools.md), [specs/record.md](specs/record.md)
- **Why:** [040](plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md), [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md), [043](plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md), [046](plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md)

**Stories:**

- A tool returns a 400 KiB report, the Context holds the first part, and the model asks for page 5 when it needs it.
- After a compaction the model reads back one old tool result by its reference.

| Item | What happens |
|---|---|
| Page | 32 KiB of text, about 8K tokens, by default; one value that the spill threshold, the preview and both read tools all use |
| Spill | a text result longer than a page is stored whole before its Message; the Message holds the first 2K tokens and a line that names the reference and the page count; images and files are not spilled |
| `read_result(ref, page?)` | returns a page of a spilled result, any other Tool Result of a call the Context holds or a Segment closed, or a user or assistant Message by `msg:` reference; declares `read` |
| `read_segment(segment, page?)` | offered once a Session has a Segment; returns its Messages as they were, tool results included and reasoning left out |
| A removed original | reads as removed, with what the conversation still keeps of it |
| Fork | reads the bodies from its parent's log |
| Events | subscribers see `tool_result_spilled` with the size, never the body |
| Only over a Record | a Session spills only when its Transcript keeps Entries and a read-back tool is installed, so every reference has a reader; otherwise the result is kept whole |

- **In a service** the Runner installs the default read-back over its store, and an AgentProfile may replace it. `read_result` is offered whenever the Agent has a tool or a Segment exists, so the tool list does not change when a result first spills.
- **`read_result` reads only its own Session's results;** another reference fails with a list of the references the Context holds.

## Models

### G-F07 — Models and providers

**Status:** ✅

A provider is a few Go values, not code. The model bridge (package `model`) is four layers, each with a default a team can replace: the wire API, the connection, the retry and the meaning of an error. Any vendor that speaks a supported wire API works without new code, and your own client works by implementing `gotato.Model`.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [001](plan/decisions/001-one-implementation-per-wire-api.md), [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md), [042](plan/decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md), [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)

**Stories:**

- A team points the bridge at its provider's endpoint with a key and a wire API, and runs an agent with no new code.
- A team whose provider needs a signed request supplies its own HTTP client.
- A developer brings a model of their own by implementing `gotato.Model`.

| Layer | Default | A team replaces it with |
|---|---|---|
| Wire API | `ChatCompletions`, `Responses`, `AnthropicMessages` | a type that encodes a request and decodes the reply's server-sent events |
| Connection | the complete endpoint URL, a key (an empty key sends none, so a keyless local server works), headers, a cache key header and an HTTP client (connect timeout 10 s, response-header timeout 60 s) | an HTTP client whose round tripper signs, refreshes a token or goes through a proxy |
| Retry | 2 retries, 200 ms backoff, 60 s longest wait; a provider's `Retry-After` wins over the backoff | `model.WithRetry`; an empty retry is off |
| Error meaning | the default classifier says retryable, overflow and the limit named | `model.WithClassifier`, usually a function that calls the default and changes its answer |

**Nothing is read from an address or a name.** Gotato keeps no table of providers or models. What a model is, the team states in its `ModelConfig`; a fact left out has the cautious answer.

| Fact | Means | If omitted |
|---|---|---|
| Window | the context window in tokens, or `unknown` written out; compaction takes its ceiling from it | `model.New` refuses it |
| Cache retention | how long the provider keeps the prompt cache ([G-F33](#g-f33--prompt-cache-retention)) | unknown: the provider's default |
| Round reasoning | the provider takes back only the current Round's reasoning | every reasoning artifact is sent, which a provider accepts |
| Thinking binding | the provider checks the prefix a thinking block was written under; Claude is asked to drop a block whose prefix changed rather than refuse it | not sent |

- **A call is retried** when no response came, or on 429 (except a hard quota), 500, 502, 503, 504, 529, or an error that says rate-limited, overloaded or service unavailable. A refusal as too long is never retried.
- **An error the provider sends mid-stream** is marked retryable on all three wire APIs but is not sent again by the bridge once the stream has started; the caller decides. A broken stream is asked for again ([G-F31](#g-f31--recovering-a-cut-stream)).
- **A request refused as too long** (a 400 or 413 that says so, or a stop for the context window mid-reply) is a `context_overflow` and ends a Run, after one compaction ([G-F06](#g-f06--compaction)). A team adds its own wording by wrapping the default classifier.
- **The request carries only what the Agent and the Sampling set.** Temperature and the output limit go on chat and Anthropic; reasoning effort and summary go on Responses, and a request that sets them on the other two is refused as `not_supported` before anything is sent. The model's own default Sampling sits under the Session's, field by field.
- **The cache key** goes in the body field or header you name; Responses defaults to `prompt_cache_key`, Anthropic caches by `cache_control`, and a provider that keys by a session header needs `CacheKeyHeader`, else every Turn is cold.
- **Caching on the chat wire:** Gotato sends no `cache_control` there, so a gain needs something between you and the model to add it, or the Anthropic wire.
- Not yet run against a real provider: Gemini's thought signature on the chat wire, and DeepSeek's rule on reasoning.

### G-F31 — Recovering a cut stream

**Status:** ✅

A reply cut off partway does not fail the Run. A stream cut after some output is asked for again, the way one cut before any output is.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/model.md](specs/model.md)

**Stories:**

- A mobile client's provider drops a stream halfway through a long answer, and the user gets the full answer anyway.

- The same request is sent again within the retry count, and a `model_retried` Event says so with the attempt, the wait and the reason.
- The reply cut off stays in the Record only as a log, a Cut-off reply, and no model is ever sent it.
- **A stream reader who sees `model_retried` discards what it received of that reply.**
- When the attempts run out the Run fails with `model_failure` and the Record keeps the last Cut-off reply.
- A refusal of the request, an Interrupt, a canceled context and a deadline are not cuts.
- A bare model client with no Session gets a retryable "stream interrupted" error instead; a model of your own that retries inside its stream reports it first and starts the reply over.

### G-F33 — Prompt cache retention

**Status:** ✅

The team states how long its provider keeps the prompt cache as a fact of the model, and Gotato guesses none from an address or a name. It is set on the model, not per Agent.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [042](plan/decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md)

**Stories:**

- A team on a provider with a one-hour cache states it once, and an idle Session is compacted before the cache expires.

- The Anthropic wire asks for the one-hour cache when retention is 1 hour or more, which doubles the price of a cache write.
- The Responses wire asks for 30 minutes when it is exactly 30 minutes.
- Any other retention keeps the provider's default.
- Compaction reads it to time an idle compaction ([G-F06](#g-f06--compaction)).
- Gotato does not refresh a warm Session's cache; a keepalive is in *Later*.

## Storage and instances

### G-F03 — Stores

**Status:** ✅

Every Session is saved to a store as its Record, an append-only log of Entries. The store is the truth, and a Runner's memory is a cache.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth), [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/record.md](specs/record.md)
- **Why:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md), [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md), [017](plan/decisions/017-the-record-is-an-append-only-log.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md)

**Stories:**

- A developer tests with the memory store and ships on the file store.
- A team backs the store with the database it already runs, and proves its adapter with the conformance suite.
- A pod dies mid-Run, another loads the Session from the store and continues.

| Store | For |
|---|---|
| memory | tests and short-lived programs |
| file (`store.NewFileStore(dir)`) | one process on local disk; a crash never tears a write |
| your own adapter | the database you run, through the store contract ([G-N08](#not-planned)) |

- **A Record ID** is at most 100 bytes of valid UTF-8 with no control character, slash, backslash, `..` or leading dot; another ID is refused with `invalid_argument` by every store. A store may refuse more: the file store spells uppercase and non-ASCII characters in the directory name and refuses a name past 200 bytes.
- **The Record is append-only.** Every step is appended as it happens at a position the store never gives twice. A compaction appends a Checkpoint and a load reads from the last one. An Entry of an unknown kind or a document from a newer schema is refused.
- **The Record grows with every step.** A team that wants to bound storage deletes entries before a Checkpoint itself.
- **A Record deleted and created again** has a new identity, so an old writer is refused and an old stream ends.
- **The contract is small:** two seams, Records and Liveness, each call one ordinary conditional write or read; an optional lister gives a page of Session summaries, most recently updated first (without it `Runner.Sessions` is `not_supported`); and an optional, separate Definitions store keeps AgentProfile and TaskSpec documents ([G-F36](#g-f36--agent-profiles-and-task-specs)). Records gains no methods after 0.3.0.
- **A conformance suite** in `store/storetest` runs every check against an adapter, several handles standing for several instances: create, load, append, a race of writers with one winner, a stale writer refused, Liveness expiry, listing and paging. Its own tests check that it fails the adapters that break a rule.
- After a crash the file store removes leftover temporary files a day old or older.

### G-F27 — Service: many instances

**Status:** ✅

A Runner hosts Sessions inside your service on as many instances as you run. Any instance answers a request as if it were the only one, and no Session is bound to an instance.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special), [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md), [020](plan/decisions/020-no-session-is-bound-to-an-instance.md), [024](plan/decisions/024-gotato-builds-no-infrastructure.md), [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md), [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md), [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md)

**Stories:**

- A rolling deploy replaces every pod, and each conversation continues on a new pod.
- A load balancer sends a request to the wrong pod, and the request still runs once, on one pod.
- An operator drains a pod, and every Run on it finishes and is saved before it exits.
- A service gives each tenant its own limit of concurrent Runs.

| Item | What happens |
|---|---|
| Save every step | the prompt, a model call before it is made, a streaming reply every second or 4 KiB, the reply, tools before they run, each tool result; a crash loses at most the step in flight |
| One writer | one instance writes a Session at a time; a second instance that tries stops before it calls the model |
| Busy policy | a second Run on a busy Session is rejected with 409 (`reject`, the default) or waits (`wait`) |
| Capacity | past `Limits{MaxActiveRuns}` a new Run is rejected with 429, marked not run; off by default |
| Admitter | `host.Config.Admitter` decides whether a Run may start, with the caller's context carrying who asks, so a limit for each tenant stays right; a refusal is marked not run |
| Activation limit | `Bounds.MaxActiveSessions` counts Sessions in use; a Run that would activate one more is refused before anything is loaded: `overloaded`, HTTP 503, marked not run |
| Warm Session | after a Run the Record stays in memory with no claim, for 60 idle minutes by default; before each Run the instance checks the stored head and reloads if it changed |
| Memory limit | warm Sessions count toward the activation limit; the longest-idle is evicted first, and a Run is refused only when every live Session is running |
| Instance name and recovery | `Config.Instance` is a label made unique per start, so a name shared by two live processes is safe; after an outage longer than the TTL the Runner takes a new name and goes on, with no restart; `Runner.Close` removes the row so Sessions move at once |
| Liveness | each instance keeps one row, timed by the store's clock; when it expires the instance's claims end with no write; an instance that cannot show its row renewed writes nothing and refuses new work as not run, and `/readyz` answers 503 with `lapsed` |

**A request that reaches an instance that is not the writer:**

| The Session is | The instance |
|---|---|
| idle, or its writer is gone | claims it, loads from the last Checkpoint, serves the request |
| in use by its live writer | waits for the release up to a bound, then claims it; past the bound 409 with `Retry-After` |
| in a Run on its live writer, and the request is a message for that Run | answers `not_writer` with the writer's address ([G-F26](#g-f26--steering-a-run)) |

**Where many instances differ from one:**

| Place | What differs |
|---|---|
| The writer dies mid-Run | the step in flight is lost, a running tool is answered with an unknown outcome, the stream ends, and the Session waits for the dead row to expire |
| Two requests reach two instances at once | whichever opens it first runs; the other waits, then 409 |
| A stream on an instance that is not the writer | stored Events arrive at its next look, no deltas |
| Limits | `MaxActiveRuns`, `MaxActiveSessions` and the load an Admitter sees count per instance |
| A rolling deploy | an instance on an older contract version refuses what it does not know |

**Drain** (SIGTERM, or `Runner.Drain` from your endpoint) marks the instance draining, answers `/readyz` 503, refuses new Runs and takeovers, and lets every Run finish, save and release. At the drain timeout the Runs still going are stopped, saved and released, and each stopped Run's stream ends with its result and a reconnect code. Create and fork still work, and a compaction is refused as `draining`.

**Your load balancer routes;** Gotato builds no network between instances. It needs:

- a consistent hash on the `session-id` header, so every request for a Session reaches its writer;
- one retry on another instance after a 503 marked "not run";
- readiness from `/readyz`.

`POST /v1/sessions` needs no header and answers with the new ID in `session-id`. `Gotato-Not-Run: true` marks a request that never ran, and `Gotato-Retryable: true` one that may succeed later; send to another instance only when both are present, except `not_writer`, which goes to its `Gotato-Writer` address. A connection that broke after sending may have run and is not retried.

**Settings,** on `host.Config` and the HTTP handler:

| Setting | Default | Why |
|---|---|---|
| `Limits{MaxActiveRuns}` | 0, no bound | the team sizes its instances |
| drain timeout | 10 s, negative none | most Runs finish in a rolling deploy |
| write bound | 10 s | a Run's last write outlives a canceled caller; a slower store fails the write as unknown |
| Liveness TTL | 10 s | rides out a slow store; a dead instance's Sessions wait this long |
| Liveness wait | 5 s | how long a request for a Session running elsewhere waits before 409 |
| warm Sessions | 10,000, negative none | past it the longest-idle is evicted |
| warm TTL | 60 minutes, negative none | how long an idle Session stays in memory |
| forwarding (HTTP only) | off | forwards messages, notices and aborts, and only to addresses your `allow` function accepts; `serve --forward` in the test CLI is for tests only |
| request body cap | 1 MiB | |
| stream keep-alive | 15 s | |
| stream look | 1 s, negative off | how often an idle Session stream reads the store for another instance's Events |

## Endpoints, watching and extending

### G-F14 — HTTP and streaming

**Status:** ✅

A bot, a job or another service talks to Sessions like any other API. You mount the handler from `adapter/http`, a Go module of its own, on your own mux. The handler only translates the Runner's calls and errors.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/events.md](specs/events.md), [specs/errors.md](specs/errors.md)
- **Why:** [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030](plan/decisions/030-every-entry-is-an-event.md)

**Stories:**

- A chat bot runs a message and streams the reply to its user as it is written.
- A browser reconnects after a network drop and gets every Event it missed.
- A dashboard lists Sessions, most recently updated first.

| Routes | What they do |
|---|---|
| `GET /healthz`, `GET /readyz` | liveness with the contract version; readiness with `active_runs` and `active_sessions`, or 503 when draining or lapsed |
| `GET /v1/agents`, `GET /v1/tasks` | the registered AgentProfiles with their Effect caps, and the registered TaskSpecs with the AgentProfile each runs |
| `POST /v1/sessions`, `GET /v1/sessions`, `GET` and `DELETE /v1/sessions/{id}` | create (with `agent` or `task`), list a page, read, delete |
| `POST …/fork` | fork a Session |
| `POST …/runs`, `POST …/runs/stream` | a Run answered when it settles, or streamed as server-sent events |
| `POST …/messages`, `POST …/notices`, `POST …/abort` | steer, notice and stop ([G-F26](#g-f26--steering-a-run), [G-F29](#g-f29--runtime-notices)) |
| `POST …/compact`, `GET …/context` | compact on demand, inspect the next request |
| `GET …/events`, `GET …/stream` | every stored Event as JSON lines, or the Session's stream |

- A run request carries `prompt` or `continue: true`, may set `timeout_ms`, and may set an `agent` that binds the Session to that registered Agent, which repairs a Session whose stored Agent was dropped.
- `GET /v1/sessions` answers one page with `limit` and `cursor`, 100 Sessions when `limit` is not set, and each item holds the listing columns only. A store with no lister answers 400 `not_supported`. A Session updated while a client pages may repeat or be skipped.
- A Run that settles failed, canceled or past its deadline still answers 200 with the outcome in the body; a request refused as too long is such a Run. Only request-level problems use these statuses:

| Status | When |
|---|---|
| 201 | a Session was created or forked |
| 400 | the request is invalid |
| 404 | the Session does not exist (marked not run) |
| 408 | the caller canceled the request |
| 409 | busy; the Run is not active; the Session ID is taken (`already_exists`); the Session is closing or closed; another writer took the Session; a stream cannot resume (`cannot_resume`); or a message or abort for a Run another instance writes (`not_writer`) |
| 413 | the body is too large |
| 422 | a call outside a Run was refused as too long (`context_overflow`) |
| 429 | the Runner is at capacity, or a message queue is full (`limit_exceeded`; marked not run) |
| 500 | a settled Run could not be saved |
| 502 | a forward to the writer failed, so the message may have reached it (not marked) |
| 503 | draining or past the activation limit, both marked not run; or the store failed |
| 504 | the request's deadline passed |

- **Every error body** carries `code`, `message`, `not_run` and `retryable`; the headers `Gotato-Not-Run: true` and `Gotato-Retryable: true` say the same. They are facts, not a retry policy.
- **Streaming:** `/runs/stream` and `/stream` send each Event as `event: <kind>` with one flat JSON object and end a Run with `event: result`. The handler reads a Subscription, so a slow client never slows the Run.
- **A run stream answers 200 once the Run is starting.** A request refused before that gets its status with the run route's headers; only a failure after it started arrives as `event: error` or in the result.
- **Stream resume:** `GET /v1/sessions/{id}/stream` sends the stored Events after a number, from the Record, then the live ones across Runs, on any instance. Each stored Event's number is its SSE `id`; `Last-Event-ID` or `after` says where to start.
- **Record epoch:** the first frame and the `Record-Epoch` header carry the Record's identity. A client that resumes with another epoch, or an `after` past the last Event, gets 409 `cannot_resume` before the stream starts, and starts over at 0.
- **Ends:** a drain ends a stream with `event: reconnect` and a wait spread from 0.5 to 1.5 seconds; any other end is `event: error` with no reconnect. An idle stream sends a comment every 15 seconds.
- **Deltas are visible only on the writer.** A `text_delta`, `reasoning_delta` or `tool_progress` is sent and never stored, so a stream on another instance sees stored Events only, and a streaming card loses its deltas when the writer changes. A subscriber that skipped deltas takes the final text from the Run's result.
- **Go API:** call the Runner directly and subscribe: `Runner.StreamRun`, `Runner.StreamSession`, `Runner.SubscribeInstance` ([G-F13](#g-f13--events)). Authentication, logging and access control are yours: wrap the handler ([G-N22](#g-n22--access-control-belongs-to-applications)).

### G-F13 — Events

**Status:** ✅

Every step of a Session is an Event, so you can watch, log or measure it. A stored Event is an Entry of the Record, and a Session never waits for a reader.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen), [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030](plan/decisions/030-every-entry-is-an-event.md)

**Stories:**

- A developer follows one Session to deliver its answer to a chat.
- An operator watches every Session on an instance for telemetry.
- A team measures what a Run cost and how long each step took, from its Events alone.

- **The Record is the Event log.** A stored Event's number is its position in the Record, so every instance reads the same number. It reaches subscribers only after it is written.
- **One Go type for each kind,** and in JSON one flat object whose `kind` names its type; a kind a later version adds reads back as an unknown Event.
- **Three places:** the Record (a subscriber can read it again), sent only (deltas) and the Instance (an Event from an instance that holds no claim).
- **Durations** are numbers of milliseconds in a key ending `_ms`, and a tool is named by `tool_id`.

| Events | What they report | Kept |
|---|---|---|
| Run and Turn | a Run starts (with the caller's `request_id`) and settles with status, cause and stop reason; a Turn starts and ends with its usage and its built Context | Record |
| Reply | the reply starts and ends with the Message, the model and why it stopped; the text so far is kept every second or few KiB; a stream broke and its text is kept as a Cut-off reply; the bridge calls again | Record |
| Deltas | a piece of the reply's text or reasoning, or a tool's own progress | sent only |
| Tool | a tool starts and ends with status, duration and failure code; a result enters the Transcript or is spilled | Record |
| Tool loading | a tool is loaded or unloaded, with why | Record |
| Message and notice | a Steer, FollowUp, Interrupt or notice waits, is committed, is dropped or is refused, with why | Record |
| Abort | an Abort arrived | Record |
| Claim | an instance claims a Session with why, or releases it | Record |
| Session | a Session is created, or its metadata changes | Record |
| Compaction | a compaction starts, ends or fails, with why; a Segment is closed | Record |
| Extension | an advisory extension failed, or a ToolSet did not resolve, and the Run went on | Record |
| Instance | a request arrives, waits or is refused with why; a write was refused because another instance holds the claim; the instance's row lapsed; a Session was loaded, saved or unloaded or deleted; a store write failed; a stream skipped Events | Instance |

- **A rebuild reads only the facts.** A load starts at the last Checkpoint and reads the facts after it; every other stored Event is a log entry for people and telemetry.
- **A subscriber that falls behind** catches up from the Record by number and rejoins live, so it never misses a stored Event; only deltas can be skipped.
- **An Instance's stream** skips Events when it falls behind and says how many.

| Subscription | For | Carries |
|---|---|---|
| A Run's stream (`Runner.StreamRun`, `/runs/stream`) | the caller of a Run | the Session's stream from the Run's first Event to its last, with the same numbers |
| A Session's stream (`Runner.StreamSession`, `GET …/stream`) | an application that shows a conversation; a client that reconnects | every Event across Runs after any number: first from the Record, then live |
| An Instance's stream (`Runner.SubscribeInstance`) | the service's telemetry | every Event of every Session on the instance plus Instance Events; no single number, so no replay |

- **A Run not saved** (`SkipSave`) has only its Run's stream, numbered from 1.
- **A caller of `Run` needs none of this:** the response carries the final text.

### G-F15 — Extensions

**Status:** ✅

Extensions add behavior at fixed points of the Loop, without forking it, with `gotato.WithExtension`.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced), [5 Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team refuses a risky tool call by checking its arguments before it runs.
- A team stops a Run after ten Turns, and records why.

| Stage | Runs |
|---|---|
| `TurnPreparer` | before every model call, with no tool call open; may rewrite the Transcript. Automatic compaction is one |
| `ContextTransformer` | after the Context is built, before it is sent |
| `MessageConverter` | when Messages are converted for the model |
| `PreToolUse` | before a tool runs; may refuse the call |
| `PostToolUse` | after a tool runs |
| `TurnStopper` | at the end of a Turn; may stop the Run |

- Each stage runs on the Run's goroutine, so a slow extension slows the Run.
- An extension marked advisory may fail without failing the Run, and the failure is stored as `extension_failed` with the stage.
- An error from a stage ends the Run; a Gotato error keeps its code, any other is `extension_failure`.
- There is no observer stage: a service reads Events from a Subscription.

## Library and testing

### G-F37 — A light library with a versioned contract

**Status:** ✅

Gotato is a library you add with `go get`, and it builds, tests and deploys with your service. The core imports only the standard library, and every module you do not need stays out of the build.

- **Design:** [5 Keep subtracting](DESIGNS.md#5-keep-subtracting), [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md), [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md), [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md)

**Stories:**

- A developer runs an agent in a ten-line `main` with a fake model, and nothing else is needed.
- A service adds the HTTP adapter and the MCP adapter as separate modules, and imports neither into its core.
- A team upgrades and reads before-and-after notes for every break.

- **Each package is one layer** and imports only the layers below it: `host`, `profile`, `store`, `record`, `gotato`, `ref`. The model bridge, `testkit` and the adapters sit beside them and import down. A test fails when the order breaks.
- **The adapters are modules of their own** (`adapter/http`, `adapter/mcp`), and no core or runtime package imports them.
- **The root module depends only on one YAML library,** which only the test tools use.
- **A program that imports the agent, Runner, file store, HTTP handler and model bridge and does nothing else** links 3 modules and builds to a 9.0 MB stripped binary (measured 2026-11-03 with `bench/refservice/footprint.sh`, Go 1.27.2, Apple M3).
- **The contract is versioned.** The Go API, the HTTP contract and the shape of a saved Session change only through versioned breaks, with before-and-after notes in the changelog. `GET /healthz` reports the HTTP contract version, `1` today.
- **The core needs Go 1.24;** the MCP adapter needs Go 1.25.

### G-F22 — Test kit and measurement

**Status:** ✅

You test agents without a network or a paid model, and measure Gotato on a fixed workload.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md), [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md)

**Stories:**

- A developer tests a tool-using agent with a scripted model, and the test never touches a network.
- A team tests its own store adapter with the conformance suite.
- A maintainer measures a release against the reference workload and compares it with the last one.

- **`testkit`** has a scripted model, a replay model, a model that answers from the request, a fake tool, an Event recorder, a ready-made Session, and the `echo` and `demo` models.
- **`internal/cmd/gotato`** is a CLI that drives a Runner by hand and in end-to-end tests that assert on JSON. It is our test tool, a thin client with no runtime logic and no contract ([G-N28](#not-planned)).
- **The reference workload** (`internal/reftasks`, our test tool) is a fixed set of tasks, each with a fake model and a check, from a single lookup to a Session of 1,000 messages; the long Sessions that compact and merge over 100 Runs are played only when named.
- **`internal/workload`** plays the tasks through a Runner and reports every failed Run, tool call and check with its cause, token usage, cost at your price, cache hit rate, Gotato's time per Turn next to the model's and the store's, and whether each Turn's Context matches an inspection. All numbers come from the Run's Events.
- **A live run** calls a paid model, so it runs on demand and never in CI; it prices tokens at the rates you give. It also runs the Recap check ([G-F06](#g-f06--compaction)).
- **Fault injection** plays each reference task with an instance dying after each write, then checks that another instance opens exactly what that write left, every tool call is answered, and the next Run completes. It also cuts a stream after every Event and resumes it, runs three instances behind a hashing proxy with one killed, and checks that messages to one Session land once each.
- **The reference service** (`bench/refservice`) is the smallest service around Gotato, and times its start to the first `/healthz`.
- **A model's wire API and its quirks over HTTP** are played by `internal/fakemodel`, so Gotato runs no checks against real providers itself.

## When something fails

Every error a Gotato layer returns answers three questions, set where it is made and never guessed by a caller or a transport: its code says what happened, `NotRun` says nothing with side effects happened so another instance may run the same request, and `Retryable` says the same request sent later may succeed. Over HTTP the code picks the status ([G-F14](#g-f14--http-and-streaming)), and the error body and the two headers carry the answers.

| What the user sees | Not run | Retryable | Feature |
|---|---|---|---|
| `draining`: the instance takes no new Run | yes | yes, on another instance | [G-F27](#g-f27--service-many-instances) |
| `overloaded`: past the activation limit | yes | yes, on another instance | [G-F27](#g-f27--service-many-instances) |
| `limit_exceeded` from `MaxActiveRuns` or your Admitter | yes | yes | [G-F27](#g-f27--service-many-instances) |
| `busy`: the Session has a Run, here or elsewhere | yes | yes, after the Run in flight | [G-F27](#g-f27--service-many-instances) |
| `not_writer`: a message or Stop for a Run another instance writes | yes | yes, at the writer named in the error | [G-F26](#g-f26--steering-a-run) |
| `not_found`: the Session does not exist | yes | no | [G-F02](#g-f02--sessions) |
| `session_closing`, `session_closed`: the live Session takes no more calls | yes | no | [G-F01](#g-f01--agents-and-runs) |
| `already_exists`: the Session ID you chose is taken | not marked | no | [G-F02](#g-f02--sessions) |
| `invalid_argument`: a bad request, a `gotato.` key, an unknown Agent or TaskSpec, an option given twice | yes, before a Run | no | [G-F02](#g-f02--sessions), [G-F36](#g-f36--agent-profiles-and-task-specs) |
| `store_failure`: the store could not write or read | unknown for a Run's write; yes for a load, create, delete or read | yes | [G-F03](#g-f03--stores) |
| `model_failure` the provider calls transient | no, it ran | yes | [G-F07](#g-f07--models-and-providers) |
| `context_overflow`: the provider still refuses after a compaction | no | no | [G-F06](#g-f06--compaction) |
| `limit_exceeded` from the compaction ceiling or a message over its size | no, it ran | no | [G-F06](#g-f06--compaction), [G-F01](#g-f01--agents-and-runs) |
| a tool fails (`tool_execution_failure`, `tool_argument_failure`, `deadline_exceeded`) | the Run goes on | the model may try again | [G-F09](#g-f09--tools-and-their-effect) |
| a tool the model had not loaded (`tool_not_loaded`) | the call did not run | the model calls it again | [G-F34](#g-f34--tool-search) |
| a ToolSet is down | the Run goes on without it | next Run | [G-F10](#g-f10--tool-sets) |
| a stream is cut after output | the reply is asked for again | within the retries | [G-F31](#g-f31--recovering-a-cut-stream) |
| the writer dies | the step in flight is lost | another instance continues | [G-F27](#g-f27--service-many-instances) |
| a client cannot resume a stream (`cannot_resume`) | not run | start over at 0 | [G-F14](#g-f14--http-and-streaming) |

- **Codes** are `Code…` constants whose name matches the string, and a sentinel is an `Err…` variable that carries a code. `errors.Is` matches a sentinel and `gotato.IsCode` matches any error of a code.
- **A Run that settled `completed` never carries an error;** every other status always does.
- Every code has its status, exit code and retry advice in RUNBOOK.md.

## Later

Ideas worth building once they are needed. None has a milestone yet.

| Idea | Note |
|---|---|
| Telemetry adapters | a package mapping an Instance's Events onto OpenTelemetry; teams can write their own ([G-N11](#not-planned)) |
| Model catalog | model names and prices next to the providers; a model's window comes from its `ModelFacts.Window`, never looked up |
| Provider rate-limit information | limits, remaining calls and reset time reported with usage |
| Idempotency keys | a repeated request ID is not run twice (a `RequestID` is recorded and echoed already, and nothing looks for a repeat) |
| Examples (G-F24) | one-shot use, a persistent Session, compaction, forks, dynamic tools |
| Token budget per Run | a cap on tokens one Run spends; a `TurnStopper` does it today |
| Filtering the Session list | paging by limit and cursor exists; a filter by owner or metadata waits for a service that needs it |
| Loading only the recent Transcript | a load already starts from the last Checkpoint; loading less waits until needed |
| Prompt cache keepalive (G-F33) | refresh a warm Session's cache before it expires; each refresh pays a cache read of the whole prefix, so only if measurements show users returning inside the retention often enough |
| Changes that keep the cache (G-F32) | a changed instruction, or a tool switched on for one Session mid-Session, appended after the Transcript (a notice, the tool's definition) instead of rewritten into the prefix; a compaction folds it back. Build it when a service really does this and the cache cost shows; tools the model loads itself are already covered ([G-F34](#g-f34--tool-search)) |
| Stopping a group of Runs across Instances | abort every Run whose Session has some Metadata, on all Instances. Today: list and abort each; a Run elsewhere answers `not_writer`. It must not become a group, role or supervisor in the runtime ([G-N01](#not-planned)) |
| Examples for common load balancers | the hashing, retry and readiness rules of [G-F27](#g-f27--service-many-instances) written out for the balancers teams run |

## Not planned

What Gotato will not build, and why. Look here before proposing a feature; each belongs to the application, an MCP server, or the tools themselves.

| ID | Not built | Why |
|---|---|---|
| G-N01 | agent organization: roles, teams, hierarchies | Gotato provides peer agents; applications compose roles |
| G-N02 | task graphs, scheduling, orchestration, pipelines, project planning | they live in the application |
| G-N03 | global resource scheduling: concurrency, CPU and memory, quotas, provider quotas | Gotato provides cancelable, lightweight Runs; the Runner only bounds concurrent Runs and keeps one per Session |
| G-N04 | product UI | Gotato has no human-facing surface; the CLI is a test tool |
| G-N05 | a required service | library use stands on its own; the service is the Runner behind `adapter/http`, and core packages never depend on it |
| G-N06 | memory architecture | applications choose long-term memory |
| G-N07 | a workflow language | ordinary Go is the default; a workflow package, if one exists, leaves the Loop unchanged |
| G-N08 | storage engines | Gotato defines the store contract and ships memory and file stores; an application backs it with its own database through an adapter that owns schema and migrations |
| G-N09 | tool catalogs | filesystem, Git, HTTP tools and skill-format adapters (SKILL.md) come from MCP servers or the application |
| G-N10 | context organization beyond full history | retrieval, windowing and summarization pipelines are your own `ContextBuilder` |
| G-N11 | telemetry backends | Events have a stable shape; mapping them is a subscriber in the application |
| G-N12 | tool approval | tools are written by the team and allowed by name, the Effect cap hides what is above it, a risky tool refuses its own arguments, a `PreToolUse` extension adds a check |
| G-N13 | a cloud coding agent | coding stays on developers' machines ([PHILOSOPHY.md, question 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)) |
| G-N14 | a remote dev environment, shell or sandbox | work that needs a machine runs on a laptop, in CI or in a coding product; reading a large result is paging ([G-F35](#g-f35--large-tool-results); [decision 041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md)) |
| G-N15 | a general-purpose assistant | Gotato is for purpose-built agents ([PHILOSOPHY.md, question 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)) |
| G-N16 | a separate platform | a platform beside your services is a second system to run |
| G-N17 | infrastructure | discovery, routing, autoscaling and deployment already exist where you run |
| G-N18 | ready-made clients | bots, schedulers and front ends are the callers' code |
| G-N19 | structured output | a tool of the Agent's own (below) |
| G-N20 | MCP features beyond tools | below |
| G-N21 | a last Turn that answers | the deadline is the caller's context, so the caller sends a notice before it, for example ten seconds ahead with a timer; Gotato does not guess the lead or force the model to stop calling tools |
| G-N22 | access control | below |
| G-N23 | withdrawing a waiting message | the caller holds the message ID and can send the next message or a Stop; a waiting message already ends as committed, dropped or refused |
| G-N24 | notices for a settings change and an interrupted Run | an interrupted Run ends in the Record as `run_ended` `failed` with cause `interrupted`, and its started calls are answered with an unknown-outcome result the model reads; the instruction is built from the current Agent on every request (keeping the cache across a change is in *Later*); the names are free for a service, since `Session.Notice` takes any kind but the two reserved |
| G-N25 | a native form for a notice | below |
| G-N26 | a load signal in the Liveness row | the row stays an address and a draining mark; `/readyz` reports `active_sessions` and `active_runs`; routing is the load balancer's, and an instance at its limit refuses as not run |
| G-N27 | a gRPC adapter | HTTP is the one wire transport and every operation would repeat HTTP's; a service that wants gRPC maps `host.Runner` itself, as `adapter/http` shows |
| G-N28 | a shipped CLI | it would be a third contract needing versions; `internal/cmd/gotato` stays as a test tool that promises nothing; a service writes its own `main` |

The feature test says no to G-N23, G-N26, G-N27 and G-N28: a Go service embedding an agent has not asked for them, and a request should name the case.

### G-N19 — Structured output is a tool of the Agent's own

Give the Agent a tool whose arguments are the shape, such as `submit_ticket`, and say in its instruction to call it with the result. Gotato checks the arguments against the schema before the tool runs ([G-F09](#g-f09--tools-and-their-effect)), and arguments that do not fit go back as a failed Tool Result so the model corrects them. The tool receives the value decoded and keeps it where the service keeps the Run's result. The model usually answers once more after the call, so an instruction such as "after submitting, reply only 'Done'" keeps it short.

### G-N20 — MCP features Gotato does not build

The MCP adapter ([G-F12](#g-f12--the-mcp-adapter)) maps a server's tools onto Tools and stops there.

| Not built | Why |
|---|---|
| Code mode | each step of a Run would be a program that cannot be replayed, and a tool that runs code is a bound nobody can read ([decision 026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md)) |
| MCP resources | a Session reads data through tools the team chooses; a resource has no Effect and no schema check |
| Prompts | the instruction is the Agent's definition, where a reviewer reads it |
| Sampling | a Run's cost and bounds would depend on a server the team may not control |
| Elicitation | a person's answer needs a waiting state in the Loop, which Gotato does not have ([decision 005](plan/decisions/005-no-approval-system.md)) |
| Gotato as an MCP server | callers reach Sessions through the HTTP handler and the Go API |
| OAuth | every service already authenticates its callers and holds its credentials ([decision 025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)); a server's headers come from the application |

### G-N22 — Access control belongs to applications

Who the caller is, and who may touch which Session, are the application's. Gotato builds no identity type, "not allowed" error or hook. It passes the request's `context.Context`, with all its values, to every store and extension call for that request ([decision 025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)).

- **A refusal is `not_found`:** a store wrapper returns not found for a Session the caller does not own (404, and it does not reveal the Session exists).
- **Every request-driven call reads through the store,** so a wrapper that guards load, log, append, delete and list also guards a Run, a cancel, a steer, a follow-up, an interrupt and a notice.
- **Work Gotato starts on its own has no caller:** an idle compaction and a Liveness renewal carry none, and a following Run keeps the values of the request that started the Run before it, without its cancellation. What an empty caller means is the team's rule.

### G-N25 — A notice has no native form

A notice could go in a provider's own form: a `system` message mid-conversation on Anthropic Messages, a `developer` message on Responses. Gotato does not build it. A notice is tagged text in a user turn on every wire API, which needs no model list that changes with each release, follows the Tool Results or the next prompt so the cached prefix and the reasoning in flight stay, and rests on no documentation that a provider may change. A team that wants a native form writes its own Model, which receives the notice as a Message of role `notice`.
