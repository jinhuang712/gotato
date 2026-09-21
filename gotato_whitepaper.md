# Gotato Whitepaper

**Version:** 0.3  
**Language:** English  
**Status:** Historical design record (the earlier architectural baseline)

> **Note:** This whitepaper is retained as the original design record. It is no
> longer the constitution: the root documents (`PHILOSOPHY.md`, `DESIGN.md`,
> `GOALS.md`, `PROPOSAL.md`, `FEATURES.md`) are the documents of record, and
> where they disagree with this file they win. See `AGENTS.md` for the read
> order.

---

## 0. Definition

> **Gotato is a minimalistic, composable Go agent runtime.**

Gotato provides the standard runtime primitives needed to build agentic applications in Go without prescribing what those applications must become.

It is deliberately broader than a single agent loop and deliberately smaller than an application framework. A useful Gotato installation should provide reusable concepts for **Agent, Session, Context, Model, Tool, Tool Registry, Event, Extension, Provider, Persistence, CLI, and Testing** while keeping those concepts small, orthogonal, and Go-native.

Gotato has no built-in UI. A terminal application, desktop application, service, automation system, or another runtime can all build on the same foundation.

Gotato does not define agent organizations. It does not know what a Master, Operator, Worker, supervisor, child agent, or sub-agent is. Applications may create such roles, but inside Gotato they are all simply agents.

This document was the project's original architectural baseline. The current documents of record are **PHILOSOPHY** and **DESIGN** (which constrain decisions throughout the lifetime of the repository), **FEATURES** (the standard runtime surface), and **GOALS** (what Gotato is and is not trying to become). Where this whitepaper disagrees with them, they win.

---

# 1. PHILOSOPHY

PHILOSOPHY contains the beliefs that sit above implementation detail. These principles should not change merely because a particular feature is convenient to add.

## G-P01 — Less Is More

Gotato should become more useful by requiring less conceptual machinery.

Its quality is not measured by the number of abstractions, built-in products, orchestration systems, or configuration layers it accumulates. Its quality is measured by whether a developer can understand the runtime, compose it with ordinary code, and use only the pieces that are actually needed.

“Less” does not mean artificially removing useful capabilities. It means preferring a small set of orthogonal concepts over a large set of overlapping concepts, hidden behaviors, and mandatory subsystems.

Every major addition should answer a simple question:

> Does this make the reusable agent runtime more complete, or does it make Gotato responsible for an application that should exist above it?

## G-P02 — Agents Should Be Highly Cheap and Disposable

Creating an agent should not feel like provisioning infrastructure.

Applications should be free to create an agent for bounded work, use it briefly, and discard it immediately afterward. Long-lived agents are valid, but they are not the assumption around which the runtime is designed.

Cheap and disposable is a first-class property. Gotato should continuously resist hidden per-agent weight, mandatory background services, oversized state, and lifecycle ceremony that makes short-lived agents unnatural.

## G-P03 — No Agent Is a Sub-Agent

Gotato has only agents.

It does not define intrinsic main agents, sub-agents, child agents, supervisor agents, worker agents, reviewer agents, or planner agents.

Applications may create relationships between agents. An agent may delegate to another agent, supervise another agent, or exist because another agent requested work. Those relationships belong to the application. They do not create a different class of agent in Gotato.

This keeps the agent primitive independent from the topology built around it.

## G-P04 — Runtime Primitives, Not Product Doctrine

Gotato should provide reusable primitives without forcing applications into one product model.

A coding environment, a desktop assistant, an automated service, a research system, and an asynchronous multi-agent runtime may all require sessions, contexts, tools, events, and model execution. They should be able to share Gotato without inheriting one another's application semantics.

## G-P05 — Composition Over Centralization

Gotato should compose with the host program rather than attempting to become the host program.

Applications should be able to replace or extend persistence, providers, tools, context strategies, observability, and execution policy without routing every decision through one monolithic global object.

A useful piece should remain independent when independence makes the system easier to understand.

## G-P06 — Ordinary Go Should Remain Ordinary Go

A developer using Gotato should still feel like they are writing Go.

Gotato should not require a parallel worldview, a proprietary workflow language, or a deep framework-specific inheritance tree. The repository should prefer direct, explicit, unsurprising composition.

