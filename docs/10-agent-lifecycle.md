# Agent Lifecycle and Retirement

**Status:** Draft

> A Run may finish without ending its Agent. Orchestration decides when a live Agent is retired; Conversation identity may outlive the Agent that currently serves it.

## 1. Four different lifecycles

Gotato separates:

```text
Run
  one Prompt or Continue; ends with agent_end

Agent
  one live Core execution unit; may process many Runs

Conversation
  an addressable application thread; may outlive a live Agent

Host / Orchestration
  process and coordination lifecycle; drains and stops many Agents
```

`agent_end` ends a Run, not an Agent. A directly held Agent remains usable after a Run settles until its caller closes it. Go garbage collection is not an Agent lifecycle mechanism: a live goroutine must be closed explicitly.

## 2. Agent lifecycle

```text
Created ── ready ──► Idle
Idle ── Prompt/Continue ──► Busy
Busy ── Run settled ──► Idle
Idle/Busy ── close request ──► Closing
Closing ── local work stopped ──► Closed
```

A Core Agent owns the transition of its execution unit. Its lifecycle surface is conceptually:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Close(context.Context) error
}
```

- `Idle` accepts a new execution command.
- `Busy` accepts only the controls defined by the Core contract.
- `Closing` accepts no new Prompt or Continue; admission and the close transition are serialized.
- `Closed` rejects every command and never starts Model, Tool, or Extension work.

The current Run is not implicitly successful because the Agent is closing. A graceful close lets it settle; an Orchestration policy may explicitly cancel or abort it first. `Close` is idempotent and rejects new Prompt/Continue commands once closing begins.

## 3. Close and graceful shutdown

Closing is idempotent. The first close request changes the Agent to `Closing`; later requests wait for or observe the same close operation. A `Close` error reports lifecycle failure or caller wait expiry, not the current Run's `RunResult` error; a failed Run can still be followed by successful Agent closure.

A context-aware close operation has two responsibilities:

```text
request graceful close
  ↓
reject new Runs
  ↓
settle or explicitly abort the current Run
  ↓
finish local protected observers and owned work
  ↓
close private channels
  ↓
Closed
```

The close Context bounds how long the caller waits. If it expires while closing continues, the caller receives the Context error; the Agent still finishes closing in the background. A caller that needs immediate termination must explicitly cancel or abort the active Run before closing.

Close does not wait for a Host's remote delivery bridge. Core execution settlement and remote delivery settlement remain separate.

## 4. Retirement is an ownership decision

An Agent may request retirement after its current Run, for example when an ephemeral sub-agent has completed its task. This is a non-blocking intent, not permission to mutate an Orchestration routing table from inside the Agent goroutine.

```text
Agent Core ── retirement intent ──► Orchestration
                                      │ policy
                                      ▼
                         persist / discard Conversation
                                      │
                                      ▼
                               Core Close
```

Orchestration or the direct application owner decides whether to honor the intent. Only an explicit trusted capability or policy-defined Model/Tool/Extension result may create the intent; ordinary Model text such as “delete me” has no lifecycle authority. A model or Tool cannot bypass owner policy by directly destroying a process or another Agent.

A retirement policy may be:

```text
Retain       keep the Agent until explicit close
AfterRun     close after the selected Run settles
AfterIdle    close after an idle TTL
Ephemeral    close after completion and retain no Conversation
```

`AfterRun` and `AfterIdle` are Orchestration policies. `AfterIdle` starts its TTL only when no Run is admitted and resets on a new Run. Core supplies deterministic close behavior; it does not own TTLs, capacity eviction, or business retention.

## 5. Conversation outlives a live Agent

A Conversation is an Orchestration-owned addressable entity. Its stable identity is not the current AgentID:

```text
ConversationID / ConversationKey
          ↓
  live Agent handle, if present
          ↓
      Agent Core
```

When an Agent is retired with conversation retention enabled:

```text
live AgentID → persisted Core snapshot / transcript
           → Conversation becomes Dormant
           → next request creates a new AgentID
```

The ConversationID remains stable while AgentID may change. An AgentID is never reused and is not sufficient to recreate an in-memory Agent.

A retained Conversation requires an Agent definition or factory reference, a versioned Core state snapshot, and an atomic record update. Without those, retirement discards the only live state and the Conversation is closed or lost according to policy.

## 6. Conversation lifecycle

```text
Absent ── create ──► Active
Active/Dormant ── retirement begins ──► Retiring
Retiring ── retained close ──► Dormant
Retiring ── discard close ──► Closed
Dormant ── resolve and rehydrate ──► Active
Active/Dormant ── application close ──► Closed
Closed ── archive/delete policy ──► Archived or removed
```

A process-local Orchestration MUST serialize concurrent create-or-resolve operations for one Conversation key. It must not create two live Agents for the same exclusive Conversation unless the application explicitly selects that policy.

A failed durable snapshot MUST NOT be reported as a successful retained retirement. The safe default is to keep the live route and report the retirement failure, or to close the Conversation explicitly if the caller selected discard semantics. Retiring an already Dormant Conversation is idempotent and does not rehydrate an Agent; explicit Conversation close may delete its retained snapshot.

## 7. Ephemeral and persistent sub-agents

A spawned Agent is independent by default. Orchestration may choose one of two common policies:

```text
Ephemeral child:
  run one task → return result → close → retain no Conversation

Persistent child:
  create Conversation → run task → retire to Dormant or remain Active
```

`SpawnID`, parent AgentID, and parent RunID correlate the child with its origin. They do not automatically establish ownership or cancellation inheritance. A Workflow or Orchestration group may explicitly add child cleanup, fail-fast, or cascading cancellation.

## 8. Hosted shutdown

Host drain operates above Agent retirement:

```text
readiness false
  ↓
stop new external admission
  ↓
Orchestration stops creating or dispatching new work
  ↓
active Runs settle or cancel
  ↓
retire or close live Agents according to retention policy
  ↓
flush or abandon remote delivery within its deadline
```

A Host process ending is not a Conversation close. If the drain deadline expires while an Agent remains Busy or Closing, the Host reports an incomplete drain; it cannot forcibly kill a Go goroutine. A retained Conversation may be rehydrated by a later Host if its persistence contract supports it.

## 9. Design rule

The safe rule is:

> **Core can finish work and request retirement; the owner closes the Agent; Orchestration decides whether the Conversation survives.**

This keeps Core free of registries and persistence while giving multi-Agent systems a deterministic way to clean up short-lived Agents and recover long-lived Conversations.
