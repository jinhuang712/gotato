# Gotato Refactor Prompt

> **Historical record (completed one-time refactor).** The governance documents this prompt asked for now exist and are the tracked constitution; see `AGENTS.md` for the read order. The whitepaper is no longer the constitution — the root documents are. Treat the instructions below as the record of that refactor, not as an active task.

You are working on an **existing Gotato repository**.

Your job is to refactor the existing codebase so it converges on the architecture defined by `gotato_whitepaper.md` while preserving useful existing functionality and Git history.

Do not treat this as a greenfield rewrite unless the current repository is genuinely unsalvageable and the evidence is documented. Prefer incremental restructuring, compatibility layers, and explicit migrations.

The whitepaper is the project constitution. When implementation convenience conflicts with the whitepaper, follow the whitepaper unless you can demonstrate a contradiction in the whitepaper itself. If you find such a contradiction, document it before changing architecture.

---

# 1. Inputs

You have been given:

```text
gotato_whitepaper.md
this prompt
```

Assume the current directory is the existing Gotato repository, or locate the repository in the current workspace before proceeding.

Do not ask for confirmation for routine refactoring work. Inspect the repository, infer the current architecture, and proceed autonomously.

Only stop for user input when one of the following is true:

- a destructive migration cannot be made safely,
- credentials or external access are required and unavailable,
- two product requirements are genuinely contradictory,
- a public compatibility decision cannot be inferred from repository history or documentation.

---

# 2. First Principle

The target identity is:

> **Gotato is a minimalistic, composable Go agent runtime.**

The following principles are non-negotiable:

- **Less is More.**
- **Agents should be highly cheap and disposable.**
- **No agent is a sub-agent. There are only agents.**
- **Agent as a Goroutine.**
- **Session is what happened. Context is what the model sees now.**
- **CLI is a first-class interface for humans, scripts, and coding agents.**
- Gotato has no project-specific UI.
- Gotato may be a broad runtime repository, but its concepts and mandatory dependencies must remain small and layered.

Do not reduce Gotato to a single agent loop library. The standard runtime should include reusable Agent, Session, Context, Tool Registry, Event, CLI, persistence/testing, provider, and extension primitives as defined by the whitepaper.

Do not turn Gotato into Mow. Gotato must not learn Master, Operator, Worker, Task Graph, worktree integration, global multi-agent scheduling, or Mow-specific application semantics.

---

# 3. Preserve the Existing Repository

Before changing code:

1. inspect the repository tree;
2. inspect `go.mod`, `go.work`, existing packages, commands, docs, examples, tests, CI, release configuration, and Git history;
3. run the current test/build/lint commands you can discover;
4. capture the current `git status`;
5. identify public APIs and current users/examples;
6. identify existing features that already satisfy the new whitepaper;
7. identify conflicting abstractions, duplicated responsibilities, and accidental application-level concepts.

Do not delete useful functionality merely because the package names differ from the target vocabulary.

Treat the existing implementation as evidence, not as authority.

---

# 4. Git Safety and Initialization

## If `.git` already exists

Do **not** reinitialize Git and do **not** discard history.

Run:

```bash
git status --short --branch
git log --oneline -n 20
```

Record the current baseline.

If the working tree already contains unrelated user changes:

- do not overwrite them;
- avoid destructive resets;
- isolate your edits carefully;
- document any collision.

Create a focused branch if repository conventions allow it, for example:

```bash
git switch -c refactor/runtime-foundation
```

If the repository already has a branch policy, follow it instead.

## If `.git` does not exist

Initialize it:

```bash
git init
git add .
git commit -m "chore: establish existing gotato baseline"
```

Then continue on a focused refactor branch if appropriate.

Do not add the coding agent, model, or tool as an author/co-author unless explicitly requested by the user.

---

# 5. Convert the Whitepaper into Repository Governance Documents

Before major code refactoring, read `gotato_whitepaper.md` completely and create or replace these root documents:

> **Already done (one-time refactor).** These documents now exist and are the documents of record (`AGENTS.md` names them as the constitution). Do not recreate or replace them from the whitepaper again; edit the existing documents instead.

```text
PROPOSAL.md
PHILOSOPHY.md
DESIGN.md
FEATURES.md
GOALS.md
AGENTS.md
GITFLOW.md
```

Do not create shallow summaries. Preserve the terminology, constraints, and architectural intent of the whitepaper.

## `PROPOSAL.md`

Derive from the whitepaper's definition, architecture, repository shape, and development direction.

It should explain:

- what Gotato is;
- why the refactor exists;
- the target runtime layers;
- the relationship among Agent, Session, and Context;
- the role of CLI;
- package/dependency direction;
- migration principles;
- major phases of the refactor;
- what existing functionality should be preserved or moved rather than discarded.

It should be a practical proposal, not a duplicate of PHILOSOPHY.

## `PHILOSOPHY.md`

Extract the complete PHILOSOPHY section.

Keep it technology-light. This document governs the project's worldview.

It must prominently preserve:

