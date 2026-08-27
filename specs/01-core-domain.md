# 01. Core Domain

**Status:** Draft

> Agent owns state. Run owns activity. Turn owns one Model response and its Tool batch. Agent Routine owns a child Run relationship.

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

## 2. Run

A Run begins when `Prompt` or `Continue` is accepted and ends after terminal `agent_end` delivery settles.

Every Run MUST have an immutable correlation identifier unique within the process lifetime. Service transports MAY add their own external identifier while retaining the Core Run ID.

The embedded Run produces a `RunResult` containing final assistant output, usage, terminal status, Run ID, and correlation data.

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

A Run reaches a terminal result through:

```text
Model completion with no continuation
all Tool Results request termination
TurnStopper decision
Context cancellation
deadline or local limit
fatal Model, Extension, or runtime failure
```

Tool execution failures normally become failed Tool Results and allow the next Model Turn.

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

An Agent factory creates or retrieves one Agent per isolated conversation. The service layer manages external Run identity, admission, caching, and transport lifetime around this Core state model.
