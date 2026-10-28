# 008. Agents are flat, and the application composes them

Decided: 2026-10-16    Status: current

## The problem
Roles, hierarchy, orchestration, task structure and memory differ for every application. A runtime type for any of them is weight that each service embedding Gotato carries, and it would ship one application's idea of the others' work.

## The choice
There are only Agents. Roles and lineage are how an application composes agents, and they live in `Session.Metadata`. A Session stays generic: tasks, projects and workflows live in metadata too. Composing agents is ordinary Go code in the application, which already knows its own tasks. Gotato provides Sessions, the Context and Compaction, and what to remember across Sessions is built on them.

## Not chosen
- Sub-agents, supervisors, workers or any other role as a type: a ready vocabulary, but roles belong to the application.
- Multi-agent orchestration, an agent-to-agent protocol such as A2A, or worktrees: built-in composition, but the application already knows its tasks.
- Typed fields on a Session for tasks, projects or workflows: convenient for one application, but each keeps its own structure, so one Session type serves them all only if the structure stays in metadata.
- A built-in memory architecture: what to remember across Sessions differs by application.

## Consequences
One flat definition serves every application, and the hard rule in [AGENTS.md](../../AGENTS.md) keeps a role from becoming a type. An application that keeps a group of Agents does its own listing, aborting and cleanup ([012](012-a-run-stops-through-its-context-and-the-record-is-the-trace.md)).