## G-P07 — Continuity and Attention Are Different Things

What has happened in an interaction is not the same as what a model should see right now.

Gotato therefore treats long-lived continuity and turn-specific attention as distinct concerns. The runtime should preserve history when continuity matters while allowing each model call to receive only the context that is useful for the current turn.

## G-P08 — Explicit Behavior Over Hidden Intelligence

The runtime should make important behavior observable and understandable.

A caller should be able to determine what an agent saw, what tools it had, what it called, what it produced, why it stopped, and what failed.

Convenience should not depend on invisible policy that cannot be inspected or replaced.

## G-P09 — Machine Usability Is a First-Class Use Case

Gotato should be easy to operate not only by humans but also by scripts and coding agents.

A project that can be developed, tested, inspected, and debugged through stable machine-readable interfaces is easier to evolve autonomously and easier to validate reliably.

---

# 2. DESIGN

DESIGN translates the philosophy into durable engineering rules. These rules are intentionally stronger than ordinary implementation suggestions.

## G-D01 — Agent as a Goroutine

An agent should fit naturally into Go's concurrency model.

The intended mental model is:

```go
go agent.Run(ctx, session)
```

This is a design principle, not a requirement that every public API literally use this exact signature.

An agent should not inherently require its own process, container, daemon, service, or scheduler.

## G-D02 — One Agent Primitive

Gotato should maintain one fundamental agent abstraction and one fundamental execution model.

Different applications may configure agents with different prompts, tools, models, context strategies, or lifecycles, but the runtime should not fork into separate core implementations for planner, worker, supervisor, reviewer, or sub-agent roles.

Role is application composition, not Gotato type hierarchy.

## G-D03 — Agent, Session, and Context Are Distinct

Gotato should make three concepts explicit:

### Agent — who acts?

An Agent owns or references the reusable behavior required to execute an agentic loop: model access, tools, runtime policies, extensions, and loop behavior.

### Session — what continuity exists?

A Session represents the logical continuity of an interaction. It records what happened across turns and runs: messages, tool interactions, usage, runtime metadata, events, and context-management state.

### Context — what does the model see now?

Context is the turn-specific projection supplied to a model call. It may contain all session history, only recent history, a compacted history, selected resources, summaries, or another application-defined projection.

The canonical distinction is:

> **Session is what happened. Context is what the model sees now.**

An Agent may run against a Session many times. A Session may outlive an individual agent execution. A Context may be rebuilt for every turn.

## G-D04 — Session Is a First-Class Runtime Primitive

Session continuity is sufficiently generic to belong in Gotato.

A standard Session should be able to represent at least:

- session identity,
- messages,
- model outputs,
- tool calls and tool results,
- runtime metadata,
- usage,
- important execution events,
- context/compaction metadata,
- and optional application metadata.

Session should not know application-specific concepts such as Task Graph, Master, Operator, worktree integration, or product workflow.

## G-D05 — Session Storage Is Pluggable

Gotato should define storage contracts without forcing one persistence engine.

Expected implementations may include:

- in-memory storage,
- JSONL or file-backed storage,
- SQLite-backed storage,
- application-provided stores.

A library-only use case should not require a database.

## G-D06 — Session Forking Is a Generic Primitive

Gotato should support creating a new Session from an existing Session state or checkpoint when practical.

Forking is a state operation, not an agent hierarchy operation.

Useful cases include exploring an alternative path, testing a different model, creating an isolated bounded execution, or starting a derived application flow.

## G-D07 — Context Is Built Through Explicit Strategies

Context management should not be buried inside opaque message mutation.

Gotato should expose a small context-building abstraction that can support strategies such as:

- full history,
- sliding window,
- compacted history,
- summary + recent turns,
- selected references,
- application-defined projection.

Context strategies should be composable and inspectable.

## G-D08 — Compaction Belongs to the Standard Runtime

Long-running agent sessions inevitably encounter context-window pressure.

Gotato should therefore provide standard hooks and implementations for compaction without dictating one universal summarization policy.

Compaction should preserve traceability: applications should be able to tell what was compacted and what representation replaced it.

## G-D09 — One Minimal Agentic Loop

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

Session, context, streaming, events, and extensions should support this loop rather than create competing hidden execution semantics.

## G-D10 — Reusable Agent Configuration and Mutable Run State Are Separate

