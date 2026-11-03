# Gotato — Features

What Gotato does today, and what it will and won't do, told from the side of the developer who embeds it in a Go service: what they do, what happens, and what they see when it fails. How a part works inside is in [specs/](specs/ARCHITECTURE.md), and why it is built that way is in [plan/decisions/](plan/decisions/README.md).

Status marks: ✅ built · 🚧 built, with the gap named beside it · ⬜ planned. A ✅ says the feature is built, not that it has been proved in real use.

## Summary

```text
Features
├── ✅ F1: Runs — run an Agent, get a settled result, steer it while it works
│   ├── ✅ F1.1: Run an Agent — the Loop calls the model and tools until the model answers
│   │   ├── ✅ F1.1.1: Prompt — start a Run with a new message
│   │   └── ✅ F1.1.2: Continue — go on with no new message
│   ├── ✅ F1.2: A settled result — one of four statuses, with the reply and the usage
│   ├── ✅ F1.3: How a Run ends — the model's answer, your stop, a limit, a deadline or a failure
│   ├── ✅ F1.4: A Run's deadline — 30 minutes by default, set per Agent or per request
│   ├── ✅ F1.5: Steer a Run in flight — from any instance
│   │   ├── ✅ F1.5.1: Steer — taken at the next Turn
│   │   ├── ✅ F1.5.2: Follow up — taken where the Run would settle
│   │   ├── ✅ F1.5.3: Interrupt — the model call stops at once
│   │   └── ✅ F1.5.4: Stop — everything stops, committed Turns stay
│   ├── ✅ F1.6: Messages outlive their Run — an accepted message starts the next Run
│   ├── ✅ F1.7: Limits — each guards a real failure
│   ├── ✅ F1.8: Your code panics — it costs that step only
│   └── ✅ F1.9: Usage — tokens and cache reads, priced by you
├── ✅ F2: Sessions — one conversation that lasts across messages and days
│   ├── ✅ F2.1: Create a Session — your own ID and a starting history
│   ├── ✅ F2.2: Fork a Session — a new conversation from the same history
│   ├── ✅ F2.3: One Run at a time — a second is refused or waits
│   ├── ✅ F2.4: Your own metadata — keys under gotato. are refused
│   ├── ✅ F2.5: Delete a Session
│   └── ✅ F2.6: Read a Session — its Record and its Events
├── ✅ F3: Agent profiles and tasks — define once, use again
│   ├── ✅ F3.1: Agent profiles — the whole definition of a kind of Agent
│   │   ├── ✅ F3.1.1: Register inline — in your code
│   │   ├── ✅ F3.1.2: Register from a store — by name, choosing only what your code offers
│   │   └── ✅ F3.1.3: Replace a profile — new and rebuilt Sessions get it
│   ├── ✅ F3.2: A default Agent — for a request that names none
│   ├── ✅ F3.3: Task specs — a job that runs again, with its background and narrower bounds
│   │   ├── ✅ F3.3.1: A task only narrows — a wider one is refused
│   │   ├── ✅ F3.3.2: Run a task — a new Session, the background added once
│   │   ├── ✅ F3.3.3: Open a task Session with your own ID — safe to retry
│   │   └── ✅ F3.3.4: A task Session keeps its spec — later edits do not reach it
│   └── ✅ F3.4: List Agents and tasks
├── ✅ F4: Context — what the model sees on each Turn
│   ├── ✅ F4.1: Built every Turn — from the Record, never edited in place
│   ├── ✅ F4.2: Laid out for the cache — what never changes first, what grows last
│   │   ├── ✅ F4.2.1: Cache markers
│   │   └── ✅ F4.2.2: A cache key — one conversation, one cache
│   ├── ✅ F4.3: Reasoning goes back for the current Round
│   ├── ✅ F4.4: Notices — tell the model what is not the user's words
│   │   ├── ✅ F4.4.1: Your own notice
│   │   ├── ✅ F4.4.2: The continue notice — go on after the output limit
│   │   └── ✅ F4.4.3: The time-away notice — the user was gone a while
│   ├── ✅ F4.5: Your own Context — retrieval or any other way to choose what the model sees
│   └── ✅ F4.6: Inspect the next request — without calling the model
├── ✅ F5: Compaction — long conversations fit the window, and nothing becomes unreadable
│   ├── ✅ F5.1: Close a Segment — what came since the last Checkpoint becomes one Segment
│   │   ├── ✅ F5.1.1: The Record is never rewritten
│   │   ├── ✅ F5.1.2: A Segment is written once — the cache before it holds
│   │   ├── ✅ F5.1.3: What a Segment keeps — built by code
│   │   ├── ✅ F5.1.4: The Recap — the one part a model writes, off by default
│   │   └── ✅ F5.1.5: The kept tail — the latest stays as it was
│   ├── ✅ F5.2: Merge Segments — past their budget, the oldest become one
│   │   ├── ✅ F5.2.1: The Segment budget
│   │   ├── ✅ F5.2.2: The merged Segment — first in the Context, named by its range
│   │   ├── ✅ F5.2.3: A Recap of the Recaps — by a model, or a digest by code
│   │   └── ✅ F5.2.4: The one rewrite of what was sent — the cache is lost on purpose
│   ├── ✅ F5.3: Read back — anything moved out is read again by its reference
│   ├── ✅ F5.4: The ceiling — a hard line, not a cost knob
│   ├── ✅ F5.5: When it runs
│   │   ├── ✅ F5.5.1: Past the ceiling — before any model call
│   │   ├── ✅ F5.5.2: After a refusal as too long — then the call goes again once
│   │   ├── ✅ F5.5.3: When idle — before the cache or the warm time ends
│   │   ├── ✅ F5.5.4: Past the Segment budget — a merge alone, at a Run's start
│   │   └── ✅ F5.5.5: On demand
│   └── ✅ F5.6: Replace a part — the frame keeps the guarantees
├── ✅ F6: Tools — what an Agent can do, and how much it may change
│   ├── ✅ F6.1: A Go function as a tool — arguments checked before it runs
│   ├── ✅ F6.2: Effect and the cap — a tool above the cap is never seen
│   ├── ✅ F6.3: Mistakes go back to the model — the Run goes on
│   ├── ✅ F6.4: Tools in parallel — one at a time by default
│   ├── ✅ F6.5: Tool sets — tools that arrive as a group
│   │   ├── ✅ F6.5.1: Three install modes — active, listed, folded
│   │   ├── ✅ F6.5.2: A set that fails — only its own tools are missing
│   │   └── ✅ F6.5.3: Switch a tool per Session
│   ├── ✅ F6.6: Tool search — the model loads tools one by one from a catalog
│   └── ✅ F6.7: Large results — kept whole, read in pages
├── ✅ F7: Model — any provider that speaks a supported wire API
│   ├── ✅ F7.1: Three wire APIs
│   ├── ✅ F7.2: State a model's facts — Gotato guesses none
│   ├── ✅ F7.3: Sampling — per Agent, narrowed by a task
│   ├── ✅ F7.4: Retries — which errors, how often, how long
│   ├── ✅ F7.5: A request refused as too long — handed to compaction
│   ├── ✅ F7.6: Recover a cut stream — asked for again, not failed
│   ├── ✅ F7.7: Prompt cache retention — the cache the provider is asked for
│   └── ✅ F7.8: Replace a layer — wire, connection, retry, meaning of an error
├── ✅ F8: Store — each Session saved as a Record, and the store is the truth
│   ├── ✅ F8.1: Memory store
│   ├── ✅ F8.2: File store — a crash never tears a write
│   ├── ✅ F8.3: Your own store — two small seams over your database
│   ├── ✅ F8.4: Conformance suite — prove an adapter keeps the rules
│   ├── ✅ F8.5: An append-only Record — a load reads from the last Checkpoint
│   ├── ✅ F8.6: Session IDs — which IDs are refused
│   ├── ✅ F8.7: List Sessions — a page at a time, newest first
│   └── ✅ F8.8: Keep definitions — profile and task documents
├── ✅ F9: Many instances — any instance answers as if it were the only one
│   ├── ✅ F9.1: Every step saved as it happens — a crash loses the step in flight
│   ├── ✅ F9.2: One writer per Session
│   ├── ✅ F9.3: Takeover — any instance continues a Session whose writer is gone
│   ├── ✅ F9.4: Instance liveness — a lost instance's Sessions are freed
│   ├── ✅ F9.5: A request on the wrong instance
│   │   ├── ✅ F9.5.1: A new Run — waits for the release, then takes over
│   │   └── ✅ F9.5.2: A message for a Run elsewhere — names the writer
│   ├── ✅ F9.6: Warm Sessions — idle Sessions stay in memory, checked before use
│   ├── ✅ F9.7: The memory limit — the longest idle goes first
│   ├── ✅ F9.8: Admission — your rule for whether a Run may start
│   └── ✅ F9.9: Drain — finish or hand over before the instance stops
├── ✅ F10: Events — every step is a typed Event
│   ├── ✅ F10.1: The Record is the Event log — written, then sent
│   ├── ✅ F10.2: Subscribe to a Session — from any position
│   ├── ✅ F10.3: A subscriber that falls behind — slows only itself
│   ├── ✅ F10.4: Watch an instance
│   └── ✅ F10.5: Events a later version adds — read as unknown, never refused
├── ✅ F11: Extensions — behavior at fixed points of the Loop, without forking it
│   ├── ✅ F11.1: Before a model call — may rewrite the Transcript
│   ├── ✅ F11.2: Shape what the model sees — the Context and its Messages
│   ├── ✅ F11.3: Before a tool — may refuse the call
│   ├── ✅ F11.4: After a tool
│   ├── ✅ F11.5: At a Turn's end — may stop the Run
│   └── ✅ F11.6: Advisory extensions — their failure never fails the Run
├── ✅ F12: HTTP API — an optional adapter over the same calls
│   ├── ✅ F12.1: Routes
│   ├── ✅ F12.2: Status codes and errors — one error body
│   ├── ✅ F12.3: Stream a Run — server-sent events
│   ├── ✅ F12.4: Watch a Session and resume — after a drop, on any instance
│   └── ✅ F12.5: Forward to the writer — when you turn it on
├── ✅ F13: MCP adapter — an MCP server's tools as a tool set
│   ├── ✅ F13.1: Connect a server — over HTTP or a subprocess
│   ├── ✅ F13.2: Effect from the server's annotations
│   ├── ✅ F13.3: Authentication per call
│   ├── ✅ F13.4: Filter and rename tools
│   └── ✅ F13.5: Results and progress — what the model can read
└── ✅ F14: Library — added to a Go service like any other library
    ├── ✅ F14.1: Runs with no server or database
    ├── ✅ F14.2: Layers that import only downward
    ├── ✅ F14.3: Modules you opt into
    ├── ✅ F14.4: A small footprint
    ├── ✅ F14.5: A versioned contract
    └── ✅ F14.6: Test kit — fake models, a reference workload, fault injection
```

