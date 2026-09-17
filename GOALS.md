# Gotato Goals

This document preserves the project goals, non-goals, necessary tradeoffs, and compatibility principles. It is constrained by [PHILOSOPHY.md](PHILOSOPHY.md) and [DESIGN.md](DESIGN.md).

---

## 1. Goals

### G-G01 — Primary Goal

> **Make capable agents an ordinary, lightweight, composable runtime primitive in Go.**

Gotato should be practical both for a single embedded Agent and as the foundation beneath a much larger application.

### G-G02 — Standard Runtime Goal

Provide enough reusable runtime infrastructure that applications do not need to rebuild session continuity, context construction, tool registration, structured events, CLI diagnostics, and basic testing every time.

### G-G03 — Cheap Execution Goal

Agent configuration and execution should remain light enough that applications can create and discard bounded agents freely under application-controlled concurrency.

Performance should be measured rather than asserted. Benchmarks should distinguish Gotato overhead from model latency, context size, tool processes, and provider behavior.

**Measurable targets** (to be verified by benchmarks in the repository, not asserted):

| Metric | Target |
|---|---|
| `NewAgent` + `Close` with a fake model | allocations and wall time independent of any other live agent; no goroutine left after `Close` |
| Committing one message to a Session | cost independent of transcript length (no whole-transcript re-serialization per commit) |
| Event subscription | no goroutine leak after `Close` on the stream |
| Building a Context from a Session | linear in the number of selected messages, not in total history for window strategies |

### G-G04 — Composability Goal

Applications should be able to use Gotato as a library, a CLI runtime, a foundation for a desktop product, a foundation for a service, and a foundation for an orchestration system. None of those products should need to fork the core agent runtime to create their own role model.

### G-G05 — Session and Context Goal

Gotato should make long-lived continuity practical without forcing every model call to carry all history. Session and Context should be reusable enough that higher-level applications can project their own semantics cleanly.

### G-G06 — Agent-Friendly Development Goal

The repository should be operable by coding agents through documented Go APIs, deterministic tests, stable CLI commands, machine-readable output, structured events, and clear repository guidance. A coding agent should be able to implement a change and validate it without relying on a graphical interface.

### G-G07 — Repository Goal

Gotato should grow into a coherent runtime repository, not a single oversized package. The repository may contain many useful packages while keeping dependencies layered and concepts small.

---

## 2. Non-Goals

### G-N01 — No Built-In Agent Organization

Gotato does not define Master, Operator, Worker, supervisor, sub-agent, team, swarm, or organizational hierarchy.

### G-N02 — No Application Task Graph

Gotato does not own durable application Tasks, task dependency graphs, integration pipelines, or project-level scheduling.

### G-N03 — No Global Multi-Agent Resource Scheduler

Applications that run many agents remain responsible for global concurrency, CPU/RAM protection, build/test limits, provider quotas, and other system-level resource policy. Gotato provides cancellable lightweight runs that such schedulers can control.

The `orchestration` package in this repository is an optional service layer built on the runtime: an application-side scheduler, not part of the runtime foundation.

### G-N04 — No Product UI

Gotato does not aim to become an IDE or desktop product. Its official CLI is a runtime interface and diagnostic surface, not a substitute for every possible product UI.

### G-N05 — No Mandatory Service Mode

A server mode may exist (the repository contains an HTTP host and a gRPC adapter as an optional service layer), but library and CLI use must not depend on it.

### G-N06 — No Universal Memory Doctrine

Gotato may provide Session, Context, persistence interfaces, and compaction primitives. It does not prescribe one semantic long-term memory architecture for all applications.

### G-N07 — No Workflow DSL as Core Identity

Ordinary Go composition remains the default. If workflow packages exist, they are optional and must not redefine Agent semantics.

---

## 3. Necessary Tradeoffs

### G-T01 — A Larger Standard Runtime Is Accepted to Avoid Rebuilding Basics

Gotato intentionally owns more than an agent loop. Session, Context, Tool Registry, Events, CLI, and Testing increase repository size, but they reduce duplicated infrastructure across every application built on top. The constraint is conceptual clarity, not minimal file count.

### G-T02 — More Primitives Require Stronger Boundaries

Once Session and Context become first-class, the project must resist turning them into application-specific state containers. Generic continuity belongs in Gotato. Product semantics belong above it.

### G-T03 — Persistence Convenience Must Not Become a Database Requirement

Providing standard persistence is useful. Requiring persistence for every run is not. The in-memory store is always sufficient for library use.

### G-T04 — CLI Stability Becomes Part of Compatibility

A first-class CLI creates an external contract used by scripts and coding agents. Changes to command names, fields, exit codes, and JSON output require deliberate compatibility handling.

### G-T05 — Cheap Agents Do Not Mean Unlimited Agents

Gotato should make agents cheap. It should not claim that infinite concurrency is safe. Applications remain responsible for global scheduling and resource governance.

---

## 4. Compatibility Principles

1. **Public Go API.** Exported identifiers in the root package and the standard runtime packages are part of the contract. Additive change is preferred. Removal goes through deprecation (a documented release with a `// Deprecated:` comment) before deletion when the cost is reasonable.
2. **CLI contract.** Command names, flag names, JSON field names, JSONL event shapes, and exit codes are versioned behavior. Additions are free; renames and removals need a migration note.
3. **Event kinds.** New event kinds may be added. Existing event kinds and their documented payload keys are not renamed without a migration note. Consumers must tolerate unknown kinds.
4. **Wire contracts of the optional service layer** (HTTP host, gRPC) follow their own `ContractVersion`; they are not the runtime contract.
5. **Breaking changes** are recorded in `MIGRATION.md` with a before/after example.
6. **Clarity wins.** A confused abstraction is not preserved indefinitely solely to avoid a version bump (G-D30).
