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
| [DESIGNS.md](DESIGNS.md) | **drafted** | re-approval needed for small fixes: `RunPreparer` added to the extension stages, MCP moved to *Later*, references point to FEATURES G-F27 |
| [GOALS.md](GOALS.md) | approved | goals that pass or fail, and benchmarks tracked against a reference |
| [FEATURES.md](FEATURES.md) | **drafted** | rewritten around behavior and checked claim by claim against the code; waiting for review |
| [GLOSSARY.md](GLOSSARY.md) | no mark | the terms every doc uses; also maps each term to its name in today's code |
| ROADMAP.md | **not written** | next |
| [README.md](README.md) | old | rewrite last; it still opens with the old tagline |

## Next steps

1. **Get FEATURES and DESIGNS approved.** Ask the maintainer to review both.
2. **Write ROADMAP.md.** It schedules what GOALS and FEATURES name, with exit criteria that point at GOALS IDs. What it must place:
   - the measurement tools every *(to build)* method in GOALS needs: a reference workload, a live run against a real provider, fault injection, and a multi-instance harness;
   - the ⬜ items in FEATURES G-F27: save every Turn, conflict on a stale save, Session ownership across instances, the warm Session and its TTL;
   - the Effect cap (FEATURES G-F09);
   - the ownership design session. The maintainer asked for a dedicated discussion before anything is built; DESIGNS → Open questions lists what it must answer, and one requirement is already fixed: a second instance stops before it calls the model.
3. **Rewrite README.md** last, as a summary of the others, with the new tagline.

## Open questions the maintainer still owns

All are listed in [DESIGNS.md → Open questions](DESIGNS.md#open-questions): ownership across instances, what a warm Session holds and what happens to a message that arrives mid-Run, how a Turn is saved, store conformance, the model catalog, compaction, and when the code takes the glossary's names.

## How to work here

- **Follow [AGENTS.md](AGENTS.md)**, especially *Editing the Project Documents*: the maintainer's writing rules (no "company", few sentences, tables only for real comparisons, plain words, no slogans, never compare Gotato to Pi) and the rule to re-read a whole doc before changing it.
- **Agree before writing.** Discuss a doc's key points with the maintainer, write it in one pass, have its claims checked against the code (an independent review caught real errors twice), then ask for approval.
- **Worktrees.** `EnterWorktree` with a name bases the worktree on `origin/main`. To start from local `main`, run `git worktree add .claude/worktrees/<name> -b wt/<name> main`, then `EnterWorktree` with that path. To land, `ExitWorktree` with `keep`, then `git merge --ff-only wt/<name>` on `main`, then remove the worktree and the branch. The `wt` plugin's land script only handles branches named `wt-*`.
- **Checks before a commit:** `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test ./...`, `go build ./cmd/gotato`. If lint reports an issue in a path under a removed worktree, run `golangci-lint cache clean`; it is a stale cache entry.
- **Commits** follow [GITFLOW.md](GITFLOW.md): English, conventional subjects, and no tool attribution.
