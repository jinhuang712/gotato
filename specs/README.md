# Gotato Specifications

> Implementable contracts for one Go Agent kernel and its official service path.

Read [the philosophy](../docs/00-philosophy.md) before these specifications.

## Milestone map

```text
Phase 1: Kernel
Scope → Domain → Model → Loop → Events
           │                 │
           └── Tools/ToolSets┘
                    │
               Extensions
                    │
       Agent Routines · Errors · API
                    │
             Acceptance Tests

Phase 2: Service
Agent factory → Service preset → gRPC → Kubernetes lifecycle
```

## Reading order

1. [Scope and principles](00-scope-and-principles.md)
2. [Core domain](01-core-domain.md)
3. [Messages and Models](02-messages-and-models.md)
4. [Agent loop](03-agent-loop.md)
5. [Events](04-events.md)
6. [Tools and ToolSets](05-tools-and-toolsets.md)
7. [Extensions](06-extensions.md)
8. [Agent Routines, concurrency, and cancellation](07-agent-routines-and-concurrency.md)
9. [Errors and limits](08-errors-and-limits.md)
10. [Public API and packages](09-public-api-and-packages.md)
11. [Testing and acceptance](10-testing-and-acceptance.md)
12. [Delivery roadmap](11-delivery-roadmap.md)
13. [Official support](12-official-support.md)
14. [Agent service and gRPC](13-agent-service-and-grpc.md)
15. [Future directions](14-future-directions.md)
16. [Decisions and open questions](15-decisions-and-open-questions.md)

## Keywords

- **MUST:** required behavior.
- **SHOULD:** recommended default with documented alternatives.
- **MAY:** optional behavior compatible with the contract.
- **FUTURE:** outside the current milestone.
