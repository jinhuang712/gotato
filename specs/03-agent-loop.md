# 03. Agent Loop

**Status:** Draft

> Embedded APIs and remote services share one observable loop.

## 1. Canonical sequence

```text
accept Run
emit agent_start

repeat:
  emit turn_start
  transform and convert context
  stream and assemble assistant Message
  commit assistant Message

  resolve, validate, and preflight Tool Calls
  execute the Tool batch
  commit Tool Results in source order

  emit turn_end
  apply Steering Messages
  continue after Tool Results
  apply Follow-up Messages when otherwise complete

emit agent_end
```

Implementations MAY split this sequence into internal functions while preserving its Events and state transitions.

## 2. Prompt

`Prompt` MUST:

1. validate the input Message;
2. accept exclusive Agent execution;
3. emit `agent_start`;
4. commit and emit the user Message;
5. enter the canonical Turn loop;
6. return the terminal `RunResult` after `agent_end` settles.

## 3. Continue

`Continue` MUST enter the same loop without adding a user Message. The current model-facing transcript MUST end in a state eligible for continuation, such as a user or Tool Result Message.

## 4. Tool batch

Tool Calls enter preflight in assistant source order.

Sequential mode executes and emits results in source order.

Bounded parallel mode MUST:

```text
preflight in source order
respect the configured concurrency limit
emit completion Events as calls finalize
commit Tool Result Messages in source order
```

A Tool marked sequential MAY cause the batch to execute sequentially.

## 5. ToolSet activation

The built-in ToolSet activation Tool follows ordinary Tool execution semantics. Successful activation updates Agent state after the current Tool batch. Newly visible Tools enter the next Model request.

## 6. Steering

Steering Messages accepted during a Run MUST be queued in acceptance order and committed after the current Turn's Tool batch.

## 7. Follow-up

Follow-up Messages MUST be queued in acceptance order and committed when the Agent would otherwise complete.

## 8. Turn stopping

A Turn stopper runs after `turn_end`. A stop decision completes the Run before queue polling and another Model call.

## 9. Settlement

`agent_end` is the final Event. `Prompt`, `Continue`, and `WaitForIdle` MUST return after awaited terminal subscribers settle.
