# Gotato Design

DESIGN turns [PHILOSOPHY.md](PHILOSOPHY.md) into durable engineering rules. A change that conflicts with a rule needs either a documented amendment to the constitution or a documented, migration-friendly breaking change (G-D30).

Vocabulary:

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
| **Extension** | a hook or middleware that wraps the runtime |

---

## 1. The Agent Primitive

### G-D01 — Agent as a Goroutine

An agent fits Go's concurrency model: `go agent.Run(ctx, session)` is the mental model, not a mandated signature. An agent runs inside the caller's process with no process, container, daemon, service, or scheduler of its own.

The core agent is one goroutine that owns the Run in flight; the caller blocks on `Prompt` or `Continue` and cancels through its `context.Context`.

### G-D02 — One Agent Primitive

Gotato has one agent abstraction and one execution model. Applications configure agents with different prompts, tools, models, context strategies, and lifecycles; planner, worker, supervisor, reviewer, and sub-agent are configurations of that one implementation. Role is application composition.

### G-D18 — Agent Identity Is Flat

Agent identity carries no parent or child IDs, sub-agent types, delegation trees, or supervisor semantics. Applications record such relationships in their own metadata, for example `Session.Metadata`.

### G-D10 — Reusable Configuration, Per-Run State

Reusable configuration holds the model provider and selection, tool registry, context builder, hooks, and execution policy. Mutable state belongs to a Run or Session, and every new execution starts clean.

Concretely: steer and follow-up messages left at the end of a Run are discarded, and the agent commits history to a `Transcript` supplied by the caller (a Session) or to a private one that dies with the agent.

### G-D11 — `context.Context` Owns Cancellation and Deadlines

Standard `context.Context` carries cancellation and deadlines through agent execution, model calls, tool calls, and, where appropriate, storage and stream consumers. Runtime deadlines (`CoreLimits.RunDeadline` and others) are derived contexts; Gotato has one cancellation mechanism, Go's.

---

## 2. Agent, Session, and Context

### G-D03 — Agent, Session, and Context Are Distinct

- **Agent — who acts?** The reusable behavior that executes the loop: model access, tools, runtime policies, extensions, loop behavior.
- **Session — what continuity exists?** What happened across turns and runs: messages, tool interactions, usage, runtime metadata, events, context-management state.
- **Context — what does the model see now?** The per-turn projection given to a model call: full history, recent history, compacted history, selected resources, summaries, or an application-defined projection.

An Agent may run against a Session many times, a Session may outlive any agent, and a Context may be rebuilt every turn. In code, the loop appends to a `Transcript` (the Session) and sends the model the output of a `ContextBuilder`; "all history" and "model input" are separate values.

### G-D04 — Session Is a First-Class Primitive

A Session represents identity, messages, model outputs, tool calls and results, runtime metadata, usage, important execution events, context and compaction metadata, and optional application metadata.

Session stays generic. Task graphs, agent roles, worktree integration, and product workflow live in `Metadata`.

### G-D05 — Session Storage Is Pluggable

Gotato defines the `session.Store` contract and ships in-memory and file-backed stores. Applications back it with the database they already run (GOALS G-N08). Library use works with the in-memory store alone.

### G-D06 — Forking Is a State Operation

A new Session can be created from an existing Session's state, for alternative paths, model comparisons, isolated bounded work, or derived flows. The fork records its parent Session ID as lineage metadata; agents are untouched.

### G-D07 — Context Is Built Through Explicit Strategies

Context is produced by a small, composable `ContextBuilder`. Gotato ships full history with static blocks, a tail panel, and compaction; applications implement `ContextBuilder` for any other projection (GOALS G-N10). Strategies are inspectable: a caller can ask "what would the model see for this Session now" without running a model.

### G-D08 — Compaction Belongs to the Standard Runtime

Gotato provides compaction hooks and implementations, including summarizers, and lets each application choose its summarization policy. Every compaction is recorded in the Session, naming what was replaced and what replaced it.

---

## 3. The Loop

### G-D09 — One Minimal Agentic Loop

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

Session, context, streaming, events, and extensions all serve this one loop.

### G-D17 — Extensions Wrap the Loop

Extensions add cross-cutting behavior: tracing, metrics, logging, policy checks, result transformation, custom context handling, provider-specific behavior. Each runs at a bounded stage of the one loop (`ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver`, `TurnStopper`, `RunPreparer`).

---

## 4. Tools

### G-D12 — Tool Registry Is a First-Class Primitive

The Tool Registry registers, unregisters, looks up, lists, describes, activates, and deactivates tools, supporting both static and dynamic tool surfaces. `ToolSet` and `ToolSource` plug in staged and external surfaces such as MCP servers; discovery policy and authorization build above them.

### G-D13 — Tools Are Capabilities

The core Tool contract is small and structured: identity, description, input schema, execution, structured result or error. Concrete tools (filesystem, shell, Git, browser, product) come from MCP servers and from applications through that contract (GOALS G-N09).