## F1: Runs

**Status:** ✅

You define an Agent once and run it. A message goes in, the Loop calls the model and the tools it asks for until the model answers, and a settled result comes out. While it works you can change its course from any instance.

### F1.1: Run an Agent

**Status:** ✅

A Run is one pass of the Loop over a Session, from a message to the model's final answer. In a small program you run a Session directly; in a service you run it through the host or an HTTP request.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md), [022](plan/decisions/022-a-session-is-a-goroutine-over-its-record.md)

**Stories:**

- A developer builds an agent from a model, an instruction and a few Go tools, and runs it in a small program with no server and no store.
- A service runs the same Agent for each user message and reads one settled result.

#### F1.1.1: Prompt

**Status:** ✅

A Prompt sends one message and returns when the Run settles.

#### F1.1.2: Continue

**Status:** ✅

A Continue resumes the Loop with no new message, for a Run that stopped before the model answered. A request that sets both a prompt and continue is refused, since the prompt would be dropped without a word.

### F1.2: A settled result

**Status:** ✅

Every Run that starts ends with one of four statuses: `completed`, `failed`, `canceled` or `deadline_exceeded`. The result carries the final message, why the model stopped and the token usage.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/session.md](specs/session.md), [specs/errors.md](specs/errors.md)
- **Why:** [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md), [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md)

**Stories:**

- A service reads one status and knows whether to show the reply or an error.

- A `completed` Run never carries an error, and every other status always does.
- A Run refused before it started has an empty result and an error that says it did not run.
- A Run whose reply or end could not be saved settles `failed`, never `completed`, and the next Run starts without that reply.
- A Run whose only failed write was its release stays `completed`, and the instance writes the release again until it lands.
- In a service the result and an error come together: the error says whether the request succeeded as a whole, and a result with a Session means a Run started and settled.

### F1.3: How a Run ends

**Status:** ✅

