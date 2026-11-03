# Context

The model never reads the Transcript directly. On every Turn Gotato builds a Context, the model's view of the Session, and sends that. The aim is a request that only grows at its end, so the provider's prompt cache keeps hitting, and that the model can always reach what was left out, through a tool.

The Context is built in the core, package `gotato`: the `ContextBuilder`, the layout of the request, the tags, the Notices and the spill belong to the Loop that sends it. What it is built from is the Transcript, which in a service is a Record, and package `record` supplies the Segments, the compaction that closes them and the read-back tools (`record.md`, `compaction.md`). This spec is a thread through those two layers of the [map](ARCHITECTURE.md).

## TL;DR
- A `ContextBuilder` turns the Transcript into the Context on every Turn; the default shows the whole committed history, and nothing is committed back.
- The request is laid out static first: system, tools, then Messages. A cache breakpoint sits at the end of each of the three, when it is there, and a Notice or a new Message is only ever appended.
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

Each Turn starts from the Transcript, so the Context is a fresh value every time and never a thing that is edited. The builder picks the Messages, the Extensions may reshape them, and neither result is written back. Step 6 is the one safe moment to rewrite the Transcript, which is how a compaction happens. If it did rewrite, steps 2 to 5 run again. If the provider refuses the request as too long, step 6 runs once more with that refusal.

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

The provider's cache matches on a prefix, so what changes least goes first. A breakpoint is placed only where its part exists: no system text means no first breakpoint, and no tools no second. Two Turns with the same prefix hash share a cacheable prefix, and the hash is in every `context_built` Event and in an inspection. A Session with a tool catalog adds the lists of loaded and unloaded tools to what the hash covers. Both also carry an estimate in tokens of each part of the request (`estimate_system_tokens`, `estimate_tool_defs_tokens`, `estimate_segments_tokens`, `estimate_history_tokens`, `estimate_tool_results_tokens`; bytes over 4, the same rule as `approx_tokens`), and `gotato context inspect` prints them with each one's share in percent. A Record written before they existed reads them as 0. The tags sentence appears only once a Message holds text that Gotato wrote, so a Session without Notices or Segments sends no such sentence. Nothing that changes each Turn is added, not even the time: a fact the model needs now comes from a tool. The one time the model is told is the `time_away` Notice, which is appended once and kept as a Message.

## The cache key and the markers

Two things on the request help the provider find its cache: a key that names the conversation, and markers that say where a cached prefix ends.

The key rides on every request, so a provider that shards its cache serves one conversation from one place. In a service the host sets it to a hash of the Session ID, `gotato-` and 32 hex digits, because an ID the caller chose may name a user or a tenant and the key leaves the service. A fork keeps its parent's key, so its first Turn reads the parent's cached prefix. A program without a host sets its own with `WithCacheKey`. Where the key goes on the wire, a body field or a header, is the model bridge's (`model.md`).

The markers are the three breakpoints of the stack above. On the Anthropic wire they become `cache_control` marks, and when the model takes back only the current Round's reasoning the bridge adds one more where the current Round starts: leaving the last Round's reasoning out changes the request at its first thinking block, and this mark keeps the prefix before it readable from the cache. The provider takes at most four marks, so the last four are kept. The other wires cache by prefix and the key, with no marks.

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

The queue is shared with Steers, so Notices and Steers go in the order they arrived. A Notice is stored once, as a Message, so every Instance that builds the Context writes the same bytes. It never starts a Round, which keeps the model's reasoning in flight, and it never starts or extends a Run. Gotato sends two of its own: `continue`, after a reply hit the output limit when `CoreLimits.MaxTruncatedContinuations` allows a continuation (it is off by default), and `time_away`, after a long pause (a host sets how long with `Config.TimeAwayAfter`, 30 minutes by default). A service names its own kinds.