Reusable configuration may include:

- model provider,
- model selection,
- tool registry,
- context builder,
- hooks,
- execution policy.

Mutable execution state belongs to a run/session and should not leak into unrelated executions.

A new execution must not accidentally inherit a previous run's mutable state.

## G-D11 — `context.Context` Owns Cancellation and Deadlines

Go's standard `context.Context` should propagate cancellation and deadlines through:

- agent execution,
- model calls,
- tool calls,
- storage operations where appropriate,
- streaming consumers where appropriate.

Gotato should not create a parallel cancellation universe when standard Go semantics are sufficient.

## G-D12 — Tool Registry Is a First-Class Primitive

A standard runtime needs more than an anonymous `[]Tool`.

Gotato should provide a Tool Registry capable of supporting operations such as:

- register,
- unregister,
- lookup,
- list,
- describe,
- activate,
- deactivate.

The registry should enable both static and dynamic tool surfaces without defining application-level orchestration.

Tool discovery systems, MCP catalogs, authorization policy, or deferred-loading policy may be built above or beside the registry.

## G-D13 — Tools Are Capabilities, Not Applications

The core Tool contract should remain small and structured:

- identity,
- description,
- input contract/schema,
- execution,
- structured result/error.

Filesystem, shell, Git, browser, MCP, or product-specific tool packages may be provided in the repository, but they should not make those capabilities mandatory for every agent.

## G-D14 — Models Use a Small Capability-Aware Contract

Gotato should expose a stable model contract for normal agent execution without pretending every provider has identical capabilities.

Provider-specific features should be expressed through adapters or optional capability interfaces where possible instead of continuously expanding one universal interface.

## G-D15 — Streaming Is a Runtime Primitive

Streaming should expose structured execution progress independent of any UI.

Callers should be able to consume model deltas, tool lifecycle, context events, usage, errors, and terminal events through a Go-native streaming abstraction.

## G-D16 — Event Stream Is a First-Class Primitive

Gotato should emit structured runtime events that applications can observe without parsing logs.

Expected event families include:

- agent/run lifecycle,
- turn lifecycle,
- context build/compaction,
- model request/response,
- tool call/result,
- usage,
- error,
- session update.

Applications such as Mow may map Gotato events into higher-level application events.

## G-D17 — Extensions Wrap the Runtime; They Do Not Replace It

Gotato should support hooks or middleware for cross-cutting behavior such as:

- tracing,
- metrics,
- logging,
- policy checks,
- result transformation,
- custom context handling,
- provider-specific behavior.

Extensions must not silently create a second agent loop with incompatible semantics.

## G-D18 — No Intrinsic Agent Hierarchy

The runtime must not require parent agent IDs, child agent IDs, sub-agent types, delegation trees, or supervisor semantics as part of Agent identity.

An application may store such relationships as its own metadata.

## G-D19 — No Application Orchestration in the Runtime Foundation

Gotato should not own application-level concepts such as:

- task graphs,
- worker pools,
- master/operator roles,
- workflow dependency management,
- project integration,
- desktop state,
- global multi-agent resource scheduling.

Gotato may provide generic lower-level primitives that such systems use: sessions, contexts, events, tools, execution, and CLI access.

## G-D20 — No Mandatory Background Daemon

Embedding Gotato as a Go library must remain first-class.

A CLI or optional server may exist, but using the standard runtime should not require a companion daemon.

## G-D21 — No Built-In UI

Gotato should not ship a project-specific desktop or terminal user experience as part of its architectural identity.

It may provide CLI output intended for humans, but UI products belong above the runtime.

## G-D22 — CLI Is a First-Class Runtime Interface

Gotato must provide an official CLI over its standard runtime primitives.

The CLI serves three audiences equally:

- humans,
- shell automation,
- coding agents.

Core runtime capabilities should be testable without writing a custom Go program.

## G-D23 — Machine-Readable CLI Semantics Are Mandatory

Important CLI commands should support stable machine-readable output, including where appropriate:

```text
--json
--jsonl
--quiet
--no-color
--timeout
```

Stdout should contain requested data. Diagnostics should prefer stderr. Exit codes should be meaningful and documented.

The CLI must not require scraping decorative human text to determine whether an operation succeeded.