A Run ends when the model gives its final answer, or earlier for one of a few named reasons.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md), [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A team caps Turns or tool calls with a stop of its own at the end of a Turn, since the core counts neither.

| The Run ends because | Status |
|---|---|
| the model gave its final answer | `completed` |
| your stop at the end of a Turn said so ([F11.5](#f115-at-a-turns-end)) | `completed` |
| it was stopped ([F1.5.4](#f154-stop)) or its caller's context ended | `canceled` |
| its deadline passed ([F1.4](#f14-a-runs-deadline)) | `deadline_exceeded` |
| it is still past the compaction ceiling after the Turn's compaction ([F5.4](#f54-the-ceiling)) | `failed`, `limit_exceeded` |
| the provider still refuses the request as too long after a compaction | `failed`, `context_overflow` |
| a write to the store failed | `failed`, `store_failure` or `conflict` |
| the model failed after its retries, or your code failed | `failed`, with the code |

### F1.4: A Run's deadline

**Status:** ✅

A Run stops through its context. In a service an Agent has a Run timeout, 30 minutes by default, and negative means none. A task may shorten it ([F3.3.1](#f331-a-task-only-narrows)), and a request's own timeout wins.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md)

**Stories:**

- A chat service gives each Run two minutes, so a user never waits on a stuck answer.

- A Run past its deadline settles `deadline_exceeded`, with every Turn it committed kept.
- A request may not set a negative timeout; only an Agent may say "no deadline".

### F1.5: Steer a Run in flight

**Status:** ✅

While a Run works you can change its course without starting over, from any instance. A message either joins the Run in flight or, when none is going, starts a new one.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen), [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md)

**Stories:**

- A user adds a correction while the agent is still working, and the agent takes it at its next Turn.
- A user presses stop, and the Run settles `canceled` with every Turn it committed kept.
- A bot sends a follow-up question that the Run answers before it settles.

- A Run in flight takes the message and the call answers at once that it is queued, with the message's ID (HTTP 202).
- With no Run going the message is the prompt of a new Run, and the call holds until that Run settles (HTTP 200 with the Run).
- A message that arrives while a Run is ending waits for the release and starts the next Run; it is never answered `busy`.
- A message for a Run another instance writes is answered with that instance's address ([F9.5.2](#f952-a-message-for-a-run-elsewhere)).

#### F1.5.1: Steer

**Status:** ✅

A Steer is taken at the next Turn boundary. Up to 8 may wait; one more is refused with `limit_exceeded`.

#### F1.5.2: Follow up

**Status:** ✅

A Follow up is taken where the Run would otherwise settle, and its Turns count as Turns of the same Run. Up to 4 may wait.

#### F1.5.3: Interrupt

**Status:** ✅

An Interrupt takes effect at once. The model call in flight is canceled and its text kept as a Cut-off reply, a tool in flight finishes, and the message starts the next Turn of the same Run. A write cut in half by a tool can leave half a side effect, so a tool is never cut. It shares the Steer queue.

#### F1.5.4: Stop

**Status:** ✅

A Stop cancels everything in flight, tools included, and each tool decides how to stop safely. The Run settles `canceled`. A Stop with no Run anywhere fails with `invalid_state`, and naming the Run makes a retry safe.

### F1.6: Messages outlive their Run

**Status:** ✅

A message the instance accepted is not lost because its Run ended. It becomes a following Run on the same Session, in the order accepted, and the first caller gets the first Run's result.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md)

**Stories:**

- A user sends two quick messages while the agent answers the first, and both get an answer.

- A message is dropped, with its reason in a `message_dropped` Event, only for a cause that ends the work: the Run was stopped or past its deadline, the instance drains, the writer failed, or the queues are full.
- A message the writer accepted but had not saved is lost if the writer dies; the caller sees the stream end and sends it again.

### F1.7: Limits

**Status:** ✅

Each limit guards a real failure, a zero value means its default, and one past a queue's bound is refused with `limit_exceeded`.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A tool hangs on a network call, and after two minutes the call fails and the Run goes on.

| Limit | Default | Guards |
|---|---|---|
| messages and bytes a Session loads | 10,000 messages, 32 MiB | what a load reads and the model is sent |
| one message | 4 MiB | one reply filling memory |
| waiting Steers, Follow ups and notices | 8, 4, 16 | what waits for the Run's next boundary |
| tools at once | 1 | tools running together that were never meant to |
| one model call, one tool call | 2 minutes each | one stuck call holding the Run, even a tool that ignores its context; negative turns it off |
| continues after the output limit | 0, off | how many times a Run asks the model to go on ([F4.4.2](#f442-the-continue-notice)) |

### F1.8: Your code panics

**Status:** ✅

No call into your code can end the process or hold a lock of Gotato's. A panic costs the step it happened in.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/session.md](specs/session.md), [specs/errors.md](specs/errors.md)
- **Why:** [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md)

**Stories:**

- A tool panics on bad data, the model reads that the call failed, and the Run goes on.

- A tool's panic fails that call and the Run goes on.
- A tool source's panic fails that source.
- A model, store or extension panic fails the Run with a code.

### F1.9: Usage

**Status:** ✅

Every Run reports its token usage, cache reads and writes included. Gotato keeps no price table and reports no money.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/model.md](specs/model.md)

**Stories:**

- A team prices each Run at its own rates from the usage in the result.

- Input tokens count every prompt token processed, cached or not, and the cache reads and writes are parts of it, so you price the uncached part at the input rate and the cache parts at their own rates.
- A provider that reports no cache field leaves that part 0.
- A request's own ID is recorded with the Run and echoed in the response; Gotato does not remove repeats, so a repeated ID runs again.

## F2: Sessions

**Status:** ✅

A Session is one conversation that runs an Agent. It keeps every Message, a record of each Run, its usage, its Events and every compaction, as its Record in the store.

### F2.1: Create a Session

**Status:** ✅

You create a Session with your own ID or a generated one, your metadata and a starting history. It answers a summary of the new Session.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/session.md](specs/session.md), [specs/record.md](specs/record.md)
- **Why:** [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md)

**Stories:**

- A service creates a Session for a chat with the chat's ID and a starting history, and runs it later by that ID.

- An ID that is taken fails with `already_exists`, also when two instances create it at once.
- Instead of an Agent a create may name a task ([F3.3.3](#f333-open-a-task-session-with-your-own-id)).
- A Run with no Session creates one too.

### F2.2: Fork a Session

**Status:** ✅

A fork starts a new Session from another's history and records the parent and the position.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/record.md](specs/record.md)
- **Why:** [017](plan/decisions/017-the-record-is-an-append-only-log.md)

**Stories:**

- A user tries another answer from the same point, and the service forks the Session.

- A fork activates nothing, so the first request activates it on whichever instance receives it.
- It keeps its parent's cache key, so its first Turn reads the parent's cached prefix.
- It reads its parent's large results and Segments from the parent's log.

### F2.3: One Run at a time

**Status:** ✅

A Session runs one Run at a time. A second Run on a busy Session is refused with `busy` (HTTP 409), or waits, as the host's busy policy says.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md)

**Stories:**

- Two browser tabs send a prompt at once, and the second is told the Session is busy.

- Refusal is the default; a refusal is marked not run, so it is safe to send again after the Run in flight.
- A message for the Run in flight is not a second Run ([F1.5](#f15-steer-a-run-in-flight)).

### F2.4: Your own metadata

**Status:** ✅

A Session holds your own fields, such as a chat ID or an owner, as metadata. The keys Gotato owns start with `gotato.`, and a caller that sets one is refused.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md)

**Stories:**

- A service keeps the chat ID and the owner in metadata, and lists Sessions with them.

- Gotato's keys hold the Agent, the task the Session was opened for, a tool switched on or off, the model last asked for and the cache key of a fork.
- A `gotato.` key from a caller is refused with `invalid_argument`, naming the key, before anything is written.
- In Go, metadata changes as the first write of a Run; an empty value deletes a key.
- Over HTTP, metadata is set at creation only. A value that changes often belongs in your own store, keyed by the Session ID.

### F2.5: Delete a Session

**Status:** ✅

Deleting a Session removes its Record. A Record created again under the same ID is a new Record, so an old writer is refused and an old stream ends.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md)

**Stories:**

- A user asks to forget a conversation, and the service deletes its Session.

- A delete of a Session that another instance is running is answered `not_writer`.
- A Run whose Session is deleted under it ends `not_found`.

### F2.6: Read a Session

**Status:** ✅

You read a Session's Record, its stored Events, and its summary, on any instance, straight from the store.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/record.md](specs/record.md), [specs/events.md](specs/events.md)
- **Why:** [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md)

**Stories:**

- A support engineer opens a Session to see every step the agent took.

## F3: Agent profiles and tasks

**Status:** ✅

A host registers the Agents it runs as profiles, and the jobs that run again as tasks, from code or from a store of its own. The core runs without them.

### F3.1: Agent profiles

**Status:** ✅

A profile is the whole definition of a kind of Agent: its name, model, instruction, tools and tool sets, Effect cap, Sampling, extensions, Context builder, limits, compaction parts, read-back tool and Run timeout. How a host keeps its Sessions, such as the warm time, is the host's own setting.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/profile.md](specs/profile.md)
- **Why:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)

**Stories:**

- A service registers a support Agent and a billing Agent, and each Session runs one of them by name.

- Each name is registered once, and a later registration replaces it.
- A name that is not registered is not an Agent, whatever a store holds.
- A Session looks its profile up by name each time it is built.

#### F3.1.1: Register inline

**Status:** ✅

A profile written in your code is registered as a value. Only an inline profile can carry Go values: extensions, tool sources and compaction parts.

#### F3.1.2: Register from a store

**Status:** ✅

A profile kept in your database is registered by name, and read from the store each time a Session is built. A stored profile is data only: a model name, tool and tool set names, numbers and text. It chooses from what your code offers in a name catalog and never adds to it; a name the catalog lacks is refused. The documents are kept by [F8.8](#f88-keep-definitions).

#### F3.1.3: Replace a profile

**Status:** ✅

A replaced profile reaches new and rebuilt Sessions, never a Run in flight.

### F3.2: A default Agent

**Status:** ✅

The host names the profile a request runs when it names none. With no default, such a request fails as an unknown Agent.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/profile.md](specs/profile.md)
- **Why:** [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)

**Stories:**

- A chat service with one Agent sets it as the default, and its callers never name it.

- A Run may name another registered Agent, which binds the Session to it from then on and repairs a Session whose stored Agent was dropped.

### F3.3: Task specs

**Status:** ✅

A task is a job that runs again, such as a report each morning: a name, its profile, a background text, and bounds that only narrow the profile.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/profile.md](specs/profile.md)
- **Why:** [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md), [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md)

**Stories:**

- A service registers a morning report job and runs it by name every day.
- A job narrows its Agent, so the report can read orders but never refund one.

- A task is registered inline or from a store, like a profile.
- A task names a registered one or carries one as a value in Go; over HTTP only a registered name is accepted.

#### F3.3.1: A task only narrows

**Status:** ✅

A task's tools and tool sets are some of its profile's, its Effect cap is no higher, its timeout no longer and its output limit no higher. Its other Sampling fields may differ. A task that widens its profile is refused with the bound named, and the check runs again each time the Session is built.

#### F3.3.2: Run a task

**Status:** ✅

Running a task opens a new Session of its profile. The background is added once, after the instruction, so it is part of the cached prefix and never repeated.

#### F3.3.3: Open a task Session with your own ID

**Status:** ✅

A create may name a task instead of an Agent, and the Session is then run by its ID like any other. An ID you derive, such as the job and the day, makes the create safe to retry: a second create is `already_exists` and makes no second Session.

#### F3.3.4: A task Session keeps its spec

**Status:** ✅

The Record keeps the task as it was when the Session opened, so every instance builds the Session the same after the registered task changes. A task Session runs its task's profile and refuses another Agent.

### F3.4: List Agents and tasks

**Status:** ✅

The host lists the registered profiles with their Effect caps, and the registered tasks with the profile each runs, sorted by name.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/profile.md](specs/profile.md)
- **Why:** [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md)

**Stories:**

- A scheduler lists the tasks to know which jobs it can start.

- A stored task that cannot be read is listed without its profile, not left out.

## F4: Context

**Status:** ✅

On every Turn Gotato builds what the model sees from the Session, in a fixed order so the provider can reuse its cached prefix. You can replace how it is built, and inspect it exactly as it is sent.

### F4.1: Built every Turn

**Status:** ✅

The Context is a value built from the Record on every Turn: the instruction, then the tools, then the Segments and the Transcript. It is never edited in place.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md)

**Stories:**

- A developer puts a schema or a runbook in the Agent's instruction, and it is the same bytes on every Turn.

- Reference text the Agent carries goes in its instruction.
- Nothing that changes every Turn is added; a fact the model needs now, such as the time, comes from a tool.
- A large tool result stays out of the Context except for its preview ([F6.7](#f67-large-results)).

### F4.2: Laid out for the cache

**Status:** ✅

What never changes comes first and what grows comes last, and a change Gotato makes itself is appended at the end instead of rewritten at the top. A provider then serves most of each request from its cache.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [006](plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md)

**Stories:**

- A long support Session pays the full input price only for the newest messages of each Turn.

- The tools are sorted by ID, so the same tools give the same bytes.
- A few changes rewrite bytes already sent, on purpose and at a known moment: the last Round's reasoning, a compaction's tool list and a merge of the oldest Segments ([F5.2.4](#f524-the-one-rewrite-of-what-was-sent)).

#### F4.2.1: Cache markers

**Status:** ✅

On the Anthropic wire, cache markers go after the system part, after the tools and on the last message; up to the last four are kept.

#### F4.2.2: A cache key

**Status:** ✅

A cache key rides on every request, so a provider serves one conversation from one cache. In a service it is a hash of the Session ID, and a fork keeps its parent's.

### F4.3: Reasoning goes back for the current Round

**Status:** ✅

When the model's facts say the provider takes back only the current Round's reasoning, Gotato sends the reasoning of the current Round and drops the earlier, at the moment a new Round begins.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md)

**Stories:**

- A team on a reasoning model states the fact once, and the requests carry what the provider expects.

- Without the fact every reasoning artifact is sent, which a provider accepts.

### F4.4: Notices

**Status:** ✅

Gotato, your service or an extension sometimes has to tell the model something that is not the user's words. A notice is a message of its own kind, so the model does not take it for the user and no new Round begins.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md)

**Stories:**

- A service tells the model that a deadline is near, without pretending the user said it.
- A user comes back after a weekend, and the model is told how long they were away and what time it is.

- A notice goes in right before the next model call, in arrival order, and never between a tool call and its result.
- It never starts or extends a Run; one left when a Run settles waits for the next Run.
- It is appended once and never changed, so the cached prefix holds.
- Every wire API receives a notice as tagged text in a user turn.
- Not yet run against a real provider: whether one answers a user turn that holds only a notice.

#### F4.4.1: Your own notice

**Status:** ✅

You send a notice with a kind of your own and its text. The call answers at once with the notice's number (HTTP 202) and never waits for the model. Your notice survives its writer: the next Run on any instance queues it again. An empty kind or text, or one of Gotato's own kinds, is refused with `invalid_argument`; a full queue or a notice over the message limit is refused with `limit_exceeded`.

#### F4.4.2: The continue notice

**Status:** ✅

When a reply stops at the output limit, the Run asks the model to go on, up to the number of times you allow; it is off by default.

#### F4.4.3: The time-away notice

**Status:** ✅

When a message starts a Run after 30 minutes idle, the model is told how long the user was away and the time now. The host sets the threshold, and a negative value turns it off. Gotato's own notices are not kept: a leftover is dropped with its Run, and the time away is worked out again from the last Run's end.

### F4.5: Your own Context

**Status:** ✅

Retrieval, a window, or any other way to choose what the model sees is your own Context builder, set on the Agent.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md)

**Stories:**

- A team adds the three most relevant documents to each request with a builder of its own.

### F4.6: Inspect the next request

**Status:** ✅

You can see the request the next Turn would send, built as a Run builds it, without calling the model.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/context.md](specs/context.md)
- **Why:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md)

**Stories:**

- A developer asks why a Session got expensive and reads the size of each part of the next request.

- The report gives the prefix hash, a token estimate for each part (instruction, tool definitions, Segments, history, tool results) and whether the next Run compacts first.
- Every Turn reports the same prefix hash and estimates in an Event.
- An inspection during a Run is refused as `busy`.
- An inspection cannot know a tool loaded later in a Run, and it runs your extensions and tool sources, so they must have no side effects.

## F5: Compaction

**Status:** ✅

A Session can live for weeks, and a long Run can outgrow the window. Compaction works in two levels: it closes what came since the last Checkpoint into a Segment, and when the Segments grow past their budget it merges the oldest. Nothing moved out of the model's view becomes unreadable.

### F5.1: Close a Segment

**Status:** ✅

A compaction closes what came since the last Checkpoint, less a kept tail, into one Segment, and appends a Checkpoint that lists the Segments the Context uses.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view), [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [033](plan/decisions/033-code-builds-a-segment-and-the-model-writes-only-the-recap.md), [034](plan/decisions/034-a-segment-is-written-once.md)

**Stories:**

- A support Session lives for a month, and its requests keep fitting the model's window with no setting beyond the window.

- The tools the closed Segment or the kept tail called stay loaded; the others unload ([F6.6](#f66-tool-search)).

#### F5.1.1: The Record is never rewritten

**Status:** ✅

The Messages a compaction covers stay in the Record; they are no longer loaded into the Context.

#### F5.1.2: A Segment is written once

**Status:** ✅

A Segment never changes after it is closed, so the cached prefix before the newest Segment holds.

#### F5.1.3: What a Segment keeps

**Status:** ✅

Code builds the Segment, so what it keeps never depends on a model.

| In the Segment | How |
|---|---|
| the user's messages, each Run's final reply | word for word, cut at 2K tokens each with a line saying how to read the rest; each carries a reference |
| each tool call | one line inside its Run: time, tool, arguments cut short, the outcome and the reference of its result; repeats counted |
| your service's notices | word for word; Gotato's own are left out |
| the rest of a Run's text, reasoning and tool results | left out of the Context, and read back by reference ([F5.3](#f53-read-back)) |

#### F5.1.4: The Recap

**Status:** ✅

The Recap ends a Segment: what happened, the key facts, the decisions, what is still open and where it stands. It is the one part a model writes, the Agent's own on the cached prefix. It is off by default, and a team turns it on only after its own check on its model endpoint passes. A Segment with no Recap, or one whose Recap broke the rules, is kept without one, and the compaction says why. Not yet run against a real model.

#### F5.1.5: The kept tail

**Status:** ✅

The latest Run's Turns stay as they were after the Segments, up to 20K tokens and at most a quarter of the window. The cut falls on a Turn boundary, so a tool call keeps its result, and the tail always holds at least the latest Turn. When the provider takes back only the current Round's reasoning, the kept tail loses its reasoning.

### F5.2: Merge Segments

**Status:** ✅

Segments have a token budget. Past it, the oldest merge into one Segment, so a Session of any age still fits.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view), [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [043](plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md)

**Stories:**

- A Session open for a year still fits the window, with its first months in one merged Segment.

#### F5.2.1: The Segment budget

**Status:** ✅

The budget is half the ceiling, or 32K tokens with no ceiling.

#### F5.2.2: The merged Segment

**Status:** ✅

The merged Segment always opens the Context and is named by the range it stands for, such as 1-3. A merge replaces two or more of the oldest Segments, by default enough to bring them under half the budget.

#### F5.2.3: A Recap of the Recaps

**Status:** ✅

The merged Segment's Recap is written by the model from the Recaps alone, or by code as a digest when no model writes it.

#### F5.2.4: The one rewrite of what was sent

**Status:** ✅

A merge changes the start of the Context, so the next request is read at full price once. This is the cost of a Session that never outgrows the window, paid rarely and at a known moment.

### F5.3: Read back

**Status:** ✅

Nothing moved out of the model's view is unreadable. The model reads any Segment back in full, and any message or tool result by its reference. A merged Segment's originals stay readable by their own numbers.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/compaction.md](specs/compaction.md), [specs/tools.md](specs/tools.md)
- **Why:** [043](plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md)

**Stories:**

- After a compaction the model needs an order number from an old tool result, and reads it back by its reference.

- Two read tools do it: one for a Segment, one for a result or a message, both declared `read`.
- A removed original reads as removed, with what the conversation still keeps of it.
- A Session reads only its own results; another reference fails with a list of those it holds.

### F5.4: The ceiling

**Status:** ✅

The ceiling is the size a Context may reach: the model's window less the larger of 10% of it and 16K tokens, unless the Agent sets its own. It is a hard line, not a cost knob.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view), [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A team states its model's window and never sees a request refused as too long.

- A Run still past the ceiling after the Turn's compaction ends with `limit_exceeded`, whoever set it.
- A team that wants a cost cap writes its own stop at the end of a Turn ([F11.5](#f115-at-a-turns-end)).
- With no window and no ceiling there is no compaction by size; it runs only after a refusal as too long, if the refusal is recognized.

### F5.5: When it runs

**Status:** ✅

Each compaction records why it ran.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md)

**Stories:**

- A developer compacts one Session by hand, keeping the last few messages.

#### F5.5.1: Past the ceiling

**Status:** ✅

Before any model call, mid-Run included, a Context past the ceiling is compacted, and merged too when that brings the Segments past their budget.

#### F5.5.2: After a refusal as too long

**Status:** ✅

When the provider refuses a request as too long before any output, the Session compacts and sends the call again, once per Turn.

#### F5.5.3: When idle

**Status:** ✅

A warm Session idle until five minutes before its prompt cache or its warm time would end is compacted, when at least 20K tokens came since the last Checkpoint. It runs outside any Run, and a message that arrives cancels it. It is on by default and can be turned off.

#### F5.5.4: Past the Segment budget

**Status:** ✅

At the start of a Run, when the Segments are past their budget and there is nothing new to close, the Session merges alone.

#### F5.5.5: On demand

**Status:** ✅

You compact a Session by a call, keeping a number of recent messages, or only when enough has come since the last Checkpoint. It answers that it did nothing when there is nothing to close or too little. During a drain it is refused as `draining`.

### F5.6: Replace a part

**Status:** ✅

When to compact, where to cut, how a Segment reads, who writes the Recap and how many Segments merge are parts a team can replace. The frame that writes the batch and keeps the guarantees is fixed.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/compaction.md](specs/compaction.md)
- **Why:** [046](plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md)

**Stories:**

- A team swaps when to compact, without copying the rest.

- A part that fails, panics or breaks a rule drops that compaction with `compaction_failed`; nothing is written and the Run goes on.
- The settings are numbers only: the ceiling, the window, the Segment budget, the reasoning drop, the tail size and the time zone of rendered times (UTC by default).

## F6: Tools

**Status:** ✅

A tool is one thing an Agent can do, and each declares how much it can change: its Effect. The tools a Session offers can change while it runs.

### F6.1: A Go function as a tool

**Status:** ✅

A Go function becomes a tool, its input schema comes from the Go type, and every call is checked against it before the tool runs.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md)

**Stories:**

- A developer turns an order lookup function into a tool, and the model's arguments are checked before it runs.

- A tool learns the Session a call belongs to, so an extension that asks a person knows which conversation to ask in.

### F6.2: Effect and the cap

**Status:** ✅

Every tool declares its Effect: `read`, `write_local`, `write_external` or `destructive`, and a tool that declares none counts as destructive. An Agent may set a cap, and a tool above it is never seen.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [005](plan/decisions/005-no-approval-system.md), [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md), [014](plan/decisions/014-the-effect-cap-hides-tools-above-it.md), [015](plan/decisions/015-only-the-agent-sets-the-effect-cap.md)

**Stories:**

- A team gives a read-only Agent a cap, and a refund tool in the same service is never offered to it.

- A tool above the cap is in no request, no inspection, no tool listing and no search index.
- A guessed call of one gets a failed result saying the Session does not allow it.
- Only the Agent sets the cap; a Session can neither raise it nor switch on a hidden tool.

### F6.3: Mistakes go back to the model

**Status:** ✅

An unknown tool, arguments that are not valid JSON or fail the schema, and a tool's own error come back to the model as a failed result, and the Run goes on.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/tools.md](specs/tools.md), [specs/errors.md](specs/errors.md)
- **Why:** [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md)

**Stories:**

- The model passes a date in the wrong format, reads why it failed, and calls again with the right one.

- A failed result carries a code that says why; no tool or extension can set or change it.
- A tool's error text reaches the model only when the tool marks it public; otherwise it reads `tool failed`.

### F6.4: Tools in parallel

**Status:** ✅

Tools run one at a time by default. With a higher limit, up to that many run at once, and their results keep the order the model asked for. A tool marked sequential always runs alone.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A team lets three read tools run together and keeps its write tools sequential.

### F6.5: Tool sets

**Status:** ✅

A tool set is a group of tools with a name, a description and one source, such as an MCP server. Its tools are named `<set>.<tool>`, and the namespace is the set's.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md)

**Stories:**

- A service installs an MCP server's tools as a group, and the model sees only the group's description until it needs one.

- A tool set is resolved when a Run starts, never inside one, so its changes take effect at the next Run.
- A tool source is asked again at the start of every Turn; one that fails keeps its last set.
- An ID two sources supply is refused.

#### F6.5.1: Three install modes

**Status:** ✅

| Mode | What the model sees |
|---|---|
| active | every tool of the set, always |
| listed (the default) | the set's description and all its tool names, in the catalog |
| folded | the set's description and a count of its tools |

#### F6.5.2: A set that fails

**Status:** ✅

A tool set that fails to resolve is skipped whole. The catalog keeps its description marked unavailable, the other tools work, and a warning comes once per Run while it stays down.

#### F6.5.3: Switch a tool per Session

**Status:** ✅

A host switches one tool on or off for one Session, without touching the Agent. A tool switched off is never in the catalog, found or loaded. It is a Go call; over HTTP there is no tool listing or switch.

### F6.6: Tool search

**Status:** ✅

A service can offer hundreds of tools. The model reads a catalog and loads the tools it needs one by one, so the request grows only by those.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md)

**Stories:**

- A model facing 300 tools reads one catalog, loads the three it needs, and the request grows only by those.

- The search tool declares `read`, so every cap keeps it; it loads tools by name, or by a query that loads the best 3 and lists 5 more.
- The search covers only tools the Session may use, and a team can plug in its own search.
- A call to a tool not loaded yet does not run; the tool is loaded and the model is told to call again, which costs one round.
- A loaded tool stays until the next compaction.
- On the Anthropic and Responses wires every tool is in the request from the start, deferred, so the tool list never changes; on chat completions a loaded tool is added to the list, which costs one cache read per load.
- Not yet run against a real provider.

### F6.7: Large results

**Status:** ✅

A text result over one page is kept whole in the Record, and the model gets a preview and a reference and reads the rest in pages.

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/tools.md](specs/tools.md), [specs/record.md](specs/record.md)
- **Why:** [040](plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md), [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md)

**Stories:**

- A tool returns a 400 KiB report, the Context holds the first part, and the model asks for page 5 when it needs it.

- A page is 32 KiB of text, about 8K tokens, by default; the spill threshold, the preview and the read tools all use it.
- The preview holds the first 2K tokens and a line naming the reference and the page count.
- Images and files are not spilled.
- Subscribers see the size of a spilled result, never the body.
- An Agent may replace the read-back tool, and a hosted Session gets the default over its store.

## F7: Model

**Status:** ✅

A provider is configuration, not code. The model bridge has four layers, each with a default a team can replace, and any vendor that speaks a supported wire API works without new code.

### F7.1: Three wire APIs

**Status:** ✅

Gotato speaks Chat Completions, Responses and Anthropic Messages, each implemented once. A connection is an endpoint, a key and headers; an empty key sends none, so a local server works.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [001](plan/decisions/001-one-implementation-per-wire-api.md), [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)

**Stories:**

- A team points the bridge at its provider's endpoint with a key and a wire API, and runs an agent with no new code.
- A developer brings a model client of their own.

- The cache key goes in the body field or header you name; a provider that keys by a session header needs it named, or every Turn is cold.
- Gotato sends no cache markers on the chat wire, so a gain there needs something between you and the model to add them.

### F7.2: State a model's facts

**Status:** ✅

Gotato keeps no table of providers or models and reads nothing from an address or a name. The team states what a model is, and a fact left out has the cautious answer.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [042](plan/decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md), [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)

**Stories:**

- A team states its model's window once, and compaction takes its ceiling from it.

| Fact | Means | If left out |
|---|---|---|
| window | the context window in tokens, or unknown written out | the bridge refuses the model |
| cache retention | how long the provider keeps the prompt cache ([F7.7](#f77-prompt-cache-retention)) | the provider's default |
| round reasoning | the provider takes back only the current Round's reasoning ([F4.3](#f43-reasoning-goes-back-for-the-current-round)) | every reasoning artifact is sent |
| thinking binding | the provider checks the prefix a thinking block was written under | nothing is sent about it |

- Not yet run against a real provider: Gemini's thought signature on the chat wire, and DeepSeek's rule on reasoning.

### F7.3: Sampling

**Status:** ✅

Temperature, the output limit and reasoning settings are set per Agent, under the model's own defaults field by field. A task may lower the output limit ([F3.3.1](#f331-a-task-only-narrows)).

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md), [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md)

**Stories:**

- A team runs a creative Agent warm and an extraction Agent cold on the same model.

- The request carries only what the Agent and the Sampling set.
- Reasoning settings go on Responses; a request that sets them on the other two wires is refused as `not_supported` before anything is sent.

### F7.4: Retries

**Status:** ✅

A call is sent again when no response came, on 429 except a hard quota, on 500, 502, 503, 504 or 529, or when the error says rate-limited, overloaded or unavailable. A provider's wait wins over the backoff.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md)

**Stories:**

- A provider returns 503 for a second, and the Run never notices.

- The default is 2 retries, a 200 ms backoff and a 60 s longest wait; a team sets its own or none.
- A refusal as too long is never retried.
- An error the provider sends mid-stream is marked retryable, but the bridge does not send it again once the stream started; a cut stream is the exception ([F7.6](#f76-recover-a-cut-stream)).
- When the retries run out the Run fails with `model_failure`.

### F7.5: A request refused as too long

**Status:** ✅

A 400 or 413 that says the request is too long, or a stop for the context window mid-reply, is recognized as `context_overflow`. The Session compacts and tries once more ([F5.5.2](#f552-after-a-refusal-as-too-long)).

- **Design:** [3 The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md)

**Stories:**

- A team's provider words its refusal differently, and the team adds the wording to the default classifier.

### F7.6: Recover a cut stream

**Status:** ✅

A reply cut off partway does not fail the Run. A stream cut after some output is asked for again, the way one cut before any output is.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/model.md](specs/model.md)

**Stories:**

- A provider drops a stream halfway through a long answer, and the user gets the full answer anyway.

- The same request goes again within the retry count, and an Event says so with the attempt, the wait and the reason.
- A stream reader who sees that Event discards what it received of that reply.
- The reply cut off stays in the Record only as a log, and no model is ever sent it.
- A refusal, an Interrupt, a canceled context and a deadline are not cuts.

### F7.7: Prompt cache retention

**Status:** ✅

The team states how long its provider keeps the prompt cache, once, on the model. Gotato asks the provider for that cache and times idle compaction by it.

- **Design:** [2 A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [042](plan/decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md)

**Stories:**

- A team on a provider with a one-hour cache states it once, and an idle Session is compacted before the cache expires.

- The Anthropic wire asks for the one-hour cache when retention is an hour or more, which doubles the price of a cache write.
- The Responses wire asks for 30 minutes when it is exactly 30 minutes.
- Any other retention keeps the provider's default.
- Gotato does not refresh a warm Session's cache.

### F7.8: Replace a layer

**Status:** ✅

Each layer of the bridge has a default a team can replace.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/model.md](specs/model.md)
- **Why:** [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)

**Stories:**

- A team whose provider needs a signed request supplies its own HTTP client.

| Layer | Default | A team replaces it with |
|---|---|---|
| wire API | the three above | its own encoder of a request and decoder of a reply |
| connection | an HTTP client with a 10 s connect and a 60 s response-header timeout | a client that signs, refreshes a token or goes through a proxy |
| retry | as in [F7.4](#f74-retries) | its own counts and waits, or none |
| meaning of an error | the default classifier | usually a function that calls the default and changes its answer |

## F8: Store

**Status:** ✅

Every Session is saved to a store as its Record, an append-only log. The store is the truth, and an instance's memory is a cache.

### F8.1: Memory store

**Status:** ✅

The memory store keeps Records in the process, for tests and short-lived programs.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md)

**Stories:**

- A developer tests an agent with the memory store and no setup.

### F8.2: File store

**Status:** ✅

The file store keeps Records on local disk for one process, and a crash never tears a write.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md)

**Stories:**

- A developer ships a single-process bot on the file store.

- After a crash it removes leftover temporary files a day old or older.
- Its liveness uses the local clock, so one directory shared across machines needs synced clocks.

### F8.3: Your own store

**Status:** ✅

A team backs the store with the database it already runs. The contract is two seams, Records and Liveness, and each call is one ordinary conditional write or read.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary), [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md), [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md), [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md)

**Stories:**

- A team stores Sessions in the database it already runs, through an adapter of its own.

- The adapter owns the schema and the migrations.
- Listing and definitions are optional seams of their own.
- The Records seam gains no methods after 0.3.0.

### F8.4: Conformance suite

**Status:** ✅

A suite runs every rule of the contract against an adapter, with several handles standing for several instances.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md)

**Stories:**

- A team proves its adapter with the suite before it ships.

- It checks create, load, append, a race of writers with one winner, a stale writer refused, Liveness expiry, listing and paging.
- Its own tests check that it fails adapters that break a rule.

### F8.5: An append-only Record

**Status:** ✅

Every step is appended as it happens, at a position the store never gives twice. A compaction appends a Checkpoint, and a load reads from the last one.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/record.md](specs/record.md), [specs/store.md](specs/store.md)
- **Why:** [017](plan/decisions/017-the-record-is-an-append-only-log.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md)

**Stories:**

- A pod dies mid-Run, and another loads the Session from its last Checkpoint and continues.

- An entry of an unknown kind, or a document from a newer schema, is refused.
- The Record grows with every step; a team that wants to bound storage deletes entries before a Checkpoint itself.

### F8.6: Session IDs

**Status:** ✅

A Session ID is at most 100 bytes of valid UTF-8 with no control character, slash, backslash, `..` or leading dot. Every store refuses another with `invalid_argument`.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md)

**Stories:**

- A service uses its chat IDs as Session IDs.

- A store may refuse more: the file store spells uppercase and non-ASCII characters in the directory name and refuses a name past 200 bytes.

### F8.7: List Sessions

**Status:** ✅

A store with a lister gives a page of Session summaries, most recently updated first, with a cursor for the next.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/store.md](specs/store.md)
- **Why:** [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md)

**Stories:**

- A dashboard lists Sessions, most recently updated first.

- A page holds 100 Sessions unless you set a limit.
- A store with no lister answers `not_supported`.
- A Session updated while a client pages may repeat or be skipped.

### F8.8: Keep definitions

**Status:** ✅

An optional store keeps profile and task documents by kind and name, and knows nothing of what they mean ([F3.1.2](#f312-register-from-a-store)).

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/store.md](specs/store.md), [specs/profile.md](specs/profile.md)
- **Why:** [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)

**Stories:**

- A team edits its report job in its database, and the next run uses the new version.

- The memory and file stores keep definitions too.

## F9: Many instances

**Status:** ✅

A host runs Sessions inside your service on as many instances as you run. Any instance answers a request as if it were the only one, and no Session is bound to an instance. Gotato builds no network between instances: your load balancer routes.

### F9.1: Every step saved as it happens

**Status:** ✅

The prompt, a model call before it is made, a streaming reply every second or 4 KiB, the reply, tools before they run and each tool result are written as they happen. A crash loses at most the step in flight.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md), [specs/record.md](specs/record.md)
- **Why:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md)

**Stories:**

- A rolling deploy replaces every pod, and each conversation continues on a new pod.

- When the writer dies mid-Run, a running tool is answered with an unknown outcome the model reads, and the stream ends.

### F9.2: One writer per Session

**Status:** ✅

One instance writes a Session at a time, through a claim in the store. A second instance that tries stops before it calls the model.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md), [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md)

