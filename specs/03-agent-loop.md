# 03. Agent Loop

**Status:** Draft

> **Agent Core executes one canonical Loop; Orchestration controls admission.**

## 1. Command admission

`Prompt` and `Continue` are commands delivered to one Agent goroutine. The routine accepts one execution command only when it is Free. Before `agent_start`, Core validates input, assigns Run identity and Context, and emits `agent_start`.

Agent Core does not own the external request queue. A direct caller may receive a typed busy/not-available error. A Host or application may hold the request, queue it, prioritize it, or choose a control action before dispatch.

A busy or invalid command creates no Run and emits no start Event.

## 2. Prompt and Continue

`Prompt` validates and commits a user Message before its first Model request, then runs the Loop and returns after Core execution settlement. `Continue` appends no user Message and is valid only when the transcript ends in a Model-continuable state such as a user or Tool Result Message. It never synthesizes user input.

The public Go call may wait on a result channel; Core keeps the execution unit and state authority private.

## 3. Normative algorithm

```text
Agent goroutine receives one Prompt or Continue command
create Run identity and Context
emit agent_start

if Prompt: commit and emit user Message

repeat:
  increment Turn and check Run Context/Core limits
  emit turn_start
  snapshot private Agent state
  transform and convert context
  resolve visible Tools
  open Model stream
  assemble one assistant Message
  commit assistant Message and lifecycle Events
  preflight Tool Calls in source order
  execute admitted Tools under configured Core bound
  finalize all admitted outcomes
  commit Tool Result Messages in source order
  emit turn_end
  process accepted control messages at defined boundaries
  continue or settle

emit one agent_end
send RunResult through the result channel
mark Agent execution Free
```

No step consults an external request queue, Conversation registry, Host resource, or platform state.

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

Preflight is source ordered. Sequential mode executes and completes source ordered. Bounded parallel mode limits active workers, reports completion Events in actual completion order, and commits results in source order. Every admitted Tool settles before `turn_end`.

Tool concurrency is inside one Agent execution. It does not allow a second Prompt to enter the same Agent goroutine.

## 6. ToolSet activation

Activation uses the ordinary Tool path and commits state after the current batch. Newly active Tools appear only in the next Model request. Activation is idempotent and cannot partially expose a failed ToolSet.

## 7. Steering and Follow-up

`Steer`, `FollowUp`, and `Abort` are control commands delivered through the Agent boundary:

```text
Orchestration / caller
        │ control channel
        ▼
Agent goroutine
```

Steering does not interrupt current Model or Tool work by default; the Agent consumes it at the defined safe boundary. Follow-up supplies a subsequent continuation command, not a general external request queue. Abort cancels the current Run. The Orchestrator decides whether an external request should become one of these controls.

## 8. Stop and cancellation

TurnStopper runs after `turn_end` and before continuation selection. A stop preserves the Turn and settles the Run. Every stage receives Run Context; cancellation is checked before admission, Turn, Model, Tool, queued continuation, and terminal observer dispatch. Committed state is not rolled back.

Cancellation may arrive through a Context or explicit channel command. It does not imply ownership of another Agent routine.

## 9. Settlement

```text
Busy → terminal decision
     → settle local owned work
     → emit agent_end
     → await terminal local observers
     → send RunResult
     → Free
```

No retry, Model call, Tool, or control-driven continuation starts after `agent_end`. Core does not wait for Host delivery or for unrelated Agent goroutines.

## 10. Failure behavior

Tool errors normally become failed Tool Results and allow continuation. Malformed Model protocol, fatal Model error, blocking Extension error, invariant failure, cancellation, deadline, and exhausted Core limit settle the Run.

A failure in another Agent routine is communicated as a result or Event. It does not automatically terminate this Agent unless the caller or Orchestrator chooses that policy.

## 11. Equivalence

Given equivalent initial Agent state, Model stream, Tools, options, and cancellation timing, Embedded and Hosted execution MUST produce the same canonical Event kinds/order, committed transcript, and terminal Core status. Protocol acknowledgements, queue policy, and delivery timing are not Core facts.
