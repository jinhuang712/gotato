# Agent Routines and Concurrency

**Status:** Draft

> This document describes the advanced concurrency model behind the simple Agent interface.

## 1. Why this is an advanced concept

A basic caller sees an Agent interface. Internally, Core gives each Agent a dedicated execution unit so its state transitions are easy to reason about:

```text
Agent handle
  └── Agent Routine
        ├── private state
        ├── one goroutine
        ├── command boundary
        └── result / Event boundary
```

The Routine is an implementation and coordination concept. Callers do not create goroutines or manage channel topology merely to call an Agent.

## 2. Single-flight execution

One Agent Routine processes one Prompt or Continue at a time:

```text
Free ── dispatch one Prompt ──► Busy
Free ◄──── result / settlement ─ Busy
```

A second Prompt is not executed concurrently by the same Routine. Whether it waits, queues, is rejected, or becomes a control action is decided by the caller or Host.

This protects Agent state without making Core a general request scheduler.

## 3. Spawn

An Agent may create another independent Agent Routine directly or request one through a Host/application coordinator:

```text
Agent A ── spawn request ──► Agent B
```

B has independent state, channels, limits, and lifecycle. Spawn provenance may be represented by IDs for correlation; it does not create resource ownership or automatic lifetime inheritance.

No Agent directly mutates another Agent. Communication uses the Agent contract or explicit channel-backed coordination.

## 4. Run and Routine

A Run is one Prompt or Continue accepted by a Routine. It has an identity, Context, Event sequence, and settled result. A Run is not a container that owns another Agent Routine.

The Routine remains available for another execution only after its current Run settles. Request queuing between Runs belongs to the caller or Host.

## 5. Communication and control

```text
caller / Host ── command ──► Agent A
Agent A        ── result ───► caller / Host
Agent A        ── Event ────► observer / Host
Agent A        ── spawn ────► application / Host
Agent A        ── command ──► Agent B
```

These relationships may use Go interfaces, private channels, or a protocol adapter. The semantic contract stays independent of the connection mechanism.

## 6. Cancellation

Cancellation is an explicit Context signal or command:

```text
caller / Host
     │ Cancel
     ▼
Agent Routine
     ├── current Model
     ├── current Tools
     └── local owned work
```

Creating B does not automatically place B in A's cancellation tree. An application or Host may choose cascading cancellation and send B an explicit signal.

## 7. Core and Host bounds

Core bounds one Agent's local work:

```text
Turns and Tool Calls
parallel Tool workers
active ToolSets
Tool result and progress volume
Run, Model, and Tool deadlines
```

Host bounds the surrounding coordination:

```text
Agent instances
queued Prompts
active Runs
spawn requests
Event delivery
```

A rejected request does not create a Run or Routine. A Core command rejection does not create new work.

## 8. Routine groups

A group is a coordination pattern over independent Routines:

```text
coordinator
  ├── Agent Routine A
  ├── Agent Routine B
  └── Agent Routine C
```

Collect-all, fail-fast, collect-partial, and first-success are Host/application policies. They do not establish resource ownership or automatic cancellation inheritance.

## 9. Events and correlation

Each Routine and Run has its own identity and Event sequence. A spawn request may carry:

```text
origin Agent ID
origin Run ID
Spawn ID
created Agent ID
created Run ID
```

These fields describe provenance. They do not merge transcripts or create a parent Event history. `agent_end` remains terminal for the specific Run that produced it.

## 10. Remote placement

The initial implementation may keep Routines in one process. A Hosted composition may later connect independent Routines across a process boundary while preserving command identity, result correlation, cancellation, bounds, and Event meaning.

Remote placement is a Host/protocol concern. It must not turn an Agent into an owned child resource or introduce a second Agent Loop.
