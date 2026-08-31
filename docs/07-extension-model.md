# Core Extension Model

**Status:** Draft

> Extensions add focused behavior at named Agent stages without taking over the Agent.

## 1. Why Extensions exist

The Core owns the Agent Loop and state transitions. Extensions add local behavior at explicit stages without requiring a fork of the Loop or a second framework:

```text
Core stage → Extension → Core continues
```

Extensions are installed during Agent construction. They receive read-only snapshots or stage-specific values, not a mutable Agent pointer or a protocol stream.

## 2. Extension points

```text
ContextTransformer
MessageConverter
PreToolUse
PostToolUse
EventObserver
TurnStopper
```

Each point has one responsibility and receives the owning Run Context. The exact package names may evolve; the stage boundaries are the contract.

## 3. Context and Message conversion

```text
read-only Agent snapshot
        ↓
ContextTransformer chain
        ↓
MessageConverter chain
        ↓
Model request
```

Transformers may select, add, prune, or compact the request context but cannot mutate committed history. Converters map runtime Messages to provider-neutral Model Messages and cannot store provider representations in Core transcript state.

## 4. Tool stages

Pre-Tool-Use runs after complete argument assembly, resolution, and Schema validation:

```text
Proceed
Block with Tool Result
```

Installed Pre components run in order until one blocks or fails. A blocked Tool is not executed and still passes through Post-Tool-Use.

Post-Tool-Use receives executed, blocked, failed, and cancelled outcomes. It runs in reverse installation order and may normalize safe content, redact, add bounded metadata, or attach a termination hint. It preserves Tool identity and `Executed` truth.

## 5. Event observers

An observer is local and bounded:

```text
create Event → observer A → observer B → Core continues
```

It must be fast and Context-aware. A blocking observer may hold Core at its declared boundary; an advisory observer must not block the Loop. Remote delivery belongs to the Host, not to an Extension.

## 6. Turn stopping

A TurnStopper runs after `turn_end` and before continuation selection. It can settle the Run while preserving the completed Turn and its Events. A stopper error is blocking by default.

## 7. Ordering and failure

```text
Pre extensions:  A → B → C
Tool executor:   at most once
Post extensions: C → B → A
Observers:       registration order
```

Blocking Extension failure settles the current Run. An Extension must not synchronously call `Prompt`, `Continue`, or `Reset` on the same Agent from an awaited stage; that would re-enter the Agent execution unit.

Extensions may schedule application work only with an explicit Context and result channel. Unbounded or fire-and-forget goroutines are not permitted.

## 8. Host policies are not Core Extensions

The following belong to Host / Orchestration:

```text
AgentFactory
ConversationResolver
AdmissionController
AgentCache
EventProjector
DeliveryBridge
ErrorMapper
DrainPolicy
```

They surround Core operations and coordinate Agents. They do not alter Core transcript or Loop semantics.
