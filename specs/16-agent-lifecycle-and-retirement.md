# 16. Agent Lifecycle and Retirement

**Status:** Draft

> Run settlement, Agent closure, Conversation retention, and Host drain are separate contracts.

## 1. Scope and identities

This specification defines the lifecycle of one Core Agent, the retirement policies applied by Orchestration, and the relationship between a live Agent and an addressable Conversation.

The identities have different meanings:

```go
type AgentID string          // one live Core execution unit
type ConversationID string   // one addressable application conversation
type ConversationKey string  // caller-selected lookup key within its namespace
type RunID string             // one accepted Prompt or Continue
type SpawnID string           // one creation/correlation request
type RetirementReason string  // bounded diagnostic reason for a close request
type AgentGeneration uint64   // optional incarnation number for one Conversation
```

`AgentID` MUST be unique for the lifetime of the system and MUST NOT be reused after Agent closure. `RunID` identifies execution, not Agent lifetime. `ConversationID` remains stable across Agent recreation. `ConversationKey` is a routing key and is not required to be globally unique without its application or tenant namespace.

## 2. Four lifecycles

The implementation MUST distinguish:

```text
Run
  Created → Accepted → Running → Settling → Settled

Agent
  Created → Idle ⇄ Busy → Closing → Closed

Conversation
  Absent → Active ⇄ Dormant
             ↓        ↓
          Retiring → Closed → Archived/Removed

Host / Orchestration
  Serving → Draining → Stopped
```

A terminal Run MUST NOT implicitly close its Agent. A Host or process shutdown MUST NOT be treated as a Conversation close.

## 3. Core Agent lifecycle

A Core Agent MUST provide one mutation authority for its lifecycle and private state. Its Agent goroutine or equivalent execution unit owns these transitions:

```text
Created ── initialization complete ──► Idle
Idle ── accepted Prompt/Continue ────► Busy
Busy ── Run settled ─────────────────► Idle
Idle/Busy ── close request ───────────► Closing
Closing ── local work stopped ───────► Closed
```

The Agent MAY transition from `Created` directly to `Closed` when construction or startup fails. A `Closed` Agent MUST reject Prompt, Continue, Steer, FollowUp, Reset, and new observation registrations. It MUST start no Model, Tool, Extension, or continuation work.

An Agent MUST reject new Prompt and Continue commands after it enters `Closing`. The transition into `Closing` and command admission are serialized by the Agent authority: a command accepted before the transition is the current Run, and a command after it is rejected. It MAY accept an idempotent close observation or a control needed to settle the current Run according to the selected close policy.

## 4. Core close contract

The public Core contract MUST expose a context-bounded close operation. The exact method name may evolve; its semantics are:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Close(context.Context) error
}
```

`Close` MUST be idempotent. Concurrent callers MUST observe one close operation and MUST NOT close the same channel or release the same resource twice. The `Close` error reports lifecycle/close failure or a waiting Context expiry; it is not the current Run's `RunResult` error. A Run may settle as failed while close still succeeds. A graceful close MUST:

```text
mark the Agent Closing
reject new execution commands
allow the current Run to settle
finish local protected observation and owned work
close private command/result/Event resources
mark the Agent Closed
```

The close Context bounds the waiting caller. If it expires, `Close` returns the Context error, but the Agent MUST continue its already-started close operation. The Agent becomes `Closed` only after owned work returns; Go cannot forcibly terminate a Tool or Model that ignores Context, so such an Agent remains `Closing` and its stuck lifecycle MUST remain observable. A caller requiring the current Run to stop MUST issue the explicit Core cancellation/Abort policy before or while closing.

`Close` MUST NOT wait for remote Host delivery. Core execution settlement and remote delivery settlement remain independent.

The API MAY expose an additive lifecycle capability:

```go
type AgentLifecycle interface {
    Agent
    Status() AgentStatus
    Done() <-chan struct{}
}
```

`Done` MUST be closed exactly once after the Agent is Closed. `Status` MUST NOT expose mutable Core state.

## 5. Run and Agent settlement

The normal completion path is:

```text
Busy
  → settle current Run
  → emit exactly one Run terminal agent_end
  → await terminal local observers
  → return RunResult
  → Idle, unless Closing
