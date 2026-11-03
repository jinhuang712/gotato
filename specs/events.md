# Events

A Run does many things, and a caller wants to watch them: a UI that shows the reply, a bot that waits for the answer, a service that measures itself. Gotato reports every Step as an Event, one Go type for each kind. A stored Event is also an Entry of the Record, so a client that leaves and comes back can read exactly what it missed. This spec shows how Events are sealed, written, numbered, delivered and resumed. It follows one thread through the layers of the [map](ARCHITECTURE.md): the core makes and sends Events, the Writer in `store` writes them, the host adds the Instance's own, and the HTTP adapter streams them.

## TL;DR
- Every Event embeds a Header, and lives in one of three places: stored in the Record, sent only (a delta), or on the Instance.
- A stored Event is written before it is sent, and its number is its Entry's position, so every Instance reads the same number.
- A Session never waits for a subscriber. A subscriber that falls behind catches up from the Record by number, and a client resumes the same way after a lost connection.

## One stream, sealed by a Header

An Event is any type that embeds a Header, and its kind decides where it lives.

```text
 gotato.Event      sealed: a type is an Event by embedding Header and stating its Kind and Class
 Header            time   session_id   number   run_id   turn

 stored    run_started  reply_ended  tool_started ...   number = its Entry's position   ─▶ the Record
 delta     text_delta  reasoning_delta  tool_progress    number = the stored Event it follows ─▶ sent, never stored
 instance  run_rejected  session_fenced  session_saved   no number                       ─▶ this Instance's subscribers
```

Sealing means a caller switches on the type and reads checked fields, and no outside type can pose as an Event. In JSON an Event is one flat object whose `kind` names its type, and a kind a later version adds comes back as an `UnknownEvent`, so an old consumer keeps running. A delta is not stored because what it carried ends up whole in a Message. An Instance Event happens where no Claim is held, so no Record can take it.

## An Event states its own class

An Event type says where it lives with its own `Class()`, beside `Kind()`, and no table of the core holds the answer ([decision 048: Parts with one job](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)). So the Events of the core and the Events of the service are the same kind of thing, and so is an Event of a host's own: it embeds `gotato.Header`, states `Kind()` and `Class()`, and needs no `MarshalJSON`.

```text
 gotato.MarshalEvent(e)      any Event ─▶ {"kind": e.Kind(), ...its fields}      one flat object, kind first
 gotato.UnmarshalEvent(raw)  a kind the core defines ─▶ its type
                             any other kind (the service's, a host's, a later version's) ─▶ UnknownEvent
 UnknownEvent                keeps the JSON; MarshalEvent writes the same bytes back; its class is empty
```

The core defines the Events a Session makes: every stored Event, the deltas, and the two Instance Events it publishes itself, `session_unloaded` (on Close, and the Runner publishes the same type when it evicts a warm Session) and `events_skipped`. The nine that only a Runner publishes (`run_requested`, `run_waiting`, `run_rejected`, `session_fenced`, `liveness_lapsed`, `session_deleted`, `session_loaded`, `session_saved`, `store_write_failed`) are types of `host` with the same kinds and fields, so a client of the HTTP API sees no difference. A Go program that reads the Instance stream switches on `host.RunRejected` and the others; `gotato.UnmarshalEvent` reads them as an `UnknownEvent`, because Gotato ships no Go client that needs more ([decision 026: No machine, client or catalog](../plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md)). `adapter/http` writes every Event with `MarshalEvent`.

## An Event's Run is stamped when it is numbered

The writer that numbers Events holds the span of the Run: it opens when `run_started` is written and closes when `run_ended` is. An Event that another goroutine writes (a queued message, a drop, an Abort) is stamped with the open Run and its Turn at that moment, so every Event with a `run_id` lies between that Run's `run_started` and `run_ended`. One that arrives outside a Run has no `run_id`. An Abort that comes before `run_started` is written right after it.

## What a Turn's Context is made of

`context_built` carries, next to its prefix hash and sizes, an estimate in tokens of each part of the request: `estimate_system_tokens`, `estimate_tool_defs_tokens`, `estimate_segments_tokens`, `estimate_history_tokens` and `estimate_tool_results_tokens` (the instruction, the tool definitions, the Segment and Recap Messages, the user and assistant Messages, and the Tool Results). They use the one rule of the code, bytes of the JSON encoding over 4, so they add up to the estimate of the request within the rounding of each part. They are an estimate, not what the provider counts, and they are always written, 0 included. A Record written before they existed reads them as 0.

## Written, then sent

A stored Event reaches a subscriber only after the Journal has written it.

```text
 Session            Journal (Writer)          Store        Feed ─▶ Subscribers
   │ stage the Step's Events
   │ Write(events) ───▶ │ number them 42, 43, 44
   │                    │ Append(after=41) ─▶ │
   │                    │ ◀────── ok ──────── │
   │ ◀─ numbered Events │
   │ Publish(42, 43, 44) ─────────────────────────────▶ queued
   │ text_delta ──────────────────────────────────────▶ queued at once, not written
   │            if Write fails: the Run fails, nothing of the Step is sent
```