## G-D24 — CLI and Go Library Share the Same Runtime

The CLI must be a thin client of Gotato packages, not a separate implementation of agent semantics.

A behavior that exists only in CLI code and cannot be exercised through the runtime API is an architectural smell unless it is inherently presentation-specific.

## G-D25 — Testing Is a First-Class Runtime Surface

Gotato should make itself easy for coding agents and normal tests to exercise deterministically.

The repository should provide test utilities such as:

- fake model,
- replay model,
- fake tool,
- event recorder,
- session fixtures,
- context fixtures,
- deterministic failure injection where useful.

A coding agent should be able to modify Gotato, run unit tests, run CLI scenarios, inspect structured output, and diagnose failures without a GUI.

## G-D26 — Repository Can Be Broad While Concepts Stay Small

Gotato may be a substantial repository similar in spirit to a runtime monorepo.

`Less is More` constrains the conceptual model and mandatory dependencies, not the number of useful packages in the repository.

The repository may include:

- core runtime packages,
- standard session/context packages,
- providers,
- standard tools,
- persistence adapters,
- MCP support,
- CLI,
- testing utilities,
- examples.

These packages should remain layered so users can depend only on what they need.

## G-D27 — Package Direction Must Remain Layered

A recommended dependency direction is:

```text
applications / cmd/gotato
          |
          v
standard runtime packages
(session / context / registry / event / extensions)
          |
          v
core execution packages
(agent / model / tool / message / streaming)
          |
          v
Go standard library + narrow external dependencies
```

Provider and tool adapters should depend inward. Core packages should not depend outward on product packages.

## G-D28 — Configuration Should Prefer Go Values and Simple Files

Library usage should favor explicit Go construction.

CLI usage may support configuration files and environment variables, but Gotato should avoid inventing a large configuration language when ordinary Go values or simple structured configuration are sufficient.

## G-D29 — Observability Must Be Additive

Logs, traces, metrics, and usage tracking should be attachable without changing the core semantics of agent execution.

Observability should be rich enough to support debugging but must not become a mandatory centralized service.

## G-D30 — Backward Compatibility Is Valuable, but Clarity Wins

Because Gotato is a reusable runtime, API stability matters.

However, preserving a confused abstraction forever is worse than making a well-documented breaking change during an intentional refactor.

Breaking changes should be explicit, migration-friendly, and justified against PHILOSOPHY and DESIGN.

---

# 3. FEATURES

FEATURES describes the intended repository surface. It is not a promise that every item exists today or that every package name is final.

## G-F01 — Agent Runtime

- reusable Agent configuration,
- run/turn execution,
- tool-call loop,
- final result,
- termination reason,
- usage aggregation,
- cancellation/deadline propagation.

## G-F02 — Session

- session ID,
- message history,
- model/tool interaction history,
- metadata,
- usage,
- runtime events or event references,
- context/compaction metadata,
- create/load/save/close lifecycle,
- fork/branch support where practical.

## G-F03 — Session Store Interfaces

- in-memory store,
- file/JSONL store,
- optional SQLite implementation,
- application-defined storage adapter.

## G-F04 — Context Runtime

- ContextBuilder interface,
- full-history strategy,
- window strategy (rejected/future: `PROPOSAL.md` and FEATURES.md G-F04 deliberately do not provide sliding windows),
- compacted strategy,
- summary + recent strategy (rejected/future: per-Turn summaries are deliberately not provided, see `PROPOSAL.md` and FEATURES.md G-F04),
- selected-reference projection,
- custom application strategy.

## G-F05 — Context Inspection

The runtime and CLI should make it possible to inspect:

- source session state,
- selected messages/resources,
- compaction state,
- approximate or provider-reported token usage,
- final model context.

## G-F06 — Context Compaction

- compaction trigger hooks,
- summarizer interface,
- compacted segment metadata,
- explicit replacement/retention behavior,
- events for compaction start/finish/failure.

## G-F07 — Model Interface

- non-streaming or unified streaming request path,
- tool definitions,
- tool requests,
- provider usage,
- provider errors,
- optional capability discovery.

## G-F08 — Provider Packages

Provider adapters may include major model providers while keeping provider-specific dependencies isolated from core packages.

## G-F09 — Tool Interface

