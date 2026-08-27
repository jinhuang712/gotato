# 01. Core Domain

**Status:** Draft

> Agent owns state. Run owns activity. Turn owns one Model response and its Tool batch. Agent Routine owns a child Run relationship. The service owns how Agents are hosted.

```text
Agent
  └── active Run
        ├── Turn 1 → Model → Tools
        ├── Turn 2 → Model → Agent Routine
        └── Turn 3 → Model → finish
```

## 1. Agent

An `Agent` MUST own or reference:

```text
system instructions
current Model
Message history
individual Tools
registered ToolSets
active ToolSets
runtime options
Steering and Follow-up queues
streaming state
```

An Agent MUST serialize state mutations through one active Run. State inspection MUST use snapshots or equivalent read-only views.

The service decides how Agent instances are created, retained, and located. The runtime owns the consistency of each instance.

## 2. Run

A Run begins when `Prompt` or `Continue` is accepted and ends at execution settlement: the terminal Event has been emitted and awaited observers have returned.

Every Run MUST have an immutable correlation identifier unique within the process lifetime. The service and transport MAY add request, stream, and external identifiers; added identifiers MUST NOT replace the runtime Run ID.

The Run produces a `RunResult` containing final assistant output, usage, terminal status, Run ID, and correlation data.

Whether a remote consumer received the Event stream is delivery settlement. It is owned by the service and is independent of Run completion.

## 3. Turn

A Turn begins before one Model call and ends after its assistant Message and requested Tool executions are finalized.

A Run contains one or more Turns. Turns MUST have deterministic sequence numbers within the Run.

## 4. Tool Call and Tool Use

A Tool Call is the Model-produced request. A Tool Use is the Runtime execution attempt created after resolution and validation.

Each Tool Use MUST retain:

```text
Run ID
Turn sequence
Tool Call ID
qualified Tool ID
validated arguments
execution and block status
final outcome
```

## 5. State invariants

- The accepted Prompt Message MUST be committed before its Model Turn.
- An assistant Message MUST be committed before Pre-Tool-Use.
- A Tool Result MUST reference a Tool Call from the current assistant Message.
- Batch Tool Results MUST be committed in assistant source order.
- ToolSet activation MUST affect the next Model Turn.
- Terminal Runs MUST start no new Model, Tool, or Agent Routine work.
- `Reset` and Run mutation MUST be mutually exclusive.
- Snapshots MUST isolate internal slices and maps from caller mutation.

## 6. Completion

A Run reaches its terminal result through:

```text
Model completion with no continuation
all Tool Results request termination
TurnStopper decision
Context cancellation
deadline or local limit
fatal Model, Extension, or runtime failure
```

Tool execution failures normally become failed Tool Results and allow the next Model Turn.

A transient Model failure MAY be retried inside the Run. Retry, compaction, and queued continuation MUST all complete before the terminal Event; nothing resumes execution after it.

## 7. Agent Routine relationship

An Agent Routine records:

```text
Routine ID
Routine name
parent Run ID
child Run ID
Routine status
Routine Result
```

The child Run uses a distinct child Agent and retains normal Agent, Run, Turn, Tool, and Event semantics.

## 8. Service relationship

The service layer owns external Run identity, admission, conversation resolution, caching, transport lifetime, and delivery around this Core state model.

It invokes the canonical runtime API. It MUST NOT maintain a second Agent state machine or reproduce loop behavior.
