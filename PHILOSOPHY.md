# Gotato Philosophy

> **Gotato is a minimalistic, composable Go agent runtime.**

This document holds the beliefs that sit above implementation detail. They do not change merely because a particular feature is convenient to add. Engineering rules derived from these beliefs live in [DESIGN.md](DESIGN.md); the intended runtime surface lives in [FEATURES.md](FEATURES.md); what Gotato is and is not trying to become lives in [GOALS.md](GOALS.md).

Gotato provides the standard runtime primitives needed to build agentic applications in Go without prescribing what those applications must become. It is deliberately broader than a single agent loop and deliberately smaller than an application framework. It has no built-in UI. It does not define agent organizations: it does not know what a Master, Operator, Worker, supervisor, child agent, or sub-agent is. Applications may create such roles, but inside Gotato they are all simply agents.

---

## G-P01 — Less Is More

Gotato should become more useful by requiring less conceptual machinery.

Its quality is not measured by the number of abstractions, built-in products, orchestration systems, or configuration layers it accumulates. Its quality is measured by whether a developer can understand the runtime, compose it with ordinary code, and use only the pieces that are actually needed.

"Less" does not mean artificially removing useful capabilities. It means preferring a small set of orthogonal concepts over a large set of overlapping concepts, hidden behaviors, and mandatory subsystems.

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

> **Session is what happened. Context is what the model sees now.**

## G-P08 — Explicit Behavior Over Hidden Intelligence

The runtime should make important behavior observable and understandable.

A caller should be able to determine what an agent saw, what tools it had, what it called, what it produced, why it stopped, and what failed.

Convenience should not depend on invisible policy that cannot be inspected or replaced.

## G-P09 — Machine Usability Is a First-Class Use Case

Gotato should be easy to operate not only by humans but also by scripts and coding agents.

A project that can be developed, tested, inspected, and debugged through stable machine-readable interfaces is easier to evolve autonomously and easier to validate reliably.

---

## Core Manifesto

**Less is More.**

**Agents should be highly cheap and disposable.**

**No agent is a sub-agent. There are only agents.**

**Session is what happened. Context is what the model sees now.**

**The CLI is a first-class interface for humans, scripts, and coding agents.**

Gotato should be complete enough that higher-level products can build on it directly, and restrained enough that those products do not become part of Gotato itself.
