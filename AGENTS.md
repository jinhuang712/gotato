# AGENTS.md — Instructions for Coding Agents Working in Gotato

You are working in the Gotato repository: **a minimal, synchronous, Go-native agent runtime** that is useful on its own and embeds into running, multi-instance Go services (PROPOSAL.md). This file tells you how to work here. The constitution is in the documents it points to.

Every change passes the feature test: *would a Go service embedding an agent want this on its own?* Anything else belongs to an application built on Gotato.

## Read Order

```text
1. PROPOSAL.md          — why Gotato exists, and what it refuses to become
2. PHILOSOPHY.md        — the beliefs every change follows
3. DESIGNS.md           — design principles, and the decisions that follow from them
4. GLOSSARY.md          — one name per thing, and its name in today's code
5. plan/GOALS.md        — goals that pass or fail, and benchmarks tracked against a reference
6. FEATURES.md          — implementation inventory with status markers, and what is not planned
7. relevant package docs and tests (start with the *_test.go next to what you change)
```

These documents are the single source of truth for Gotato's design. PROPOSAL to FEATURES are in the repository root; GOALS, ROADMAP and TODO are in `plan/`.

## Hard Architectural Rules

- **Never introduce `sub-agent`, `ChildAgent`, `SupervisorAgent`, `WorkerAgent`, or any role as a Gotato runtime type.** There are only agents. Roles are application composition.
- **Keep Agent identity flat.** Roles, hierarchy, and lineage belong to applications, stored in `Session.Metadata`.
- **Keep Agent, Session, Transcript and Context separate.** An Agent is a definition; a Session runs it and commits to its Transcript; the model receives the output of a `ContextBuilder` (the Context). Model input is always a built value, separate from the full history. In code, the Agent is `service.Agent`, the live Session `gotato.Session` and its Record `session.Record`; GLOSSARY.md maps every term.
- **Keep Session generic.** Task graphs, project state, worktrees, and product workflow live in `Metadata`.
- **Keep Context as model-view construction**, built from the Transcript on every Turn.
- **Preserve library-first use.** A small `main` must be able to build an agent with a fake model and run it in-process, with server, daemon, and database all optional.
- **Keep the service on top.** `service` and `service/httpapi` turn the runtime into a service by composing it; Agent semantics stay in the runtime. `adapter/mcp` is the other adapter: it supplies an MCP server's tools as a `ToolSet`. `adapter/mcp` is a Go module of its own, with a `replace` to the root. Core and standard runtime packages must never import it. `examples/larkbot` is a Go module of its own too, an application (a Lark chat bot and a local-cluster harness) that imports the root module and the adapters through `replace`; nothing imports it.
- **UI lives in applications.** Gotato has no human-facing surface of its own; widgets, TUIs, and desktop state belong to the applications above it.
- **The CLI is our test tool, not a deliverable.** `internal/cmd/gotato` is a thin client of the packages: it carries no runtime logic, so it tests the library's real behavior. If the CLI needs a behavior, add it to a package first. It promises no contract: its flags, JSON fields and exit codes are not versioned.
- **Prefer ordinary Go** over framework machinery: options functions, interfaces, channels, `context.Context`.
- **Preserve `context.Context` cancellation** through every model call, tool call, storage call, and stream.
- **Keep the package layout** (guides/packages.md shows the tree). The module root holds no Go code: it holds the documents and the module files, and `gotato/layering_test.go` fails when a `.go` file appears there. A directory's name is its package's name, so no import needs an alias. A test-helper package sits next to what it tests, like `net/http/httptest`: `session/storetest` beside `session`.
- **Keep dependencies layered** (DESIGNS.md → Dependencies point inward). The core package imports only the standard library and the shared packages of `internal/`: it lives in `gotato/`, so code reads `gotato.NewSession`. `session`, `modelctx`, `testkit` import the core. Providers and the service layer import inward. `gotato/layering_test.go` enforces this; keep it passing.
- **`internal/` holds two kinds of package.** Shared packages (today only `internal/text`) import the standard library only, and any shipped package, the core included, may import them; `gotato/layering_test.go` lists them. Our test tools (`internal/reftasks`, `internal/workload`, `internal/fakegateway`, `internal/cmd/...`) are imported by no shipped package.
- **Prefer the standard library.** Add a dependency only after checking that the standard library or a smaller solution falls short. The root module depends only on `gopkg.in/yaml.v3` (used by `gateway`).

