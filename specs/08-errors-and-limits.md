# 08. Errors and Limits

**Status:** Draft

> Errors have stable categories and safe messages. Limits stop new governed work at an explicit admission point; they never produce an unbounded partial execution.

## 1. Runtime error shape

The implementation SHOULD expose a typed error equivalent to:

```go
type ErrorCode string

type RuntimeError struct {
    Code      ErrorCode
    Operation string
    Message   string
    Retryable bool
    Details   map[string]string
    Cause     error
}
```

`Message` is safe for Model and transport exposure. `Cause` is application-controlled diagnostic state and MUST NOT be serialized by default. `Details` contains bounded, non-secret structured values.

Errors SHOULD support `errors.Is` and `errors.As`. Wrapping an error MUST preserve its category. A transport mapper MAY add status details but MUST NOT replace the Core code.

## 2. Error codes

Core MUST provide stable categories equivalent to:

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

Implementations MAY use separate Go sentinel values for `invalid_state` and `busy`, but the service mapping MUST distinguish a busy Agent from an arbitrary invalid operation.

## 3. Error ownership matrix

| Origin | Model-facing result | Run effect |
|---|---|---|
| Tool executor | failed Tool Result | continue when loop is valid |
| Tool resolution | failed/blocked Tool Result | continue unless protocol state is corrupt |
| Tool Schema validation | failed Tool Result | continue |
| Pre/Post Extension | safe Extension error | terminal by default |
| Model provider | Model error | retry only inside Run, otherwise terminal |
| Model stream protocol | protocol error | terminal |
| Routine child Run | Routine Result | parent policy decides |
| Context cancellation | no fabricated Tool result | terminal cancelled/deadline |
| limit admission | typed limit result | no governed work starts |
| Runtime invariant | internal error | terminal |

A Tool failure MUST NOT be converted into a transport-level failure while the parent Run can continue reasoning. A protocol or invariant failure MUST NOT be hidden as Tool content.

## 4. Safe diagnostics

Public messages MUST NOT include credentials, authorization headers, raw provider responses, unrestricted Tool payloads, or stack traces. An application MAY attach a redacted diagnostic cause to logs and traces.

Panic recovery MUST exist at these boundaries:

```text
Tool executor
Extension stage
Event observer/subscriber
Routine task
service callback
transport projection callback
```

A recovered panic is classified at the boundary that caught it. It MUST settle the owning operation or produce the configured safe advisory result; it MUST NOT silently disappear.

## 5. Limits shape

The Runtime SHOULD accept a value equivalent to:

```go
type Limits struct {
    MaxTurns                 uint32
    MaxToolCalls             uint32
    MaxActiveToolSets        uint32
    MaxVisibleTools          uint32
    MaxParallelTools         uint32
    MaxToolResultBytes       uint64
    MaxToolProgressBytes     uint64
    MaxToolProgressUpdates   uint32
    MaxRoutinesPerRun        uint32
    MaxConcurrentRoutines    uint32
    MaxRoutineDepth          uint32
    RunDeadline              time.Duration
    ModelCallDeadline        time.Duration
    ToolCallDeadline         time.Duration
    RoutineDeadline          time.Duration
}
```

Each limit MUST have a defined zero meaning. For count and byte limits, zero means "no work admitted" when the field is explicitly configured; an omitted option uses the preset default. A negative duration is invalid. A zero duration means no deadline only where the option explicitly documents that behavior.

Service-wide quotas, billing budgets, process memory, and fleet admission are not local Runtime limits.

## 6. Admission points

Limits are enforced before the work they govern:

```text
MaxTurns               before next Turn
MaxToolCalls           before next Tool Use
MaxActiveToolSets      before activation commit
MaxVisibleTools        before Model request
MaxParallelTools       before worker launch
MaxToolResultBytes     before final result commit
MaxToolProgress...     before progress publication
MaxRoutinesPerRun      before child creation
MaxConcurrentRoutines  before child queue admission
MaxRoutineDepth        before child factory call
Deadlines              before and during owned operation
```

A limit check MUST be atomic with admission under the owning Run coordinator. Two concurrent child or Tool requests MUST NOT both pass a bound that only has one remaining slot.

## 7. Limit behavior

When a limit is reached, the Runtime MUST:

1. stop admitting new work governed by that limit;
2. retain already committed state;
3. emit the applicable typed failure Event or outcome;
4. cancel active child work when the policy requires it;
5. settle the affected Tool, Routine, or Run exactly once.

Progress-volume limits MAY truncate or coalesce optional updates. They MUST preserve the final Tool or Routine outcome and MUST NOT turn a protected lifecycle Event into progress.

## 8. Cancellation and deadlines

Every owned operation receives the nearest active Context and MUST select on it while blocking. Child deadlines MUST fit within the parent deadline; a child MUST NOT extend parent lifetime by deriving a longer deadline.

Cancellation is idempotent. It prevents future admission and asks active Model, Tool, Extension, observer, and Routine work to settle. It does not roll back committed transcript or Event history.

## 9. Terminal classification

The first terminal cause selected by the Runtime determines the Run status, subject to the configured drain policy:

```text
normal completion       → completed
explicit Abort          → cancelled
Context cancellation    → cancelled
deadline expiry         → deadline_exceeded
local limit exhaustion  → limit_exceeded
fatal Model/Extension   → failed
invariant failure       → failed
```

A cancellation received after a normal terminal decision MUST NOT replace an already settled result.

## 10. Service mapping

The default gRPC mapping is:

```text
invalid input / command       → InvalidArgument
unknown Agent                 → NotFound
busy / invalid state          → FailedPrecondition
admission or delivery limit  → ResourceExhausted
cancelled                     → Canceled
deadline exceeded             → DeadlineExceeded
Model unavailable             → Unavailable
internal invariant            → Internal
```

Tool and Routine failures remain Runtime Results and Events when parent reasoning can continue. Shared quotas and fleet-wide rejection remain service concerns.
