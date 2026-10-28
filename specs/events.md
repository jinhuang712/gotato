# Events

A Run does many things, and a caller wants to watch them: a UI that shows the reply, a bot that waits for the answer, a service that measures itself. Gotato reports every Step as an Event, one Go type for each kind. A stored Event is also an Entry of the Record, so a client that leaves and comes back can read exactly what it missed. This spec shows how Events are sealed, written, numbered, delivered and resumed.

## TL;DR
- Every Event embeds a Header, and lives in one of three places: stored in the Record, sent only (a delta), or on the Instance.
- A stored Event is written before it is sent, and its number is its Entry's position, so every Instance reads the same number.
- A Session never waits for a subscriber. A subscriber that falls behind catches up from the Record by number, and a client resumes the same way after a lost connection.

## One stream, sealed by a Header

An Event is any type that embeds a Header, and its kind decides where it lives.

```text
 gotato.Event      sealed: a type is an Event only by embedding Header
 Header            time   session_id   number   run_id   turn

 stored    run_started  reply_ended  tool_started ...   number = its Entry's position   ─▶ the Record
 delta     text_delta  reasoning_delta  tool_progress    number = the stored Event it follows ─▶ sent, never stored
 instance  run_rejected  session_fenced  session_saved   no number                       ─▶ this Instance's subscribers
```

Sealing means a caller switches on the type and reads checked fields, and no outside type can pose as an Event. In JSON an Event is one flat object whose `kind` names its type, and a kind a later version adds comes back as an `UnknownEvent`, so an old consumer keeps running. A delta is not stored because what it carried ends up whole in a Message. An Instance Event happens where no Claim is held, so no Record can take it.

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

The number counts the whole Record, not one Run, so two Instances give the same Event the same number. Events are sent in the order of their numbers. A delta's number only says where it sits, so a client resumes from the last `id`, and the deltas before it are not sent again. A number means the same Event only within one Record epoch, so the number goes with the epoch ([record.md](record.md)). A Run that is not saved has no Record, so its Events are numbered from 1 on its own stream, and those numbers are not positions.

## Feed and Subscription

A Feed fans each Event out to Subscriptions, and publishing never blocks.

```text
 Session ── Publish ──▶ Session's Feed ──▶ Run's stream        one Run, first to last Event
                            │          └──▶ Session's stream    across Runs, from any number
                            └── mirror ──▶ Instance's Feed ──▶ Instance's stream
 Instance Events ───────────────────────────▶ Instance's Feed    every Session, plus these
                      each Subscription has its own queue and one goroutine, in order
```

The Session puts the Event in each queue and moves on, so one slow reader slows only itself. A Run's stream and a Session's stream carry the Session's numbers. An Instance's stream mixes many Sessions, so it has no single number and cannot be read again. When its subscriber falls behind it skips Events and says how many in an `events_skipped` Event. Each stored Event names its Session and number, so the subscriber can read what it missed from the Record. Instance Events are not in a Run's stream.

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

A full queue drops its oldest delta first. With none to drop, the subscriber leaves live mode and reads by number from the EventSource, which is the Record, then rejoins when it has read as far as the Feed has published. The same path covers a gap: a number that jumps means another Instance wrote the Session, and the missing Events are read. Watch for the last arrow. A store that is slow to show its last write is given a moment, and then the subscriber ends. A Session over a Record that is not stored can read back only a recent window of Events.

## Resume with Last-Event-ID

A client reconnects to any Instance with the last number and epoch it saw, and gets everything after, with no gap and no repeat.

```text
 Client                          Instance B                      Store
   │ GET /v1/sessions/s1/stream
   │ Last-Event-ID: 40, Record-Epoch: E1
   │ ───────────────────────────▶ │ Load ───────────────────────▶ │
   │                              │ ◀──── epoch E1, head 58 ───── │  E1 is E1, and 40 <= 58
   │ ◀── event: stream {E1, after 40, head 58}
   │ ◀── id: 41 ... id: 58        │ Log(after 40) ──────────────▶ │  from the Record
   │ ◀── id: 59, deltas ...       │ live; or polls the Store when the writer is elsewhere
   │ ◀── event: reconnect         │ this Instance is draining: come back, after a random wait
   │ epoch differs, or 40 > head: 409 cannot_resume, with the current epoch and head
```

The `after` parameter wins over the header, which a browser's EventSource sends by itself. The Instance checks the epoch and the position before it streams, then reads the Record first and goes live. It does not need to be the writer: an idle stream looks in the Store at intervals, which is the cost of binding no Session to an Instance. The stream follows one epoch, and ends with an error if the Record is deleted or created again. A client that sends no epoch is checked by position only, so it should send one.

## What this buys you
- A client can drop its connection, or lose its Instance, and resume on any other one without gaps.
- A slow client slows its own stream and never the Run.
- One name and one number for each thing: the log, the stream and the telemetry say the same.

## See also
[FEATURES.md → G-F13](../FEATURES.md#g-f13--events-) and [G-F14](../FEATURES.md#g-f14--http-and-streaming-), decisions [015](../plan/decisions/015-every-entry-is-an-event.md) and [014](../plan/decisions/014-every-step-is-written-as-facts-and-a-log.md), [GLOSSARY.md](../GLOSSARY.md). Related specs: [session.md](session.md), [record.md](record.md) and [multi-instance.md](multi-instance.md).
