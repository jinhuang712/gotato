# Gotato Goals

Goals, boundaries, tradeoffs, and compatibility principles, constrained by [PHILOSOPHY.md](PHILOSOPHY.md) and [DESIGN.md](DESIGN.md).

---

## 1. Goals

### G-G01 — Primary Goal

> **Make capable agents an ordinary, lightweight, composable runtime primitive in Go.**

Gotato serves a single embedded Agent and the foundation beneath a much larger application equally well.

### G-G02 — Standard Runtime

Applications get session continuity, context construction, tool registration, structured events, CLI diagnostics, and test utilities from Gotato instead of rebuilding them.

### G-G03 — Cheap Execution

Agent configuration and execution stay light enough that applications create and discard bounded agents freely under their own concurrency control. Performance is measured by benchmarks that separate Gotato overhead from model latency, context size, tool processes, and provider behavior.

| Metric | Target |
|---|---|
| `NewAgent` + `Close` with a fake model | allocations and wall time independent of other live agents; zero goroutines left after `Close` |
| Committing one message to a Session | cost independent of transcript length |
| Event subscription | its goroutine is released on `Close` |
| Building a Context from a Session | linear in the selected messages; window strategies scale with the window, not total history |

### G-G04 — Composability

Gotato works as a library, a CLI runtime, and the foundation for desktop products, services, and orchestration systems. Each builds its own role model on the shared core runtime.

### G-G05 — Session and Context

Long-lived continuity is practical, and each model call carries only the context it needs. Session and Context are general enough for applications to project their own semantics onto them.

### G-G06 — Agent-Friendly Development

Coding agents operate the repository through documented Go APIs, deterministic tests, stable CLI commands, machine-readable output, structured events, and clear repository guidance, implementing and validating changes from a terminal.

### G-G07 — Coherent Repository

Gotato grows as a coherent runtime repository of layered packages with small concepts.

---

## 2. Boundaries

### G-N01 — Agent Organization Belongs to Applications

Gotato provides peer agents. Applications compose roles, teams, and hierarchies.

### G-N02 — Task Graphs Belong to Applications

Durable application tasks, task dependency graphs, integration pipelines, and project-level scheduling live in the application.

### G-N03 — Global Resource Scheduling Belongs to Applications

Applications that run many agents own global concurrency, CPU and memory protection, build and test limits, provider quotas, and other system-level resource policy. Gotato provides cancellable, lightweight runs those schedulers control.

The `service` package bounds concurrent Runs and serializes Runs per Session; cross-agent CPU, memory, and quota scheduling stays with the deploying application.

### G-N04 — Product UI Belongs to Applications

IDEs and desktop products build on Gotato. Its official CLI is a runtime interface and diagnostic surface.

### G-N05 — Service Mode Is Optional

`gotato serve` and `gotato-grpc` are first-class ways to run Gotato, and library and CLI use stand on their own. All three drive the same `service.Runner`, and the runtime packages are independent of the service.

### G-N06 — Memory Architecture Belongs to Applications

Gotato provides Session, Context, persistence interfaces, and compaction primitives. Applications choose their long-term memory architecture.

### G-N07 — Ordinary Go Is the Workflow Language

Ordinary Go composition is the default. Workflow packages, if any, are optional and keep Agent semantics unchanged.

---

## 3. Tradeoffs

### G-T01 — A Larger Standard Runtime in Exchange for Shared Basics

Gotato owns more than an agent loop. Session, Context, Tool Registry, Events, CLI, and Testing add repository size and remove duplicated infrastructure from every application. The constraint is conceptual clarity, not file count.

### G-T02 — More Primitives, Firmer Boundaries

First-class Session and Context stay generic containers. Generic continuity belongs in Gotato; product semantics belong above it.

### G-T03 — Persistence Is Available, Not Required

Gotato ships standard persistence, and every run works without it: the in-memory store is sufficient for library use.

### G-T04 — CLI Stability Is Part of Compatibility

Scripts and coding agents depend on the CLI. Command names, fields, exit codes, and JSON output change only with deliberate compatibility handling.

### G-T05 — Cheap Agents, Bounded Concurrency

Gotato makes agents cheap; safe concurrency limits are set by the application, which owns global scheduling and resource governance.

---

## 4. Compatibility Principles

1. **Public Go API.** Exported identifiers in the root package and the standard runtime packages are the contract. Prefer additive change; remove through a documented `// Deprecated:` release first when the cost is reasonable.
2. **CLI contract.** Command names, flag names, JSON field names, JSONL event shapes, and exit codes are versioned. Additions are free; renames and removals need a migration note.
3. **Event kinds.** New kinds may be added. Existing kinds and their documented payload keys change only with a migration note. Consumers tolerate unknown kinds.
4. **Service wire contracts** (HTTP, gRPC) follow their own `ContractVersion`, separate from the runtime contract.
5. **Breaking changes** are recorded in `MIGRATION.md` with a before/after example.
6. **Clarity wins** over preserving a confused abstraction (G-D30).
