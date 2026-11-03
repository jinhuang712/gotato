# AGENTS.md: how to work in Gotato

Gotato is a minimal, synchronous, Go-native agent runtime. It is useful on its own and embeds into running, multi-instance Go services ([PROPOSAL.md](PROPOSAL.md)). This file is the first one a coding agent reads in a new window. It holds the read order, the layout and the rules for the work; the documents it points to hold the design.

Every change passes the questions in *Keeping it small*, and what fails them belongs to an application built on Gotato or to the service around it.

## Read order

Read these in order when the window is new or was compacted. Each answers one question, and a fact lives in the one document that owns it.

| # | Read | Answers |
|---|---|---|
| 1 | [PROPOSAL.md](PROPOSAL.md) | Why Gotato exists, and what it refuses to become |
| 2 | [PHILOSOPHY.md](PHILOSOPHY.md) | The beliefs every change follows |
| 3 | [DESIGNS.md](DESIGNS.md) | The guidelines the design must always satisfy |
| 4 | [GLOSSARY.md](GLOSSARY.md) | One name per thing, and its name in today's code |
| 5 | [plan/GOALS.md](plan/GOALS.md) | The goals that pass or fail, and the benchmarks tracked against a reference |
| 6 | [FEATURES.md](FEATURES.md) | What it does and will do, with status marks, and what is not planned |
| 7 | [plan/ROADMAP.md](plan/ROADMAP.md) | When, and how far along |
| 8 | [plan/TODO.md](plan/TODO.md) | The open defects, discussions and checks |
| 9 | [specs/ARCHITECTURE.md](specs/ARCHITECTURE.md) | How the parts fit together |
| 10 | [specs/](specs/) | How each part works inside, one spec per part: the map in ARCHITECTURE.md, then session, context, tools, record, compaction, store, profile, model, host, http, mcp and testing, with errors and events across the parts |
| 11 | [plan/decisions/](plan/decisions/) | Why it is built that way: one file per decision, with the alternatives not chosen |
| 12 | [CHANGELOG.md](CHANGELOG.md) | What changed in each release, with before and after notes for breaks |
| 13 | The package docs and tests | The doc comment of the package you change, then the `*_test.go` next to it |

## Layout

The module root holds the documents and the module files, and no Go code. The tree of packages and what each import costs is in `wiki/reference/packages.md`.

| Path | Holds |
|---|---|
| `PROPOSAL.md`, `PHILOSOPHY.md`, `DESIGNS.md`, `GLOSSARY.md`, `FEATURES.md`, `CHANGELOG.md` | The reasons and the contract, as in the read order |
| `README.md`, `RUNBOOK.md`, `CONTRIBUTING.md` | The front page, what to do when a service built on Gotato misbehaves, and how to change and release Gotato |
| `plan/` | `GOALS.md`, `ROADMAP.md`, `TODO.md` and `decisions/` |
| `specs/` | One spec per part |
| `wiki/` | The wiki: how-to pages for people who use the library, with `wiki/README.md` as its front page |
| `scripts/` | `check.sh` runs every check, and `cli-scenario.sh` smoke-runs the test CLI |
| `gotato/`, `ref/`, `record/`, `store/`, `profile/`, `host/` | The layers of the runtime, from the core down to the host (see *Hard architectural rules*) |
| `store/storetest/` | The Store contract suite |
| `model/`, `testkit/` | The bridge to a model over HTTP, and the fakes for tests |
| `internal/` | Shared packages (`text`, `hostlink`), and our own test tools (`reftasks`, `workload`, `fakemodel`, and `cmd/` with the test CLI `gotato`, `gotato-workload`, `gotato-weight` and `gatewayconf`) |
| `adapter/http/`, `adapter/mcp/` | Adapters, each a Go module of its own |
| `examples/larkbot/` | An application, a Go module of its own |
| `bench/refservice/` | The reference service that GOALS measures, a Go module of its own |
| `notes/` | Work notes, ignored by Git and never committed |

