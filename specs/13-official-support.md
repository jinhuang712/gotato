# 13. Official Support

**Status:** Planned packages

> Official packages extend Core or Host contracts without enlarging the Core boundary.

## 1. Core packages

```text
Agent Core
Typed-function Tool adapter
JSON Schema helpers
deterministic testkit
Core Extensions
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

Each package owns protocol dependencies and external failure mapping.

## 4. Orchestration packages

```text
Agent Factory
Conversation Owner
bounded Agent Cache
Admission Controller
Event Projector and Bridge
Drain Policy
Routine Coordinator
```

These are optional for Embedded mode and required only by the Hosted composition that uses them.

## 5. Transport packages

```text
gRPC Protobuf contract
gRPC server and Go client
optional HTTP/SSE or Connect projection
```

Transport packages map Core facts; they do not create alternative Agent semantics.

## 6. Infrastructure assets

```text
Kubernetes deployment examples
Gateway integration examples
health and drain configuration
observability integration
```

These are deployment assets, not Core libraries.

## 7. Extensions

Independent packages may provide OpenTelemetry, structured logging, context compaction, authorization, approvals, cost accounting, and Model routing through the correct Core or Host joint.

## 8. Compatibility

Official packages depend on stable public contracts. Their dependencies and release cadence remain separate from Core where practical.