---

## 5. Models, Streaming, and Events

### G-D14 — Small, Capability-Aware Model Contract

One stable model contract covers normal execution. Provider-specific features live in adapters and optional capability interfaces, keeping the core contract small. Core carries opaque provider artifacts (for example reasoning signatures) without interpreting them.

### G-D15 — Streaming Is a Runtime Primitive

`EventStream` delivers structured execution progress, independent of any UI: model deltas, tool lifecycle, context events, usage, errors, and terminal events.

### G-D16 — Event Stream Is a First-Class Primitive

Applications observe structured events directly: agent and run lifecycle, turn lifecycle, context build and compaction, model request and response, tool call and result, usage, errors, session updates. Applications may map them into their own higher-level events.

### G-D29 — Observability Is Additive

Logs, traces, metrics, and usage attach without changing execution semantics. Observability is rich enough for debugging and optional to deploy.

---

## 6. Responsibilities Above the Runtime

### G-D19 — Orchestration Lives in Applications

Task graphs, agent pools, agent roles, workflow dependencies, project integration, desktop state, and multi-agent resource scheduling live in applications. Gotato supplies the primitives they use: sessions, contexts, events, tools, execution, and CLI access.

The `service` package (Session store, Agent per Run, admission, cancellation, HTTP and gRPC adapters) is built on the runtime and composes Sessions, Agents, and Contexts exactly as an application would. Core and standard runtime packages stay independent of it.

### G-D20 — Library Use Is Self-Contained

Embedding Gotato as a Go library is first-class and needs no companion daemon. The CLI and server are optional ways to run the same runtime.

### G-D21 — UI Lives Above the Runtime

Desktop and terminal user experiences are products built on Gotato. Gotato's own human-facing surface is CLI output.

---

## 7. The CLI

### G-D22 — CLI Is a First-Class Runtime Interface

`cmd/gotato` exposes the standard runtime to three equal audiences: humans, shell automation, and coding agents. Every core runtime capability is testable from the CLI without writing a Go program.

### G-D23 — Machine-Readable CLI Semantics

Important commands offer stable machine-readable output and controls where appropriate: `--json`, `--jsonl`, `--quiet`, `--no-color`, `--timeout`. Stdout carries data, stderr carries diagnostics, and documented exit codes report success, so callers never scrape human text.

### G-D24 — CLI and Library Share One Runtime

The CLI is a thin client of the Gotato packages. Every CLI behavior is reachable through the runtime API, except what is inherently presentation.

---

## 8. Testing

### G-D25 — Testing Is a First-Class Runtime Surface

The repository ships deterministic test utilities: fake and replay models, a fake tool, an event recorder, session and context fixtures, and failure injection where useful. A coding agent can modify Gotato, run unit tests and CLI scenarios, inspect structured output, and diagnose failures from a terminal. CI runs without paid model calls.

---

## 9. Repository Structure

### G-D26 — Broad Repository, Small Concepts

The repository may hold many useful packages; `Less is More` constrains the conceptual model and the mandatory dependencies. Packages are layered so users depend only on what they need.

### G-D27 — Package Direction Is Layered

```text
applications / cmd/gotato / service (Runner, HTTP and gRPC adapters)
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

Dependencies point inward: provider and tool adapters depend on core, and the root package imports only the standard library. `layering_test.go` enforces the direction.

### G-D28 — Go Values and Simple Files for Configuration

Library use configures with Go values (`NewAgent(WithModel(...), ...)`). The CLI adds configuration files and environment variables, using simple structured formats in place of a custom configuration language.

### G-D30 — Compatibility Matters; Clarity Wins

API stability matters for a reusable runtime, and a well-documented breaking change during an intentional refactor beats preserving a confused abstraction. Breaking changes are explicit, migration-friendly, justified against PHILOSOPHY and DESIGN, and recorded in `MIGRATION.md` when user-facing.

---

## Governance

### Admission Questions

Before adding a concept to Gotato, ask:

1. Is this reusable runtime semantics or one application's policy?
2. Can an application build it cleanly from existing primitives?
3. Does it introduce a new mandatory worldview?
4. Does it keep Agent execution cheap and disposable?
5. Does it keep Agent, Session, and Context distinct?
6. Does it keep agents peers, free of intrinsic hierarchy?
7. Does it keep CLI and library semantics identical?
8. Can it be tested deterministically?

### Drift Signals

Reconsider the design when:

- agent roles or sub-agents appear in Gotato types;
- Session starts acting as a project or task database;
- Context becomes a synonym for all Session history;
- an Agent needs a daemon or database to run;
- the CLI implements behavior the library lacks;
- dynamic tools need a second hidden execution engine;
- useful deterministic tests need real model APIs;
- an optional integration becomes a dependency of core packages;
- a fresh agent run needs cleanup from a previous run.
