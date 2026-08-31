# Gotato Specifications

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

These specifications define the atomic Agent Core, the Orchestration required to coordinate multiple Agents, and the optional protocol-facing Host that exposes that coordination as a Hosted Agent Service. A direct single-Agent call may use Core alone; a multi-Agent or Hosted system requires an Orchestration responsibility. They make the three project principles concrete:

1. Agents are self-contained goroutines: each owns its state and work.
2. Infrastructure hosts. Orchestration coordinates. Host exposes. Agent Core executes.
3. Tight Core, Open Extensions.

Use the [Glossary](../docs/glossary.md) for the shared vocabulary.

## Scope

The specifications cover:

```text
Agent Core
  Agent interface · conversation state · canonical Loop
  Model and Tool contracts · Events · cancellation · limits

Orchestration / Host
  Agent identity · handle retention · creation · routing
  admission · lifecycle · retirement · coordination · delivery

Adapters
  LLM · Tool · and optional protocol adapters
```

Infrastructure is outside the implementation scope. The specifications define the integration signals a Host may expose, but do not define a Gateway, Kubernetes operator, broker, database, or secrets platform.

A direct single-Agent caller uses Core without Orchestration or Host. A multi-Agent embedded caller supplies Orchestration in application code or uses the Gotato layer. A Conversation-aware caller additionally needs a Conversation record and a key-to-handle or key-to-rehydration mapping. A remote caller reaches the same Core semantics through Host and an adapter:

```text
Embedded, single: Go service → Agent Core
Embedded, multi:  Go service → Orchestration → Agent Core × N
Hosted:           Client → protocol adapter → Host → Orchestration → Agent Core × N
```

Architecture documents explain rationale and usage. These specifications define stable values, state transitions, ordering, bounds, failure semantics, Orchestration behavior, service behavior, and acceptance. When an illustration conflicts with a specification, the specification wins.

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
10. Orchestration, Host, and protocol adapters
11. Runtime and Host API
12. Testing and acceptance
13. Delivery roadmap
14. Official support
15. Future directions
16. Decisions and open questions
17. Agent lifecycle and retirement

## Keywords

- **MUST:** required behavior.
- **SHOULD:** recommended default with documented alternatives.
- **MAY:** optional behavior compatible with the contract.
- **FUTURE:** outside the current delivery scope.
