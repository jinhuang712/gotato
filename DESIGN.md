# Gotato Design

DESIGN translates [PHILOSOPHY.md](PHILOSOPHY.md) into durable engineering rules. These rules are intentionally stronger than ordinary implementation suggestions. A change that violates one of them needs either a documented contradiction in the constitution or a documented, migration-friendly breaking change (see G-D30).

The vocabulary used here is fixed:

| Term | Meaning |
|---|---|
| **Agent** | who acts: reusable behavior needed to execute an agentic loop |
| **Run** | one execution of the loop against a Session, triggered by a prompt or continuation |
| **Turn** | one model call plus the tool executions it requests |
| **Session** | what happened: the logical continuity of an interaction across turns and runs |
| **Context** | what the model sees now: the turn-specific projection supplied to one model call |
| **Tool** | a capability with identity, description, input schema, execution, structured result |
| **Tool Registry** | the runtime primitive that owns tool identity, visibility, and activation |
| **Event** | a structured runtime fact emitted during execution |
| **Extension** | a hook or middleware that wraps the runtime without replacing it |

---

## 1. The Agent Primitive

### G-D01 — Agent as a Goroutine

An agent should fit naturally into Go's concurrency model. The intended mental model is:

```go
go agent.Run(ctx, session)
```

This is a design principle, not a requirement that every public API literally use this exact signature. An agent must not inherently require its own process, container, daemon, service, or scheduler.

The core agent is one goroutine that owns the Run in flight; the caller's goroutine blocks on `Prompt` or `Continue` and may cancel through its `context.Context`. Any realization that keeps this property is acceptable.

### G-D02 — One Agent Primitive

Gotato maintains one fundamental agent abstraction and one fundamental execution model.

Different applications may configure agents with different prompts, tools, models, context strategies, or lifecycles, but the runtime must not fork into separate core implementations for planner, worker, supervisor, reviewer, or sub-agent roles.

Role is application composition, not Gotato type hierarchy.

### G-D18 — No Intrinsic Agent Hierarchy

The runtime must not require parent agent IDs, child agent IDs, sub-agent types, delegation trees, or supervisor semantics as part of Agent identity.

An application may store such relationships as its own metadata (for example in `Session.Metadata`).

### G-D10 — Reusable Agent Configuration and Mutable Run State Are Separate

Reusable configuration may include the model provider, model selection, tool registry, context builder, hooks, and execution policy.

Mutable execution state belongs to a run/session and must not leak into unrelated executions. A new execution must not accidentally inherit a previous run's mutable state.

Concretely: control messages (steer/follow-up) left over at the end of a Run are discarded, and the history an agent commits to is a `Transcript` supplied by the caller (a Session) or a private one that dies with the agent, never state that silently carries over between unrelated executions.

### G-D11 — `context.Context` Owns Cancellation and Deadlines

Go's standard `context.Context` propagates cancellation and deadlines through agent execution, model calls, tool calls, storage operations where appropriate, and streaming consumers where appropriate.

Gotato must not create a parallel cancellation universe when standard Go semantics are sufficient. Runtime-configured deadlines (`CoreLimits.RunDeadline` etc.) are implemented by deriving standard contexts, never by a second mechanism.

---

## 2. Agent, Session, and Context

### G-D03 — Agent, Session, and Context Are Distinct

Gotato makes three concepts explicit:

- **Agent — who acts?** Owns or references the reusable behavior required to execute an agentic loop: model access, tools, runtime policies, extensions, and loop behavior.
- **Session — what continuity exists?** Represents the logical continuity of an interaction. It records what happened across turns and runs: messages, tool interactions, usage, runtime metadata, events, and context-management state.
- **Context — what does the model see now?** The turn-specific projection supplied to a model call. It may contain all session history, only recent history, a compacted history, selected resources, summaries, or another application-defined projection.

> **Session is what happened. Context is what the model sees now.**

An Agent may run against a Session many times. A Session may outlive an individual agent execution. A Context may be rebuilt for every turn.