## Naming

Exported names follow one set of rules, so a reader can guess a name before looking it up. They follow the standard library where it has a habit, and [GLOSSARY.md](GLOSSARY.md) for what a thing is called.

| Rule | Example |
|---|---|
| American spelling, in Go names and in wire strings alike | `canceled`, as in `context.Canceled` |
| One verb per action, the glossary's | a Run is stopped with `Abort` |
| An error code is `Code…` of type `ErrorCode`; a sentinel error is `Err…` and carries a code; a code's name matches its string | `gotato.CodeBusy` is `"busy"`; `service.ErrBusy` has it |
| A type or method is named as the glossary names the thing | a method that returns a `session.Record` is called `Record` |
| A constant is prefixed with the name of its type | `RunStatusRunning`, `ToolEffectRead` |
| A struct of settings is a `Config` or a `Policy`, never `Options`; a function type that configures is an `Option`, prefixed when a package has two | `CompactPolicy`, `session.WriterOption` |
| `Load` reads from a store or a path, `Parse` from bytes, `From…` builds from a value in memory | `gateway.LoadYAML(path)`, `session.FromDocument(doc)` |
| A boolean method is the adjective with no `Is` (`Retryable()`, `Durable()`); a noun keeps `Is` (`IsCheckpoint()`), as the standard library does | `Durable()`, `IsCheckpoint()` |
| An Event type is a past participle; a stream piece is a `…Delta`, and `ToolProgress` is the one other noun | `RunStarted`, `TextDelta` |
| A package's main type is built by a bare `New` | `service.New`, `gateway.New` |
| A package's sentinel errors live in its `errors.go`, in one `var` block; the codes live in `gotato/errors.go` | `service.ErrBusy` in `service/errors.go`, `gotato.CodeBusy` in `gotato/errors.go` |
| Every exported name has a doc comment that starts with its name, which the linter checks | `// RunStarted is published when a Run starts.` |
| A field that holds a count names its unit: `Tokens`, `Messages` or `Bytes` | `TailTokens`, `KeepMessages`, `TokensBefore` |
| The reason an Event happened is a `…Why` type and the JSON field `why`; the reason a thing stopped or was asked for is a `…Reason` | `ClaimWhy`, `CompactionWhy`; `StopReason`, `CompactReason` |
| `With…` is an Option, or a function that returns a modified copy of its value, as the standard library's `Request.WithContext` does | `WithModel`; `Event.WithNumber` |
| A name read with its package says what it is; a bare noun that does not (`Block`) takes the noun it belongs to | `gotato.ContextBlock` |

## Development Workflow

1. **Inspect before editing.** Read the package, its tests, and the FEATURES.md row you are about to change.
2. **Focused tests first, then full tests.**
   ```bash
   go test ./session/...                 # focused
   gofmt -l . && go vet ./... && go test -race ./...   # full, root module
   golangci-lint run ./...               # 0 issues; config in .golangci.yml
   (cd adapter/mcp && gofmt -l . && go vet ./... && go test -race ./... && golangci-lint run ./...)  # when you touched ToolSet, Tool or ToolResult
   (cd examples/larkbot && gofmt -l . && go vet ./... && go test -race ./... && golangci-lint run ./...)  # when you touched the example, or a type it uses (service.Runner, service.Agent, ToolSet)
   ```
