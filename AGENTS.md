# AGENTS.md — Instructions for Coding Agents Working in Gotato

You are working in the Gotato repository: **a minimal, synchronous, Go-native agent runtime** that is useful on its own and embeds into running, multi-instance Go services (PROPOSAL.md). This file tells you how to work here. The constitution is in the documents it points to.

Every change passes the feature test: *would a Go service embedding an agent want this on its own?* Anything else belongs to an application built on Gotato.

## Read Order

```text
1. PROPOSAL.md          — why Gotato exists, and what it refuses to become
2. PHILOSOPHY.md        — the beliefs every change follows
3. DESIGNS.md           — design principles, and the decisions that follow from them
4. GLOSSARY.md          — one name per thing, and its name in today's code
5. GOALS.md             — goals that pass or fail, and benchmarks tracked against a reference
6. FEATURES.md          — implementation inventory with status markers, and what is not planned
7. relevant package docs and tests (start with the *_test.go next to what you change)
```

These root documents are the single source of truth for Gotato's design.

## Hard Architectural Rules

- **Never introduce `sub-agent`, `ChildAgent`, `SupervisorAgent`, `WorkerAgent`, or any role as a Gotato runtime type.** There are only agents. Roles are application composition.
- **Keep Agent identity flat.** Roles, hierarchy, and lineage belong to applications, stored in `Session.Metadata`.
- **Keep Agent, Session, Transcript and Context separate.** An Agent is a definition; a Session runs it and commits to its Transcript; the model receives the output of a `ContextBuilder` (the Context). Model input is always a built value, separate from the full history. In code, the Agent is `service.Agent`, the live Session `gotato.Session` and its Record `session.Record`; GLOSSARY.md maps every term.
- **Keep Session generic.** Task graphs, project state, worktrees, and product workflow live in `Metadata`.
- **Keep Context as model-view construction**, built from the Transcript on every Turn.
- **Preserve library-first use.** A small `main` must be able to build an agent with a fake model and run it in-process, with server, daemon, and database all optional.
- **Keep the service on top.** `service`, `service/httpapi`, and `adapter/grpc` turn the runtime into a service by composing it; Agent semantics stay in the runtime. Core and standard runtime packages must never import them.
- **UI lives in applications.** Gotato's human-facing surface is CLI output; widgets, TUIs, and desktop state belong above it.
- **CLI and library share runtime semantics.** `cmd/gotato` is a thin client of the packages. If you need a behavior in the CLI, add it to a package first.
- **Machine-readable CLI behavior is part of the contract.** `--json`/`--jsonl` output fields and exit codes are versioned. Stdout is data, stderr is diagnostics.
- **Prefer ordinary Go** over framework machinery: options functions, interfaces, channels, `context.Context`.
- **Preserve `context.Context` cancellation** through every model call, tool call, storage call, and stream.
- **Keep dependencies layered** (DESIGNS.md → Dependencies point inward). The root package imports only the standard library. `session`, `modelctx`, `toolregistry`, `testkit` import the root package. Providers and the service layer import inward. `layering_test.go` enforces this; keep it passing.
- **Prefer the standard library.** Add a dependency only after checking that the standard library or a smaller solution falls short. The root module depends only on `gopkg.in/yaml.v3` (used by `gateway`).

## Development Workflow

1. **Inspect before editing.** Read the package, its tests, and the FEATURES.md row you are about to change.
2. **Focused tests first, then full tests.**
   ```bash
   go test ./session/...                 # focused
   gofmt -l . && go vet ./... && go test -race ./...   # full, root module
   golangci-lint run ./...               # 0 issues; config in .golangci.yml
   (cd adapter/grpc && go test ./... && golangci-lint run ./...)  # when you touched service or root types it maps
   ```
3. **Add regression tests for behavior changes.** A bug fix ships with a test that fails before the fix.
4. **Use fake/replay models** (`testkit.FakeModel`, `testkit.ReplayModel`) for deterministic tests. Never make a unit test depend on a network provider or a credential.
5. **Use CLI integration tests** (`cmd/gotato/cli_test.go`) for user-visible runtime behavior; assert on JSON, not on prose.
6. **Update docs when public behavior changes**: package doc comments, `FEATURES.md` status markers, `README.md` examples, `cmd/gotato/README.md` for CLI changes, and `CHANGELOG.md` (*Unreleased*, with a before/after note for breaks).
7. **Do not silently break CLI JSON schemas or exit codes.** Add fields; do not rename or remove without a migration note.
8. **Format**: `gofmt -l .` must print nothing.

