# Session

A conversation with an agent has to survive a crash, a deploy and a week of silence, and it has to accept a new message while a Run is still going. Gotato splits this into parts with one job each. An Agent says how to run. A Session is the conversation that runs it. A Run is one request to that Session, made of Turns, and everything it does is written to the Session's Record as it happens.

## TL;DR
- An Agent is a definition and holds nothing; a Session is one conversation, and its Record in the Store is the truth. The live Session is a goroutine built over that Record for each Run.
- A Run is made of Turns, and a Turn of Steps that are each written before the next one starts. Every Run ends exactly once, in one of five ways.
- Steer, FollowUp and Interrupt reach a Run in flight, each at its own point. What is only queued in memory is lost if the Instance dies; what is written is not.

## Who holds what

The Agent is on top and holds nothing; the Record at the bottom holds everything that must last.

```text
 Agent            model, instruction, tools, Effect cap, limits   (a definition, shared)
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

Read it top to bottom as "what is cheap to lose". The Agent is shared by many Sessions, so it can hold no conversation. The live Session is built when a Run needs it and can be dropped at any time, because the Record holds everything it needs. Its ID is the Record's ID, so whichever Instance builds it, it is the same Session. Keeping the Agent separate from the Session is what lets one definition serve many conversations.

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

The model call is written before it is made, so a Writer that has lost its Claim stops at step 2, before it spends a model call. Tool calls are written as started before they run, so a crash leaves calls that are known to have started. Results commit in the order the model asked for them, even when the Tools ran in parallel. A crash loses at most the Step in flight: the next Instance finds the Run started and never ended, and ends it as `failed` with the cause `interrupted`.

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

A Steer lands after the Tool Results of the Turn that is running, or at settlement, where it keeps the Run going for one more Turn. An Interrupt cuts a model call short; a Tool in flight finishes first, and then it lands like a Steer. A FollowUp is used only where the Run would end, so it never changes a Run that is making progress. The queues are bounded, and each message is written as queued before it can be committed, so the Record never shows a commit with no queueing. In the core, a message that reaches a Run already writing its end is refused with `invalid_state`; a Runner keeps it for the following Run instead.

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
[FEATURES.md → G-F02](../FEATURES.md#g-f02--sessions-), [G-F01](../FEATURES.md#g-f01--agents-and-runs-) and [G-F26](../FEATURES.md#g-f26--steering-a-run-), decisions [004](../plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [016](../plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [021](../plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md), [028](../plan/decisions/028-every-step-is-written-as-facts-and-a-log.md) and [038](../plan/decisions/038-a-limit-guards-a-real-failure.md), [GLOSSARY.md](../GLOSSARY.md). The Record, the Events and the Context have specs of their own: [record.md](record.md), [events.md](events.md) and [context.md](context.md).