There are six Go modules: the root, `adapter/http`, `adapter/mcp`, `internal/cmd/gotato`, `examples/larkbot` and `bench/refservice`. CI checks each of them.

## While you work

- Before you change something, read the document that owns it, in full, in your own window.
- Track where the project stands by scanning `plan/TODO.md`, `plan/ROADMAP.md` and `plan/GOALS.md`.
- Never hand a document to a subagent to read or write. A subagent may read the code.
- Write a decision in `plan/decisions/` at once when a choice had another way open, may be undone later and has a reason the code does not show.
- Add what cannot be handled now to `plan/TODO.md` at once, as a defect, a discussion or a check.
- When the documents and the code disagree, tell the maintainer and let them decide.
- Change a page of the wiki in the same change as the behavior it describes. A new page starts from `wiki/_template.md` and joins `wiki/README.md`, and the wiki holds no fact that another document owns.
- When a change is done, update `CHANGELOG.md` and the feature's status in `FEATURES.md` in the same change.
- When a todo is handled, delete it from `plan/TODO.md` in the same change.
- After a change, check only what it touched against the rules, with `scripts/check.sh` on the modules it touched.
- Check the goals at the end of a checkpoint or before a release, and never for a small change.
- When the maintainer stops you, stop at once, with every agent you started and every agent those started.

## Hard architectural rules

### Agents and sessions

- **Never introduce `sub-agent`, `ChildAgent`, `SupervisorAgent`, `WorkerAgent` or any role as a Gotato runtime type.** There are only agents, and roles are application composition.
- **Keep Agent identity flat.** Roles, hierarchy and lineage belong to applications and live in `Session.Metadata`.
- **Keep Agent, Session, Transcript and Context separate.** An Agent is a definition, a Session runs it and commits to its Transcript, and the model receives the output of a `ContextBuilder` (the Context), a built value apart from the full history. In code the Agent is `profile.AgentProfile`, the live Session is `gotato.Session` and its Record is `record.Record`; [GLOSSARY.md](GLOSSARY.md) maps every term.
- **Keep Session generic.** Task graphs, project state, worktrees and product workflow live in `Metadata`.
- **Keep Context as model-view construction,** built from the Transcript on every Turn.
- **Preserve `context.Context` cancellation** through every model call, tool call, storage call and stream.

### Library first

- **Preserve library-first use.** A small `main` must be able to build an agent with a fake model and run it in-process, with server, daemon and database all optional.
- **Keep the service on top.** `host` and `adapter/http` turn the runtime into a service by composing it, and Agent semantics stay in the runtime.
- **Keep adapters outside the core.** `adapter/http` is the HTTP API over `host`, and `adapter/mcp` supplies an MCP server's tools as a `ToolSet`. Each is a Go module of its own with a `replace` to the root, and no core or runtime package imports one.
- **Keep applications on top.** `examples/larkbot` is a chat bot and a local-cluster harness in a Go module of its own. It imports the root module and the adapters through `replace`, and nothing imports it.
- **UI lives in applications.** Gotato has no human-facing surface of its own, so widgets, TUIs and desktop state belong to the applications above it.
- **The CLI is our test tool, not a deliverable.** `internal/cmd/gotato` is a thin client of the packages, and a Go module of its own because `serve` needs `adapter/http`. It carries no runtime logic, so a behavior it needs goes into a package first. It promises no contract: its flags, JSON fields and exit codes are not versioned.
- **Prefer ordinary Go** over framework machinery: option functions, interfaces, channels and `context.Context`.

### Packages and layers

