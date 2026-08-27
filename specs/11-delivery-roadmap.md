# 11. Delivery Roadmap

**Status:** Draft

> Build vertically from kernel contracts to remote service access.

```text
Phase 1: contracts → text loop → Tool loop → Moving Parts → ToolSets → Routines → hardening
Phase 2: Agent factory → cache → service preset → gRPC → Kubernetes lifecycle
Phase 3: adapters → optional state → durable execution as required
```

## Phase 1 — Go Agent kernel

### Milestone 1.1: Contracts and testkit

```text
Message and Event types
Model and Tool contracts
scripted Model
recording Tool
Event recorder
Agent state
```

**Exit:** text and Tool scenarios can be specified entirely with fakes.

### Milestone 1.2: Streamed text loop

```text
Prompt
Model stream assembly
assistant Message commitment
Agent, Turn, and Message Events
Abort and WaitForIdle
```

**Exit:** a text-only Run has deterministic state and Event order.

### Milestone 1.3: Tool loop

```text
Tool Call assembly
JSON Schema validation
Tool execution and errors
progress Events
Model → Tool → Model continuation
```

**Exit:** one- and multi-Turn Tool scenarios pass.

### Milestone 1.4: Moving Parts

```text
ContextTransformer
MessageConverter
PreToolUse
PostToolUse
EventObserver
TurnStopper
sequential and bounded parallel Tool batches
Steering and Follow-up
```

**Exit:** the Pi-semantic compatibility matrix and Moving Parts ordering tests pass.

### Milestone 1.5: ToolSets

```text
ToolSet composition
qualified identity
activation Tool
staged visibility
active ToolSet limits
typed-function adapter
```

**Exit:** a Model can discover Grafana, activate it, and call a concrete Grafana Tool on the next Turn.

### Milestone 1.6: Agent Routines

```text
Routine identity and lifecycle
child Agent factory
Context cancellation tree
bounded Routine Group
Routine Events
spawn_agent Tool example
```

**Exit:** a parent Agent spawns bounded child Agents, receives correlated Results, and cancels the complete tree through one Context.

### Milestone 1.7: Kernel hardening

```text
stable errors
local limits
panic boundaries
race tests
Go documentation
embedded examples
API review
```

**Exit:** the embedded kernel meets the phase-one acceptance specification.

## Phase 2 — Agent as a Service

### Milestone 2.1: Service state

```text
Agent factory
bounded in-process Agent cache
TTL and idle eviction
active-Run pinning
static Agent registration
admission bounds
```

### Milestone 2.2: Service lifecycle

```text
bounded Event bridge
readiness
drain
structured errors
service metrics
```

### Milestone 2.3: gRPC

```text
Protobuf contract
bidirectional attached Run
gRPC server
gRPC Go client
error and cancellation mapping
```

### Milestone 2.4: Kubernetes baseline

```text
health probes
graceful shutdown
deployment example
resource and autoscaling guidance
observability example
```

**Exit:** two Go services communicate with a Gotato Agent over gRPC in a replicated Kubernetes deployment.

## Phase 3 — Ecosystem

Model adapters, capability adapters, Extensions, optional HTTP projection, external state, remote Agent Routine execution, and durable Runs proceed as independent packages backed by concrete use cases.
