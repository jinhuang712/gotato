# 08. Errors and Limits

**Status:** Draft

> Errors and bounds belong to the goroutine or coordinator that can act on them. Core limits one Agent execution; Orchestration limits the channel network.

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
agent_spawn_failure
limit_exceeded
cancelled
deadline_exceeded
internal_invariant_failure
```

`busy` means that one Agent goroutine cannot accept another execution command now. It does not prescribe whether the caller should wait, queue, reject, or start another Agent.

## 3. Error ownership

```text
Tool executor        failed Tool Result; current Agent may continue
Tool validation       failed/blocked Tool Result
Extension             terminal current Run failure by default
Model provider        retry inside current Run only when policy permits
Model protocol        terminal current Run failure
Spawn request         Result/Event; caller/Host policy decides continuation
Context cancellation  terminal cancelled/deadline for affected Run
Core limit            typed affected outcome; no new governed work
Invariant failure     terminal failure
Host admission        Host request outcome; no Agent command dispatched
Queue policy          caller/Host outcome; no Core state change
```

A Tool failure must not become a transport failure while the current Agent Run can continue. A failure in another Agent routine does not automatically terminate this Agent.

## 4. Panic boundaries

Panic recovery exists around Tool executors, Extensions, observers, Agent creation callbacks, Host callbacks, and Transport projection callbacks. A recovered panic is classified at its boundary, settles or reports according to policy, and never silently disappears.

## 5. Core limits

Core limits apply to one Agent goroutine and its current Run:

```go
type CoreLimits struct {
    MaxTurns uint32
    MaxToolCalls uint32
    MaxActiveToolSets uint32
    MaxVisibleTools uint32
    MaxParallelTools uint32
    MaxToolResultBytes uint64
    MaxToolProgressBytes uint64
    MaxToolProgressUpdates uint32
    RunDeadline time.Duration
    ModelCallDeadline time.Duration
    ToolCallDeadline time.Duration
}
```

Zero semantics must be documented per field; explicitly configured zero count/byte limits admit no work. Negative durations are invalid. Agent goroutine count, external Prompt queues, streams, fleet quotas, billing, and process memory are not Core limits.

## 6. Orchestration limits

An Orchestrator may separately limit:

```text
Agent goroutines
queued Prompts
active dispatched Runs
Spawn requests
Event delivery bridges
streams and clients
```

These bounds are enforced before dispatch or goroutine creation. They do not alter Core transcript or Loop semantics.

## 7. Admission points

Core checks:

```text
Agent execution command  before Run creation
Turns                    before next Turn
Tool Calls               before Tool Use
ToolSets                 before activation
Visible Tools            before Model request
Parallel Tools           before worker launch
Results/progress         before publication or commitment
Deadlines                before and during owned work
```

Orchestration checks:

```text
remote request           before queue or dispatch
queue capacity           before enqueue
Agent creation           before goroutine start
Stream/Event delivery    before bridge allocation or publication
```

Checks are atomic with respect to the goroutine or coordinator that owns the relevant bound.

## 8. Limit behavior

On Core limit exhaustion, the Agent stops admitting governed work, retains committed state, emits the applicable typed outcome, cancels active local work when policy requires, and settles the affected Run once.

On Host limit exhaustion, the Host rejects or queues according to its policy. It does not partially dispatch a request and then claim that the Agent accepted it.

Progress may truncate or coalesce; final outcomes remain authoritative.

## 9. Cancellation

Every blocking operation selects on its owning Context or control channel. Cancellation prevents future work and asks active work to settle; it does not roll back committed Messages or Events.

Cancellation of an independent Agent routine requires an explicit command. A spawn origin does not automatically create a cancellation tree.

## 10. Terminal classification

```text
normal completion   → completed
Abort/Context       → cancelled
deadline            → deadline_exceeded
Core limit          → limit_exceeded
fatal Model/Ext     → failed
invariant failure   → failed
```

The first selected terminal cause wins. Cancellation after a terminal decision does not replace a settled result.

## 11. Host mapping

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

The mapping must not reclassify Tool failures or independent Agent failures that remain Core Results.
