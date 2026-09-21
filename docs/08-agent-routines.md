# Agent Routines and Concurrency

**Status:** Draft

> This document describes the Agent execution units that Core provides and the Orchestration required to coordinate more than one.

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

The Routine is Core's execution unit, not a resource the caller has to assemble. Callers do not create goroutines or manage channel topology merely to call one Agent. When multiple Routines must be revisited or coordinated, application code or Gotato Orchestration owns the handles and coordination channels.

Run settlement does not close a Routine. A retained Routine remains available until its owner closes it. A short-lived Routine may be closed after its task settles; there is no retirement or Conversation to preserve. Go garbage collection is not an Agent lifecycle mechanism.

## 2. Single-flight execution

One Agent Routine processes one Prompt or Continue at a time:

```text
Idle ── dispatch one Prompt ──► Busy
Idle ◄──── result / settlement ─ Busy
```

A second Prompt is not executed concurrently by the same Routine. While `Closing` or `Closed`, it is rejected rather than classified as ordinary busy work. Whether an Idle Agent request waits, queues, is rejected, or becomes a control action is decided by application Orchestration or Host policy.

This protects Agent state without making Core a general request scheduler.

## 3. Spawn

An Agent may create another independent Agent Routine directly or request one through an application Orchestration coordinator:

```text
Agent A ── spawn request ──► Agent B
```

B has independent state, channels, limits, and lifecycle. The runtime has no Spawn type; a service or application that wants to correlate B with its origin records that lineage itself in `Session.Metadata` (see §9). That correlation does not create resource ownership or automatic lifetime inheritance, and there is no intrinsic child-retention policy: any per-task or long-lived handling of B is application policy. A derived line of work is `session.Fork` plus another Run.

No Agent directly mutates another Agent. Communication uses the Agent contract or explicit channel-backed coordination.

## 4. Run and Routine

A Run is one Prompt or Continue accepted by a Routine. It has an identity, Context, Event sequence, and settled result. A Run is not a container that owns another Agent Routine.

The Routine remains available for another execution only after its current Run settles. Request queuing between Runs belongs to application Orchestration or Host.

## 5. Communication and control

```text
caller / Orchestration ── command ──► Agent A
Agent A        ── result ───► caller / Orchestration
Agent A        ── Event ────► observer / Orchestration
Agent A        ── spawn ────► application / Orchestration
Agent A        ── command ──► Agent B
```

These relationships may use Go interfaces, private channels, or a protocol adapter. The semantic contract stays independent of the connection mechanism.

## 6. Cancellation

Cancellation is an explicit Context signal or command:

```text
caller / Orchestration / Host
     │ Cancel
     ▼
Agent Routine
     ├── current Model
     ├── current Tools
     └── local owned work
```

Creating B does not automatically place B in A's cancellation tree. Application Orchestration or Host may choose cascading cancellation and send B an explicit signal.

## 7. Core and Host bounds

Core bounds one Agent's local work:

```text
Turns and Tool Calls
parallel Tool workers
active ToolSets
Tool result and progress volume
Run, Model, and Tool deadlines
```

Application Orchestration / Host bounds the surrounding coordination:

```text
Agent instances
queued Prompts
active Runs
derived-work requests
close operations
Event delivery
```

A rejected request does not create a Run or Routine. A Core command rejection does not create new work.

## 8. Routine groups

A group is a coordination pattern over independent Routines. Therefore, multiple Routines require a coordinator somewhere, even if it is only application code holding fixed Agent handles; the Gotato Orchestration components provide the reusable form, with Host exposing it for remote coordination:

```text
coordinator
  ├── Agent Routine A
  ├── Agent Routine B
  └── Agent Routine C
```

Collect-all, fail-fast, collect-partial, and first-success are application Orchestration/Host policies. They do not establish resource ownership or automatic cancellation inheritance.

## 9. Events and correlation

> **Superseded (see [PROPOSAL.md §5a](../PROPOSAL.md) and [MIGRATION.md](../MIGRATION.md)):** `Spawn ID`, origin Run IDs, and intrinsic parent/child retention are not part of the current runtime. Gotato has only agents (PHILOSOPHY G-P03; DESIGN G-D18); there is no Spawn type and no `Event.SpawnID` or `Event.OriginRunID`. The content below is historical.

Each Routine and Run has its own identity and Event sequence. An application that derives one line of work from another records that lineage itself, in `Session.Metadata` (with the parent Session ID from `session.Fork`):

```text
application-defined metadata keys, e.g. origin Session ID / origin Run ID
```

That lineage is application data. It does not merge transcripts or create a parent Event history. `agent_end` remains terminal for the specific Run that produced it.

## 10. Remote placement

The initial implementation may keep Routines in one process. Orchestration may later connect independent Routines across a process boundary while preserving command identity, result correlation, cancellation, bounds, close acknowledgement, and Event meaning.

Remote placement is an Orchestration/Host protocol concern. It must not turn an Agent into an owned child resource or introduce a second Agent Loop. A remote close is complete only after the Core side acknowledges `Closed`; delivery of that acknowledgement may settle later.
