# 018. Events are typed values, written before they are sent, and no subscriber slows a Session

Decided: 2026-10-16    Status: current

## The problem
Callers watch Runs: a UI, a bot, a service's telemetry. Clients leave and come back, instances go away and subscribers are slow. An Event's number has to mean the same Event on every instance, and watching must never slow the Run.

## The choice
Each kind of Event is a type of its own, so a caller switches on the type and reads fields the compiler checks, and on the wire `kind` names it. An Event's name says what happened, in the glossary's words: a change of state is the thing and a past participle (`run_started`), a delta is the thing and `delta` (`text_delta`), and a Tool's own report is `tool_progress`. A stored Event is written before it is sent, so a client never holds an Event the Record lacks. Most Events ride the write of their step; the few between steps, such as a retry or a queued message, get a write of their own; deltas are sent at once, marked with the number of the Event they follow. A stream resumes from a position: a stored Event's number is its Entry's position, which every instance reads the same, and a client reconnects to any instance with the last number (and the Record's epoch) and gets every later Event from the Record, then the live ones. A Session never waits for a subscriber: it puts each Event in the subscribers' queues and moves on, and a slow subscriber catches up from the Record by number. Every number about a Run comes from Events and `Usage`.

## Not chosen
- One Event type with a map of fields: one type to learn, but nothing the compiler checks.
- Names that mix tenses, or words of their own: a name is read in code, logs and dashboards, so it should say what happened.
- Sending an Event first and writing it with the next step: Events arrive sooner, but a resumed stream could show one number with two meanings.
- A stream that must reconnect to the same instance, or start over: an instance can go away at any time.
- Observers that the Session's goroutine calls and waits for: one slow client or exporter would slow every step.
- Counters inside Gotato, or a metrics endpoint of its own: a team computes cache hits, tokens, failures and timings from its own telemetry and gets the same numbers.

## Consequences
An Event waits for the write of its step. A stream continues on any instance with no gaps or duplicates, a slow client slows only itself, and a number is only meaningful within one Record epoch, which the client sends. See [030](030-every-entry-is-an-event.md).
