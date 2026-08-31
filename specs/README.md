# Gotato Specifications

> **Agent as a Service.**

> Gotato is a minimal, Go-native runtime for self-contained, stateful Agents.

These specifications define the minimal Agent Core and the optional Host that exposes the same Core semantics as a Hosted Agent Service. They make the three project principles concrete:

1. Agents are self-contained goroutines: each owns its state and work.
2. Infrastructure hosts. Orchestration coordinates. Agent Core executes.
3. Tight Core, Open Extensions.

Use the [Glossary](../docs/glossary.md) for the shared vocabulary.

## Scope

The specifications cover:

```text
Agent Core
  Agent interface · conversation state · canonical Loop
  Model and Tool contracts · Events · cancellation · limits

Agent Host / Orchestration
  Agent creation · routing · admission · lifecycle · delivery

Adapters
  LLM · Tool · and optional protocol adapters
```

Infrastructure is outside the implementation scope. The specifications define the integration signals a Host may expose, but do not define a Gateway, Kubernetes operator, broker, database, or secrets platform.

A direct Go caller uses Core without a Host. A remote caller reaches the same Core through a Host and an adapter:

```text
Embedded: Go service → Agent Core
Hosted:   Client → protocol adapter → Host / Orchestration → Agent Core
```

Architecture documents explain rationale and usage. These specifications define stable values, state transitions, ordering, bounds, failure semantics, service behavior, and acceptance. When an illustration conflicts with a specification, the specification wins.

## Reading order

1. Scope and principles
2. Core domain
3. Messages and Models
4. Agent Loop
5. Events and delivery
6. Tools and ToolSets
7. Extensions
8. Agent Routines and concurrency
9. Errors and limits
10. Agent Host and protocol adapter
11. Runtime and Host API
12. Testing and acceptance
13. Delivery roadmap
14. Official support
15. Future directions
16. Decisions and open questions

## Keywords

- **MUST:** required behavior.
- **SHOULD:** recommended default with documented alternatives.
- **MAY:** optional behavior compatible with the contract.
- **FUTURE:** outside the current delivery scope.
