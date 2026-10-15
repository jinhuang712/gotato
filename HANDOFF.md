# Handoff — the documentation rewrite

Written 2026-10-15, for the next session that picks this work up. Delete this file once the rewrite is finished. Every decision below already lives in its own doc; this page only says where things stand and what comes next.

## What this work is

The project documents are being rebuilt with the `what-we-build` doc set, one doc at a time, in this order: PROPOSAL → PHILOSOPHY → DESIGNS → GOALS → FEATURES → ROADMAP → README. Each doc opens with a review mark (`<!-- what-we-build: approved|drafted YYYY-MM-DD -->`). Only the maintainer approves, and any edit sets the mark back to `drafted`.

The direction, in one line: **agent as a service for Go**, a minimal library that makes the agent session small, cheap and safe to run on many pods, for purpose-built agents. Read [PROPOSAL.md](PROPOSAL.md) and [PHILOSOPHY.md](PHILOSOPHY.md) before anything else.

## Where each doc stands

| Doc | Mark | Note |
|---|---|---|
| [PROPOSAL.md](PROPOSAL.md) | approved | |
| [PHILOSOPHY.md](PHILOSOPHY.md) | approved | |
| [DESIGNS.md](DESIGNS.md) | **drafted** | re-approval needed for small fixes: `RunPreparer` added to the extension stages, MCP moved to *Later*, references point to FEATURES G-F27, *Failure* now says that a failing tool returns a failed result and the Run goes on, and the rename's open question is narrowed to the stored record's name |
| [GOALS.md](GOALS.md) | approved | goals that pass or fail, and benchmarks tracked against a reference |
| [FEATURES.md](FEATURES.md) | **drafted** | rewritten around behavior and checked claim by claim against the code, including the claims the 2026-10-16 gateway and Loop fixes added; waiting for review |
| [GLOSSARY.md](GLOSSARY.md) | no mark | the terms every doc uses; also maps each term to its name in today's code |
| [ROADMAP.md](ROADMAP.md) | **drafted** | written 2026-10-16 from the maintainer's decisions, and checked against the other docs and the code; waiting for review |
| [README.md](README.md) | old | rewrite last; it still opens with the old tagline |

## Next steps

1. **Get FEATURES, DESIGNS and ROADMAP approved.** Ask the maintainer to review all three.
2. **Settle the Events claim.** The GOALS intro and DESIGNS → Events and telemetry say that every number can be computed from Events and `Usage`. Memory, CPU, startup and footprint cannot, and no Event times a store load or save yet. ROADMAP Phase 1 adds those Events and limits the claim to the numbers about a Run; GOALS is approved, so its wording waits for the maintainer.
3. **Rewrite README.md** last, as a summary of the others, with the new tagline.

## Open questions the maintainer still owns

All are listed in [DESIGNS.md → Open questions](DESIGNS.md#open-questions): ownership across instances, what a warm Session holds and what happens to a message that arrives mid-Run, how a Turn is saved, store conformance, the model catalog, compaction, and what the stored record is called. [ROADMAP.md](ROADMAP.md) places the design discussion that answers the first three and the last one.

## How to work here

- **Follow [AGENTS.md](AGENTS.md)**, especially *Editing the Project Documents*: the maintainer's writing rules (no "company", a TL;DR outline on top, then `##` groups and `###` items that are easy to scan, plain words, no slogans, never compare Gotato to Pi) and the rule to re-read a whole doc before changing it.
- **Agree before writing.** Discuss a doc's key points with the maintainer, write it in one pass, have its claims checked against the code (an independent review caught real errors twice), then ask for approval.
- **Worktrees.** `EnterWorktree` with a name bases the worktree on `origin/main`. To start from local `main`, run `git worktree add .claude/worktrees/<name> -b wt/<name> main`, then `EnterWorktree` with that path. To land, `ExitWorktree` with `keep`, then `git merge --ff-only wt/<name>` on `main`, then remove the worktree and the branch. The `wt` plugin's land script only handles branches named `wt-*`.
- **Checks before a commit:** `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test ./...`, `go build ./cmd/gotato`. If lint reports an issue in a path under a removed worktree, run `golangci-lint cache clean`; it is a stale cache entry.
- **Commits** follow [GITFLOW.md](GITFLOW.md): English, conventional subjects, and no tool attribution.