**Stories:**

- Two requests for one Session reach two pods at once, and the agent answers once.

- Whichever instance opens it first runs; the other waits, then answers 409.

### F9.3: Takeover

**Status:** ✅

When a Session's writer is gone or idle, any instance claims it, loads it from the last Checkpoint and serves the request.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [020](plan/decisions/020-no-session-is-bound-to-an-instance.md)

**Stories:**

- A pod is killed, and the user's next message is answered by another pod with the whole conversation.

- A Session whose writer died waits until the dead instance's row expires, 10 s by default.

### F9.4: Instance liveness

**Status:** ✅

Each instance keeps one row in the store, timed by the store's clock. When the row expires, the instance's claims end with no write, so its Sessions are free.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md)

**Stories:**

- An instance loses its store for a minute, refuses work while it is out, and goes on under a new name when it is back.

- An instance that cannot show its row renewed writes nothing, refuses new work as not run, and answers not ready.
- A name shared by two live processes is safe, since each start makes it unique.
- Closing an instance removes its row, so its Sessions move at once.

### F9.5: A request on the wrong instance

**Status:** ✅

Your load balancer should send every request for a Session to its writer, by a consistent hash on the `session-id` header. When it misses, the request still runs once.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [020](plan/decisions/020-no-session-is-bound-to-an-instance.md), [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md)

