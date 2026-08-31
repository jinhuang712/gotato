# Agent Routines

**Status:** Draft

> **Agent Routines run as goroutines with private state and explicit channels.**

## 1. Meaning

In Gotato, an Agent runs as a goroutine; it is not a passive object that a Host executes on its behalf:

```text
Agent Routine
  = Agent identity
  + private state
  + one goroutine
  + inbound command channel
  + result / Event channels
```

The routine owns its local Agent state and executes the Model → Tool → Model Loop. The Orchestrator, Conversation registry, user request queue, and shared application resources remain outside the routine.

A goroutine is not a fire-and-forget task. The Agent Routine has a stable handle, explicit commands, bounded work, cancellation, Events, and a settled result.

## 2. One Prompt at a time

One Agent Routine processes one Prompt or Continue at a time:

```text
Free ── dispatch one Prompt ──► Busy
Free ◄──── result / agent_end ── Busy
```

A second Prompt is not executed concurrently by the same routine. Whether it waits, queues, is rejected, or causes a control action is decided by the caller or Orchestrator:

```text
incoming Prompts
       ↓
Orchestrator policy
  reject · FIFO · priority · Steer · Abort
       ↓
Agent Routine channel
```

The routine reports whether it is available; it does not design the external request policy.

## 3. Spawn

An Agent Routine may create another Agent Routine directly or request one through a coordinator:

```text
Agent A goroutine ── go / spawn ──► Agent B goroutine
        │
        └── optional coordinator channel ──► Agent B
```

B is an independent Agent with its own state and channel endpoints. A may receive a handle or correlation ID for B, but A does not own B's state, resources, or lifetime. Spawn origin may be recorded for correlation; it is not a scheduling hierarchy.

Spawn can also be initiated by an application or Host. All paths create the same kind of Agent Routine.

## 4. Communication

Routines communicate with explicit messages:

```text
Agent A ── Command / Result / Event channel ── Agent B
Agent A ── command channel ──► Orchestrator
Agent B ── result channel ────► caller
```

No Routine directly edits another Agent's state. Shared mutable state requires an explicit application capability; it is not created by spawning.

## 5. Run and Routine

A `Run` is one Prompt or Continue handled by an Agent Routine. It is a work identity and result channel correlation, not a container that owns another Agent.

The Agent Routine is the long-lived goroutine-backed execution unit. A Run begins when the routine accepts a command and settles when it emits its terminal Event and releases the current execution slot.

## 6. Cancellation

Cancellation is explicit:

```text
caller / Orchestrator
        │ cancel command or Context signal
        ▼
Agent Routine
        │
        ├── current Model
        ├── current Tools
        └── local owned work
```

Creating B does not automatically make B part of A's cancellation tree. An application or Orchestrator may send B a cancellation command or retain a Context relationship as a policy. The Core does not infer ownership from spawn provenance.

## 7. Bounds

Core bounds apply to one Agent Routine and its current Run:

```text
Turns and Tool Calls
parallel Tool workers
active ToolSets
Tool result and progress volume
Run, Model, and Tool deadlines
```

Orchestration bounds apply to the channel network:

```text
Agent goroutines
queued Prompts
active Runs
spawn requests
Event delivery
```

A rejected orchestration request does not create a new Agent Routine. A rejected Core command does not create a Run.

## 8. Routine groups

A group is an orchestration or application coordination pattern over independent Agent Routines:

```text
coordinator
  ├── Agent Routine A
  ├── Agent Routine B
  └── Agent Routine C
```

Supported policies may include collect-all, fail-fast, collect-partial, and first-success. These policies decide how the coordinator waits and reacts; they do not create ownership between the routines.

## 9. Events and correlation

Each Agent Routine and Run has its own identity and Event sequence. A spawn request may carry:

```text
origin Agent ID
origin Run ID
spawn request ID
created Agent ID
created Run ID
```

These fields describe correlation and provenance. They do not merge transcripts or create a parent Event history. `agent_end` remains terminal for the specific Run that produced it.

## 10. Orchestration and remote placement

The initial implementation uses local goroutines and channels. A future Host may connect routines across a process boundary, but the remote channel must preserve command identity, result correlation, cancellation, bounds, and Event meaning.

Remote placement is a transport/orchestration concern. It must not turn an Agent Routine into a service-owned child object or introduce a second Agent Loop.
