# Core Extension Model

**Status:** Draft

> Extensions customize named stages of an Agent goroutine without taking over its private state or Orchestration.

## 1. Core extension points

```text
ContextTransformer
MessageConverter
PreToolUse
PostToolUse
EventObserver
TurnStopper
```

Extensions are explicit Go components installed during Agent construction. They do not receive a mutable Agent pointer or a transport stream.

## 2. Context and Message conversion

```text
read-only Agent snapshot
        ↓
ContextTransformer chain
        ↓
MessageConverter chain
        ↓
Model request
```

Transformers may select, add, prune, or compact context but cannot mutate committed history. Converters map runtime Messages to provider-neutral Model Messages and cannot store provider representations in Core transcript state.

## 3. Tool stages

Pre-Tool-Use runs after complete argument assembly, resolution, and Schema validation:

```text
Proceed
Block with Tool Result
```

All installed Pre components run in order until one blocks or fails. A blocked Tool is not executed and still passes through Post-Tool-Use.

Post-Tool-Use receives executed, blocked, failed, and cancelled outcomes. It runs in reverse installation order and may normalize safe content, redact, add metadata, enforce bounds, or attach a termination hint. It must preserve Tool identity and `Executed` truth.

## 4. Event observers

An observer is local and awaited:

```text
create Event → observer A → observer B → Core continues
```

It must be fast, Context-aware, and bounded. Remote delivery is not an observer; it belongs to the Host Event Bridge. Observer failure uses an explicit blocking or advisory mode. Panics are recovered at the boundary.

## 5. Turn stopping

A TurnStopper runs after `turn_end` and before continuation selection. It can settle the Run while preserving the completed Turn and its Events. A stopper error is blocking by default.

## 6. Ordering

```text
Pre extensions:  A → B → C
Tool executor:   at most once
Post extensions: C → B → A
Observers:       registration order
```

The order is deterministic in Embedded and Hosted modes.

## 7. Failure and reentrancy

Blocking Extension failure settles the current Run. Tool execution failure follows Tool Result semantics. An Extension must not synchronously call `Prompt`, `Continue`, or `Reset` on the same Agent from an awaited stage; that would re-enter the same Agent goroutine.

Extensions may schedule external application work only with an explicit Context and result channel. Unbounded or fire-and-forget goroutines are not permitted.

## 8. Hosted policies are not Core Extensions

The following belong to Orchestration, not Core:

```text
AgentFactory
ConversationResolver
AdmissionController
AgentCache
EventProjector
EventBridge
ErrorMapper
DrainPolicy
```

They surround Core operations and cannot alter Core transcript or Loop semantics.

## 9. Official extension packages

OpenTelemetry, structured logging, context compaction, authorization integration, approval integration, cost accounting, and Model routing may be packaged independently. They enter through the Core or Host boundary that owns their semantics.
