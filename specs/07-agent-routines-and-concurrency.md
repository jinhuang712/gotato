# 07. Agent Routines, Concurrency, and Cancellation

**Status:** Draft

> **Agents are self-contained goroutines; Orchestration coordinates their work.**

## 1. Agent Routine

An Agent Routine is the running form of one Agent:

```text
Agent Routine
  = Agent identity
  + private Agent state
  + one Agent goroutine
  + command channel
  + result / Event channels
```

The Agent execution unit is the only mutation authority for that Agent's private state. It runs one canonical Loop and processes one Prompt or Continue at a time.

A Routine is not a wrapper around a child Agent Run. A goroutine is not fire-and-forget: the Routine has an identity, explicit commands, limits, cancellation, Events, and a settled result.

Run settlement does not close the Routine. Its lifecycle is:

```text
Created → Idle ⇄ Busy → Closing → Closed
```

A Routine entering `Closing` accepts no new Prompt or Continue. A graceful close settles the current Run; explicit cancellation may be selected before close. A direct owner or Orchestration must close a Routine explicitly—Go garbage collection is not a lifecycle mechanism. `agent_end` is terminal for a Run, not for the Routine.

### 1.1 Retirement

An ephemeral Routine may request retirement after its current Run, but the owner decides whether to honor it. Orchestration may close it after a completed child task, after an idle TTL, or during capacity eviction. A persistent child may instead retain a Conversation and become dormant. Parent/child correlation does not by itself authorize closing or canceling another Routine.

## 2. Single-flight execution

```text
Idle ── one Prompt/Continue ──► Busy
Idle ◄──── terminal result ───── Busy
```

A second execution command is not processed concurrently by the same Agent goroutine. Core may report `busy` or not-available to a direct caller; `Closing` and `Closed` are lifecycle rejections, not busy results. Application Orchestration may instead queue or reject the external request before sending it to the Agent.

This is an execution property, not ownership of a Conversation, Host, Orchestration, or shared resource.

## 3. Spawn

An Agent goroutine may create another Agent Routine directly or request one through a factory/Orchestration:

```text
Agent A goroutine ── go / Spawn ──► Agent B goroutine
        │
        └── optional factory / Orchestration channel ──► Agent B
```

B has independent state, channels, limits, and lifecycle. Spawn provenance may be represented by `SpawnID`, origin Agent ID, and origin Run ID. These are correlation fields only; they do not create a resource hierarchy.

No Agent directly mutates B. Communication uses channel-backed commands, results, and Events.

## 4. Run

A Run is one Prompt or Continue accepted by an Agent Routine. It has a Run ID, Context, Event sequence, and settled Result. A Run does not own another Agent Routine.

The routine becomes available for another execution only after its current Run reaches terminal settlement. Request queuing between Runs belongs to application Orchestration.

## 5. Communication and control

```text
caller / Host ── command channel ──► Agent A
Agent A        ── result channel  ──► caller / Host
Agent A        ── Event channel   ──► observer / Host
Agent A        ── Spawn channel   ──► factory / Orchestration
Agent A        ── command channel ──► Agent B
```

The protocol may be implemented with Go channels behind stable handles. The Agent does not inspect user connections or decide external scheduling policy.

## 6. Cancellation

Cancellation is an explicit Context signal or channel command:

```text
caller / Orchestration
        │ Cancel
        ▼
Agent Routine
        ├── current Model
        ├── current Tools
        └── local owned work
```

Creating B does not automatically place B in A's cancellation tree. Application Orchestration may choose cascading cancellation and send B an explicit signal. Spawn provenance alone does not imply lifetime inheritance.

## 7. Core limits

Core limits apply to one Agent Routine and its current Run:

```text
Turns and Tool Calls
retained Messages and transcript bytes
parallel Tool workers
active ToolSets
Tool result and progress volume
Run, Model, and Tool deadlines
```

Core does not decide how many external Prompts wait for that routine.

## 8. Orchestration limits

Orchestration limits apply to the channel network:

```text
Agent goroutines
queued Prompts
active Runs
Spawn commands
retirement and close operations
Event delivery
```

Application Orchestration may implement FIFO, priority, reject-while-busy, safe-boundary Steer, immediate Abort, or a policy that creates another Agent routine. These are application/Host coordination decisions. Orchestration is unnecessary for one directly held Routine, but multiple Routines that must be revisited or coordinated require these responsibilities somewhere in the application or Host.

## 9. Groups

A group is a coordinator over independent Agent routines:

```text
coordinator goroutine
  ├── Agent Routine A
  ├── Agent Routine B
  └── Agent Routine C
```

Collect-all, fail-fast, collect-partial, and first-success are coordination policies. They do not establish resource ownership or automatic cancellation inheritance. A group waits through result channels and applies its own policy to completed results.

## 10. Events and correlation

Each Agent Routine and Run has its own identity and Event sequence. A spawn request may carry:

```text
origin Agent ID
origin Run ID
Spawn ID
created Agent ID
created Run ID
```

These fields describe provenance. They do not merge transcripts or create a parent Event history. `agent_end` remains terminal for the specific Run that produced it.

## 11. Remote placement

The initial implementation uses local goroutines and channels. A future Host may connect routines across a process boundary, but the remote protocol must preserve command identity, result correlation, cancellation, bounds, close acknowledgement, and Event meaning.

Remote placement does not change the Agent Routine model and must not turn one Agent into an owner of another. A remote close is complete only when the Core side acknowledges `Closed`; delivery of that acknowledgement may settle later.