**Stories:**

- A load balancer sends a request to the wrong pod, and the request still runs once, on one pod.

#### F9.5.1: A new Run

**Status:** ✅

A Run for a Session in use by its live writer waits for the release up to a bound, 5 s by default, then claims it. Past the bound it answers 409 with a retry time.

#### F9.5.2: A message for a Run elsewhere

**Status:** ✅

A Steer, Follow up, Interrupt, notice or Stop for a Run another instance writes is answered `not_writer`, not run and retryable, with the writer's address when the host has one. With no Run going the instance takes the Session over instead. Gotato never connects to the address itself; over HTTP it can forward ([F12.5](#f125-forward-to-the-writer)).

### F9.6: Warm Sessions

**Status:** ✅

After a Run the Session stays in memory with no claim, for 60 idle minutes by default. Before each Run the instance checks the stored head and reloads if another instance wrote since.

- **Design:** [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md)

**Stories:**

- A user's next message arrives within the hour, and the instance answers without loading the Session.

- A warm Session holds no claim, so any instance may take the Session over at any time.
- A stream on an instance that is not the writer sees stored Events at its next look, once a second by default, and no partial text.

### F9.7: The memory limit

**Status:** ✅

The instance counts the Sessions it holds in memory, warm ones included. Past the limit the longest idle is evicted first, and a Run is refused only when every live Session is running.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md), [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)