```

If the Agent is `Closing`, the current Run follows its selected graceful or cancellation policy and then the Agent proceeds to `Closed`. `agent_end` remains a Run Event. Agent closure is represented by an Agent lifecycle signal, not by a second `agent_end`.

No Run, Model call, Tool, Extension, or control-driven continuation may begin after `agent_end` or after the Agent enters `Closed`.

## 6. Retirement intent and authority

A Core Agent MAY expose an additive capability for requesting retirement:

```go
type RetirableAgent interface {
    Agent
    RequestRetirement(context.Context, RetirementReason) error
}
```

`RequestRetirement` MUST be non-blocking with respect to the current Run and MUST NOT directly mutate an external routing table, Conversation record, or another Agent. It records an intent to close after the current Run or at the next safe boundary.

Only an explicit trusted capability or a policy-defined Model/Tool/Extension result may create this intent. Ordinary Model text that says “stop” or “delete me” has no lifecycle authority. The reason and any requested retention mode are bounded metadata; the owner still chooses the effective policy.

The direct application owner or Orchestration MUST decide whether to honor the intent. A Model response or Tool MUST NOT bypass that owner policy to destroy another Agent or terminate a process. If the capability is disabled, the request MUST return a typed not-supported or policy-denied error and MUST NOT change lifecycle state.

A request honored by a direct owner MAY call `Close` after Run settlement. An Orchestration owner MUST coordinate persistence, route removal, and close as one retirement workflow.

## 7. Retirement policies

Orchestration MAY assign one policy to a newly created Agent or Conversation:

```text
Retain:
  keep the live Agent until explicit close

AfterRun:
  after the selected Run settles, retire the Agent

AfterIdle:
  retire after an idle duration with no admitted Run

Ephemeral:
  retire after the task settles and retain no Conversation state
```

The default policy for a directly constructed Core Agent MUST be `Retain`. An empty retirement policy in an `AgentRequest` means `Retain`. Core MUST NOT invent an idle TTL or automatically self-close merely because a Run settled.

An Orchestration MUST NOT evict a Busy Agent without an explicit cancellation or abort decision. `AfterIdle` begins its TTL only after Run settlement and while no Run is admitted; a new admission cancels or resets that timer. A capacity or TTL retirement MUST first prevent new admission and then use the close contract.

## 8. Conversation record and routing

A retained Conversation MUST be represented outside Core by an Orchestration-owned record equivalent to:

```go
type ConversationRecord struct {
    ID              ConversationID
    Key             ConversationKey
    AgentName       AgentName
    LiveAgentID     AgentID
    Generation      AgentGeneration
    Status          ConversationStatus
    StateVersion    uint64
    CoreSnapshot    []byte // or an application-owned durable reference
}
```

`LiveAgentID` is set only while the route is live; it is empty in `Dormant`, `Closed`, or `Archived` records. During `Retiring`, it remains set and blocks rehydration until the close/route transition completes.

The exact storage representation may evolve. The semantics MUST preserve:

```text
ConversationID / Key → live Agent handle, when hot
ConversationID / Key → Agent definition + Core state, when dormant
```

An `AgentID` is not a locator, handle, or recovery record. It MUST NOT be used alone to rehydrate an Agent.

A process-local Orchestration MUST serialize concurrent create-or-resolve operations for one Conversation key. It MUST NOT create two live exclusive Agents for that key unless the application explicitly selects that policy. Each route entry SHOULD carry the AgentGeneration and an admission lease or equivalent fence. Retirement marks the entry `Retiring` before persistence; dispatch either holds a lease accepted before that transition or is rejected/retried. Rehydration installs a new generation atomically only after the old live route is closed.

## 9. Retaining a Conversation while retiring an Agent

For a retirement that retains the Conversation, Orchestration MUST use this logical order:

```text
mark Conversation retiring / stop new admission
  ↓
settle or explicitly cancel the current Run
  ↓
obtain a consistent Core snapshot or transcript reference
  ↓