- Less is More;
- Agents Should Be Highly Cheap and Disposable;
- No Agent Is a Sub-Agent;
- Runtime Primitives, Not Product Doctrine;
- Composition Over Centralization;
- Ordinary Go;
- Continuity vs attention;
- machine usability.

Do **not** move `Agent as a Goroutine` into PHILOSOPHY. It belongs in DESIGN.

## `DESIGN.md`

Extract and organize all durable design constraints.

It must include at minimum:

- Agent as a Goroutine;
- one Agent primitive;
- Agent / Session / Context distinction;
- Session first-class runtime primitive;
- Context strategies and compaction;
- Tool Registry;
- Event Stream;
- CLI first-class interface;
- machine-readable CLI semantics;
- testing surface;
- no intrinsic agent hierarchy;
- no Mow/application orchestration in Gotato;
- no mandatory daemon;
- no built-in UI;
- layered package direction.

## `FEATURES.md`

Turn the FEATURES section into a module-oriented implementation inventory.

Use status markers after you audit the existing code:

```text
[done]
[partial]
[missing]
[needs-refactor]
```

For each feature, point to the relevant existing package/files when known.

The file should become a living implementation checklist.

## `GOALS.md`

Preserve:

- project goals;
- non-goals;
- necessary tradeoffs;
- compatibility principles;
- measurable runtime goals where applicable.

## `AGENTS.md`

Write instructions for future coding agents working in this repository.

It must include:

### Read order

```text
1. PHILOSOPHY.md
2. DESIGN.md
3. GOALS.md
4. FEATURES.md
5. PROPOSAL.md
6. relevant package docs/tests
```

### Hard architectural rules

- never introduce `sub-agent` as a Gotato runtime type;
- never make Agent roles intrinsic to Gotato;
- keep Agent, Session, Context separate;
- keep Session generic rather than application-specific;
- keep Context as model-view construction, not a synonym for Session;
- preserve library-first use;
- no mandatory daemon;
- no UI logic in Gotato;
- CLI and library must share runtime semantics;
- machine-readable CLI behavior is part of the contract;
- prefer ordinary Go over custom framework machinery;
- preserve `context.Context` cancellation;
- keep dependencies layered.

### Development workflow

- inspect before editing;
- run focused tests first, then full tests;
- add regression tests for behavior changes;
- use fake/replay models for deterministic tests where possible;
- use CLI integration tests for user-visible runtime behavior;
- update docs when public behavior changes;
- do not silently break CLI JSON schemas or exit codes;
- do not add a dependency without checking whether a standard-library or smaller solution exists.

### Definition of done

A change is not complete until relevant tests pass and the affected CLI/API behavior is exercised.

## `GITFLOW.md`

Write a lightweight Git policy suitable for coding agents and human contributors.

Include:

- preserve history;
- small focused branches;
- conventional, descriptive commits;
- one conceptual change per commit when practical;
- no destructive reset of user work;
- rebase/update before merge when appropriate;
- tests before commit/merge;
- documentation changes with public API changes;
- no AI/model/tool attribution in commit authorship unless explicitly requested;
- how to handle breaking changes and migration notes;
- how to handle generated files;
- preferred branch names such as `feat/...`, `fix/...`, `refactor/...`, `docs/...`, `test/...`.

Do not create an elaborate enterprise Git process.

---

# 6. Produce a Refactor Audit

Create:

```text
REFACTOR_AUDIT.md
```

This is an implementation artifact, not part of the permanent constitution.

It must contain:

1. current repository architecture;
2. package map;
3. current Agent semantics;
4. current session/history/context behavior;
5. current tool architecture;
6. current event/streaming architecture;
7. current CLI state;
8. current persistence state;
9. current testability;
10. public API compatibility concerns;
11. mapping from existing components to target whitepaper components;
12. code that should be preserved;
13. code that should be moved/renamed;
14. code that should be deprecated or removed;
15. highest-risk refactor points.

Do not speculate when code can answer the question. Read the repository.

---

# 7. Write an Incremental Migration Plan

Create:

```text
REFACTOR_PLAN.md
```

Do not propose a giant rewrite.

Organize the migration into independently testable stages.

A reasonable target sequence is below, but adapt it to the existing repository after audit.

## Stage A — Establish boundaries

- stabilize package dependency direction;
- isolate core Agent/Model/Tool/message semantics;
- identify reusable runtime state vs application state;
- create compatibility wrappers where required.

## Stage B — First-class Session

- introduce or normalize `Session`;
- migrate existing conversation/history state into it;
- add storage interfaces;
- add memory/file-backed implementation first;
- preserve compatibility where possible.

## Stage C — First-class Context

- separate Session history from model-visible Context;
- add ContextBuilder/strategy abstraction;
- move existing compaction/window logic into this layer;
- make final context inspectable.

## Stage D — Tool Registry

- normalize Tool contract;
- add registry/list/lookup/activate/deactivate semantics;
- migrate existing static tool sets without breaking simple use;
- retain room for dynamic/deferred tools.

