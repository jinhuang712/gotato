# TODO

## ongoing

### [P1][DISCUSSION] Does 0.3.0 raise the HTTP contract version?

- description: `httpapi.ContractVersion` is `"1"`, the value of 0.2.0, and `GET /healthz` reports it. Unreleased changes the answer of `GET /v1/sessions` from an array to `{"sessions": […], "next": "…"}`, with listing columns only and no `record_epoch`, so a client written for 0.2.0 breaks. G-G06 says each break that touches HTTP raises the version. The other HTTP changes are additive: `task` on `POST /v1/sessions` and `GET /v1/tasks`. Recommendation: raise it to `"2"` in 0.3.0, and say so in the break's CHANGELOG entry, since the version is only a mark for a rolling deploy and costs nothing.
- created: 2026-11-03
- status: open
- found: reading the CHANGELOG entry of the list change against `adapter/http/handler.go` while checking G-G06.
- next: decide, then change the constant and the guides that quote it in the same commit.
- blocks: [CP4.4](ROADMAP.md#cp4-the-030-release), [G-G06](GOALS.md#g-g06--breaking-changes-are-written-down)

### [P1][ISSUE] Gotato's time per Turn looks 10 to 35% above 0.2.0

- description: the current code takes more time per Turn than `v0.2.0` at 10, 100 and 1,000 messages, with a fake model and a memory store. `history_10`: 0.12 to 0.13 ms against 0.10. `history_100`: 0.40 to 0.46 ms against 0.31 to 0.39. `history_1000`: 2.9 to 3.0 ms against 2.2 to 2.7. The shipped benchmark, `BenchmarkReferenceTasks`, agrees on the current code (0.13, 0.46 and 3.4 ms). The machine had other work running (load average about 5), so every time is noisy and about twice the values of 2026-10-17. The cause is not known; the layers of decisions 044 to 051 touched the Record, the Header and the Session.
- created: 2026-11-03
- status: open
- found: `gotato-workload -plays 30` built from `v0.2.0` (`git archive`) and from the current code, run alternately three times each on 2026-11-03.
- next: repeat the alternation on a quiet machine, and if the gap holds, profile `history_100` and `history_1000` and find what the layers added.
- blocks: [CP4.2](ROADMAP.md#cp4-the-030-release), [G-B05](GOALS.md#g-b05--gotatos-time-per-turn), [G-B09](GOALS.md#g-b09--cpu-per-turn-at-10-100-and-1000-messages)

### [P1][TASK] Say in the release procedure that the nested modules' `require` moves with the root

- description: `adapter/mcp/go.mod` and `adapter/http/go.mod` both require the root at `v0.2.0` and point at `../..` with a `replace`, which a consumer ignores. `adapter/http` imports `host`, which `v0.2.0` does not have, so `go get` of `adapter/http@v0.3.0` alone would not build unless its `require` names the new root version. The steps of RUNBOOK.md (*Cut a release*) do not say to set it, and `adapter/http` has no tag yet. Recommendation: its first tag is `v0.3.0`, in step with the root and `adapter/mcp`.
- created: 2026-11-03
- status: open
- found: reading the go.mod files while writing the 0.3.0 release steps.
- next: add the step to RUNBOOK.md: in the release commit, set the `require` of each tagged nested module to the new root version, then tag all three on that commit.
- blocks: [CP4.5](ROADMAP.md#cp4-the-030-release)

### [P2][TASK] Script the resident memory of the reference service

- description: G-B08 cites the resident memory of `bench/refservice`, but no script or benchmark measures it; `footprint.sh` and `BenchmarkStartup` are the only ones. The two values on record, 12 and 16 MB on 2026-10-16 and 12.1 and 19.7 MB on 2026-11-03, were taken by hand with `ps`, the second after 200 health checks and 200 `POST /v1/sessions`, so they may not be the same procedure.
- created: 2026-11-03
- status: open
- found: running every scripted check of GOALS on 2026-11-03.
- next: add the procedure to `bench/refservice` beside `footprint.sh`, print one JSON object, and name it in its README and in G-B08.
- blocks: [CP4.2](ROADMAP.md#cp4-the-030-release), [G-B08](GOALS.md#g-b08--memory-per-task)

### [P2][DISCUSSION] Should the footprint have a limit?

- description: G-B12 is a comparison with Google ADK for Go, so nothing fails when the binary grows. A reflection bug put 1.6 MB into the reference service (10.6 MB, 9.0 MB after the fix of 2026-11-03), and a goal that only records a value did not catch it. A threshold would fail the moment the limit is crossed. Recommendation: add a threshold on the stripped binary of the reference service, set by the maintainer from the 9.0 MB of 2026-11-03 with some headroom (10 MB, for example), checked by `bench/refservice/footprint.sh`.
- created: 2026-11-03
- status: open
- found: re-reading G-B12 for the 2026-11-03 measurement.
- related: [G-B12](GOALS.md#g-b12--footprint)

### [P2][TASK] Play the live goals that no driver plays yet

- description: G-B14's cost per Turn, G-B16's time and cost, and G-B15 are not measured, because their method is not built. `long_session` runs on a scripted fake model, so a live run cannot play it, and no task pauses past a provider's cache retention. The numbers of G-B01 to G-B04 are from 2026-10-16 and the live run needs a model endpoint and its price.
- created: 2026-11-03
- status: open
- found: re-reading the methods of G-B14, G-B15 and G-B16.
- next: build a live form of the long session, and a task with a pause, in `internal/reftasks`, so `gotato-workload -live` can play them.
- related: [G-B14](GOALS.md#g-b14--context-size-and-cost-per-turn-by-the-sessions-age-), [G-B15](GOALS.md#g-b15--the-first-request-after-an-idle-return-), [G-B16](GOALS.md#g-b16--cost-of-a-compaction)