atomically persist the new Conversation generation
  ↓
close the live Agent
  ↓
remove the live handle and mark Conversation Dormant
```

The persisted state MUST identify the Agent definition/factory needed for rehydration and MUST have a version that prevents stale concurrent writes. A successful rehydration increments `AgentGeneration` and installs the new `(ConversationID, AgentID, Generation)` tuple with a compare-and-swap or equivalent fence. A failed snapshot or record commit MUST NOT be reported as successful retained retirement. The safe default is to keep the live route and report failure, or to use an explicitly selected discard policy. After a successful record commit but before Core close acknowledgement, the Conversation remains `Retiring` and MUST NOT be concurrently rehydrated.

On a later request, Orchestration MUST create a new AgentID, restore the persisted state, atomically install the new live handle, and then dispatch the request. The ConversationID remains stable; the AgentID changes.

## 10. Discarding a Conversation

For `Ephemeral` or explicit discard retirement:

```text
stop new admission
  ↓
settle or cancel current Run
  ↓
close Agent
  ↓
remove live route
  ↓
delete or mark Conversation Closed
```

After discard, a request using the same key MUST either create a new Conversation under an explicit create policy or return a closed/not-found error. It MUST NOT silently reuse the discarded Core state. Retiring an already `Dormant` Conversation is idempotent and MUST NOT rehydrate an Agent merely to close it; an explicit Conversation close may delete its retained snapshot.

## 11. Spawned Agents and Workflow ownership

A spawned Agent MUST receive independent AgentID, private state, lifecycle, and Run identities. Its default relationship to the parent is correlation only:

```text
parent AgentID + parent RunID + SpawnID → child AgentID
```

Orchestration MAY assign:

```text
Ephemeral child     close after one task; retain no Conversation
Persistent child    retain a Conversation and allow later resolution
Workflow-owned child close/cancel according to an explicit group policy
```

Parent completion MUST NOT automatically close a child. A Workflow or Orchestration group that requires fail-fast, cascading cancellation, or child cleanup MUST declare and enforce that policy explicitly.

## 12. Agent lifecycle signals

Run Events and Agent lifecycle signals MUST remain distinguishable. `agent_end` is terminal for one Run. Agent closure SHOULD be observable through a separate bounded lifecycle boundary with semantic equivalents of:

```text
agent_created
agent_retirement_requested
agent_closing
agent_closed
agent_retirement_failed
```

A lifecycle signal MUST include AgentID, optional ConversationID, reason, and generation/correlation metadata. It MUST NOT be inserted into a Run's per-Run Event sequence unless the Event contract explicitly defines empty-Run lifecycle records.

Orchestration MAY project these signals to a Host stream. Remote delivery of `agent_closed` is not required for Core closure to complete.

## 13. Host drain

Host drain MUST compose with Agent retirement:

```text
Serving → Draining
  readiness false
  stop external admission
  Orchestration stops new creation/dispatch
  active Runs settle or cancel by deadline
  live Agents close according to retention policy
  delivery bridges flush or abandon by their deadline
→ Stopped
```

A Host MUST NOT claim that a retained Conversation is closed merely because its process stopped. If the drain deadline expires while an Agent remains `Busy` or `Closing`, the Host reports an incomplete drain; it cannot forcibly kill a Go goroutine. Rehydration after restart is available only when the persistence and Agent factory contracts are implemented.

## 14. Acceptance

Tests MUST prove:

```text
Run settlement leaves a Retain Agent usable
Close rejects new Runs and is idempotent
Close during Busy settles or cancels exactly once
Done closes exactly once when supported
no work starts after Closing/Closed
retirement intent does not block the current Run
AfterRun and AfterIdle policies close at the declared boundary
Busy Agents are not evicted without explicit cancellation
retained retirement persists before route removal
rehydration creates a new AgentID for the same ConversationID
route generation fencing prevents stale-handle dispatch
failed persistence does not claim successful retention
Ephemeral children close without retaining Conversation state
parent completion does not implicitly close independent children
Host drain and remote delivery settlement remain distinct
expired drain reports stuck Busy/Closing Agents rather than false completion
```