- stable identity,
- description,
- input schema/validation,
- execution with `context.Context`,
- structured result,
- structured error.

## G-F10 — Tool Registry

- register/unregister,
- activate/deactivate,
- list/lookup/describe,
- active set inspection,
- dynamic registration support,
- event hooks for tool-surface changes.

## G-F11 — Standard Tool Packages

Optional packages may include:

- filesystem read/write/edit/search,
- shell execution,
- Git primitives,
- HTTP/fetch primitives where appropriate.

These should remain optional capabilities, not assumptions baked into every Agent.

## G-F12 — MCP Integration

An optional MCP package may provide:

- MCP client lifecycle,
- server configuration,
- tool discovery,
- tool adaptation into the Gotato Tool contract,
- lazy connection where useful,
- compatibility with dynamic Tool Registry behavior.

MCP is a standard integration, not the definition of Gotato.

## G-F13 — Runtime Events

Structured events for:

- run started/completed,
- turn started/completed,
- context built/compacted,
- model request/response,
- tool requested/started/completed,
- session updated,
- usage,
- error/cancellation.

## G-F14 — Streaming API

- typed event stream,
- model delta forwarding where appropriate,
- cancellation-aware consumers,
- terminal event/result correlation.

## G-F15 — Extensions / Hooks

- before/after model call,
- before/after tool call,
- before/after context build,
- before finish,
- event observation,
- policy hooks where bounded and explicit.

## G-F16 — CLI: `gotato run`

Expected capabilities:

```text
gotato run "inspect this repository"
gotato run --session <id> "continue"
gotato run --json ...
gotato run --events=jsonl ...
```

The exact command syntax may evolve, but one-off and session-backed execution must be easy to automate.

## G-F17 — CLI: Session Operations

Expected command family:

```text
gotato session create
gotato session list
gotato session show <id>
gotato session resume <id>
gotato session fork <id>
gotato session events <id>
```

## G-F18 — CLI: Context Operations

Expected command family:

```text
gotato context inspect <session>
gotato context build <session>
gotato context compact <session>
```

Machine-readable forms should be supported.

## G-F19 — CLI: Tool Operations

Expected command family:

```text
gotato tools list
gotato tools describe <name>
gotato tools active
gotato tools activate <name>
gotato tools deactivate <name>
```

## G-F20 — CLI: Event Inspection

Expected capabilities:

```text
gotato events --session <id> --jsonl
gotato run ... --events=jsonl
```

This is especially important for coding-agent-driven integration testing.

## G-F21 — CLI: `doctor`

`gotato doctor` should inspect the environment and report actionable diagnostics for configured providers, credentials, storage, tools, optional MCP connections, and other runtime prerequisites.

`gotato doctor --json` should be suitable for automated diagnosis.

## G-F22 — Testing Package

A dedicated testing package should include reusable fixtures and fakes for deterministic runtime tests.

Suggested components:

```text
FakeModel
ReplayModel
FakeTool
EventRecorder
SessionFixture
ContextFixture
FailureInjector
```

## G-F23 — Scenario Testing

The project may provide a scenario runner through Go tests or CLI to execute deterministic agent flows from fixtures and assert:

- final status,
- tool calls,
- event sequence,
- session state,
- context behavior,
- termination reason.

## G-F24 — Examples

Examples should demonstrate composition rather than one blessed application architecture:

- one-shot agent,
- persistent session,
- context compaction,
- session fork,
- dynamic tools,
- MCP integration,
- concurrent agents,
- CLI automation.

## G-F25 — Documentation as Runtime Contract

Public packages and CLI behavior should be documented with enough precision that a coding agent can discover the intended semantics from the repository itself.

---

# 4. GOALS

## G-G01 — Primary Goal

> **Make capable agents an ordinary, lightweight, composable runtime primitive in Go.**

Gotato should be practical both for a single embedded Agent and as the foundation beneath a much larger application.

## G-G02 — Standard Runtime Goal

Provide enough reusable runtime infrastructure that applications do not need to rebuild session continuity, context construction, tool registration, structured events, CLI diagnostics, and basic testing every time.

## G-G03 — Cheap Execution Goal

Agent configuration and execution should remain light enough that applications can create and discard bounded agents freely under application-controlled concurrency.

