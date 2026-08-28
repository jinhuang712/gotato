# 03. Agent Loop

**Status:** Draft

> The service, direct Go caller, and child Agent all execute one loop. This specification fixes the loop's ordering, admission points, queue boundaries, and settlement behavior.

## 1. Run admission

`Prompt` and `Continue` are the only operations that create a Run.

Before `agent_start`, the Runtime MUST:

1. validate the operation and input;
2. acquire the Agent's exclusive mutation owner;
3. create the Run ID and Run Context;
4. snapshot the initial state needed by the loop;
5. initialize the Run limit counters;
6. emit and dispatch `agent_start`.

If any step before ownership fails, no Run exists and no `agent_start` Event is emitted. A busy Agent returns a typed busy error without changing transcript state.

## 2. Prompt

`Prompt(ctx, message)` MUST:

1. validate the Message as a user Message;
2. admit one exclusive Run;
3. emit `agent_start`;
4. begin the first Turn boundary;
5. commit the user Message before the first Model request;
6. emit the user Message lifecycle Events;
7. execute the canonical Turn loop;
8. emit exactly one `agent_end`;
9. wait for terminal observers and return the settled `RunResult`.

The user Message belongs to the Run that accepted it. If the Run is cancelled after the Message commits, the Message remains in the Agent transcript.

## 3. Continue

`Continue(ctx)` MUST use the same admission and loop path without appending a user Message. It is eligible only when the transcript ends in a Model-continuable state:

```text
user Message
Tool Result Message
```

A transcript ending in an incomplete assistant Message, an uncommitted Tool Call, or an invalid custom Message returns a typed invalid-state error. `Continue` MUST NOT emit a synthetic user Message.

## 4. Canonical algorithm

The following pseudocode is normative. Internal functions MAY differ, but their externally observable behavior MUST match:

```text
accept Prompt or Continue
create Run Context and Run ID
emit agent_start

if Prompt:
    commit user Message
    emit user Message lifecycle

repeat:
    increment Turn number
    check Run limits and Context
    emit turn_start

    snapshot Agent state
    transform context
    convert Messages
    resolve visible Tools
    build ModelRequest

    open ModelStream
    assemble exactly one assistant Message
    commit assistant Message
    emit assistant Message lifecycle

    resolve and preflight Tool Calls in source order
    execute the Tool batch under its configured bound
    finalize every admitted Tool outcome
    commit Tool Result Messages in assistant source order

    emit turn_end
    run TurnStopper
    if stopper terminates:
        settle Run

    commit accepted Steering at the defined boundary
    if Tool Results require continuation:
        continue repeat
    if accepted Steering requires continuation:
        continue repeat
    if Follow-up is queued:
        commit Follow-up and continue repeat

    settle Run

emit agent_end
wait for terminal observers
return RunResult
```

The Runtime MUST perform no Model call between `assistant Message` commitment and Tool preflight. It MUST perform no Tool execution after `turn_end` for that Turn.

## 5. Turn boundaries

The canonical Event order for a normal Turn is:

```text
turn_start
  user Message lifecycle when Prompt supplied input
  assistant message_start
  assistant message_update ...
  assistant message_end
  Tool execution lifecycle and progress
  Tool Result Message lifecycle
turn_end
```

The user Message lifecycle is emitted only for `Prompt`, never for `Continue`. A Turn with no Tool Calls still emits `turn_end` and then applies stop, Steering, and Follow-up policy.

A Model response containing Tool Calls is not a terminal response even when it also contains text, unless a Tool or Extension explicitly requests termination.

## 6. Tool batch scheduling

Tool Calls are assigned source indexes `0..n-1` from the assistant Message.

Sequential mode:

```text
preflight  0 → 1 → 2
execute    0 → 1 → 2
complete   0 → 1 → 2
commit     0 → 1 → 2
```

Bounded parallel mode:

```text
preflight  0 → 1 → 2
execute    at most N active
complete   actual completion order
commit     0 → 1 → 2
```

