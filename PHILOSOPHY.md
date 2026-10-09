# Gotato Philosophy

> **Gotato is a minimal, synchronous, Go-native agent runtime.**

Gotato provides the standard primitives for building agentic applications in Go: broader than a single agent loop, smaller than an application framework. Applications own their interfaces and agent organizations; inside Gotato every agent is a peer.

These beliefs sit above implementation detail and hold even when a feature would be convenient. [DESIGN.md](DESIGN.md) turns them into engineering rules, [GOALS.md](GOALS.md) states what Gotato aims for, and [FEATURES.md](FEATURES.md) tracks the runtime surface.

---

## G-P01 — Less Is More

Gotato grows more useful by requiring less conceptual machinery. Its quality is measured by whether a developer can understand the runtime, compose it with ordinary code, and use only the pieces they need.

"Less" means a small set of orthogonal concepts in place of overlapping concepts, hidden behaviors, and mandatory subsystems. Every major addition answers one question:

> Does this make the reusable agent runtime more complete, or does it belong to an application above it?

## G-P02 — Agents Are Cheap and Disposable

Creating an agent is as light as starting a unit of work. Applications create an agent for bounded work, use it briefly, and discard it. Long-lived agents are supported; the runtime is designed around short-lived ones.

Gotato keeps per-agent weight, background services, state, and lifecycle ceremony to the minimum that short-lived agents need.

## G-P03 — There Are Only Agents

Gotato has one kind of agent. Main, sub, child, supervisor, worker, reviewer, and planner are roles an application gives to peers.

Delegation, supervision, and "created because another agent asked" are application relationships. The agent primitive stays independent of the topology built around it.

## G-P04 — Runtime Primitives, Application Doctrine

Gotato provides reusable primitives; applications choose their product model. A coding environment, a desktop assistant, an automated service, a research system, and an asynchronous multi-agent runtime all share sessions, contexts, tools, events, and model execution while each keeps its own semantics.

## G-P05 — Composition Over Centralization

Gotato composes into the host program. Applications replace or extend persistence, providers, tools, context strategies, observability, and execution policy piece by piece, each piece independent wherever that keeps the system easier to understand.

## G-P06 — Ordinary Go

Using Gotato is writing Go: direct, explicit, unsurprising composition with ordinary types, interfaces, and functions. Gotato asks for no parallel worldview, workflow language, or framework inheritance tree.

## G-P07 — Continuity and Attention Are Different Things

What has happened in an interaction and what a model should see right now are separate concerns. Gotato preserves history for continuity and gives each model call the context useful for that turn.

> **Session is what happened. Context is what the model sees now.**

## G-P08 — Explicit, Inspectable Behavior

Important behavior is observable. A caller can determine what an agent saw, which tools it had, what it called, what it produced, why it stopped, and what failed. Every convenience is backed by policy the caller can inspect and replace.

## G-P09 — Machine Usability Is First-Class

Gotato is built to be operated by scripts and coding agents as well as humans. Stable, machine-readable interfaces for developing, testing, inspecting, and debugging make the project easier to evolve autonomously and to validate reliably.

---

## Core Manifesto

**Less is More.**

**Agents should be highly cheap and disposable.**

**There are only agents.**

**Agent as a Goroutine.**

**Session is what happened. Context is what the model sees now.**

**The CLI is a first-class interface for humans, scripts, and coding agents.**

Gotato is complete enough for higher-level products to build on directly, and restrained enough that those products stay above it.
