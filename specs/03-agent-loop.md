# 03. Agent Loop

**Status:** Draft

> Embedded callers, Hosts, and child Agents enter the same Core loop.

## 1. Run admission

`Prompt` and `Continue` are the only Core operations that create a Run. Before `agent_start`, Core validates input, acquires the Agent mutation owner, creates Run identity and Context, initializes counters, and emits `agent_start`. A busy or invalid operation creates no Run and emits no start Event.

## 2. Prompt and Continue

`Prompt` validates and commits a user Message before its first Model request, then runs the loop and returns after execution settlement. `Continue` appends no user Message and is valid only when the transcript ends in a Model-continuable state such as a user or Tool Result Message. It never synthesizes user input.

## 3. Normative algorithm

```text
accept Prompt or Continue
create Run Context and Run ID
emit agent_start

if Prompt: commit and emit user Message

repeat:
  increment Turn and check Context/limits
  emit turn_start
  snapshot state
  transform and convert context
  resolve visible Tools
  open Model stream
  assemble one assistant Message
  commit assistant Message and lifecycle Events
  preflight Tool Calls in source order
  execute admitted Tools under configured bound
  finalize all admitted outcomes
  commit Tool Result Messages in source order
  emit turn_end
  apply TurnStopper
  if stopped: settle
  commit accepted Steering at its boundary
  if Tool Results require continuation: repeat
  if Steering requires continuation: repeat
  if Follow-up exists: commit next Follow-up and repeat
  settle

emit one agent_end
await terminal observers
return RunResult
```

No Model call occurs between assistant commitment and Tool preflight. No Tool execution starts after `turn_end`.

## 4. Turn order

Normal order is:

```text
agent_start
turn_start
user Message lifecycle when Prompt supplied input
assistant message_start/update/end
Tool execution and Tool Result lifecycle
turn_end
...
agent_end
```

A response with Tool Calls is not terminal unless an explicit Tool or Extension decision requests termination.

## 5. Tool scheduling

Preflight is source ordered. Sequential mode executes and completes source ordered. Bounded parallel mode limits active workers, reports completion Events in actual completion order, and commits results in source order. Every admitted Tool settles before `turn_end`. Fail-fast may cancel siblings but must represent every started Tool outcome.

## 6. ToolSet activation

Activation uses the ordinary Tool path and commits state after the current batch. Newly active Tools appear only in the next Model request. Activation is idempotent and cannot partially expose a failed ToolSet.

## 7. Steering and Follow-up

Steering is validated and appended atomically to a bounded queue. It cannot interrupt current Model or Tool work by default; it is committed after the current Tool batch and before continuation selection. Follow-up is a separate bounded queue consumed only when the Run would otherwise settle. When both exist, Steering is committed first. Overflow is reported at acceptance.

## 8. Stop and cancellation

TurnStopper runs after `turn_end` and before continuation selection. A stop preserves the Turn and settles the Run. Every stage receives Run Context; cancellation is checked before admission, Turn, Model, Tool, queued continuation, and terminal observer dispatch. Committed state is not rolled back.

## 9. Settlement

```text
Running → terminal decision → settle owned children
        → emit agent_end → await terminal observers → Settled
```

No retry, queue poll, Model call, Tool, Routine, or Event starts after `agent_end`. Core does not wait for Host delivery.

## 10. Failure behavior

Tool errors normally become failed Tool Results and allow continuation. Malformed Model protocol, fatal Model error, blocking Extension error, invariant failure, cancellation, deadline, and exhausted Core limit settle the Run.

## 11. Equivalence

Given equivalent initial Core state, Model stream, Tools, options, and cancellation timing, Embedded and Hosted execution MUST produce the same canonical Event kinds/order, committed transcript, and terminal Core status. Transport acknowledgements and delivery timing are not Core facts.