## Stage E — Structured events and streaming

- normalize event types;
- make model/tool/context/session lifecycle observable;
- avoid log parsing as runtime API.

## Stage F — Official CLI

Implement or refactor:

```text
gotato run
gotato session ...
gotato context ...
gotato tools ...
gotato events ...
gotato doctor
```

Machine-readable behavior is mandatory.

## Stage G — Testing toolkit

- FakeModel;
- ReplayModel;
- FakeTool;
- EventRecorder;
- Session/Context fixtures;
- integration/scenario tests.

## Stage H — Optional standard packages

Only after the foundation is stable, rationalize provider, filesystem/shell/Git, MCP, persistence, and observability packages.

---

# 8. Implementation Requirements

While refactoring:

## Preserve simple library usage

A small Go program should still be able to construct an Agent and run it without starting a server or database.

## Preserve cheap/disposable execution

Do not make every Agent own heavy long-lived resources.

Share reusable provider clients/registries where appropriate, but keep mutable run state isolated.

## Keep Session and Context separate in code

Avoid structs that silently conflate:

```text
all historical state
=
model input
```

## Do not add agent hierarchy

Do not introduce core types such as:

```text
SubAgent
ChildAgent
SupervisorAgent
WorkerAgent
```

Applications can wrap/configure `Agent`.

## Keep Mow semantics out

No `TaskGraph`, `Master`, `Operator`, Mow Worker role, integration workspace, or global orchestration concepts in Gotato runtime packages.

## CLI is not a wrapper around private alternate code

The CLI should call the same exported/runtime packages used by applications.

## Avoid premature platformization

Do not add RPC services, distributed registries, service discovery, or a daemon unless existing repository functionality requires preserving them. If such functionality already exists, isolate it as optional application/service packages rather than letting it define the core.

---

# 9. CLI Contract

Build the CLI early enough that it can be used to test later stages.

At minimum, support a coherent subset of:

```bash
gotato run --json "..."
gotato session create --json
gotato session list --json
gotato session show <id> --json
gotato session resume <id> ...
gotato session fork <id> --json
gotato context inspect <id> --json
gotato context build <id> --json
gotato tools list --json
gotato events --session <id> --jsonl
gotato doctor --json
```

Exact syntax may differ if the existing repository already has a strong CLI convention.

Requirements:

- stdout for requested structured data;
- stderr for diagnostics;
- documented non-zero exits;
- no ANSI/color in machine mode;
- stable IDs;
- deterministic output fields where feasible.

---

# 10. Self-Testing Workflow

You are expected to test your own work autonomously.

Use a loop like:

```text
inspect
  -> edit
  -> format
  -> focused unit tests
  -> full Go tests
  -> build CLI
  -> run deterministic CLI scenario
  -> inspect JSON / JSONL output
  -> fix
  -> repeat
```

Prefer deterministic fake/replay models for CI and local regression tests.

Use real provider calls only when needed to validate integration behavior and credentials are available.

Do not make CI depend on paid model calls.

Useful commands should converge toward something like:

```bash
go test ./...                          # root module
(cd adapter/grpc && go test ./...)     # adapter/grpc is a separate module
go vet ./...
go build ./cmd/gotato
./gotato doctor --json
./gotato <deterministic scenario> --json
```

Adapt to the actual repository.

---

# 11. Compatibility Strategy

For public APIs that conflict with the target architecture:

1. determine current usage from examples/tests/repository history;
2. prefer compatibility adapters when cheap;
3. deprecate before removal when reasonable;
4. document breaking changes explicitly;
5. add migration examples;
6. do not preserve confusing architecture indefinitely solely to avoid a version bump.

Create `MIGRATION.md` if user-facing breaking changes become material.

---

# 12. Completion Criteria for This Refactor Session

Do not stop after writing documentation.

The session should continue until it has produced a meaningful working refactor slice.

At minimum, before considering the work complete:

- governance docs exist and match the whitepaper;
- `REFACTOR_AUDIT.md` exists;
- `REFACTOR_PLAN.md` exists;
- Git state/history is preserved;
- Agent / Session / Context target boundaries are represented in code or the first migration stage is fully implemented;
- CLI exists or has been materially advanced toward first-class runtime operation;
- deterministic tests cover the new/refactored behavior;
- `go test ./...` plus `(cd adapter/grpc && go test ./...)` (the separate module) pass, except for clearly documented pre-existing failures;
- CLI can exercise at least one real runtime path in machine-readable form;
- docs reflect the actual implementation state.

If the full whitepaper cannot be implemented in one session, complete the highest-coherence vertical slice and leave the repository in a passing, documented state with remaining work tracked in `FEATURES.md` and `REFACTOR_PLAN.md`.

---

# 13. Final Report

At the end, provide a concise report containing:

- architecture changes made;
- packages/files added, moved, or deprecated;
- tests run and results;
- CLI commands exercised;
- compatibility issues;
- remaining high-priority stages;
- current Git status.

Do not claim work is complete unless the repository state and tests support the claim.