Performance should be measured rather than asserted. Benchmarks should distinguish Gotato overhead from model latency, context size, tool processes, and provider behavior.

## G-G04 — Composability Goal

Applications should be able to use Gotato as:

- a library,
- a CLI runtime,
- a foundation for a desktop product,
- a foundation for a service,
- a foundation for an orchestration system such as Mow.

None of those products should need to fork the core agent runtime to create their own role model.

## G-G05 — Session and Context Goal

Gotato should make long-lived continuity practical without forcing every model call to carry all history.

Session and Context should be reusable enough that higher-level applications can project their own semantics cleanly.

## G-G06 — Agent-Friendly Development Goal

The repository should be operable by coding agents through:

- documented Go APIs,
- deterministic tests,
- stable CLI commands,
- machine-readable output,
- structured events,
- clear repository guidance.

A coding agent should be able to implement a change and validate it without relying on a graphical interface.

## G-G07 — Repository Goal

Gotato should grow into a coherent runtime repository, not a single oversized package.

The repository may contain many useful packages while keeping dependencies layered and concepts small.

---

# 5. NON-GOALS

## G-N01 — No Built-In Agent Organization

Gotato does not define Master, Operator, Worker, supervisor, sub-agent, team, swarm, or organizational hierarchy.

## G-N02 — No Application Task Graph

Gotato does not own durable application Tasks, task dependency graphs, integration pipelines, or project-level scheduling.

## G-N03 — No Global Multi-Agent Resource Scheduler

Applications that run many agents remain responsible for global concurrency, CPU/RAM protection, build/test limits, provider quotas, and other system-level resource policy.

Gotato should provide cancellable lightweight runs that such schedulers can control.

## G-N04 — No Product UI

Gotato does not aim to become an IDE or desktop product.

Its official CLI is a runtime interface and diagnostic surface, not a substitute for every possible product UI.

## G-N05 — No Mandatory Service Mode

A server mode may exist later, but library and CLI use must not depend on it.

## G-N06 — No Universal Memory Doctrine

Gotato may provide Session, Context, persistence interfaces, and compaction primitives. It does not prescribe one semantic long-term memory architecture for all applications.

## G-N07 — No Workflow DSL as Core Identity

Ordinary Go composition should remain the default. If workflow packages exist, they should be optional and must not redefine Agent semantics.

---

# 6. NECESSARY TRADEOFFS

## G-T01 — A Larger Standard Runtime Is Accepted to Avoid Rebuilding Basics

Gotato intentionally owns more than an agent loop.

Session, Context, Tool Registry, Events, CLI, and Testing increase repository size, but they reduce duplicated infrastructure across every application built on top.

The constraint is conceptual clarity, not minimal file count.

## G-T02 — More Primitives Require Stronger Boundaries

Once Session and Context become first-class, the project must resist turning them into application-specific state containers.

Generic continuity belongs in Gotato. Product semantics belong above it.

## G-T03 — Persistence Convenience Must Not Become a Database Requirement

Providing standard persistence is useful. Requiring persistence for every run is not.

## G-T04 — CLI Stability Becomes Part of Compatibility

A first-class CLI creates an external contract used by scripts and coding agents. Changes to command names, fields, exit codes, and JSON output require deliberate compatibility handling.

## G-T05 — Cheap Agents Do Not Mean Unlimited Agents

Gotato should make agents cheap. It should not claim that infinite concurrency is safe.

Applications remain responsible for global scheduling and resource governance.

---

# 7. REFERENCE ARCHITECTURE