**Stories:**

- A spike of new users fills an instance, and idle conversations leave memory first.

- A refusal answers `overloaded` (HTTP 503), marked not run, before anything is loaded.
- Up to 10,000 warm Sessions are kept by default.

### F9.8: Admission

**Status:** ✅

The host asks your rule whether a Run may start, with the caller's context and the instance's counts. Its default is no limit; a simple limit on Runs at once is built in.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md), [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)

**Stories:**

- A service gives each tenant its own limit of concurrent Runs.

- A refusal is `limit_exceeded` (HTTP 429), marked not run.
- Your rule is told when each Run it admitted ends, so a count per tenant stays right.
- The memory limit and the drain are checked first, since they guard the instance.
- Counts are per instance.

### F9.9: Drain

**Status:** ✅

On a stop signal, or a call from your endpoint, the instance stops taking new Runs and lets every Run finish, save and release.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [020](plan/decisions/020-no-session-is-bound-to-an-instance.md)

**Stories:**

- An operator drains a pod, and every Run on it finishes and is saved before it exits.

- It answers not ready, refuses new Runs and takeovers as `draining`, marked not run, so your load balancer sends them elsewhere.
- At the drain timeout, 10 s by default, Runs still going are stopped, saved and released, and each stream ends with a reconnect.
- Create and fork still work; a compaction is refused as `draining`.

## F10: Events

**Status:** ✅

Every step of a Session is an Event, so you can watch, log or measure it. A Session never waits for a reader.

### F10.1: The Record is the Event log

**Status:** ✅

A stored Event is an entry of the Record, and its number is its position there, so every instance reads the same number. It reaches subscribers only after it is written.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen), [1 The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md), [030](plan/decisions/030-every-entry-is-an-event.md)

**Stories:**

- A team measures what a Run cost and how long each step took, from its Events alone.

- Each kind has its own type, and in JSON one flat object whose `kind` names it.
- Durations are milliseconds, and a tool is named by its ID.
- Partial text and tool progress are sent and never stored; everything else is stored.
- A rebuild reads only the facts; every other Event is a log for people and telemetry.

| Events | What they report |
|---|---|
| Run and Turn | a Run starts and settles with its status and cause; a Turn starts and ends with its usage and its Context |
| Reply | a reply starts and ends; the text so far is kept as it streams; a cut reply is kept |
| Tool | a tool starts and ends with status and duration; a tool is loaded or unloaded |
| Message and notice | a message or notice waits, is committed, dropped or refused, with why |
| Session | a Session is created, claimed or released, or its metadata changes |
| Compaction | a compaction starts, ends or fails, with why; a Segment is closed |
| Extension | an advisory extension or a tool set failed and the Run went on |

### F10.2: Subscribe to a Session

**Status:** ✅

A subscriber follows a Session from any stored Event's number, first from the Record and then live, across Runs and on any instance.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md)

**Stories:**

- A developer follows one Session to deliver its answer to a chat.

- A Run's own stream carries the Session's Events from the Run's first to its last, with the same numbers.
- A Run that is not saved has only its own stream, numbered from 1.
- A caller of a Run needs none of this: the result carries the final text.

### F10.3: A subscriber that falls behind

**Status:** ✅

A slow subscriber never slows the Run. It catches up from the Record by number and rejoins live, so it never misses a stored Event; only partial text can be skipped, and the Run's result has the final text.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md)

**Stories:**

- A browser on a slow network falls behind a long answer, and still shows every step.

### F10.4: Watch an instance

**Status:** ✅

An instance's stream carries every Event of every Session on it, plus the instance's own: requests that arrive, wait or are refused, claims lost, liveness lapsed, Sessions loaded, saved, unloaded or deleted, and store writes that failed.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)

**Stories:**

- An operator sends every Event of an instance to the team's telemetry.

- It has no single number, so it cannot replay; when it falls behind it skips Events and says how many.
- It is a Go call; over HTTP there is none.

### F10.5: Events a later version adds

**Status:** ✅

A kind a later version adds reads back as an unknown Event, so an older reader never fails on a newer Record.

- **Design:** [5 Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md)

**Stories:**

- A dashboard built on 0.3.0 keeps working when the service upgrades.

## F11: Extensions

**Status:** ✅

