# Gotato Specifications

> Implementable contracts for one hosted Agent runtime and the service that exposes it.

These documents define what an implementation must satisfy. Each one is self-contained: it states its own invariants rather than deferring to another document.

## What is being built

A gRPC Agent service backed by a transport-independent Go runtime.

```text
Remote client
      │ RunCommand
      ▼
Agent Service
  resolve · admit · own · deliver
      │
      ▼
Runtime Kernel
  Agent · Run · Turn · Model · Tools · Events
      │
      ▼
Canonical Events ──► projection ──► RunEvent stream
```

## Two orderings

Delivery and reading are ordered differently, on purpose.

```text
Delivery order
  vertical slices, each ending in a callable service
  a slice proves runtime contracts by exercising them remotely

Reading order
  dependency order, so no contract references an undefined term
  vocabulary and loop before the service that hosts them
```

The service is the first product boundary and drives which contracts exist. It is specified after the terms it uses are defined.

## Reading order

1. [Scope and principles](00-scope-and-principles.md)
2. [Core domain](01-core-domain.md)
3. [Messages and Models](02-messages-and-models.md)
4. [Agent loop](03-agent-loop.md)
5. [Events and delivery](04-events-and-delivery.md)
6. [Tools and ToolSets](05-tools-and-toolsets.md)
7. [Extensions](06-extensions.md)
8. [Agent Routines, concurrency, and cancellation](07-agent-routines-and-concurrency.md)
9. [Errors and limits](08-errors-and-limits.md)
10. [Agent service and gRPC](09-agent-service-and-grpc.md)
11. [Runtime and service API](10-runtime-and-service-api.md)
12. [Testing and acceptance](11-testing-and-acceptance.md)
13. [Delivery roadmap](12-delivery-roadmap.md)
14. [Official support](13-official-support.md)
15. [Future directions](14-future-directions.md)
16. [Decisions and open questions](15-decisions-and-open-questions.md)

## Keywords

- **MUST:** required behavior.
- **SHOULD:** recommended default with documented alternatives.
- **MAY:** optional behavior compatible with the contract.
- **FUTURE:** outside the current delivery scope.
