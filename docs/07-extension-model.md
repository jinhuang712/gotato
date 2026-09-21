# Core Extension Model

**Status:** Draft

> **Superseded in parts (see [PROPOSAL.md §5a](../PROPOSAL.md) and [MIGRATION.md](../MIGRATION.md)):** the Core extension points below still describe the runtime, but §8's Orchestration/Host components (`AgentFactory`, `ConversationResolver`, `RetirementPolicy`, and similar) are application-level service composition above the runtime, not Gotato components. The runtime has no Conversations separate from Sessions, no retirement, and no `Reset`; derived work is `session.Fork` plus another Run with lineage in `Session.Metadata`.

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

Blocking Extension failure settles the current Run. An Extension must not synchronously call `Prompt` or `Continue` on the same Agent from an awaited stage (Core defines no `Reset` transition; a different model view belongs to a `ContextBuilder` or `ContextTransformer`); that would re-enter the Agent execution unit.

Extensions may schedule application work only with an explicit Context and result channel. Unbounded or fire-and-forget goroutines are not permitted.

## 8. Service and Host policies are not Core Extensions

> **Superseded:** `AgentFactory`, `ConversationResolver`, `RetirementPolicy`, `AgentCache`, `EventProjector`, `DeliveryBridge`, `ErrorMapper`, and `DrainPolicy` are not typed Gotato components; service policy is ordinary application composition above the runtime ([PROPOSAL.md §5a](../PROPOSAL.md); [MIGRATION.md](../MIGRATION.md)).

The following belong to the service layer or Host:

```text
AgentSpec / Agent Registry
Session Store / Resolver
AdmissionController
Per-Run Agent Construction
Close Policy
EventProjector
DeliveryBridge
ErrorMapper
DrainPolicy
```

They surround Core operations, retain or close Agent handles, and coordinate multiple Agents. They are unnecessary for one directly held Agent except for explicit Core close, but required when Agents or Sessions must be found, coordinated, or stored. They do not alter Core transcript or Loop semantics.