Extensions add behavior at fixed points of the Loop without forking it. Each runs on the Run's goroutine, so a slow extension slows the Run. An error from one ends the Run, keeping a Gotato code or `extension_failure`. There is no observer stage: a service reads Events.

### F11.1: Before a model call

**Status:** ✅

Before every model call, with no tool call open, an extension may rewrite the Transcript. Automatic compaction is one.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team trims a tool's old outputs before each call, in a way of its own.

### F11.2: Shape what the model sees

**Status:** ✅

After the Context is built and before it is sent, an extension may change it, and another may change how Messages are converted for the model.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team masks customer emails in every request.

### F11.3: Before a tool

**Status:** ✅

Before a tool runs, an extension may refuse the call, and the model reads why.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [005](plan/decisions/005-no-approval-system.md), [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team refuses a refund over a set amount by checking its arguments before it runs.

### F11.4: After a tool

**Status:** ✅

After a tool runs, an extension sees the call and its result.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team counts the calls of each tool for its own report.

### F11.5: At a Turn's end

**Status:** ✅

At the end of a Turn an extension may stop the Run, and records why.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md), [038](plan/decisions/038-a-limit-guards-a-real-failure.md)

**Stories:**

- A team stops a Run after ten Turns, or after it spends a token budget.

### F11.6: Advisory extensions

**Status:** ✅

An extension marked advisory may fail without failing the Run; its failure is stored with the stage. A panic counts as blocking.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/session.md](specs/session.md)
- **Why:** [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md)

**Stories:**

- A team adds a logging extension that must never break a conversation.

## F12: HTTP API

**Status:** ✅

A bot, a job or another service talks to Sessions like any other API. The handler is an optional adapter in a module of its own that you mount on your own mux, and it only translates the host's calls and errors. Authentication, logging and access control are yours: wrap the handler.

### F12.1: Routes

**Status:** ✅

Every host call has a route.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/events.md](specs/events.md), [specs/errors.md](specs/errors.md)
- **Why:** [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)

**Stories:**

- A job creates a Session for a task and runs it, with two requests.

