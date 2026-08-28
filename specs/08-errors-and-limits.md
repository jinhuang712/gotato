# 08. Errors and Limits

**Status:** Draft

> Errors are typed and safe; bounds stop new governed work at explicit admission points.

## 1. Error shape

```go
type RuntimeError struct {
    Code ErrorCode
    Operation string
    Message string
    Retryable bool
    Details map[string]string
    Cause error
}
```

`Message` is safe for Model and transport exposure. `Cause` is diagnostic and is not serialized by default. Details are bounded and non-secret. Wrapping preserves category and supports `errors.Is`/`errors.As`.

## 2. Core categories

Core provides categories equivalent to:

```text
invalid_argument
invalid_state
busy
model_failure
model_protocol_failure
tool_resolution_failure
tool_argument_validation_failure
tool_execution_failure
extension_failure
routine_failure
limit_exceeded
cancelled
deadline_exceeded
internal_invariant_failure
```

## 3. Error ownership

```text
Tool executor       failed Tool Result; Run may continue
Tool validation      failed/blocked Tool Result
Extension            terminal Run failure by default
Model provider       retry inside Run only when policy permits
Model protocol       terminal Run failure
Routine child        Routine Result; parent policy decides
Context cancellation terminal cancelled/deadline
Core limit           typed affected outcome; no new governed work
Invariant failure    terminal failure
```

Tool or Routine failure must not become a transport failure while the parent Core Run can continue.

## 4. Panic boundaries

Panic recovery exists around Tool executors, Extensions, observers, Routine tasks, Host callbacks, and Transport projection callbacks. A recovered panic is classified at its boundary, settles or reports according to policy, and never silently disappears.

## 5. Core limits

A Core Limits value may include:

```go
MaxTurns uint32
MaxToolCalls uint32
MaxActiveToolSets uint32
MaxVisibleTools uint32
MaxParallelTools uint32
MaxToolResultBytes uint64
MaxToolProgressBytes uint64
MaxToolProgressUpdates uint32
MaxRoutinesPerRun uint32
MaxConcurrentRoutines uint32
MaxRoutineDepth uint32
RunDeadline time.Duration
ModelCallDeadline time.Duration
ToolCallDeadline time.Duration
RoutineDeadline time.Duration
```

Zero semantics must be documented per field; explicitly configured zero count/byte limits admit no work. Negative durations are invalid. Service quotas, billing, fleet admission, and process memory are not Core limits.

## 6. Admission points

```text
Turns              before next Turn
Tool Calls         before Tool Use
ToolSets           before activation
Visible Tools      before Model request
Parallel Tools     before worker launch
Results/progress   before publication or commitment
Routines           before child creation/queue admission
Deadlines          before and during owned work
```

Checks are atomic with admission under the owner. No two children may pass a one-slot bound.

## 7. Limit behavior

On limit exhaustion Core stops admitting governed work, retains committed state, emits the applicable typed outcome, cancels active child work when policy requires, and settles the affected operation once. Progress may truncate or coalesce; final outcomes remain authoritative.

## 8. Cancellation

Every blocking operation selects on its owning Context. Cancellation prevents future work and asks active work to settle; it does not roll back committed Messages or Events. Child deadlines fit within parent deadlines.

## 9. Terminal classification

```text
normal completion   → completed
Abort/Context       → cancelled
deadline            → deadline_exceeded
Core limit          → limit_exceeded
fatal Model/Ext     → failed
invariant failure   → failed
```

The first selected terminal cause wins. Cancellation after a terminal decision does not replace a settled result.

## 10. Host mapping

A Host may map errors as:

```text
invalid input       → InvalidArgument
unknown Agent       → NotFound
busy/invalid state  → FailedPrecondition
admission/delivery  → ResourceExhausted
cancelled           → Canceled
deadline            → DeadlineExceeded
Model unavailable   → Unavailable
invariant           → Internal
```

The mapping must not reclassify Tool or Routine failures that remain Core Results.
