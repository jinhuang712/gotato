# 13. Official Support

**Status:** Planned packages

> Official packages complete common integrations while preserving one small Core.

## 1. Package families

```text
                            Gotato Core
                                 ▲
        ┌──────────────┬─────────┼─────────┬──────────────┐
        │              │         │         │              │
 Model adapters   Tool adapters  │   Extensions    Agent Routines
                                 │
                         Service / Transport
```

## 2. Kernel support

Core runtime support includes:

```text
typed-function Tool adapter
JSON Schema helpers
deterministic testkit
at least one production Model adapter
structured logging example
```

## 3. Moving Parts support

Public packages include contracts and helpers for:

```text
Context and Message transformation
Pre-Tool-Use and Post-Tool-Use
Event enrichment, projection, redaction, and sinks
Turn stopping
Tool execution and batch policies
```

## 4. Agent Routine support

The Agent Routine package includes:

```text
Spawn
Routine handle and Result
bounded Routine Group
Context and cancellation propagation
Routine lifecycle Events
spawn_agent Tool example
```

A remote Routine executor MAY follow after the local implementation is stable.

## 5. Service support

Service support includes:

```text
Agent factory
bounded in-memory Agent cache
service preset
gRPC Protobuf contract
gRPC server and Go client
bounded Event bridge
readiness and graceful drain
Kubernetes deployment baseline
```

An HTTP/SSE or Connect-style projection MAY follow the same service contracts.

## 6. Capability adapters

Independent packages MAY provide:

```text
HTTP Tool and ToolSet
gRPC Tool and ToolSet
MCP ToolSet
workflow ToolSet
remote Agent Tool
script or sandbox Tool
```

Each adapter implements public capability contracts and owns its protocol dependencies.

## 7. Runtime Extensions

Independent packages MAY provide:

```text
OpenTelemetry
structured logging
context compaction
retry
Model routing
cost accounting
authorization integration
approval integration
```

## 8. Discovery and generation

Official tooling SHOULD provide a typed-function adapter and MAY provide `go:generate` ToolSet assembly. Configuration-driven discovery builds on the same explicit ToolSet contracts.

## 9. Presentation boundary

Gotato publishes Go APIs, Protobuf, Events, examples, and diagnostic utilities. External applications own end-user CLI, TUI, web, and chat experiences.

## 10. Compatibility

Official packages depend on public Gotato contracts. Their dependencies and release cadence remain separate from Core where practical.
