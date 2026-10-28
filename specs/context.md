# Context

The model never reads the Transcript directly. On every Turn Gotato builds a Context, the model's view of the Session, and sends that. The aim is a request that only grows at its end, so the provider's prompt cache keeps hitting, and that the model can always reach what was left out, through a tool.

## TL;DR
- A `ContextBuilder` turns the Transcript into the Context on every Turn; the default shows the whole committed history, and nothing is committed back.
- The request is laid out static first: system, tools, then Messages. Cache breakpoints sit at the end of each of the three, and a Notice or a new Message is only ever appended.
- Big Tool Results and old Segments leave the Context but not the Record: the model reads them back with `read_result` and `read_segment`.

## One Turn builds one request

The Loop builds the request from the Transcript just before it calls the model, and may build it a second time.

```text
 1 commit queued Notices ─▶ Transcript
 2 read the Messages       ◀ Transcript
 3 ContextBuilder.Build         the Messages the model sees now
 4 Extensions                   may reshape those Messages
 5 AssembleRequest              system, tools, Messages, cache breakpoints
 6 TurnPreparer.PrepareTurn     may rewrite the Transcript
        │ changed ─────────────▶ back to step 2
        ▼ not changed
 7 context_built Event, then the request goes to the Model
```

Each Turn starts from the Transcript, so the Context is a fresh value every time and never a thing that is edited. The builder picks the Messages, the Extensions may reshape them, and neither result is written back. Step 6 is the one safe moment to rewrite the Transcript, which is how a [compaction](compaction.md) happens. If it did rewrite, steps 2 to 5 run again. If the provider refuses the request as too long, step 6 runs once more with that refusal.

## The request, top to bottom

The request is a stack with the most stable content on top and the newest on the bottom.

```text
┌────────────────────────────────────┐ ─ breakpoint 1: after the system part
│ instruction + context blocks       │
│ (+ one fixed sentence about tags)  │
├────────────────────────────────────┤ ─ breakpoint 2: after the tools
│ tools visible to the Agent,        │
│ sorted by ID                       │
├────────────────────────────────────┤
│ Segments (if any)                  │
│ Messages, oldest to newest         │
│ ...                                │
│ last Message                       │ ─ breakpoint 3: on the last Message
└────────────────────────────────────┘
   prefix hash = everything above, except the last Message
```

The provider's cache matches on a prefix, so what changes least goes first. Two Turns with the same prefix hash share a cacheable prefix, and the hash is in every `context_built` Event and in an inspection. The tags sentence appears only once a Message holds text that Gotato wrote, so a Session without Notices or Segments sends no such sentence. Nothing that changes each Turn is added, not even the time: a fact the model needs now comes from a tool.

## What a Message looks like on the wire

Before it is sent, each Message is cut down to what a provider needs, and its text is made safe.

```text
 Transcript Message                     what the model gets
 ┌────────────────────────┐            ┌────────────────────────┐
 │ id, usage, stop reason │  ───────▶  │ role, parts,           │  runtime fields dropped
 │ role, parts, calls     │            │ calls, results         │
 ├────────────────────────┤            ├────────────────────────┤
 │ text with "<notice>"   │  ───────▶  │ text with "&lt;notice>"│  look-alike tags escaped
 ├────────────────────────┤            ├────────────────────────┤
 │ a reply cut off        │  ───────▶  │ (left out)             │
 │ mid-stream             │            │                        │
 └────────────────────────┘            └────────────────────────┘
```

Runtime fields such as IDs and usage would differ between Instances, so they never reach the prompt bytes. Four tags belong to Gotato: `notice`, `segment`, `tool_calls` and `recap`. If a user, a tool or the model itself writes one of them, it is escaped, so nobody can speak as the system. Only Gotato's own tagged text passes as it is. A reply cut off mid-stream is kept in the Record but not shown to the model, which sees what it saw before that reply began.

## A Notice joins at the end

A Notice is a Message of its own kind. It waits in a queue, and goes in right before the next model call.

```text
 Round 1:  user ▶ assistant ▶ tool result │ queue: [Notice] ◀ service calls Notice(...)
                                          │
 next Turn:  ... tool result ▶ <notice kind="time_away">...</notice> ▶ model call
                               └─ appended at the end, never in the middle
```

The queue is shared with Steers, so Notices and Steers go in the order they arrived. A Notice is stored once, as a Message, so every Instance that builds the Context writes the same bytes. It never starts a Round, which keeps the model's reasoning in flight, and it never starts or extends a Run. Gotato sends two of its own, `continue` after a reply hit the output limit and `time_away` after a long pause; a service names its own kinds.

## A big result stays out

A Tool Result over one page is written to the Record first, and the Context keeps only its start.

```text
 Loop                          Record                  Context              Model
  │ result > one page            │                        │                    │
  │ 1 tool_result_spilled ─────▶ │ full body, Entry N     │                    │
  │ 2 Message: preview + ref ──▶ │ (records N)  ─────────▶│ preview + ref ───▶ │
  │                              │                        │ ◀ read_result(ref, page 2)
  │ 3 read the body at N ◀────── │                        │   one page back ──▶ │
```

The body goes in before the Message that points to it, so a crash between the two leaves a log Entry nobody points at and nothing worse. The Message holds a preview and the ref `result:<call_id>`, and it is written once, so the bytes already sent stay the same. A load does not read the body; `read_result` does, one page at a time, and it reads only results of its own Session. If the team has pruned the body, the tool says only the preview is left. `read_segment` is its sibling for Segments.

## What this buys you
- The cache keeps hitting: the top of the request is the same from Turn to Turn, and only the end grows.
- Any Instance builds the same request from the Record, and an inspection shows the bytes a Turn would send.
- A large result or an old stretch of history costs a few tokens, and the whole of it is one tool call away.

## See also
[FEATURES.md → G-F04](../FEATURES.md#g-f04--context-), [G-F05](../FEATURES.md#g-f05--context-inspection-), [G-F29](../FEATURES.md#g-f29--runtime-notices-), [G-F35](../FEATURES.md#g-f35--large-tool-results-), decisions [017](../plan/decisions/017-a-notice-is-a-message-of-its-own-kind.md), [019](../plan/decisions/019-a-segment-is-written-once.md) and [025](../plan/decisions/025-a-large-tool-result-is-spilled-and-read-in-pages.md), [GLOSSARY.md](../GLOSSARY.md).