```text
+----------------------------------------------------------------------------------+
|                              APPLICATIONS                                        |
|                                                                                  |
|         Mow        CLI apps        services        automation        tests        |
|          |             |               |                |               |         |
+----------+-------------+---------------+----------------+---------------+---------+
                                   |
                                   v
+----------------------------------------------------------------------------------+
|                         GOTATO STANDARD RUNTIME                                  |
|                                                                                  |
|   +-------------+   +-------------+   +--------------+   +------------------+   |
|   |   Session   |   |   Context   |   | Tool Registry|   |   Event Stream   |   |
|   |             |   |             |   |              |   |                  |   |
|   | continuity  |   | model view  |   | capability   |   | structured facts |   |
|   +------+------+   +------+------+   +------+-------+   +---------+--------+   |
|          |                 |                 |                       |            |
|          +-----------------+-----------------+-----------------------+            |
|                                    |                                             |
|                             +------v-------+                                     |
|                             |    Agent     |                                     |
|                             |             |                                     |
|                             | reusable    |                                     |
|                             | config      |                                     |
|                             +------+------+
|                                    |                                             |
|                                    v                                             |
|                             Agent Execution                                      |
|                                                                                  |
|   Extensions     Persistence     Providers     Standard Tools     Optional MCP   |
+-----------------------------------+----------------------------------------------+
                                    |
                                    v
+----------------------------------------------------------------------------------+
|                              GOTATO CORE                                         |
|                                                                                  |
|       Agentic Loop        Model Contract        Tool Contract        Messages     |
|            |                    |                    |                   |         |
|            +--------------------+--------------------+-------------------+         |
|                                    |                                             |
|                              Streaming / Usage                                   |
+-----------------------------------+----------------------------------------------+
                                    |
                                    v
+----------------------------------------------------------------------------------+
|                                     GO                                           |
|                                                                                  |
|             goroutine        context.Context        channel        interface      |
+----------------------------------------------------------------------------------+
```

### Canonical runtime relationship

```text
Session
  |
  |  "what happened"
  v
ContextBuilder
  |
  |  "what should the model see now"
  v
Context
  |
  v
Agent ---- Tool Registry
  |
  v
Agentic Loop
  |
  +---- model
  |
  +---- tools
  |
  v
Session updates + structured events
```

### Agent lifecycle

```text
reusable Agent configuration
          |
          +---------------------------+
          |                           |
          v                           v
      Run #1                      Run #2
      goroutine                   goroutine
          |                           |
      Session A                   Session B
          |                           |
      Context(s)                  Context(s)
          |                           |
        result                      result
          |                           |
          v                           v
      run state                    run state
      discarded                    discarded
```

No relationship in this diagram creates a sub-agent.

---

# 8. CLI REFERENCE MODEL

```text
                          developer / coding agent / script
                                      |
                                      v
                              +---------------+
                              |  gotato CLI   |
                              +-------+-------+
                                      |
                     same public runtime semantics
                                      |
                                      v
                        +---------------------------+
                        | Gotato Standard Runtime   |
                        +---------------------------+
```

Representative commands:

```text
gotato run
gotato session ...
gotato context ...
gotato tools ...
gotato events ...
gotato doctor
```

The CLI should make Gotato self-testable by coding agents.

---

# 9. REPOSITORY SHAPE

The final package structure should follow the principles above rather than this exact spelling, but a healthy repository may resemble:

```text
gotato/
|
+-- cmd/
|   +-- gotato/
|
+-- agent/
+-- session/
+-- context/
+-- model/
+-- tool/
+-- event/
+-- extension/
+-- streaming/
+-- providers/
+-- tools/
+-- persistence/
+-- mcp/
+-- testing/
+-- examples/
|
+-- PROPOSAL.md
+-- PHILOSOPHY.md
+-- DESIGN.md
+-- FEATURES.md
+-- GOALS.md
+-- AGENTS.md
+-- GITFLOW.md
```

Package boundaries should be discovered and refined through the existing codebase rather than imposed mechanically.

---

# 10. PROJECT GOVERNANCE

## Core admission questions

Before adding a concept to Gotato, ask:

1. Is this reusable agent runtime semantics or one application's policy?
2. Can an application implement it cleanly using existing primitives?
3. Does adding it create a new mandatory worldview?
4. Does it preserve cheap/disposable Agent execution?
5. Does it preserve the distinction between Agent, Session, and Context?
6. Does it introduce an intrinsic agent hierarchy?
7. Does it make CLI and library semantics diverge?
8. Can it be tested deterministically?

## Architectural drift signals

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

---

# 11. CORE MANIFESTO

> **Gotato is a minimalistic, composable Go agent runtime.**

**Less is More.**

**Agents should be highly cheap and disposable.**

**No agent is a sub-agent. There are only agents.**

**Agent as a Goroutine.**

**Session is what happened. Context is what the model sees now.**

**The CLI is a first-class interface for humans, scripts, and coding agents.**

Gotato should be complete enough that higher-level products can build on it directly, and restrained enough that those products do not become part of Gotato itself.