## Editing the Project Documents

When the user points out a change to a document, re-read the whole document before editing. A wrong word or row usually comes from an idea that appears in other places too. Find every place the idea appears, and rewrite them together so the document stays consistent. Do not patch only the spot the user pointed at.

When writing or rewriting a project document:

- **Use the GLOSSARY.md terms.** One name per thing. Explain a term the first time a reader meets it.
- **Keep the tone calm, pragmatic and narrative.** State Gotato's own stance ("we consider …"). Never claim what other people or teams want or do.
- **Never write "company".** Say "you", "your", "the team" or "the service". Examples stay generic, with no real organization's names or internal details.
- **Open a long document so a reader sees the whole of it at a glance, then write freely.** The opening is either a TL;DR outline or the few points the document is built around. A TL;DR lists every group in bold, with its items in a small table (name, a one-line description, and a status where the items are concrete features) or as bullets. A document built around points opens with them numbered, one sentence each, and gives each point its own section. The details follow in the same order: each group or point is a `##` heading, and each item a `###` heading. Under it, write freely and make it easy to scan: a lead sentence, then whatever reads best, such as bullets, a table, a small ASCII diagram, `code` names, or a code snippet of a few lines. Avoid long plain paragraphs. Introduce every diagram or code block with a sentence that says what it shows.
- **Use plain words, not slogans.** Bold a stance line only when the maintainer said or approved it.
- **In PROPOSAL.md,** the pain points come before the shift, and *What we want* describes needs; it does not answer the pains one by one. The proposal is a pitch.
- **Link other documents as Markdown links:** [PHILOSOPHY.md](PHILOSOPHY.md).
- **Quote only where the quote does real work,** and check every quote word for word against the original.
- **Never describe Gotato by another project's name** ("X-like", "the Go version of X"). The one project it was inspired by is credited in the README's Origin section, and only there.
- **Keep the review mark.** PROPOSAL, PHILOSOPHY, DESIGNS, GOALS, FEATURES and ROADMAP open with `<!-- what-we-build: approved|drafted YYYY-MM-DD -->`. Any edit sets the mark back to `drafted`, with the date, until the maintainer approves again.

## Deterministic Testing Toolkit

| Need | Use |
|---|---|
| a model that answers a scripted sequence | `testkit.FakeModel` |
| a model that replays recorded `ModelEvent`s and fails when exhausted | `testkit.ReplayModel` |
| a model that answers from the request (shared by agents, racy input order) | `testkit.FuncModel` with `testkit.ToolResults` / `testkit.UserText` |
| a tool that returns a fixed result / records calls | `testkit.FakeTool` |
| capture every runtime event in order | `testkit.EventRecorder` (install with `gotato.WithExtension`) |
| a pre-populated Session Record | `testkit.NewRecord("q", "a", ...)` |
| the echo / demo models used by the CLI and the reference service | `testkit.EchoModel`, `testkit.DemoModel`, `testkit.DemoEchoTool()` |
| the reference workload's tasks, with their fake models and checks | `testkit.ReferenceTasks()` |
| play tasks through a Runner and measure them (GOALS.md) | `workload.Run` in `service/workload`; `go run ./cmd/gotato-workload` prints the report |

## CLI Scenario Loop

```bash
go build -o ./bin/gotato ./cmd/gotato
export GOTATO_HOME=$(mktemp -d)
./bin/gotato doctor --json
id=$(./bin/gotato session create --json | jq -r .id)
./bin/gotato run --session "$id" --model demo --json "use-tool"
./bin/gotato events --session "$id" | head
./bin/gotato context inspect "$id" --json
./bin/gotato context compact "$id" --keep 2 --json
```

Every command above must exit 0 and emit valid JSON on stdout. Exit codes: `0` ok, `1` runtime error, `2` usage error, `3` not found, `4` run did not complete (failed, cancelled, deadline).

## Definition of Done

A change is not complete until:

- relevant tests pass (`go test -race ./...`, `go vet ./...`, `gofmt -l .` empty) in every module touched;
- the affected CLI and/or API behavior has actually been exercised (a test, or a CLI scenario whose JSON you inspected);
- documentation reflects the new state (`FEATURES.md` marker, package docs, README when user-visible);
- the commit follows `GITFLOW.md`.

Do not report work as complete when tests fail or a step was skipped; say so explicitly.
