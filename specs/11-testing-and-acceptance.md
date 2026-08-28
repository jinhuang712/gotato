# 11. Testing and Acceptance

**Status:** Draft

> Test Core without networks, Host with in-process transport, and Infrastructure separately.

## 1. Test layers

```text
Core tests
  scripted Model · Tools · Events · fake Context/clock

Host tests
  factories · admission · routing · cache · bridge · drain
  in-process gRPC and slow consumers

Integration tests
  provider adapters · capability services · Gateway/Kubernetes
```

Infrastructure tests must not be prerequisites for Core acceptance.

## 2. Core testkit

The testkit MUST include scripted Model streams, succeeding/failing/panicking/cancellable Tools, ToolSet fixtures, Event recorders, Extension fixtures, child Agent factories, fake clocks, and deterministic ID generation.

## 3. Core acceptance

Tests MUST prove:

```text
text Run completes in one Turn
Model → Tool → Model continuation
malformed arguments never execute
Tool failures become Tool Results
Continue adds no user Message
Steering and Follow-up order
stopper prevents next Model call
exactly one final agent_end
retry remains inside one Run
```

## 4. Tool and Extension acceptance

Tests MUST prove Tool validation, at-most-once execution, blocked outcomes, Pre order, Post reverse order, bounded parallel completion versus source-order commitment, ToolSet activation, deterministic visibility, and blocking/advisory Extension behavior.

## 5. Event acceptance

Tests MUST prove Event sequence, class, correlation, terminal ordering, protected-event preservation, progress coalescing, bounded queue policy, observer bounds, independent execution/delivery settlement, disconnect behavior, and bounded drain.

## 6. Routine and concurrency acceptance

Tests MUST prove child Agent isolation, single settlement, cancellation propagation, Routine bounds, lifecycle correlation, completion order versus Result spawn order, group policies, per-Agent busy rejection, Tool concurrency bounds, and race-safe queues/subscribers.

## 7. Host acceptance

Tests MUST prove:

```text
remote text Run
Tool/ToolSet/Routine Event projection
Start ordering and duplicate/post-terminal errors
Steer/FollowUp delivery
remote cancellation propagation
isolated conversations
cache hit and idle-only eviction
bounded admission and Event delivery
readiness and drain
error-to-status mapping
```

Tests must distinguish process-local cache guarantees from cross-Pod continuity. A multi-Pod test requires an explicit sticky-routing, distributed-owner, or durable-state fixture.

## 8. Equivalence acceptance

The same scripted scenario through direct Core and Hosted paths must yield the same canonical Event sequence, transcript, limits, and terminal status. Transport acknowledgement and delivery timing are excluded from Core equivalence.

## 9. Quality gates

```text
gofmt
go vet
go test ./...
go test -race ./...
```

A deterministic test uses fixed IDs, fixed clocks, explicit scripts, execution settlement rather than sleeps, and restores every fixture-owned resource. Integration failures must be distinguishable from Core contract failures.