A client never holds an Event the Record lacks, so a resumed stream never shows one number with two meanings. The cost is that an Event waits for the write of its step. Most ride that write. The few between steps, such as a retry, an Abort request or a queued message, get a write of their own because they are rare. A crash between the write and the send loses nothing: the Event is in the Record, and a reader finds it by its number. The Journal is the seam. A Session with no Journal, in library use, numbers its Events in memory and stores none.

## Order and numbering

A stored Event's number is its position in the Record, and a delta carries the number of the stored Event before it.

```text
 time ─▶
 stored    #20 reply_started ───────────────────────── #21 reply_ended    #22 tool_started
 deltas               text_delta(20)  text_delta(20)  text_delta(20)
 SSE id:       20                                          21                 22      (a delta has none)
```

The number counts the whole Record, not one Run, so two Instances give the same Event the same number. Events are sent in the order of their numbers. A delta's number only says where it sits, so a client resumes from the last `id`, and the deltas before it are not sent again. A number means the same Event only within one Record epoch, so the number goes with the epoch (`store.md`). A Run that is not saved has no Record, so its Events are numbered from 1 on its own stream, and those numbers are not positions.

## Feed and Subscription

A Feed fans each Event out to Subscriptions, and publishing never blocks.

```text
 Session ── Publish ──▶ Session's Feed ──▶ Run's stream        one Run, first to last Event
                            │          └──▶ Session's stream    across Runs, from any number
                            └── mirror ──▶ Instance's Feed ──▶ Instance's stream
 Instance Events ───────────────────────────▶ Instance's Feed    every saved Session, plus these
                      each Subscription has its own queue and one goroutine, in order
```

The calls that publish to a Feed, mirror one into another and label it belong to the host that runs the Session, and the core hands them to `host` and `testkit` through `internal/hostlink`, not through its API: a Feed's methods are `Subscribe` and `Close`. The Session puts the Event in each queue and moves on, so one slow reader slows only itself. A Run's stream and a Session's stream carry the Session's numbers. An Instance's stream mixes many Sessions, so it has no single number and cannot be read again. When its subscriber falls behind it skips Events and says how many in an `events_skipped` Event. Each stored Event names its Session and number, so the subscriber can read what it missed from the Record. Instance Events are not in a Run's stream.

## A subscriber that falls behind

Only deltas may be dropped; a stored Event is read again from the Record by number.

```text
          the queue is full and holds no delta to drop,
          or a number was skipped (an Event this Feed was not given)
 ┌──────┐ ──────────────────────────────────────▶ ┌─────────────────┐
 │ live │                                         │ catching up:    │ reads the EventSource
 └──────┘ ◀────────────────────────────────────── │ deltas skipped  │ in batches, in order
          read up to the last number published    └────────┬────────┘
                                                           │ the source cannot give them back
                                                           ▼
                                        ended: ErrEventsLost (code cannot_resume)
```

A full queue drops its oldest queued delta first, and a new delta is itself dropped when none is queued. A stored Event that finds no room sends the subscriber out of live mode: it reads by number from the EventSource, which is the Record, in batches of 128, then rejoins when it has read as far as the Feed has published. The same path covers a gap: a number that jumps means another Instance wrote the Session, and the missing Events are read. Watch for the last arrow. A store that is slow to show its last write is given a moment (100 tries, 5 milliseconds apart), and then the subscriber ends. A Session over a Record that is not stored can read back only a recent window of Events, the last 4,096.

## Resuming from a number

A client that lost its connection resumes on any instance with the last number and the epoch it saw, and gets everything after, with no gap and no repeat. The instance checks the epoch and the position first, then reads the stored Events after the number from the Record and goes live; it does not need to be the writer, and an idle stream looks in the store at intervals for what another instance wrote. A stream follows one epoch and ends if the Record is deleted or created again, so it is never handed another Record's Events. Deltas are not replayed: the stored reply holds the whole text, and a client puts it in place of its partial text. A spilled Tool Result's body is left out of every Event that is sent. How the HTTP API carries this, its parameters, frames and refusals, and why a Run lives only as long as the request that started it, are in `http.md`.

## What this buys you
- A client can drop its connection, or lose its Instance, and resume on any other one without gaps.
- A slow client slows its own stream and never the Run.
- One name and one number for each thing: the log, the stream and the telemetry say the same.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the guidelines [the store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth) and [a Session can be stopped and seen](../DESIGNS.md#9-a-session-can-be-stopped-and-seen), [FEATURES.md](../FEATURES.md) ([F10: Events](../FEATURES.md#f10-events)), decisions [018: Typed Events, written before sent](../plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md), [028: Facts and a log](../plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030: Every Entry is an Event](../plan/decisions/030-every-entry-is-an-event.md), [026: No machine, client or catalog](../plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) and [048: Parts with one job](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md).
