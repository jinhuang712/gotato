# 11. Testing and Acceptance

**Status:** Draft

> Test Agent goroutines with deterministic channels, Orchestration with request policy, and Infrastructure separately.

## 1. Test layers

```text
Core tests
  scripted Model · Tools · Agent channels · Events · fake Context/clock

Agent routine tests
  single-flight execution · spawn · result channels · cancellation

Host tests
  Agent creation · admission · queues · routing · delivery · drain
  in-process gRPC and slow consumers

Integration tests
  provider adapters · capability services · Gateway/Kubernetes
```

Infrastructure tests must not be prerequisites for Core acceptance.

## 2. Core testkit

The testkit MUST include scripted Model streams, succeeding/failing/panicking/cancellable Tools, ToolSet fixtures, Event recorders, Extension fixtures, Agent channel probes, independent Agent factories, fake clocks, deterministic ID generation, and explicit result channels.

## 3. Core acceptance

Tests MUST prove:

```text
one Agent goroutine processes one Prompt at a time
second direct execution command is not processed concurrently
Model → Tool → Model continuation
malformed arguments never execute
Tool failures become Tool Results
Continue adds no user Message
Steering and Follow-up control messages
stopper prevents next Model call
exactly one final agent_end
retry remains inside one Run
Agent state is mutated only by its goroutine
```

Tests MUST NOT require Core to choose an external Prompt queue policy.

## 4. Tool and Extension acceptance

Tests MUST prove Tool validation, at-most-once execution, blocked outcomes, Pre order, Post reverse order, bounded parallel completion versus source-order commitment, ToolSet activation, deterministic visibility, channel-aware cancellation, and blocking/advisory Extension behavior.

## 5. Event acceptance

Tests MUST prove Event sequence, class, correlation, terminal ordering, Protected Event preservation, progress coalescing, bounded channel/queue policy, observer bounds, independent Agent execution/delivery settlement, disconnect behavior, and bounded drain.

## 6. Agent Routine acceptance

Tests MUST prove:

```text
an Agent Routine is a goroutine-backed Agent
independent Agent routines have isolated state
spawn creates a new Agent routine
spawn provenance is correlation, not ownership
Agent-to-Agent communication uses explicit channels
one routine settles one current Run exactly once
cancellation requires an explicit signal or selected policy
another routine's failure does not implicitly terminate this one
routine and Agent bounds are enforced at their owning layer
```

Tests MUST NOT assume an automatic parent/child resource hierarchy.

## 7. Orchestration acceptance

Tests MUST prove:

```text
multiple Agent goroutines execute concurrently
one Agent receives no second Prompt while Busy
FIFO queue policy works when configured
reject-while-Busy policy works when configured
priority policy works when configured
safe-boundary Steer policy works when configured
immediate Abort policy works when configured
spawn and Agent creation are bounded
request/result correlation survives channel hops
```

These are Orchestration policies, not innate Core Agent behavior.

## 8. Host acceptance

Tests MUST prove:

```text
remote text Run
Tool/ToolSet/Agent Routine Event projection
Start ordering and duplicate/post-terminal errors
Steer/FollowUp delivery
remote cancellation propagation
isolated process-local Conversation routing
cache hit and idle-only eviction
bounded admission and Event delivery
readiness and drain
error-to-status mapping
```

The initial PoC tests one Host process in one Pod. Cross-Pod continuity is intentionally out of scope; its tests belong to the reserved Multi-Pod Conversation Routing design.

## 9. Equivalence acceptance

The same scripted scenario through direct Core and Hosted paths must yield the same canonical Event sequence, transcript, limits, and terminal status after the same command is dispatched to the Agent. Queue policy, dispatch timing, transport acknowledgement, and delivery timing are excluded from Core equivalence.

## 10. Quality gates

```text
gofmt
go vet
go test ./...
go test -race ./...
```

A deterministic test uses fixed IDs, fixed clocks, explicit scripts, channel synchronization instead of sleeps, execution settlement instead of arbitrary waits, and restores every fixture-owned resource. Integration failures must be distinguishable from Core contract failures.
