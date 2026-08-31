# 13. Official Support

**Status:** Planned packages

> **Official packages extend Agent Core without enlarging it.**

## 1. Core packages

```text
Agent Core
Typed-function Tool adapter
JSON Schema helpers
deterministic testkit
Core Extensions
Agent Routine / channel handle
```

## 2. Model packages

```text
Model Router
provider adapters
fallback and provider policy
usage and cost adapters
```

Model packages depend on the provider-neutral Core Model contract and do not change Agent semantics.

## 3. Capability packages

```text
HTTP Tool / ToolSet
gRPC Tool / ToolSet
MCP ToolSet
database and Redis Tools
workflow ToolSet
script or sandbox Tool
remote Agent Tool
```

Each package owns protocol dependencies and external failure mapping. Calls return through explicit channels or channel-backed handles.

## 4. Orchestration packages

```text
Agent Factory
Agent Registry and Router
bounded Agent Handle Cache
Admission Controller
Request Queue and Dispatch Policy
Event Projector and Bridge
Drain Policy
Agent Routine Coordinator
```

These are optional for Embedded mode and required only by the Hosted composition that uses them. They coordinate Agent goroutines; they do not own their private state or create a second Loop.

## 5. Protocol adapters

```text
gRPC or HTTP command/Event mapping
server and client helpers
optional SSE or Connect projection
```

Protocol adapters attach a wire protocol to the Host contract. They do not create alternative Agent semantics and are not required for Embedded use.

## 6. Infrastructure integration assets

```text
Kubernetes deployment examples
Gateway integration examples
health and drain configuration
observability integration
```

These are compatibility examples, not Gotato infrastructure or Core libraries. They must work with an existing platform rather than introduce a new one.

## 7. Extensions

Independent packages may provide OpenTelemetry, structured logging, context compaction, authorization, approvals, cost accounting, and Model routing through the correct Core or Host boundary. Long-lived work must have an explicit Context, channel, and shutdown path.

## 8. Compatibility

Official packages depend on stable public contracts. Their dependencies and release cadence remain separate from Core where practical.