3. **Add regression tests for behavior changes.** A bug fix ships with a test that fails before the fix.
4. **Use fake/replay models** (`testkit.FakeModel`, `testkit.ReplayModel`) for deterministic tests. Never make a unit test depend on a network provider or a credential.
5. **Use CLI integration tests** (`internal/cmd/gotato/cli_test.go`) as one way to test runtime behavior end to end; assert on JSON, not on prose.
6. **Update docs when public behavior changes**: package doc comments, `FEATURES.md` status markers, `README.md` examples, and `CHANGELOG.md` (*Unreleased*, with a before/after note for breaks).
7. **Keep the CLI honest.** The CLI promises no contract: its flags, fields and exit codes change freely; update its tests and `internal/cmd/gotato/README.md` with it. No `CHANGELOG.md` entry is needed for the CLI.
8. **Format**: `gofmt -l .` must print nothing.
9. **Tag every guard.** A guard is code that exists only for a failure: a check, a recover, a fence, a retry, a fixed public text. Put a tag and its reason in the comment above it, in one line, with no history ("fixed in", "bug 12"): `// [SECURITY] a redirect would send the caller's headers to a host the allow list never saw.` The tag says what the guard protects, from this closed list:
   - `[SECURITY]`: a secret, a credential, or an authorization decision.
   - `[DATA-LOSS]`: a Record, a Message or a result that would be lost or read wrong.
   - `[OWNERSHIP]`: one writer per Session across Instances (Claim, epoch, release).
   - `[LIVENESS]`: a hang, a leak or a stuck state (goroutine, lock, queue, Claim).
   - `[CONTRACT]`: behavior a documented contract or the Store suite requires.
   - `[IMPORTANT]`: a non-obvious invariant that a later change could break; the comment says which.

   A change adds a guard only with a failing test that a supported use can trigger, and it reuses or replaces an existing mechanism before it adds a second one. A guard with no tag is a candidate for deletion in review; a tag whose reason no longer holds is too.

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
- **Never describe Gotato by another project's name** ("X-like", "the Go version of X").
- **Keep the review mark.** PROPOSAL, PHILOSOPHY, DESIGNS, GOALS, FEATURES and ROADMAP open with `<!-- what-we-build: approved|drafted YYYY-MM-DD -->`. Any edit sets the mark back to `drafted`, with the date, until the maintainer approves again.

## Deterministic Testing Toolkit

| Need | Use |
|---|---|
| a model that answers a scripted sequence | `testkit.FakeModel` |
| a model that replays recorded `ModelEvent`s and fails when exhausted | `testkit.ReplayModel` |
| a model that answers from the request (shared by agents, racy input order) | `testkit.FuncModel` with `testkit.ToolResults` / `testkit.UserText` |
| a tool that returns a fixed result / records calls | `testkit.FakeTool` |
| prove a `session.Store` adapter keeps the store contract | `storetest.Run` in `session/storetest` |
| capture every Event of a live Session or a Subscription in order | `testkit.RecordEvents(session)` right after `gotato.NewSession`, or `testkit.RecordSubscription(sub)`; read `Events()`, `Kinds()`, `OfKind(kind)` |
| a pre-populated Session Record | `testkit.NewRecord("q", "a", ...)` |
| a provider's wire API and its quirks over HTTP: a refusal as too long (before output and mid-stream), a cut stream, cached tokens, a reasoning replay, a mid-conversation `system`/`developer` message, a slow reply | `fakegateway.New()` in `internal/fakegateway` behind `httptest.NewServer`; quirks by `[quirk:name]` in the last user text or the `X-Fake-Quirk` header; `g.Requests()` records every request; `go run ./internal/fakegateway/cmd/fakegateway` serves it on a port |
| the echo / demo models used by the CLI and the reference service | `testkit.EchoModel`, `testkit.DemoModel`, `testkit.DemoEchoTool()` |
| the reference workload's tasks, with their fake models and checks (our own test tool, under `internal/`) | `reftasks.ReferenceTasks()` in `internal/reftasks` (`reftasks.ToolSearchTask()` is the one whose tools load, with `Task.Options` for its ToolSets and `reftasks.WithLoadedToolsOf` to copy a history with its loads); `reftasks.LongSession(turns, perRun, resultBytes, window, recap)` for a Session that compacts and merges (`LongSessionTask()` is the 2,000-Turn task of G-G10, played only when named) |
| play tasks through a Runner and measure them (plan/GOALS.md) | `workload.Run` in `internal/workload`; `go run ./internal/cmd/gotato-workload` prints the report |

## CLI Scenario Loop

A smoke run of the test tool, to see the packages work end to end:

```bash
go build -o ./bin/gotato ./internal/cmd/gotato
export GOTATO_HOME=$(mktemp -d)
./bin/gotato doctor --json
id=$(./bin/gotato session create --json | jq -r .id)
./bin/gotato run --session "$id" --model demo --json "use-tool"
./bin/gotato events --session "$id" | head
./bin/gotato context inspect "$id" --json
./bin/gotato context compact "$id" --keep-messages 2 --json
```

Every command above should exit 0 and emit valid JSON on stdout. The tool's exit codes are in `internal/cmd/gotato/README.md`.

## Definition of Done

A change is not complete until:

- relevant tests pass (`go test -race ./...`, `go vet ./...`, `gofmt -l .` empty) in every module touched;
- the affected behavior has actually been exercised (a test, or a CLI scenario whose JSON you inspected);
- documentation reflects the new state (`FEATURES.md` marker, package docs, README when user-visible);
- the commit follows [Git Flow](#git-flow) below.

Do not report work as complete when tests fail or a step was skipped; say so explicitly.

## Git Flow

A lightweight Git policy for human contributors and coding agents. It is deliberately not an enterprise process.

### Principles

1. **Preserve history.** Never reinitialize the repository, never rewrite shared history, never force-push `main`.
2. **Never destroy user work.** No `git reset --hard`, `git checkout -- .`, `git clean -f`, or stash-drop on a working tree you did not create. If the tree contains changes you did not make, leave them alone and mention the collision in your report.
3. **Small, focused branches.** One concern per branch. Land often.
4. **One conceptual change per commit** when practical. A commit should build and pass tests on its own.
5. **Tests before commit and before merge.** `go test ./...` in every module you touched, plus `go vet ./...`.
6. **Docs travel with public changes.** A commit that changes an exported API, an event kind, or a JSON field updates the relevant documentation (and `FEATURES.md` status markers) in the same commit or the same branch.
7. **No AI/model/tool attribution in commit authorship** (no `Co-Authored-By` for a model or tool) unless the user explicitly requests it.

### Branches

`main` is the integration branch and must always build and pass tests.

Branch names use a type prefix:

```text
feat/<short-topic>
fix/<short-topic>
refactor/<short-topic>
docs/<short-topic>
test/<short-topic>
chore/<short-topic>
```

Example: `refactor/runtime-foundation`, `feat/session-file-store`, `fix/event-subscription-leak`.

### Commits

Conventional, descriptive subjects in the imperative mood, under ~72 characters:

```text
feat: add file-backed session store
fix: stop leaking a goroutine per event subscription
refactor: route agent commits through Transcript
docs: mark the file store as done in FEATURES.md
test: cover context window strategy boundaries
chore: add CI workflow
```

A body is optional but welcome when the *why* is not obvious from the diff. Reference the goal or feature (`G-G08`, `G-F14`, ...) when a commit exists to satisfy one.

### Landing a Branch

- Land a branch by rebasing it on `main` and fast-forwarding `main` to it, so `main` stays a straight line with no merge commits. Rebase a branch of your own, one that nobody else has pulled; never rewrite shared history.
- Resolve conflicts locally; run the full test suite again after resolution.
- Squash only your own noise commits ("wip", "fix typo") before opening a PR; do not squash away meaningful history.

### Breaking Changes and Migration Notes

A breaking change to the public Go API, HTTP contract, or event kinds must:

1. be called out in the commit subject with a `!` or a `BREAKING CHANGE:` body line;
2. add or update an entry under *Unreleased* in `CHANGELOG.md` with a before/after snippet;
3. prefer a deprecation period: keep the old symbol with a `// Deprecated:` comment for at least one release when the cost is low, and say in the entry when it goes.

### Generated Files

- Do not commit build output, coverage files, `go.work`, or local credentials (see `.gitignore`; `gateway.yaml` is ignored on purpose).
- Work notes (inventories, decision logs, research, drafts) go in `notes/` at the repository root, which `.gitignore` ignores. They are never committed, and no project document links to them; a document records only the result.

### Coding-Agent Checklist Before Committing

```text
git status --short                # only your intended files are staged
gofmt -l .                        # empty
go vet ./...                      # clean
golangci-lint run ./...           # 0 issues
go test ./...                     # green
go build -o /dev/null ./internal/cmd/gotato   # CLI builds
docs updated?                     # FEATURES.md / README / package docs
```