In code: the agent loop reads from and appends to a `Transcript` (the Session's committed history) and hands the model the output of a `ContextBuilder`. Structs must not conflate "all historical state" with "model input".

### G-D04 — Session Is a First-Class Runtime Primitive

A standard Session must be able to represent at least: session identity, messages, model outputs, tool calls and tool results, runtime metadata, usage, important execution events, context/compaction metadata, and optional application metadata.

Session must not know application-specific concepts such as Task Graph, Master, Operator, worktree integration, or product workflow. Application semantics go in `Metadata`, not in fields.

### G-D05 — Session Storage Is Pluggable

Gotato defines storage contracts without forcing one persistence engine. Expected implementations include in-memory storage, JSONL or file-backed storage, SQLite-backed storage, and application-provided stores.

A library-only use case must not require a database.

### G-D06 — Session Forking Is a Generic Primitive

Gotato supports creating a new Session from an existing Session state when practical. Forking is a state operation, not an agent hierarchy operation. The fork records its origin (parent session ID) as lineage metadata only.

### G-D07 — Context Is Built Through Explicit Strategies

Context management must not be buried inside opaque message mutation.

Gotato exposes a small context-building abstraction (`ContextBuilder`) able to support strategies such as full history, sliding window, compacted history, summary + recent turns, selected references, and application-defined projection.

Context strategies are composable and inspectable: a caller must be able to ask "what would the model see for this session right now" without running a model.

### G-D08 — Compaction Belongs to the Standard Runtime

Long-running agent sessions inevitably encounter context-window pressure. Gotato provides standard hooks and implementations for compaction without dictating one universal summarization policy.

Compaction preserves traceability: applications must be able to tell what was compacted and what representation replaced it. A compaction is recorded in the Session, never applied silently.

---

## 3. The Loop

### G-D09 — One Minimal Agentic Loop

At the center of Gotato remains a small loop:

```text
input / session state
        |
        v
   build context
        |
        v
      model
        |
        +---- final result ----> finish turn/run
        |
        +---- tool request
                 |
                 v
              execute
                 |
                 v
            observation
                 |
                 +-------------> build next context / model
```

Session, context, streaming, events, and extensions support this loop rather than create competing hidden execution semantics.

### G-D17 — Extensions Wrap the Runtime; They Do Not Replace It

Gotato supports hooks or middleware for cross-cutting behavior: tracing, metrics, logging, policy checks, result transformation, custom context handling, provider-specific behavior.

Extensions must not silently create a second agent loop with incompatible semantics. The existing extension points (`ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver`, `TurnStopper`) are all bounded stages of the one loop.

---

## 4. Tools

### G-D12 — Tool Registry Is a First-Class Primitive

A standard runtime needs more than an anonymous `[]Tool`.

Gotato provides a Tool Registry capable of register, unregister, lookup, list, describe, activate, and deactivate. The registry enables both static and dynamic tool surfaces without defining application-level orchestration.

Tool discovery systems, MCP catalogs, authorization policy, or deferred-loading policy may be built above or beside the registry.

### G-D13 — Tools Are Capabilities, Not Applications

The core Tool contract remains small and structured: identity, description, input contract/schema, execution, structured result/error.

Filesystem, shell, Git, browser, MCP, or product-specific tool packages may be provided in the repository, but they must not make those capabilities mandatory for every agent.

---

## 5. Models, Streaming, and Events

### G-D14 — Models Use a Small Capability-Aware Contract

Gotato exposes a stable model contract for normal agent execution without pretending every provider has identical capabilities. Provider-specific features are expressed through adapters or optional capability interfaces instead of continuously expanding one universal interface.

Opaque provider artifacts (for example reasoning signatures) are carried, never interpreted, by core.

### G-D15 — Streaming Is a Runtime Primitive

Streaming exposes structured execution progress independent of any UI. Callers consume model deltas, tool lifecycle, context events, usage, errors, and terminal events through a Go-native streaming abstraction (`EventStream`).

### G-D16 — Event Stream Is a First-Class Primitive

Gotato emits structured runtime events that applications observe without parsing logs. Expected event families: agent/run lifecycle, turn lifecycle, context build/compaction, model request/response, tool call/result, usage, error, session update.

Applications may map Gotato events into higher-level application events.

### G-D29 — Observability Must Be Additive

Logs, traces, metrics, and usage tracking are attachable without changing the core semantics of agent execution. Observability must be rich enough to support debugging but must not become a mandatory centralized service.

---

## 6. What the Runtime Must Not Own

### G-D19 — No Application Orchestration in the Runtime Foundation

Gotato must not own application-level concepts such as task graphs, worker pools, master/operator roles, workflow dependency management, project integration, desktop state, or global multi-agent resource scheduling.

Gotato may provide generic lower-level primitives that such systems use: sessions, contexts, events, tools, execution, and CLI access.

Multi-agent coordination in this repository (routing, admission, retirement, remote exposure) is an **optional service layer built on the runtime**, not part of the runtime foundation. Core and standard runtime packages never depend on it.

### G-D20 — No Mandatory Background Daemon

Embedding Gotato as a Go library remains first-class. A CLI or optional server may exist, but using the standard runtime must not require a companion daemon.

### G-D21 — No Built-In UI

Gotato does not ship a project-specific desktop or terminal user experience as part of its architectural identity. It may provide CLI output intended for humans, but UI products belong above the runtime.

---

## 7. The CLI

### G-D22 — CLI Is a First-Class Runtime Interface

Gotato provides an official CLI (`cmd/gotato`) over its standard runtime primitives. The CLI serves three audiences equally: humans, shell automation, coding agents. Core runtime capabilities must be testable without writing a custom Go program.

### G-D23 — Machine-Readable CLI Semantics Are Mandatory

Important CLI commands support stable machine-readable output, including where appropriate `--json`, `--jsonl`, `--quiet`, `--no-color`, `--timeout`.

Stdout contains requested data. Diagnostics go to stderr. Exit codes are meaningful and documented. The CLI must not require scraping decorative human text to determine whether an operation succeeded.

### G-D24 — CLI and Go Library Share the Same Runtime

The CLI is a thin client of Gotato packages, not a separate implementation of agent semantics. A behavior that exists only in CLI code and cannot be exercised through the runtime API is an architectural smell unless it is inherently presentation-specific.

---

## 8. Testing

### G-D25 — Testing Is a First-Class Runtime Surface

Gotato makes itself easy for coding agents and normal tests to exercise deterministically. The repository provides test utilities such as fake model, replay model, fake tool, event recorder, session fixtures, context fixtures, and deterministic failure injection where useful.

A coding agent must be able to modify Gotato, run unit tests, run CLI scenarios, inspect structured output, and diagnose failures without a GUI. CI must not depend on paid model calls.

---

## 9. Repository Structure

### G-D26 — Repository Can Be Broad While Concepts Stay Small

Gotato may be a substantial repository similar in spirit to a runtime monorepo. `Less is More` constrains the conceptual model and mandatory dependencies, not the number of useful packages.

Packages remain layered so users depend only on what they need.

### G-D27 — Package Direction Must Remain Layered

```text
applications / cmd/gotato / optional service layer (orchestration, host, adapters)
          |
          v
standard runtime packages
(session / modelctx / toolregistry / testkit)
          |
          v
core execution packages
(root package gotato: agent loop / model / tool / message / events / extensions)
          |
          v
Go standard library + narrow external dependencies
```

Provider and tool adapters depend inward. Core packages must not depend outward on product or service packages. The root package imports only the standard library.

### G-D28 — Configuration Should Prefer Go Values and Simple Files

Library usage favors explicit Go construction (`NewAgent(WithModel(...), ...)`). CLI usage may support configuration files and environment variables, but Gotato avoids inventing a configuration language when ordinary Go values or simple structured configuration are sufficient.

### G-D30 — Backward Compatibility Is Valuable, but Clarity Wins

Because Gotato is a reusable runtime, API stability matters. However, preserving a confused abstraction forever is worse than making a well-documented breaking change during an intentional refactor.

Breaking changes are explicit, migration-friendly, and justified against PHILOSOPHY and DESIGN. Material user-facing breaks are recorded in `MIGRATION.md`.

---

## Governance: Core Admission Questions

Before adding a concept to Gotato, ask:

1. Is this reusable agent runtime semantics or one application's policy?
2. Can an application implement it cleanly using existing primitives?
3. Does adding it create a new mandatory worldview?
4. Does it preserve cheap/disposable Agent execution?
5. Does it preserve the distinction between Agent, Session, and Context?
6. Does it introduce an intrinsic agent hierarchy?
7. Does it make CLI and library semantics diverge?
8. Can it be tested deterministically?

## Architectural Drift Signals

Reconsider the design if:

- Gotato starts defining Master/Worker/sub-agent roles;
- Session becomes a project/task database;
- Context becomes synonymous with all Session history;
- every Agent requires a daemon or database;
- the CLI implements behavior unavailable to the library;
- dynamic tools require a second hidden execution engine;
- the repository cannot run useful deterministic tests without real model APIs;
- optional integrations become mandatory dependencies of core packages;
- an application cannot create a fresh agent run without cleanup from the previous run.
