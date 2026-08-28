# 07. Agent Routines, Concurrency, and Cancellation

**Status:** Draft

> Agent state has one writer, Agent Routines own child Runs, execution is bounded, and cancellation flows down the ownership tree.

## 1. Agent exclusivity

One Agent MUST have at most one active mutating Run. Concurrent `Prompt` and `Continue` calls MUST receive a typed busy error.

## 2. Run Context

```text
Run Context
  ├── context Transformer
  ├── Model stream
  ├── Tool Calls
  ├── awaited Extensions and subscribers
  └── Agent Routines
```

The Run Context MUST reach every child operation. Child deadlines MUST fit within the parent deadline.

## 3. Abort and settlement

`Abort` MUST be idempotent. On an active Run it cancels the Run Context; on an idle Agent it preserves state.

`WaitForIdle` MUST accept a Context and return after execution settlement: the active Run, Agent Routines owned by that Run, and terminal observers have settled according to the selected Routine policy. It MUST NOT wait on remote delivery.

## 4. Parallel Tools

Parallel Tool execution MUST use a positive configured bound. Completion Events follow actual completion order; transcript commitment follows assistant source order.

The default Tool batch policy SHOULD collect all finalized results. An explicit fail-fast policy MAY cancel remaining calls.

## 5. Message queues

Steering and Follow-up queue operations MUST be concurrency-safe and preserve acceptance order within each queue.

## 6. Internal channels

Channels are internal coordination primitives. A channel MUST NOT appear in a public runtime or service signature; callers interact through operations, streams, and Event subscriptions.

Every channel MUST have:

```text
an explicit capacity
one owning package
one producer responsible for close
```

A blocking send or receive MUST participate in its owning Context:

```go
select {
case queue <- event:
case <-ctx.Done():
    return ctx.Err()
}
```

A bare send MUST NOT be used where the receiver can go away, and a sender goroutine MUST NOT outlive the Context that authorized it.

These rules apply to the Event bridge, the Steering and Follow-up queues, Tool and Routine progress, service command handoff, and worker admission.

## 7. Agent Routine

An Agent Routine MUST contain:

```text
Routine identity
parent Run identity
child Agent
child Run
child Context
Routine status
settled Routine Result
```

A Routine MUST use an Agent instance distinct from its parent and siblings.

## 8. Spawn

The Agent Routine package MUST support application-controlled spawn through an Agent factory.

Conceptually:

```go
routine, err := routines.Spawn(ctx, factory, request)
result, err := routine.Wait(ctx)
```

The final API MAY evolve while preserving asynchronous spawn, explicit cancellation, and single settlement.

## 9. Model-controlled spawn

A model-callable `spawn_agent` Tool MAY wrap the Agent Routine API.

```text
Parent Tool Call
  → Pre-Tool-Use
  → spawn Routine
  → wait or collect Routine Result
  → Post-Tool-Use
  → Parent Tool Result
```

The spawn Tool MUST use the ordinary Tool lifecycle and parent Run limits.

## 10. Lifecycle

```text
Created → Queued → Running
                    ├──► Completed
                    ├──► Failed
                    └──► Cancelled
```

A Routine MUST settle exactly once. `Wait` calls after settlement MUST return the same immutable result.

## 11. Cancellation

Parent Run cancellation MUST cancel every owned Routine Context.

Routine cancellation MUST reach the child Model, Tools, Extensions, subscribers, and nested Routines.

Sibling cancellation follows the selected Routine Group policy.

## 12. Limits

The Runtime or Routine package MUST enforce:

```text
maximum Routines spawned per parent Run
maximum concurrently active Routines
maximum Routine nesting depth
child Run deadline
child Turn and Tool limits
```

A spawn request that exceeds a bound MUST return a typed limit outcome without starting the child Agent.

## 13. Routine Events

Parent-facing lifecycle Events MUST include:

```text
routine_started
routine_completed
routine_failed
routine_cancelled
```

Correlation MUST include:

```text
routine_id
routine_name
parent_run_id
child_run_id
```

Detailed child Events retain the child Run identity and MAY be exposed through a dedicated Routine subscription.

## 14. Routine Group

A bounded Routine Group SHOULD support:

```text
collect all
fail fast
collect partial results
first success
```

The selected policy MUST define sibling cancellation, result ordering, and group error behavior.

## 15. Result ordering

Routine Group results SHOULD use spawn order for deterministic aggregation while Routine completion Events use actual completion order.

## 16. Service relationship

Local Routines execute child Agents in goroutines. A future remote Routine executor MAY invoke a child Agent service while preserving Routine identity, Context, Events, limits, and Result semantics.