On the wire a Notice is `<notice kind="…">text</notice>`, with the kind and the text escaped, in a user turn on every wire API: joined to the user Message it follows, or as a user message of its own when there is none to join, such as after an assistant reply. The Record keeps it as a Notice, so it starts no Round there either. A Notice from the service survives its writer: the host writes it to the Record as queued, and the next Run on any Instance queues every Notice the Record holds as queued and not committed, in order. Gotato's own Notices are not kept that way: a leftover is dropped with its Run, and `time_away` is worked out again.

`time_away` follows four rules, so every Instance writes the same one:

- It follows only a message that starts a Run, never a Continue.
- The idle time is read from the Record: from the end of the last Run to now, so a Session evicted or loaded elsewhere gets it the same.
- A Session that never finished a Run gets none.
- Its text says the time away in the largest whole unit that fits, minutes, hours or days, and the time now in RFC 3339, in the compaction's time zone (UTC by default).

## A big result stays out

A Tool Result over one page is written to the Record first, and the Context keeps only its start. A Session does this only when a `ReadBack` is installed, so every ref it writes has a reader.

```text
 Loop                          Record                  Context              Model
  │ result > one page            │                        │                    │
  │ 1 tool_result_spilled ─────▶ │ full body, Entry N     │                    │
  │ 2 Message: preview + ref ──▶ │ (records N)  ─────────▶│ preview + ref ───▶ │
  │                              │                        │ ◀ read_result(ref, page 2)
  │ 3 read the body at N ◀────── │                        │   one page back ──▶ │
```

The body goes in before the Message that points to it, so a crash between the two leaves a log Entry nobody points at and nothing worse. The Message holds a preview and the ref `result:<call_id>`, and it is written once, so the bytes already sent stay the same. A load does not read the body; `read_result` does, one page at a time, and it reads only results of its own Session. `read_segment` is its sibling for Segments.

Three numbers would break the preview if they disagreed: the line past which a result is spilled, the page the preview counts its pages by, and the page the read tools return. They are one number, the page size, and the `ReadBack` owns it (`gotato.WithReadBack`; `record.ReadBack{PageSize}`, 32 KiB by default). The preview is the first `CoreLimits.SpillPreviewBytes` of the body (8,000 by default, never above the page), so it is always the start of page 1. The core writes the line after it, `[Result cut: 412 KiB, 13 pages. Read it page by page with read_result(ref="result:call_9a2", page=1); ...]`, so the line is the same on every Instance. With no `ReadBack`, or over a Transcript that keeps no Entries, a result is not spilled and goes in whole. If the team has pruned the body, `read_result` says `result:<id> was removed from the Record, so only what the conversation keeps of it is left.`; it says the same of a message.

## Your own builder, and an inspection

The default `ContextBuilder` sends the whole committed history from the last Checkpoint: the Segments, then the Messages. A team that wants retrieval, a window or any other choice of what the model sees writes its own and sets it on the Agent. It receives the Transcript's Messages and returns the Messages to send; it writes nothing back, so it cannot lose history, and the next Turn builds again from the same Record.

An inspection builds the request the next Turn would send, through steps 1 to 5 above with the Agent's extensions and tools, and returns it without calling the model. It reports the prefix hash, the token estimate of each part and whether the next Run compacts first. It builds as a Run's first Turn does, so it cannot know a tool the model will load later in that Run, and it runs the extensions and tool sources, which must therefore have no side effects. During a Run it is refused as `busy`, since the Transcript is changing under it.

## What this buys you
- The cache keeps hitting: the top of the request is the same from Turn to Turn, and only the end grows.
- Any Instance builds the same request from the Record, and an inspection shows the bytes a Turn would send.
- A large result or an old stretch of history costs a few tokens, and the whole of it is one tool call away.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the principles [a request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end) and [the Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view), [FEATURES.md](../FEATURES.md) (F4: Context, F6.7: Large results), decisions [006](../plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md), [007](../plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md), [029](../plan/decisions/029-reasoning-goes-back-for-the-current-round.md), [031](../plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md), [034](../plan/decisions/034-a-segment-is-written-once.md), [040](../plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md) and [046](../plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md).
