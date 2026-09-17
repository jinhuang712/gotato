# AGENTS.md — Instructions for Coding Agents Working in Gotato

You are working in the Gotato repository: **a minimalistic, composable Go agent runtime.** This file tells you how to work here. It is short on purpose; the constitution is in the documents it points to.

## Read Order

```text
1. PHILOSOPHY.md        — the worldview; do not violate it
2. DESIGN.md            — durable engineering rules (G-Dxx)
3. GOALS.md             — goals, non-goals, tradeoffs, compatibility
4. FEATURES.md          — living implementation checklist with status markers
5. PROPOSAL.md          — target architecture and the refactor path
6. relevant package docs and tests (start with the *_test.go next to what you change)
```

`REFACTOR_AUDIT.md` and `REFACTOR_PLAN.md` are implementation artifacts of the ongoing refactor; read them when you touch structure. `docs/` and `specs/` predate the whitepaper: where they disagree with the root documents above, the root documents win.

## Hard Architectural Rules

- **Never introduce `sub-agent`, `ChildAgent`, `SupervisorAgent`, `WorkerAgent`, or any role as a Gotato runtime type.** There are only agents. Roles are application composition.
- **Never make Agent roles or hierarchy intrinsic**: no parent/child agent IDs in Agent identity. Applications may store lineage in `Session.Metadata`.
- **Keep Agent, Session, Context separate.** The agent commits to a `Transcript` (Session); the model receives the output of a `ContextBuilder` (Context). Do not add code paths where "model input" silently equals "all history" by construction.
- **Keep Session generic.** No task graphs, project state, worktrees, or product workflow fields. Application semantics live in `Metadata`.
- **Keep Context as model-view construction**, not a synonym for Session.
- **Preserve library-first use.** A small `main` must be able to build an agent with a fake model and run it with no server, daemon, or database.
- **No mandatory daemon.** `cmd/gotato-agent`, `host`, `adapter/grpc`, and `orchestration` are optional service-layer packages. Core and standard runtime packages must never import them.
- **No UI logic in Gotato.** CLI output for humans is fine; widgets, TUIs, and desktop state are not.
- **CLI and library share runtime semantics.** `cmd/gotato` is a thin client of the packages. If you need a behavior in the CLI, add it to a package first.
- **Machine-readable CLI behavior is part of the contract.** `--json`/`--jsonl` output fields and exit codes are versioned. Stdout is data, stderr is diagnostics.
- **Prefer ordinary Go** over framework machinery: options functions, interfaces, channels, `context.Context`.
- **Preserve `context.Context` cancellation** through every model call, tool call, storage call, and stream.
- **Keep dependencies layered** (DESIGN.md G-D27). The root package imports only the standard library. `session`, `modelctx`, `toolregistry`, `testkit` import the root package. Providers and the service layer import inward. Check with `go list -deps`.
- **Do not add a dependency** without first checking whether the standard library or a smaller solution suffices. The root module currently depends only on `gopkg.in/yaml.v3` (used by `gateway`).

## Development Workflow

1. **Inspect before editing.** Read the package, its tests, and the FEATURES.md row you are about to change.
2. **Focused tests first, then full tests.**
   ```bash
   go test ./session/...            # focused
   go test ./... && go vet ./...    # full, root module
   (cd adapter/grpc && go test ./...)   # only when you touched host/orchestration/root types it maps
   ```
3. **Add regression tests for behavior changes.** A bug fix ships with a test that fails before the fix.
4. **Use fake/replay models** (`testkit.FakeModel`, `testkit.ReplayModel`) for deterministic tests. Never make a unit test depend on a network provider or a credential.
5. **Use CLI integration tests** (`cmd/gotato/*_test.go`) for user-visible runtime behavior; assert on JSON, not on prose.
6. **Update docs when public behavior changes**: package doc comments, `FEATURES.md` status markers, `README.md` examples, and `MIGRATION.md` for breaks.
7. **Do not silently break CLI JSON schemas or exit codes.** Add fields; do not rename or remove without a migration note.
8. **Format**: `gofmt -l .` must print nothing.

## Deterministic Testing Toolkit

| Need | Use |
|---|---|
| a model that answers a scripted sequence | `testkit.FakeModel` |
| a model that replays recorded `ModelEvent`s per call | `testkit.ReplayModel` |
| a tool that returns a fixed result / records calls | `testkit.FakeTool` |
| capture every runtime event in order | `testkit.EventRecorder` (install with `gotato.WithExtension`) |
| a pre-populated session | `testkit.NewSession(...)` |
| the echo / demo models used by the CLI and the reference service | `testkit.EchoModel`, `testkit.DemoModel` |

## CLI Scenario Loop

```bash
go build -o ./bin/gotato ./cmd/gotato
./bin/gotato doctor --json
id=$(./bin/gotato session create --json | jq -r .id)
./bin/gotato run --session "$id" --model demo --json "use-tool"
./bin/gotato events --session "$id" --jsonl | head
./bin/gotato context inspect "$id" --json
```

Every command above must exit 0 and emit valid JSON on stdout. Exit codes: `0` ok, `1` runtime error, `2` usage error, `3` not found, `4` run did not complete (failed, cancelled, deadline).

## Definition of Done

A change is not complete until:

- relevant tests pass (`go test ./...`, `go vet ./...`, `gofmt -l .` empty) in every module touched;
- the affected CLI and/or API behavior has actually been exercised (a test, or a CLI scenario whose JSON you inspected);
- documentation reflects the new state (`FEATURES.md` marker, package docs, README when user-visible);
- the commit follows `GITFLOW.md`.

Do not report work as complete when tests fail or a step was skipped; say so explicitly.