- **Keep the package layout.** The module root holds no Go code, and `gotato/layering_test.go` fails when a `.go` file appears there.
- **Name a directory as its package.** No import then needs an alias, except `adapter/http`, whose package is named `http` like `net/http`: a program that imports both imports it as `httpapi "github.com/jinhuang712/gotato/adapter/http"`.
- **Put a test-helper package next to what it tests,** like `net/http/httptest`: `store/storetest` beside `store`.
- **Keep dependencies layered** ([decision 010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md), [decision 050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md)). Each package is one layer named for its job and imports only the layers below it: `host` -> `profile` -> `store` -> `record` -> `gotato` -> `ref`.
- **Keep the core small.** `gotato` imports only the standard library, `ref` and the shared packages of `internal/`, so code reads `gotato.NewSession`.
- **Keep `store` and `record` one way.** `store` imports `record`, never the reverse, because the Record does not know how it is saved.
- **Keep `profile` between `host` and `store`.** It holds the AgentProfiles and TaskSpecs a host registers, and imports no `host`.
- **Keep `model`, `testkit` and the adapters beside the layers.** They import down, and no layer imports them.
- **Keep the layering test passing.** `gotato/layering_test.go` enforces all of this and holds the allowed imports.
- **`internal/` holds two kinds of package.** The shared ones (`internal/text`, `internal/hostlink`) import the standard library only, and any shipped package may import them. Our test tools (`internal/reftasks`, `internal/workload`, `internal/fakemodel` and `internal/cmd/...`) are imported by no shipped package.
- **Prefer the standard library.** Add a dependency only after checking that the standard library or a smaller solution falls short. The root module depends only on `gopkg.in/yaml.v3`, which `internal/cmd/gatewayconf` (the test tools' gateway file) uses and no shipped package imports.

## Keeping it small

[Guideline 5: Keep subtracting](DESIGNS.md#5-keep-subtracting) measures weight in what a user learns and depends on: terms, exported names, settings, modules and footprint, not lines. Ask these questions of a feature, an exported name or a setting, when you add it and when you review it. Ask them in order, and the first yes gives the verdict.

| # | Ask | If yes |
|---|---|---|
| 1 | Can a Go service that embeds an agent work without it? | Delete it, or leave it to the application |
| 2 | Does the service around Gotato already do this job? | Leave it to the service, and keep only the seam |
| 3 | Do only some users need it, and can they build it on the public API? | Move it out of the core, into an adapter, an example or a page of the wiki |
| 4 | Does another mechanism already do the same job? | Merge the two |
| 5 | Is it used only by tests or by one caller inside Gotato? | Unexport it |
| 6 | Is it a setting with no stated reason, or one nobody changes? | Remove the setting and fix the default in code |

When no answer is yes, it stays, and the last question is whether it can do its job with fewer terms, names or settings; if it can, shrink it. A verdict is one of keep, shrink, unexport, move out or delete, and one that changes the contract follows *Breaking changes*.

## Naming

Exported names follow one set of rules, so a reader can guess a name before looking it up. They follow the standard library where it has a habit, and [GLOSSARY.md](GLOSSARY.md) for what a thing is called.

| Rule | Example |
|---|---|
| American spelling, in Go names and in wire strings alike | `canceled`, as in `context.Canceled` |
| One verb per action, the glossary's | a Run is stopped with `Abort` |
| An error code is `Code…` of type `ErrorCode`; a sentinel error is `Err…` and carries a code; a code's name matches its string | `gotato.CodeBusy` is `"busy"`; `host.ErrBusy` has it |
| A type or method is named as the glossary names the thing | a method that returns a `record.Record` is called `Record` |
| A constant is prefixed with the name of its type | `RunStatusRunning`, `ToolEffectRead` |
| A struct of settings is a `Config` or a `Policy`, never `Options`; a function type that configures is an `Option`, prefixed when a package has two | `CompactPolicy`, `store.WriterOption` |
| `Load` reads from a store or a path, `Parse` from bytes, `From…` builds from a value in memory | `gatewayconf.Load(path)`, `record.FromDocument(doc)` |
| A boolean method is the adjective with no `Is`; a noun keeps `Is`, as the standard library does | `Retryable()`, `IsCheckpoint()` |
| An Event type is a past participle; a stream piece is a `…Delta`, and `ToolProgress` is the one other noun | `RunStarted`, `TextDelta` |
| A package's main type is built by a bare `New` | `host.New`, `model.New` |
| A package's sentinel errors live in its `errors.go`, in one `var` block; the codes live in `gotato/errors.go` | `host.ErrBusy` in `host/errors.go`, `gotato.CodeBusy` in `gotato/errors.go` |
| Every exported name has a doc comment that starts with its name, which the linter checks | `// RunStarted is published when a Run starts.` |
| A field that holds a count names its unit: `Tokens`, `Messages` or `Bytes` | `TailTokens`, `KeepMessages`, `TokensBefore` |
| The reason an Event happened is a `…Why` type and the JSON field `why`; the reason a thing stopped or was asked for is a `…Reason` | `ClaimWhy`, `CompactionWhy`; `StopReason`, `CompactReason` |
| `With…` is an Option, or a function that returns a modified copy of its value, as the standard library's `Request.WithContext` does | `WithModel`; `Event.WithNumber` |
| A name read with its package says what it is; a bare noun that does not (`Block`) takes the noun it belongs to | `gotato.ContextBlock` |

## Development workflow

1. **Inspect before editing.** Read the package, its tests and the `FEATURES.md` row you are about to change.
2. **Run the focused tests first,** such as `go test ./record/... ./store/...`.
3. **Run the full checks with `scripts/check.sh`.** It runs gofmt on the tracked `.go` files outside `notes/`, then `go build`, `go vet`, `go test -race` and `golangci-lint` (config in `.golangci.yml`, 0 issues) in each module, and exits 1 if any step fails. Give it module paths, such as `scripts/check.sh adapter/http`, to check only those; the root is `.`.
4. **Choose the modules by what you touched.**

   | You touched | Run |
   |---|---|
   | Anything in the root module | `.` |
   | `host`, or a type the HTTP API uses (`host.Runner`, `record.Info`) | `adapter/http` too |
   | `ToolSet`, `Tool` or `ToolResult` | `adapter/mcp` too |
   | The CLI, or a package it drives | `internal/cmd/gotato` too |
   | The example, or a type it uses (`host.Runner`, `profile.AgentProfile`, `ToolSet`) | `examples/larkbot` too |
   | The reference service, or a package it embeds | `bench/refservice` too |

   The script skips lint, with a note, when `golangci-lint` is missing or was built with an older Go than the module targets (today `examples/larkbot`). CI runs it there.
5. **Add regression tests for behavior changes.** A bug fix ships with a test that fails before the fix.
6. **Use fake and replay models** (`testkit.FakeModel`, `testkit.ReplayModel`) for deterministic tests. Never make a unit test depend on a network provider or a credential.
7. **Use CLI integration tests** (`internal/cmd/gotato/cli_test.go`) as one way to test runtime behavior end to end, and assert on JSON, not on prose.
8. **Update the documents when public behavior changes:** package doc comments, the `FEATURES.md` status mark, `README.md` examples, and `CHANGELOG.md` (*Unreleased*, with a before and after note for breaks).
9. **Keep the CLI honest.** Update its tests and `internal/cmd/gotato/README.md` with it, and add no `CHANGELOG.md` entry for it, since it promises no contract.
10. **Tag every guard.** A guard is code that exists only for a failure: a check, a recover, a fence, a retry or a fixed public text. Put a tag and its reason in the comment above it, in one line and with no history ("fixed in", "bug 12"): `// [SECURITY] a client cannot make the handler read a body past the cap.` The tag says what the guard protects, from this closed list:

    | Tag | Protects |
    |---|---|
    | `[SECURITY]` | A secret, a credential, an authorization decision or a public text |
    | `[DATA-LOSS]` | A Record, a Message or a result that would be lost or read wrong; the order of a durable write |
    | `[OWNERSHIP]` | One writer per Session across Instances (Claim, epoch, release) |
    | `[LIVENESS]` | A hang, a leak, unbounded growth or a stuck state (goroutine, lock, queue, Claim) |
    | `[CONCURRENCY]` | A lock order, a "caller holds X", or a check and its change in one step |
    | `[FAILURE-CLASS]` | Why an error is, or is not, NotRun or Retryable |
    | `[CONTRACT]` | Behavior that a document, the Store suite or a serialized name requires |
    | `[SEAM]` | Code that looks unused but is an intended extension point, event, metric or introspection |
    | `[WORKAROUND]` | A deliberate duplicate or detour; the comment says when it can go |
    | `[LIMITATION]` | A known gap left on purpose; the comment says why |
    | `[IMPORTANT]` | Only when no other tag fits; the comment says what breaks if it changes |

    A guard with no tag is a candidate for deletion in review, and a tag whose reason no longer holds is stale. A change adds a guard only with a failing test that a supported use can trigger, and it reuses or replaces an existing mechanism before it adds a second one.

## Deterministic testing toolkit

| Need | Use |
|---|---|
| a model that answers a scripted sequence | `testkit.FakeModel` |
| a model that replays recorded `ModelEvent`s and fails when exhausted | `testkit.ReplayModel` |
| a model that answers from the request (shared by agents, racy input order) | `testkit.FuncModel` with `testkit.ToolResults` / `testkit.UserText` |
| a tool that returns a fixed result and records calls | `testkit.FakeTool` |
| prove a `store.Records`, `store.Liveness`, `store.Lister` or `store.Definitions` adapter keeps its contract | `storetest.RunRecords`, `RunLiveness`, `RunLister`, `RunDefinitions` in `store/storetest` |
| capture every Event of a live Session or a Subscription in order | `testkit.RecordEvents(session)` right after `gotato.NewSession`, or `testkit.RecordSubscription(sub)`; read `Events()`, `Kinds()`, `OfKind(kind)` |
| a pre-populated Session Record | `testkit.NewRecord("q", "a", ...)` |
| a provider's wire API and its quirks over HTTP: a refusal as too long (before output and mid-stream), a cut stream, cached tokens, a reasoning replay, a mid-conversation `system` or `developer` message, a slow reply | `fakemodel.New()` in `internal/fakemodel` behind `httptest.NewServer`; quirks by `[quirk:name]` in the last user text or the `X-Fake-Quirk` header; `g.Requests()` records every request; `go run ./internal/fakemodel/cmd/fakemodel` serves it on a port |
| the echo and demo models used by the CLI and the reference service | `testkit.EchoModel`, `testkit.DemoModel`, `testkit.DemoEchoTool()` |
| the reference workload's tasks, with their fake models and checks | `reftasks.ReferenceTasks()` in `internal/reftasks`; `reftasks.ToolSearchTask()` is the one whose tools load, with `Task.Options` for its ToolSets and `reftasks.WithLoadedToolsOf` to copy a history with its loads; `reftasks.LongSession(turns, perRun, resultBytes, window, recap)` builds a Session that compacts and merges, and `LongSessionTask()` is the 2,000-Turn task of [O4: A long Session stays within its window](plan/GOALS.md#o4-a-long-session-stays-within-its-window-), played only when named |
| play tasks through a Runner and measure them ([plan/GOALS.md](plan/GOALS.md)) | `workload.Run` in `internal/workload`; `go run ./internal/cmd/gotato-workload` prints the report |

## CLI scenario loop

`scripts/cli-scenario.sh` is a smoke run of the test tool, to see the packages work end to end. It builds the CLI into `bin/`, creates a Session, runs a prompt with the demo model, reads the Events, inspects the Context and compacts it, and needs `jq`. Every step must exit 0, and each JSON step must print valid JSON. The exit codes of the tool are in `internal/cmd/gotato/README.md`.

## Definition of done

A change is not complete until:

- `scripts/check.sh` passes for every module the change touched;
- the affected behavior has actually been exercised, by a test or by a CLI scenario whose JSON you inspected;
- the documents reflect the new state: the `FEATURES.md` mark, `CHANGELOG.md`, package docs, and `README.md` when it is user-visible;
- the commit follows *Git flow* below.

Do not report work as complete when a test fails or a step was skipped. Say so explicitly.

## Editing the project documents

When the maintainer points out a change to a document, re-read the whole document before editing, and do so before any edit, even of one word. A wrong word or row usually comes from an idea that appears in other places too. Find every place the idea appears and rewrite them together, so the document stays consistent. Do not patch only the spot the maintainer pointed at.

When writing or rewriting a project document:

- **Give every fact one home.** Put it in the document that owns it, and let another document say less about it. When a fact changes, update its summaries in the same change.
- **Use the [GLOSSARY.md](GLOSSARY.md) terms.** One name per thing, and explain a term the first time a reader meets it.
- **Keep the tone calm, pragmatic and narrative.** State Gotato's own stance ("we consider …"), and never claim what other people or teams want or do.
- **Name no organization.** Say "you", "your", "the team" or "the service". Examples stay generic, with no real organization's names or internal details.
- **Use plain words, not slogans.** Bold a stance line only when the maintainer said it.
- **Put the answer first.** Open a long document so a reader sees the whole of it at a glance, with a TL;DR outline or the few points the document is built around, and then write freely. [FEATURES.md](FEATURES.md) opens with its Summary tree of every group and feature, and each new feature takes the next number in its group.
- **Explain in a short paragraph,** written as one block. Put separate points in a list, one sentence to a bullet, and never stack sentences line by line.
- **Use tables and diagrams** where they read better than prose, and introduce each with a sentence that says what it shows.
- **Hold no code chunk.** A command longer than two lines belongs in a script in `scripts/`, and the document names the script. A formula, a tree or a short command is fine. The wiki is the one exception: its pages hold the code a reader copies, and each block is run by a test the page names, or the page says it has not been run.
- **Write an ID with its name,** such as "T4: A short skill". Make every reference to a document, a goal, a feature or a guideline a Markdown link wherever the table in *Where a document may link* allows it.
- **List the items instead of giving a range,** and prefer a number to an adjective, with its source or left open.
- **Write dates as `YYYY-MM-DD`.** Keep status marks out of headings, so the links to them stay stable.
- **Check a claim before you write it.** Check a behavior claim by running it or mark it "not yet verified", and treat a goal as met only with a measurement or the maintainer's own report.
- **Quote only where the quote does real work,** and check every quote word for word against the original.
- **Never describe Gotato by another project's name** ("X-like", "the Go version of X").
- **In [PROPOSAL.md](PROPOSAL.md),** the pain points come before the shift, and *What we want* describes needs without answering the pains one by one. The proposal is a pitch.
- **Link other documents as Markdown links:** [PHILOSOPHY.md](PHILOSOPHY.md).
- **Send an open point to `plan/TODO.md`** as a `DISCUSSION`, so the work can stop at any time.

### Where a document may link

A link points to what the document rests on, and a reference that the table allows is a link.

| Document | May link to |
|---|---|
| `PHILOSOPHY.md` and `CHANGELOG.md` | nothing |
| `PROPOSAL.md` | rarely anything |
| `DESIGNS.md` | a few to `PHILOSOPHY.md`, and down to its decisions and specs |
| `FEATURES.md` | the guidelines it rests on, a stance of `PHILOSOPHY.md` only where it forces a feature, and down to its specs and decisions |
| `plan/GOALS.md`, `plan/ROADMAP.md`, `plan/TODO.md` | each other, `FEATURES.md` and `DESIGNS.md`; a goal up to the wants of `PROPOSAL.md` it serves; a checkpoint down to the features it delivers |
| `specs/` | up to the document they serve, and down to their decisions |
| `plan/decisions/` | up to the document they serve, never beside them; a link may be repaired when a document moves, and the words stay |
| `README.md` | every document once in *Docs*, and the install page again from *Install* |
| `AGENTS.md` | the documents in its read order |
| `wiki/` | the specs and `FEATURES.md` it points to |

### Status marks

The status marks mean the same in every document:

| Mark | Means | Used in |
|---|---|---|
| ✅ | built, done or met | FEATURES, ROADMAP and GOALS |
| 🚧 | built, with a gap named beside it | FEATURES |
| 🟨 | in progress | ROADMAP |
| ⬜ | planned, not started or not measured yet | FEATURES, ROADMAP and GOALS |
| ❌ | measured and not met | GOALS |

## Git flow

A lightweight Git policy for human contributors and coding agents. It is deliberately not an enterprise process.

### Guidelines

1. **Preserve history.** Never reinitialize the repository, rewrite shared history or force-push `main`.
2. **Never destroy user work.** No `git reset --hard`, `git checkout -- .`, `git clean -f` or stash drop on a working tree you did not create. If the tree holds changes you did not make, leave them alone and mention the collision in your report.
3. **Keep branches small and focused.** One concern per branch, and land often.
4. **Make one conceptual change per commit** when practical. A commit should build and pass tests on its own.
5. **Test before commit and before merge,** with `scripts/check.sh` on every module you touched.
6. **Let the documents travel with public changes.** A commit that changes an exported API, an event kind or a JSON field updates the relevant documents and the `FEATURES.md` mark in the same commit or the same branch.
7. **Put no AI, model or tool attribution in commit authorship.** No `Co-Authored-By` for a model or tool unless the maintainer asks for it.

### Branches

`main` is the integration branch and must always build and pass tests. A branch name is `<type>/<short-topic>`, where the type is one of `feat`, `fix`, `refactor`, `docs`, `test` or `chore`, as in `feat/session-file-store` or `fix/event-subscription-leak`.

### Commits

A subject is conventional, in the imperative mood and under about 72 characters, as `<type>: <what changed>` with the same types as a branch.

| Type | Example |
|---|---|
| `feat` | `feat: add file-backed session store` |
| `fix` | `fix: stop leaking a goroutine per event subscription` |
| `refactor` | `refactor: route agent commits through Transcript` |
| `docs` | `docs: mark the file store as done in FEATURES.md` |
| `test` | `test: cover context window strategy boundaries` |
| `chore` | `chore: add CI workflow` |

A body is optional but welcome when the why is not obvious from the diff. Name the goal or feature (`O6`, `F4.6`) when a commit exists to satisfy one.

### Landing a branch

- Land a branch by rebasing it on `main` and fast-forwarding `main` to it, so `main` stays a straight line with no merge commits.
- Rebase only a branch of your own that nobody else has pulled, and never rewrite shared history.
- Resolve conflicts locally and run `scripts/check.sh` again after the resolution.
- Landing runs `scripts/check.sh` on every module through `.claude/wt.json`, so a red check lands nothing.
- Squash only your own noise commits ("wip", "fix typo") before opening a PR, and keep the meaningful history.

### Breaking changes

A breaking change to the public Go API, the HTTP contract or the event kinds must:

1. be called out in the commit subject with a `!` or in a `BREAKING CHANGE:` body line;
2. add or update an entry under *Unreleased* in `CHANGELOG.md` with a before and after note;
3. prefer a deprecation period: keep the old symbol with a `// Deprecated:` comment for at least one release when the cost is low, and say in the entry when it goes.

### Generated files and notes

- Do not commit build output, coverage files, `go.work` or local credentials (see `.gitignore`; `gateway.yaml` is ignored on purpose).
- Work notes (inventories, decision logs, research, drafts) go in `notes/` at the repository root, which `.gitignore` ignores. They are never committed and no project document links to them, so a document records only the result.

### Before committing

1. Run `git status --short` and check that only your intended files are staged.
2. Run `scripts/check.sh` on the modules you touched, and see it pass.
3. Check that the documents are updated: the `FEATURES.md` mark, `CHANGELOG.md`, package docs and `README.md`.
