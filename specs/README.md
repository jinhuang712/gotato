# Gotato Specifications

> **Agent as a Service, native to Go. Agents are goroutines; channels are the boundaries.**

The specifications define the contracts of a Go-native Agent Runtime and its optional Hosted composition.

```text
Agent goroutine
  ├── private state
  ├── simple canonical Loop
  ├── explicit capabilities
  └── command / result / Event channels

Orchestration goroutines
  ├── admission and request queue policy
  ├── Agent creation and routing
  ├── coordination and delivery
  └── lifecycle and drain
```

A single Agent goroutine processes one Prompt or Continue at a time. The caller or Host decides whether further external requests wait, queue, are rejected, steered, aborted, or create another Agent routine. There is no resource ownership hierarchy between Agent goroutines or between Agents and Orchestration.

Infrastructure such as Gateway, Kubernetes, load balancing, storage, and secrets surrounds Hosted mode but is not a Core dependency.

## Boundary order

```text
Infrastructure
      ↓ hosts and routes
Transport goroutines
      ↓ maps wire commands
Orchestration goroutines
      ↓ channel commands and results
Agent goroutine
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