Preflight failure creates a finalized blocked or failed outcome without invoking the executor. A Tool marked sequential MAY force the complete batch into sequential mode; the selected policy MUST be deterministic.

The default batch policy collects all finalized outcomes. A fail-fast policy MAY cancel not-yet-settled siblings, but already-started Tools still require Context-aware settlement and their final outcomes MUST be represented before `turn_end`.

## 7. ToolSet activation

An activation Tool is resolved and executed like any other Tool. Its state mutation is committed only after the current Tool batch's preflight and outcome processing complete. The next Model request, never the current one, sees newly activated Tools.

Repeated activation of an already active ToolSet is successful and produces no duplicate active entry. Unknown names, active ToolSet limits, and visible Tool limits produce typed Tool outcomes and do not partially activate state.

## 8. Steering

`Steer(message)` is accepted only while the Run is active and MUST:

- validate the Message as a steering user Message;
- append it atomically to the Steering queue;
- preserve acceptance order;
- return an acceptance error if the queue bound is reached or the Run is already terminal.

Steering accepted during a Model stream or Tool execution cannot interrupt that operation by default. It is committed after the current Turn's Tool batch and before the next continuation decision. A configured interrupt policy MAY cancel the current Run, but that is a different explicit policy.

## 9. Follow-up

`FollowUp(message)` validates and appends to a separate bounded queue. It does not affect the current Turn. When the Run would otherwise settle, the Runtime dequeues Follow-up Messages in acceptance order, commits each as a user Message, and starts another Turn.

If both Steering and Follow-up are available, Steering is committed first. A Follow-up MUST NOT be silently converted into Steering, and queue overflow MUST be reported at acceptance time.

## 10. Turn stopping

After `turn_end` and before continuation selection, the Runtime invokes the configured `TurnStopper`.

```go
type StopDecision struct {
    Stop  bool
    Reason string
}
```

A stop decision preserves the completed Turn and all its Tool Results, then settles the Run before polling Steering, Tool continuation, or Follow-up. A stopper error is a blocking Extension failure and settles the Run as failed.

## 11. Cancellation and deadlines

All loop stages receive the Run Context. Cancellation MUST be checked:

```text
before Run admission completes
before each Turn
before opening a Model stream
while receiving Model events
before each Tool executor
while a Tool or Routine is active
before committing a queued continuation
before terminal observer dispatch
```

Cancellation does not roll back already committed Messages, Tool outcomes, or Events. It prevents new owned work and asks active work to settle. The final Run status records cancellation or deadline according to the first terminal cause selected by the Runtime.

## 12. Settlement

Settlement is a one-way barrier:

```text
Running → terminal decision → cancel/settle owned children
        → emit agent_end → await terminal observers → Settled
```

`agent_end` MUST be the last canonical Event for the Run. No retry, queue poll, Model call, Tool Use, Routine spawn, or observer dispatch starts after it. `Prompt`, `Continue`, and `WaitForIdle` return only after execution settlement; remote Event delivery is outside this wait.

## 13. Failure matrix

| Failure | Transcript effect | Run effect |
|---|---|---|
| invalid Prompt | no Message | no Run / typed error |
| busy Agent | unchanged | no Run / typed busy error |
| malformed Model arguments | assistant commits; Tool does not run | terminal protocol failure |
| Tool executor error | failed Tool Result commits | Model may continue |
| blocked Tool | blocked Tool Result commits | Model may continue unless termination hint |
| blocking Extension error | prior commits remain | terminal failure |
| Context cancellation | prior commits remain | terminal cancelled/deadline result |
| Run limit | no new governed work | terminal limit result |
| fatal Runtime invariant | state is not silently repaired | terminal internal failure |

## 14. Equivalence

The direct and service callers MUST use this same loop. Given equivalent initial Agent state, Model stream, Tool outcomes, options, and cancellation timing, they MUST produce the same canonical Event kinds, correlation order, committed transcript, and terminal status. Transport acknowledgements and delivery timing are not canonical Runtime Events.
