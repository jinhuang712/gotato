# Gotato Specifications

> Normative contracts for a self-contained Agent Core and its optional hosted orchestration.

Gotato has two first-class modes:

```text
Embedded: Application → Agent Core → Model / Tools
Hosted:   Client → Transport → Orchestrator → Agent Core
```

Infrastructure such as Gateway, Kubernetes, load balancing, storage, and secrets surrounds Hosted mode but is not a Core dependency.

## Boundary order

```text
Infrastructure
      ↓ hosts and routes
Transport
      ↓ maps wire commands
Orchestration / Agent Host
      ↓ coordinates Core instances
Agent Core
      ↓ consumes stable contracts
Model and Capability Adapters
```

Specifications define stable values, state transitions, ordering, bounds, failure semantics, service behavior, and acceptance. Architecture documents explain rationale. When an illustration conflicts with a specification, the specification wins.

## Reading order

1. Scope and principles
2. Core domain
3. Messages and Models
4. Agent loop
5. Events and delivery
6. Tools and ToolSets
7. Extensions
8. Agent Routines and concurrency
9. Errors and limits
10. Agent Host and gRPC
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