| Routes | What they do |
|---|---|
| `GET /healthz`, `GET /readyz` | alive with the contract version; ready with the counts of Runs and Sessions, or 503 when draining or lapsed |
| `GET /v1/agents`, `GET /v1/tasks` | the registered Agents and tasks ([F3.4](#f34-list-agents-and-tasks)) |
| `POST /v1/sessions`, `GET /v1/sessions` | create, with an Agent or a task; list a page |
| `GET`, `DELETE /v1/sessions/{id}` | read, delete |
| `POST …/fork` | fork |
| `POST …/runs`, `POST …/runs/stream` | a Run answered when it settles, or streamed |
| `POST …/messages`, `POST …/notices`, `POST …/abort` | steer, notice and stop |
| `POST …/compact`, `GET …/context` | compact on demand, inspect the next request |
| `GET …/events`, `GET …/stream` | every stored Event as JSON lines, or the Session's stream |

- The body is capped at 1 MiB.

### F12.2: Status codes and errors

**Status:** ✅

A Run that settles failed, canceled or past its deadline still answers 200 with the outcome in the body. Only problems with the request itself use other statuses.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/errors.md](specs/errors.md)
- **Why:** [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md)

**Stories:**

- A bot reads one header and knows it may send the request to another pod.

| Status | When |
|---|---|
| 201 | a Session was created or forked |
| 400 | the request is invalid |
| 404 | the Session does not exist |
| 408 | the caller canceled the request |
| 409 | busy; the Run is not active; the ID is taken; the Session is closing; another writer took it; a stream cannot resume; or `not_writer` |
| 413 | the body is too large |
| 422 | a call outside a Run was refused as too long |
| 429 | at the Run limit, or a message queue is full |
| 500 | a settled Run could not be saved |
| 502 | a forward to the writer failed, so the message may have reached it |
| 503 | draining, past the memory limit, or the store failed |
| 504 | the request's deadline passed |

- Every error body carries the code, a message, whether it ran and whether a retry may succeed; two headers say the same.
- Send a request to another instance only when it is marked both not run and retryable, except `not_writer`, which goes to the writer it names.
- A connection that broke after sending may have run, and is not retried.

### F12.3: Stream a Run

**Status:** ✅

A streamed Run sends each Event as a server-sent event and ends with the Run's result.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md)

**Stories:**

- A chat bot streams the reply to its user as it is written.

- It answers 200 once the Run is starting; a request refused before that gets its status.
- A slow client never slows the Run.
- Partial text is sent only by the writer, so a stream on another instance sees stored Events only.
- An idle stream sends a comment every 15 seconds.

### F12.4: Watch a Session and resume

**Status:** ✅

A Session's stream sends the stored Events after a number, then the live ones across Runs, on any instance. A client that drops reconnects with the last number it saw.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/events.md](specs/events.md)
- **Why:** [030](plan/decisions/030-every-entry-is-an-event.md)

**Stories:**

- A browser reconnects after a network drop and gets every Event it missed.

- Each stored Event's number is its SSE ID, so a browser resumes on its own.
- The first frame carries the Record's identity; a client that resumes on another, or past the last Event, gets 409 `cannot_resume` and starts over at 0.
- A drain ends a stream with a reconnect and a short wait spread between clients; any other end is an error with no reconnect.

### F12.5: Forward to the writer

**Status:** ✅

When you turn it on, the handler forwards a message, notice or Stop for a Run on another instance to the writer, only to addresses your rule accepts.

- **Design:** [8 No instance is special](DESIGNS.md#8-no-instance-is-special)
- **How it works:** [specs/multi-instance.md](specs/multi-instance.md)
- **Why:** [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md)

**Stories:**

- A team without a hashing load balancer turns forwarding on for its own pod network.

- It is off by default, and a rule that accepts nothing forwards nowhere.
- A forwarded request is marked, so a second instance never forwards it again.
- A forward that fails answers 502, since the message may have arrived.

## F13: MCP adapter

**Status:** ✅

Tools a team already runs as MCP servers reach a Session as a tool set. The adapter is a module of its own, and no core package imports it. A server is one tool set, and its tools are `<server>.<tool>`.

### F13.1: Connect a server

**Status:** ✅

A server connects over streamable HTTP, used without state so any instance can make any call, or as a subprocess shared by the instance's Sessions.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md), [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md)

**Stories:**

- A team points a Session at a CRM server over HTTP, and the model finds its tools through the catalog.

- It connects on first use, so a server that is down never fails the setup.
- A connect is bounded, 10 seconds by default.
- Its description in the catalog is yours, or the first paragraph of the server's instructions.
- The tool list may be kept for a time, unless the server says it changed; by default a change is seen at once.

### F13.2: Effect from the server's annotations

**Status:** ✅

A tool's Effect comes from the server's annotations: read-only is `read`, non-destructive in a closed world is `write_local`, non-destructive in an open world is `write_external`, and anything else is `destructive`.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md)

**Stories:**

- A team names a server's read-only tools exactly, so a read-only Agent can use them.

- A tool with no annotations is `destructive`, so a cap hides it; a team sets one tool's Effect, or a default for a server that sends none.
- A tool's name is never read as its Effect.
- Adapter tools are not sequential, so with tools in parallel a server's writes run beside its reads unless a team marks them.

### F13.3: Authentication per call

**Status:** ✅

Authentication is your HTTP client: a fixed header, a refreshing token or mutual TLS. A round tripper can send the end user's token on each call, though the connection is shared.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)

**Stories:**

- A team calls a server as the user who asked, with that user's token.

### F13.4: Filter and rename tools

**Status:** ✅

A team leaves a tool out, or changes only the name the model sees, before the model sees either. The adapter reports the tools it left out and why.

- **Design:** [7 Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md)

**Stories:**

- A team renames a server's tool and hides another before the model sees either.

- A rename changes only the model's name; the Effect, the filter and the call to the server use the server's own.

### F13.5: Results and progress

**Status:** ✅

A server's result becomes what the model reads: text, images, structured content as JSON, and one line for what cannot be shown. A server's error is a failed result the model reads.

- **Design:** [4 A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **How it works:** [specs/tools.md](specs/tools.md)
- **Why:** [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md)

**Stories:**

- A server returns a chart and a table, and the model reads both.

- Progress reaches the Session as Events.
- A large result is kept whole and read in pages ([F6.7](#f67-large-results)).
- A tool that asks for input mid-call ends with a failed result; nothing waits.
- Tested against the SDK's own server over HTTP and stdio; not yet against a server of the multi round-trip revision.

## F14: Library

**Status:** ✅

Gotato is a library you add with `go get`, and it builds, tests and deploys with your service.

### F14.1: Runs with no server or database

**Status:** ✅

A small program builds an agent with a fake model and runs it in-process; a server, a daemon and a database are all optional.

- **Design:** [5 Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md)

**Stories:**

- A developer runs an agent in a ten-line program with a fake model, and nothing else is needed.

### F14.2: Layers that import only downward

**Status:** ✅

Each package is one layer and imports only the layers below it, and a test fails when the order breaks. The core imports only the standard library.

- **Design:** [10 Each layer does one job, and each default can be replaced](DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md)

**Stories:**

- A developer uses the core alone and never pulls in the host or a store.

### F14.3: Modules you opt into

**Status:** ✅

The HTTP and MCP adapters are modules of their own, and no core package imports them. The root module depends on one YAML library, which only the test tools use.

- **Design:** [6 Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md), [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md)

**Stories:**

- A service adds the HTTP adapter and the MCP adapter as separate modules.

- The core needs Go 1.24; the MCP adapter needs Go 1.25.

### F14.4: A small footprint

**Status:** ✅

A program that imports the agent, the host, the file store, the HTTP handler and the model bridge and does nothing else links 3 modules and builds to a 9.0 MB stripped binary (measured 2026-11-03, Go 1.27.2).

- **Design:** [5 Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md)

**Stories:**

- A team adds an agent to a service and its image barely grows.

### F14.5: A versioned contract

**Status:** ✅

The Go API, the HTTP contract and the shape of a saved Session change only through versioned breaks, each with a before-and-after note. The health route reports the HTTP contract version.

- **Design:** [5 Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md)

**Stories:**

- A team upgrades and reads a before-and-after note for every break.

### F14.6: Test kit

**Status:** ✅

You test agents without a network or a paid model, and measure Gotato on a fixed workload.

- **Design:** [9 A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **How it works:** [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md)
- **Why:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md), [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md)

**Stories:**

- A developer tests a tool-using agent with a scripted model, and the test never touches a network.
- A maintainer measures a release against the reference workload and compares it with the last one.

- The kit has a scripted model, a replay model, a model that answers from the request, a fake tool, an Event recorder and a ready-made Session.
- The reference workload is a fixed set of tasks, from a single lookup to a Session of 1,000 messages, each with a fake model and a check.
- It reports every failed Run, tool call and check with its cause, the usage, the cost at your price, the cache hit rate and Gotato's time per Turn beside the model's and the store's.
- Fault injection kills an instance after each write and checks that another opens exactly what the write left; it also cuts streams and runs three instances behind a hashing proxy.
- A fake provider plays each wire API and its quirks over HTTP, so Gotato runs no checks against real providers itself.
- A live run against a paid model runs on demand, never in CI.

## When something fails

Every error answers three questions where it is made: its code says what happened, "not run" says nothing with side effects happened, and "retryable" says the same request sent later may succeed.

| What you see | Not run | Retryable | Feature |
|---|---|---|---|
| `draining`: the instance takes no new Run | yes | yes, on another instance | [F9.9](#f99-drain) |
| `overloaded`: past the memory limit | yes | yes, on another instance | [F9.7](#f97-the-memory-limit) |
| `limit_exceeded` from your admission rule or the Run limit | yes | yes | [F9.8](#f98-admission) |
| `busy`: the Session has a Run | yes | yes, after it | [F2.3](#f23-one-run-at-a-time) |
| `not_writer`: a message for a Run another instance writes | yes | yes, at the writer named | [F9.5.2](#f952-a-message-for-a-run-elsewhere) |
| `not_found`: the Session does not exist | yes | no | [F2.5](#f25-delete-a-session) |
| `session_closing`, `session_closed`: the live Session takes no more calls | yes | no | [F1.1](#f11-run-an-agent) |
| `already_exists`: the ID you chose is taken | not marked | no | [F2.1](#f21-create-a-session) |
| `invalid_argument`: a bad request, a `gotato.` key, an unknown Agent or task | yes | no | [F2.4](#f24-your-own-metadata), [F3.2](#f32-a-default-agent) |
| a task wider than its profile | yes | no | [F3.3.1](#f331-a-task-only-narrows) |
| `store_failure`: the store could not write or read | unknown for a Run's write; yes for a load or a read | yes | [F8.3](#f83-your-own-store) |
| `model_failure` the provider calls transient | no, it ran | yes | [F7.4](#f74-retries) |
| `context_overflow`: still too long after a compaction | no | no | [F7.5](#f75-a-request-refused-as-too-long) |
| `limit_exceeded` from the ceiling or a message over its size | no, it ran | no | [F5.4](#f54-the-ceiling), [F1.7](#f17-limits) |
| a compaction part fails or breaks a rule | the compaction is dropped, the Run goes on | next trigger | [F5.6](#f56-replace-a-part) |
| a compaction inside a Run cannot be written | the Run ends `store_failure` | yes | [F5.1](#f51-close-a-segment) |
| a Recap fails | the Segment is kept without one | next compaction | [F5.1.4](#f514-the-recap) |
| a tool fails or is called wrong | the Run goes on | the model may try again | [F6.3](#f63-mistakes-go-back-to-the-model) |
| a tool the model had not loaded | the call did not run | the model calls again | [F6.6](#f66-tool-search) |
| a tool set is down | the Run goes on without it | next Run | [F6.5.2](#f652-a-set-that-fails) |
| a stream is cut after output | the reply is asked for again | within the retries | [F7.6](#f76-recover-a-cut-stream) |
| the writer dies | the step in flight is lost | another instance continues | [F9.3](#f93-takeover) |
| a stream cannot resume (`cannot_resume`) | not run | start over at 0 | [F12.4](#f124-watch-a-session-and-resume) |

## Later, if needed

- **Telemetry adapters:** a package that maps an instance's Events onto OpenTelemetry, when teams stop writing their own.
- **A model catalog:** model names and prices beside the providers, if teams keep copying the same facts; a window would still be stated, never looked up.
- **Provider rate-limit information:** limits, remaining calls and reset time with the usage, when a service needs to pace itself.
- **Idempotency keys:** a repeated request ID not run twice, when a caller cannot avoid repeats.
- **Examples:** one-shot use, a persistent Session, compaction, forks and dynamic tools, once the API settles after 0.3.0.
- **A token budget per Run:** a stop at the end of a Turn does it today; a built-in one if many teams write the same.
- **Filtering the Session list:** by owner or metadata, when a service needs it beyond paging.
- **Loading only the recent Transcript:** a load already starts at the last Checkpoint; less, when a measurement shows the cost.
- **Prompt cache keepalive:** refresh a warm Session's cache before it expires, only if measurements show users returning inside the retention often enough to pay for each refresh.
- **Changes that keep the cache:** a changed instruction or a tool switched on mid-Session appended after the Transcript instead of rewritten into the prefix, when a service does this and the cache cost shows.
- **Stopping a group of Runs across instances:** abort every Run whose Session has some metadata, without becoming a group or a supervisor in the runtime.
- **Examples for common load balancers:** the hashing, retry and readiness rules written out for the balancers teams run.

## Not planned

Each belongs to the application, an MCP server, or the tools themselves. Look here before proposing a feature.

- **Agent organization (roles, teams, hierarchies):** Gotato provides peer agents, and applications compose roles.
- **Task graphs, scheduling and pipelines:** they live in the application.
- **Global resource scheduling and quotas:** Runs are cancelable and light, and the host only bounds Runs at once and keeps one per Session.
- **A product UI:** Gotato has no human-facing surface.
- **A required service:** the library stands on its own, and the core never depends on the host.
- **A memory architecture:** applications choose long-term memory.
- **A workflow language:** ordinary Go decides when to call an agent.
- **Storage engines:** Gotato defines the store contract and ships two stores, and your adapter owns the schema.
- **Tool catalogs:** file, Git and HTTP tools and skill formats come from MCP servers or the application.
- **Context organization beyond full history:** retrieval, windows and summaries are your own Context builder ([F4.5](#f45-your-own-context)).
- **Telemetry backends:** Events have a stable shape, and mapping them is a subscriber in the application.
- **Tool approval:** tools are allowed by name, the cap hides what is above it, a risky tool refuses its own arguments, and an extension can add a check ([F11.3](#f113-before-a-tool)).
- **A cloud coding agent:** coding stays on developers' machines ([PHILOSOPHY.md, question 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)).
- **A remote dev environment, shell or sandbox:** work that needs a machine runs on a laptop, in CI or in a coding product, and a large result is read in pages.
- **A general-purpose assistant:** Gotato is for purpose-built agents ([PHILOSOPHY.md, question 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).
- **A separate platform:** a platform beside your services is a second system to run.
- **Infrastructure:** discovery, routing, autoscaling and deployment already exist where you run.
- **Ready-made clients:** bots, schedulers and front ends are the callers' code.
- **Structured output:** give the Agent a tool whose arguments are the shape; its arguments are checked, and a wrong shape goes back to the model to correct.
- **MCP code mode:** each step would be a program that cannot be replayed, and a tool that runs code is a bound nobody can read.
- **MCP resources:** a Session reads data through tools the team chooses, and a resource has no Effect and no check.
- **MCP prompts:** the instruction is the Agent's definition, where a reviewer reads it.
- **MCP sampling:** a Run's cost and bounds would depend on a server the team may not control.
- **MCP elicitation:** a person's answer needs a waiting state the Loop does not have.
- **Gotato as an MCP server:** callers reach Sessions through the HTTP API and the Go API.
- **MCP OAuth:** every service already authenticates its callers and holds its credentials.
- **Access control:** who the caller is and who may touch which Session are the application's; a store wrapper that answers not found for a Session the caller does not own guards every call, since each reads through the store.
- **A last Turn that answers before the deadline:** the caller sends a notice ahead of its own deadline; Gotato does not guess the lead.
- **Withdrawing a waiting message:** the caller sends the next message or a Stop.
- **Notices for a settings change or an interrupted Run:** the Record already says the Run was interrupted, and the instruction is built from the current Agent on every request.
- **A native form for a notice:** tagged text in a user turn works on every wire API with no list of models, and a team that wants another form writes its own model client.
- **A load signal in the liveness row:** routing is the load balancer's, and an instance at its limit refuses as not run.
- **A gRPC adapter:** HTTP is the one wire transport, and a service that wants gRPC maps the host itself.
- **A shipped CLI:** it would be a third contract to version; the test CLI promises nothing.
