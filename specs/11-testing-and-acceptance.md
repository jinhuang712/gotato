# 11. Testing and Acceptance

**Status:** Draft

> Test the atomic Agent Core first, then test the Orchestration path for multiple Agents and the Host/platform boundaries separately.

## 1. Test surfaces

```text
Core tests
  scripted Model · Tools · conversation state · Events · cancellation

Agent execution tests
  single-flight work · results · control · close · optional spawning

Orchestration / Host tests
  identity · handle retention · Agent creation · routing
  admission · coordination · delivery · drain

Adapter tests
  LLM normalization · Tool mapping · protocol mapping

Platform tests
  existing HTTP/gRPC service · Gateway/Kubernetes compatibility
```

Platform tests must not be prerequisites for Core acceptance. Core tests must not require a live provider, network, registry, broker, database, or deployment platform.

## 2. Core testkit

The testkit MUST include scripted Model streams, succeeding/failing/panicking/cancellable Tools, Event recorders, Extension fixtures, Agent handles, fake clocks, deterministic ID generation, and explicit result boundaries.

It SHOULD make the first single-Agent scenario easy to test without constructing Orchestration, Host, or a persistence service.

## 3. Core acceptance

Tests MUST prove:

```text
one Agent execution unit processes one Prompt at a time
Model → Tool → Model continuation
malformed arguments never execute
Tool failures become Tool Results
Continue adds no user Message
Steering and Follow-up control messages
stopper prevents the next Model call
one final agent_end
Run settlement leaves a retained Agent usable
retry remains inside one Run
Agent state is mutated only by its execution unit
```

Tests MUST NOT require Core to choose an external Prompt queue policy.

## 4. Tool and Extension acceptance

Tests MUST prove Tool validation, at-most-once execution, blocked outcomes, Pre order, Post reverse order, bounded parallel completion versus source-order commitment, optional ToolSet activation, deterministic visibility, Context-aware cancellation, and blocking/advisory Extension behavior.

## 5. Event acceptance

Tests MUST prove Event sequence, class, correlation, terminal ordering, Protected Event preservation, progress coalescing, bounded observation and delivery, independent Agent execution/delivery settlement, disconnect behavior, and bounded drain.

## 6. Agent execution acceptance

Tests MUST prove:

```text
an Agent is callable through the Core interface
private conversation state is isolated
one execution settles one Run exactly once
cancellation requires an explicit signal or selected policy
spawn creates an independent Agent when enabled
another Agent's failure does not implicitly terminate this one
an Agent can be revisited through the retained handle or external key mapping
Close rejects new Runs and is idempotent
Close during Busy settles or cancels exactly once
Done closes exactly once when supported
retirement and Conversation retention follow their declared policy
Core and Orchestration/Host bounds are enforced at their owning boundary
```

Tests MUST NOT assume an automatic parent/child resource hierarchy.

## 7. Orchestration and Host acceptance

Tests MUST prove:

```text
multiple Agents can execute concurrently
one Agent receives no second Prompt while Busy
configured queue and rejection policies work
configured priority, Steer, and Abort policies work
Agent creation, retirement, and admission are bounded
request/result correlation survives interface hops
retained Conversations rehydrate with a new AgentID without duplicate live handles
route generation fencing rejects stale-handle dispatch
Event delivery is bounded and preserves Protected Events
readiness and drain work within a deadline
```

These are Orchestration/Host policies, not innate Core behavior. The test must also prove that multiple Agents cannot be revisited without retained handles or a key-to-handle mapping.

## 8. Adapter acceptance

LLM Adapter tests MUST prove provider streams normalize into the Model contract, provider failures retain classification, and Core remains independent of provider types.

Tool Adapter tests MUST prove external protocol and authentication stay outside Core. Protocol adapter tests MUST prove wire commands and Events preserve Core identity, ordering, correlation, and settled meaning. Close commands MUST distinguish delivery-stream closure from Core Agent closure.

## 9. Embedded and Hosted equivalence

The same scripted scenario through direct Core, application Orchestration, and Hosted paths must yield the same canonical Event sequence, conversation commitment, limits, and terminal status after the command reaches the Agent. Queue policy, handle lookup timing, dispatch timing, protocol acknowledgement, and delivery timing are excluded from Core equivalence.

## 10. Quality gates

```text
gofmt
go vet
go test ./...
go test -race ./...
```

A deterministic test uses fixed IDs, fixed clocks, explicit scripts, channel synchronization instead of sleeps, execution settlement instead of arbitrary waits, and restores every fixture-owned resource. Integration failures must be distinguishable from Core contract failures.
