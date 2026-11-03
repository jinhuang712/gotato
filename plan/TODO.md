# TODO

## ongoing

### [P1][ISSUE] Gotato's time per Turn looks 10 to 35% above 0.2.0

- description: the current code takes more time per Turn than `v0.2.0` at 10, 100 and 1,000 messages, with a fake model and a memory store. `history_10`: 0.12 to 0.13 ms against 0.10. `history_100`: 0.40 to 0.46 ms against 0.31 to 0.39. `history_1000`: 2.9 to 3.0 ms against 2.2 to 2.7. The shipped benchmark, `BenchmarkReferenceTasks`, agrees on the current code (0.13, 0.46 and 3.4 ms). The machine had other work running (load average about 5), so every time is noisy and about twice the values of 2026-10-17. The cause is not known; the layers of decisions 044 to 051 touched the Record, the Header and the Session.
- created: 2026-11-03
- status: open
- found: `gotato-workload -plays 30` built from `v0.2.0` (`git archive`) and from the current code, run alternately three times each on 2026-11-03.
- next: repeat the alternation on a quiet machine, and if the gap holds, profile `history_100` and `history_1000` and find what the layers added.
- blocks: [CP4.2: The scripted benchmarks](ROADMAP.md#cp42-the-scripted-benchmarks), [B5: Gotato's time per Turn](GOALS.md#b5-gotatos-time-per-turn), [B9: CPU per Turn, at 10, 100 and 1,000 messages](GOALS.md#b9-cpu-per-turn-at-10-100-and-1000-messages)

### [P2][DISCUSSION] Should the HTTP contract HALT a release?

- description: `adapter/http` is optional and a Go module of its own, and its wire contract carries a version, `httpapi.ContractVersion`, which `GET /healthz` reports. It is still `"1"`, the value of 0.2.0, while Unreleased changes the answer of `GET /v1/sessions` from an array to `{"sessions": […], "next": "…"}`, with listing columns only and no `record_epoch`, so a client written for 0.2.0 breaks. [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down) says each break that touches HTTP raises the version, and the adapter is tagged with the root, so today a missed bump HALTs the release. A service that does not mount the adapter never sees the version. The open question is whether a break of an optional adapter should HALT the release of the core, or only the adapter's own tag.
- created: 2026-11-03
- status: open
- found: reading the CHANGELOG entry of the list change against `adapter/http/handler.go` while checking [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down).
- next: decide. Either way the version should become `"2"`, since the contract did change: change the constant, its test, the example in `wiki/http-in-five-minutes.md` and the break's CHANGELOG entry in one commit. If the answer is that it does not HALT the core, reword [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down) and guideline 12, and tag the adapter apart from the root.
- blocks: [CP4.4: Breaking changes are written down](ROADMAP.md#cp44-breaking-changes-are-written-down)

### [P2][TASK] Script the resident memory of the reference service

- description: [B8: Memory per task](GOALS.md#b8-memory-per-task) cites the resident memory of `bench/refservice`, but no script or benchmark measures it; `footprint.sh` and `BenchmarkStartup` are the only ones. The two values on record, 12 and 16 MB on 2026-10-16 and 12.1 and 19.7 MB on 2026-11-03, were taken by hand with `ps`, the second after 200 health checks and 200 `POST /v1/sessions`, so they may not be the same procedure.
- created: 2026-11-03
- status: open
- found: running every scripted check of GOALS on 2026-11-03.
- next: add the procedure to `bench/refservice` beside `footprint.sh`, print one JSON object, and name it in its README and in [B8: Memory per task](GOALS.md#b8-memory-per-task).
- blocks: [CP4.2: The scripted benchmarks](ROADMAP.md#cp42-the-scripted-benchmarks), [B8: Memory per task](GOALS.md#b8-memory-per-task)

### [P2][TASK] Play the live goals that no driver plays yet

- description: [M1: Context size and cost per Turn by the Session's age](GOALS.md#m1-context-size-and-cost-per-turn-by-the-sessions-age-)'s cost per Turn, [M3: Cost of a compaction](GOALS.md#m3-cost-of-a-compaction)'s time and cost, and [M2: The first request after an idle return](GOALS.md#m2-the-first-request-after-an-idle-return-) are not measured, because their method is not built. `long_session` runs on a scripted fake model, so a live run cannot play it, and no task pauses past a provider's cache retention. The numbers of [B1: Cache hit rate](GOALS.md#b1-cache-hit-rate-) to [B4: Error rate, split by cause](GOALS.md#b4-error-rate-split-by-cause) are from 2026-10-16 and the live run needs a model endpoint and its price.
- created: 2026-11-03
- status: open
- found: re-reading the methods of [M1: Context size and cost per Turn by the Session's age](GOALS.md#m1-context-size-and-cost-per-turn-by-the-sessions-age-), [M2: The first request after an idle return](GOALS.md#m2-the-first-request-after-an-idle-return-) and [M3: Cost of a compaction](GOALS.md#m3-cost-of-a-compaction).
- next: build a live form of the long session, and a task with a pause, in `internal/reftasks`, so `gotato-workload -live` can play them.
- related: [M1: Context size and cost per Turn by the Session's age](GOALS.md#m1-context-size-and-cost-per-turn-by-the-sessions-age-), [M2: The first request after an idle return](GOALS.md#m2-the-first-request-after-an-idle-return-), [M3: Cost of a compaction](GOALS.md#m3-cost-of-a-compaction)

### [P2][ISSUE] The doc of `model.Retry` says `MaxWait` caps every wait, and the wait after a transport error has no cap

- description: `Retry.MaxWait` is documented as the cap on the waits, but after a transport error (no response) the wait is the backoff alone and is not capped (`model/http.go`). specs/model.md states what the code does.
- created: 2026-11-03
- status: open
- found: comparing specs/model.md with the code on 2026-11-03.
- next: cap that wait by `MaxWait` as the doc says, with a test, or change the doc.

### [P2][TASK] CI should run `scripts/check.sh` for each module

- description: `.github/workflows/ci.yml` spells out gofmt, vet and test for each module, and `scripts/check.sh` does the same, so the checks have two definitions that can drift.
- created: 2026-11-03
- status: open
- next: make each CI job call `scripts/check.sh <module>`.

### [P3][IDEA] Operators see little of an instance

- description: Instance Events are readable only from Go, there is no hook for metrics, and `/readyz` reports four fields.
- created: 2026-11-03
- status: open
- found: rewriting RUNBOOK.md on 2026-11-03.
- related: [F10.4: Watch an instance](../FEATURES.md#f104-watch-an-instance)

### [P3][TASK] Two READMEs hold command blocks longer than two lines

- description: `bench/refservice/README.md` and `internal/cmd/gotato/README.md` hold multi-line commands, which the doc rules put in a script the doc names.
- created: 2026-11-03
- status: open
- next: move each block to a script beside the README and name it there.

### [P3][IDEA] A warm Session holds about 570 bytes a Message

- description: a warm Session holds 8 to 15 KB with 10 Messages, 49 to 73 KB with 100 and 575 to 586 KB with 1,000, nearly three times the Messages' own size. What the Record keeps beyond the Messages is the first place to look if this needs to come down.
- created: 2026-11-03
- status: open
- related: [B8: Memory per task](GOALS.md#b8-memory-per-task)

### [P3][TASK] About fifteen settings in FEATURES give no reason

- description: the numbers have no reason beside them: 8 waiting Steers, 4 Follow ups, 20K tokens of idle compaction, a kept tail of 20K tokens or a quarter of the window, 10,000 warm Sessions, 30 minutes before the time-away notice, 5 s of waiting for a release, 10 s for a dead instance's row, a page of 100 Sessions, a 15 s SSE comment, a 10 s MCP connect and a 2K-token cut. The guide says a setting needs a reason. The same defaults are also written in RUNBOOK.md, in specs/session.md, specs/compaction.md and specs/host.md, and in GLOSSARY.md, so a change of one number has to be made in up to four docs.
- created: 2026-11-03
- status: open
- found: reading [FEATURES.md](../FEATURES.md) against its guide on 2026-11-03.
- next: find each reason in the specs and decisions, and add it in one clause, or write down that there is none. RUNBOOK.md already holds the defaults of the host's settings, so FEATURES could say "a bound the host sets" and leave the number to RUNBOOK.md, which solves the missing reason and the second copy of the number together.
