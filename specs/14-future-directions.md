# 14. Future Directions

**Status:** Exploration

> Future capabilities enter the architecture through the layer that owns their state and failure semantics.

```text
real use case → prototype → acceptance contract → official package
```

## 1. Persistent conversations

A state package may persist Agent Messages, active ToolSets, summaries, and configuration references. The Agent factory then restores conversation-scoped Agents through that contract.

## 2. Detached and resumable Runs

A durable service may add:

```text
Run identifiers
Event cursors
reconnect
queue admission
lease ownership
checkpoint and resume
```

This capability requires explicit semantics for repeated Model calls and Tool side effects.

## 3. Dynamic ToolSet discovery

A ToolSet source may resolve capability domains from configuration or a remote catalog. Version pinning and Turn-boundary refresh preserve deterministic execution.

## 4. Protocol and execution adapters

MCP, workflows, scripts, and sandbox workers can implement Tool or ToolSet contracts while keeping execution-specific state in their packages.

## 5. Remote Agent Routines

A remote Routine executor may spawn a child Agent through another Gotato Agent service:

```text
Parent Agent
  → Routine Executor
  → remote Agent Service
  → child Run Events and Result
```

The remote form preserves Routine identity, parent/child correlation, cancellation, limits, lifecycle Events, and settled Result semantics.

## 6. Durable Agent Routines

Long-running Routines may add queue ownership, lease recovery, checkpoints, and detached result collection. These capabilities build on durable Run semantics.

## 7. Security and governance

Identity, authorization, approval, audit, credentials, and sandbox policy can integrate through service middleware, Extensions, and capability adapters.

## 8. Shared budgets

Routine trees may share token, cost, time, and Tool Call budgets. A budget Extension can allocate child limits and aggregate usage without changing Routine state ownership.

## 9. Capability networks

Organizations may compose many service-owned ToolSets into broader capability networks. Gotato's contribution remains the runtime, discovery, Routine, and service contracts used by each participating Agent.

## 10. Promotion rule

A direction becomes normative after a concrete implementation plan, at least one validated use case, and deterministic acceptance tests identify its owning layer.
