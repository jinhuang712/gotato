# Agent Routines

**Status:** Draft

> Gotato names managed child Agent execution an Agent Routine: a child Run with explicit ownership, correlation, cancellation, and bounds.

## 1. Concept

```text
Agent Routine
  = child Agent
  + child Run
  + parent Context
  + Routine identity
  + bounded execution
  + settled Routine Result
```

A goroutine may schedule local Routine work, but it is not the semantic abstraction:

```text
Agent          stateful runtime
Run            one Agent invocation
Goroutine      Go scheduling primitive
Agent Routine  managed child Agent Run
```

## 2. Place in the hosted system

```text
gRPC Client
    │
    ▼
Parent Agent Run
    │ spawn
    ▼
Agent Routine Coordinator
    │
    ▼
Child Agent
    ├── child Run
    ├── child Model and Tools
    └── child canonical Events
```

The parent and child both use the canonical runtime loop. The service can project Routine lifecycle and child correlation without understanding a second execution model.

## 3. Spawn sources

A Routine can be spawned by runtime composition or through a model-callable Tool.

Direct composition is conceptually:

```go
routine, err := routines.Spawn(ctx, factory, request)
if err != nil {
    return err
}

result, err := routine.Wait(ctx)
```

Model-controlled spawn uses an ordinary Tool:

```text
Parent Model
    │ Tool Call: spawn_agent
    ▼
Pre-Tool-Use
    ↓
spawn Agent Routine
    ↓
wait or collect Routine Result
    ↓
Post-Tool-Use
    ↓
Parent Tool Result
    ↓
Parent Model continues
```

The spawn Tool participates in normal validation, policy, Events, limits, and Tool Result commitment.

## 4. Agent construction

A Routine receives a distinct child Agent from a factory:

```text
Routine request
  child Agent name or definition
  child Prompt
  child limits
  correlation metadata
        │
        ▼
Child Agent factory
        │
        ▼
distinct Agent instance
```

Parent and child can share immutable dependencies such as clients and adapter configuration. They do not share mutable Agent transcript state.

## 5. State isolation

```text
Parent Agent
  transcript A
  active Run A
  active ToolSets A

Child Agent
  transcript B
  active Run B
  active ToolSets B
```

Child Messages stay with the child Agent. The parent receives a structured Routine Result, normally through the spawning Tool Result or Routine Group aggregation.

This prevents interleaving independently mutating transcripts.

## 6. Context and cancellation tree

```text
Parent Run Context
      ├── Parent Model
      ├── Parent Tools
      └── Routine Context
              ├── Child Model
              ├── Child Tools
              ├── Child observers
              └── Nested Routines
```

Parent cancellation reaches every owned Routine. A Routine can have a shorter deadline and an explicit idempotent cancellation handle.

```text
client Cancel / stream close / drain
              ↓
Parent Run Context cancelled
              ↓
Routine Context cancelled
              ↓
child work settles
```

Remote transport cancellation therefore reaches the complete Agent ownership tree through one runtime mechanism.

## 7. Lifecycle

```text
Created → Queued → Running
                    ├──► Completed
                    ├──► Failed
                    └──► Cancelled
```

A Routine settles exactly once. Repeated waits after settlement return the same immutable result.

Conceptually:

```go
type RoutineResult struct {
    RoutineID string
    ChildRun  RunResult
    Status    RoutineStatus
}
```

A failed child Run becomes a failed Routine Result. Parent Tool or Group policy determines how the parent proceeds.

## 8. Fan-out and fan-in

```text
                      Parent Agent
                            │
                   bounded Routine Group
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        Research Routine  Logs Routine  Code Routine
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                   deterministic Results
                            │
                            ▼
                      Parent synthesis
```

A Routine Group provides bounded, cancellation-aware coordination without introducing a workflow graph language.

Group policies include:

```text
collect all
fail fast
collect partial results
first successful result
```

The policy defines sibling cancellation, group error behavior, and which settled Results are returned.

## 9. Ordering

Routine completion Events follow actual completion order:

```text
spawn:       A → B → C
completion:  C → A → B
```

Routine Group Results normally follow spawn order for deterministic parent synthesis:

```text
results:     A → B → C
```

Completion observation and deterministic aggregation are separate concerns.

## 10. Limits

```text
maximum Routines spawned per parent Run
maximum concurrently active Routines
maximum Routine nesting depth
child Run deadline
child Turn and Tool limits
result and progress volume
```

A spawn request that exceeds a bound returns a typed limit outcome without starting the child Agent.

```text
Parent limits
      ├── Child A limits
      ├── Child B limits
      └── Child C limits
```

Shared token, cost, and time budgets can allocate child limits through a focused budget policy while preserving Routine ownership.

## 11. Events and correlation

Parent-facing Routine lifecycle Events include:

```text
routine_started
routine_completed
routine_failed
routine_cancelled
```

Correlation includes:

```text
routine_id
routine_name
parent_run_id
child_run_id
```

```text
Parent Event view
      ├── parent Agent Events
      ├── Routine lifecycle Events
      └── child correlation

Child Event view
      ├── child agent_start
      ├── child Turn and Tool Events
      └── child agent_end
```

The service can project lifecycle Events into the parent Run stream and expose detailed child Events through an explicit correlated view. Canonical child ordering remains scoped to the child Run.

## 12. Progress and delivery

Routine progress is observational and bounded. Service Event bridges may coalesce optional progress while preserving lifecycle transitions and the settled Routine Result.

A slow remote client does not create unbounded Routine memory growth.

## 13. Execution placement

Routine semantics do not depend on placement:

```text
Local placement
  Parent Runtime → goroutine → child Agent

Remote placement
  Parent Runtime → Routine Executor → Agent Service → child Agent
```

Both placements preserve:

```text
Routine identity
parent and child Run correlation
Context cancellation
limits
lifecycle Events
single settlement
Routine Result
```

Placement is an executor concern rather than a new Agent abstraction.

## 14. Ownership

```text
Runtime
  Agent · Run · Context · canonical Events

Agent Routine package
  spawn · identity · lifecycle · groups · limits · Results

Service
  remote projection · admission interaction · cross-process correlation

Application
  child Agent definitions · prompts · coordination policy
```

Agent Routines compose the existing runtime boundary and never change the canonical loop.
