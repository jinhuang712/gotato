# Agent Routines

**Status:** Draft

> An Agent Routine is a managed child Agent Run inside Core. Remote placement is a Host concern, not a second execution model.

## 1. Meaning

```text
Agent Routine
  = child Agent + child Run + parent Context
  + identity + bounds + settled Result
```

A goroutine is only a local scheduling mechanism. A Routine owns lifecycle, correlation, cancellation, and settlement.

## 2. Core composition

```text
Parent Run
   │ spawn
   ▼
Routine
   ▼
Distinct child Agent → canonical child Run
```

The child and its siblings have separate Agent instances and transcripts. They may share immutable adapters and configuration.

## 3. Spawn sources

Applications may spawn directly through a Routine coordinator. A Model may spawn through an ordinary `spawn_agent` Tool:

```text
Parent Tool Call → PreToolUse → Routine → child Run
                → Routine Result → PostToolUse → parent Tool Result
```

Both paths use the same Core loop.

## 4. Lifecycle

```text
Created → Queued → Running
                    ├── Completed
                    ├── Failed
                    └── Cancelled
```

A Routine settles exactly once. Repeated waits return the same immutable Result. A child `agent_end` is not a substitute for the parent-facing Routine terminal Event.

## 5. Cancellation

```text
Parent Run Context
       ▼
Routine Context
       ▼
child Model · Tools · Extensions · nested Routines
```

Parent cancellation reaches every owned child. A Routine may have a shorter deadline and an idempotent local cancellation handle.

## 6. Bounds

Core or the Routine coordinator enforces:

```text
Routines per parent Run
concurrently active Routines
maximum nesting depth
child deadline
child Turns and Tool Calls
result and progress volume
```

Rejected spawn does not create child work or emit `routine_started`.

## 7. Routine Groups

A bounded Group coordinates siblings without becoming a workflow engine:

```text
collect all · fail fast · collect partial · first success
```

Group Results are normally in spawn order. Completion Events follow actual completion order. The Group waits for already-started children to settle before releasing resources.

## 8. Events and correlation

Parent-facing lifecycle Events include:

```text
routine_started
routine_completed
routine_failed
routine_cancelled
```

They carry Routine ID, parent Run ID, child Run ID, and name. Detailed child Events remain scoped to the child Run and may be separately projected by a Host.

## 9. Local and remote placement

Local:

```text
Parent Core → local Routine coordinator → child Core
```

Future remote:

```text
Parent Host/Core → Routine Executor → remote Agent Host → child Core
```

Remote execution must preserve identity, cancellation, limits, Events, and single settlement. Remote placement is an orchestration/adapter feature and cannot redefine Routine semantics.

## 10. Ownership

```text
Core              Agent, Run, Context, canonical child Events
Routine package   spawn, groups, identity, limits, Results
Host              remote placement and cross-process projection
Application       child definitions and coordination intent
```
