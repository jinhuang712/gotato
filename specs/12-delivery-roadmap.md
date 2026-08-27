# 12. Delivery Roadmap

**Status:** Draft

> Every slice ends in something a client can call. Runtime contracts are proven by exercising them remotely, and the public library is extracted from contracts that survived that exercise.

```text
callable service → observed need → runtime contract → acceptance test
```

A slice that only adds interfaces is not complete. A slice completes when a client can do something it could not do before, and deterministic tests hold the behavior in place.

## Structural invariants

These hold from the first commit of code, not from a later cleanup slice:

```text
runtime packages import no Protobuf, gRPC, cache, or hosting package
wire types are mapped at the boundary, never used as domain types
one Agent loop exists
each Run emits exactly one terminal Event
every queue, batch, stream, and Routine has a stated bound
```

Retrofitting any of these is expensive enough that the first slice must already satisfy them.

## Slice 1 — A callable Run

```text
runtime      Agent state · Prompt · Model stream · Message assembly
             canonical Events · Run Context cancellation
service      static Agent registration · ephemeral Agent per request
             bounded Event bridge
transport    Protobuf contract · gRPC server · Go client
             Start and Cancel commands
testkit      scripted Model · Event recorder · fake clock · in-process gRPC
```

**Exit:** a Go client opens a stream, sends `Start`, receives ordered lifecycle and Message Events ending in one terminal Event, and cancels a Run mid-stream so cancellation reaches the scripted Model.

This slice is the smallest thing that is genuinely a hosted Agent, and it already forces decisions on Event projection, stream ownership, terminal signalling, and cancellation mapping.

## Slice 2 — The Tool loop

```text
runtime      Tool Call assembly · Schema validation
             Tool Use at most once · Tool Result commitment
             Model → Tool → Model continuation
             failed Tool Results as reasoning input
service      Tool Event projection
testkit      succeeding, failing, panicking, streaming, cancellable Tools
```

**Exit:** a remote client observes a multi-Turn Run where the Model calls Tools, receives their results, and continues. Malformed streamed arguments never reach an executor.

Without this slice the service is a Model proxy, not an Agent runtime.

## Slice 3 — Interaction and delivery policy

```text
runtime      Steering and Follow-up queues · continuation order
             TurnStopper · bounded parallel Tool batches
service      Steer · FollowUp commands
             Event classes · coalescing · queue-full policy
             slow-consumer behavior · delivery settlement
```

**Exit:** a client steers a Run in flight and queues a follow-up; a deliberately slow client triggers the documented queue-full policy without dropping a protected Event and without stalling an unrelated Run.

Delivery policy belongs here rather than later. A bridge whose behavior under load is undefined is not shippable, and defining it after clients depend on it is a breaking change.

## Slice 4 — Stateful conversations

```text
service      AgentFactory contract · conversation keys
             bounded Agent cache · per-key creation coordination
             active-Run pinning · idle-only eviction · explicit reset
             Run admission · typed rejection
```

**Exit:** two conversations run concurrently with isolated state; a second request on one conversation reaches the same Agent with its transcript intact; a concurrent mutating Run on one Agent is rejected with a typed busy error; cache eviction never removes a pinned entry.

## Slice 5 — Composition

```text
runtime      individual Tools and staged ToolSets · activation Tool
             ContextTransformer · MessageConverter
             PreToolUse · PostToolUse · EventObserver
             Agent Routine identity · lifecycle · bounded groups
service      ToolSet and Routine Event projection
             child Agent factories
```

**Exit:** a Model discovers a capability domain, activates it, and calls a concrete Tool on the next Turn; a parent Agent spawns bounded child Agents, receives correlated Results, and one client cancellation collapses the entire ownership tree.

## Slice 6 — Production baseline

```text
service      readiness · liveness · graceful drain · DrainPolicy
             structured errors · service metrics
runtime      stable typed errors · local limits · panic boundaries
deployment   health probes · shutdown handling · deployment example
             resource and autoscaling guidance · observability example
```

**Exit:** a replicated deployment serves concurrent conversations, a rollout drains without severing an in-flight Run before its deadline, and race tests pass across queues, cache, bridges, and Routines.

## Slice 7 — Core extraction

The runtime begins as an internal boundary. Publishing it is a separate act with its own preconditions.

```text
promote runtime packages to a public API
add a direct in-process consumer and examples
publish an independent testkit
semantic versioning and compatibility commitment
```

**Entry conditions**, all required:

```text
the hosted service exercises the full Model → Tool → Model loop
a direct Go consumer runs the same loop with no service present
both produce identical canonical Events for equivalent input
both share one cancellation model and one terminal Event
the service adds no Agent state machine of its own
no wire type appears in a runtime signature
runtime acceptance tests need no network
at least one real Agent has shipped against the contracts
```

**Exit:** an external Go program embeds the runtime directly, and the service and that program depend on the same published contracts.

The second consumer is the point. A single caller cannot distinguish a general contract from one shaped around its own convenience.

## Ongoing — Ecosystem

Model adapters, capability adapters, Extensions, an optional HTTP projection, external state, remote Agent Routine execution, and durable Runs proceed as independent packages, each backed by a concrete use case.
