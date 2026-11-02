# 048. The Runner and the Session are built from parts with one job each, and host plumbing leaves the public API

Decided: 2026-11-02    Status: current

## The problem
`service.Runner` has 37 fields across eight jobs, four test seams and a clock only warm copies use; warm eviction is written twice. `gotato.Session` has 49 fields and 6 locks, and the `commitUpTo` closure runs five stages in one body. The core also defines what only the service uses: nine Instance Events, five error codes and about fifteen host calls. Every exported name is contract ([010](010-gotato-is-a-light-library-with-a-versioned-contract.md)), and a library user can call them by mistake.

## The choice
**Parts.** A part does one job, with its own lock and file; the outer type calls its parts in order.
- The Runner holds `instance *heartbeat.Row` (name, row, guard, drain mark), `runs *runs.Table` (admitted Runs, counts, drain wait), `warm *warm.Cache` (idle copies, timers, one eviction), `locks`, `feeds`, `streams`, `releases`, the definitions ([044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)), the store ([049](049-the-store-contract-holds-only-what-every-store-must-do.md)), one `clock` and one `testHooks`. `heartbeat`, `runs`, `warm` and `clock` go under `service/internal/`, so no part reaches into another. Lock order: runs, then warm.
- The Session holds `def` (what the Agent gave, unlocked), `life` (commands, admission, status, close), `run` (the Run in flight, with the interrupt cancel; the abort count becomes a `sync.WaitGroup`), `events` (Journal, staging, numbers, Feed), `history` (Transcript, size, oversize fallback), `tools` (the registry, with its own lock), `queue` and `host`. 6 locks become 4, ordered events, run, queue. `commitUpTo` becomes `commitResult`, one call per stage. The Session stays one package, its files ordered by `TestFileLayers`.

**One new seam, admission.** Whether a Run may start is a policy the service has its own reasons for (one tenant gets ten Runs, another fifty), and only the Runner knows what runs on this instance. So `Admitter` decides it: `Admit(ctx, Admission, Load) (release func(), error)`, where `Admission` names the Session, the AgentProfile and the TaskSpec, `Load` is this instance's counts, and the caller's `ctx` carries who asks. `release` runs when the Run ends, so a per-tenant count stays right. The default `Limits{MaxActiveRuns}` is today's number. The memory bound (`MaxActiveSessions`, with the eviction of warm copies) and the drain stay the Runner's and run before `Admit`, since they guard the instance, not a policy. The Runner's other seams are those a user fills: definitions, `session.Records` and `session.Liveness`. Eviction, locks and the clock stay internal ([009](009-one-loop-extended-at-fixed-stages.md)). All service code reads the one clock.

**Host plumbing goes to `internal/hostlink`**: function values the core sets in `init`, which `service` and `testkit` type-check once, as grpc-go's `internal` does. It takes `WithFeed`, `WithRunStart`, `WithQueueHandoff`, `WithRequestID`, `TakeQueued`, `Requeue`, `DropQueued`, `NewFeed`, `NewInstanceFeed`, `MirrorTo`, `SetLabel`, `Publish`, `PublishLabeled`, `Session.Published`. `Feed` keeps `Subscribe` and `Close`. `RunRequested`, `RunWaiting`, `RunRejected`, `SessionFenced`, `LivenessLapsed`, `SessionDeleted`, `SessionLoaded`, `SessionSaved`, `StoreWriteFailed` and `CodeNotWriter`, `CodeDraining`, `CodeOverloaded`, `CodeCannotResume`, `CodeAlreadyExists` move to `service`, the codes listed by `service.ErrorCodes()`. Kinds and strings stay, so HTTP is unchanged. An Event states its `Class()`, replacing `eventClasses`, and `MarshalEvent` writes any Event's kind.

**Deleted and renamed.** `EventsOption`, `StreamRunOption` and `SessionStreamOption` hold nothing and go; adding `opts` later breaks no caller. `replyPartEvery` and `replyPartBytes` become constants. The settings struct `service.Liveness` becomes `service.Heartbeat`, apart from 049's `session.Liveness`.

## Not chosen
- Comments that section the types: nothing stops the next field landing anywhere.
- An interface per part: nobody replaces one, and each is code to maintain.
- Admission by numbers only: a limit per tenant needs the instance's counts, which a proxy in front cannot see.
- Eviction behind `Admitter`: a policy could then hold more Sessions than memory allows.
- A public `gotato/host`: plumbing stays contract for a second host nobody writes; if one appears, `hostlink` becomes it.
- Host calls in the core under a "for hosts" heading: still contract, still callable.
- One live Session across Runs: it changes [036](036-a-warm-copy-is-a-cache-not-a-lock.md) and [021](021-messages-reach-the-writer-and-take-effect-in-its-process.md), and needs its own decision.
- `gotato.RegisterEvent`: global state, and import order decides decoding.
- The Session across packages: its parts share one goroutine and lock order.

## Consequences
Each part is read and tested alone, and a library user sees only the Session's API. `gotato.UnmarshalEvent` reads a service Event as an `UnknownEvent`, and Gotato ships no Go client that needs more ([026](026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md)). Each rename is a break with a CHANGELOG entry. Each step keeps tests green:

1. The constants, the three option types, one `testHooks` field.
2. `service/internal/clock` for every time read, added to the layering table.
3. `warm` with one eviction, then `heartbeat`, then `runs`, whose doc states the lock order; `runs` calls `Admitter` after the memory bound and the drain, and `Admission.MaxActiveRuns` becomes the default `Limits`. After 049, rename `service.Liveness` to `service.Heartbeat`.
4. The Session's parts, a commit each: `tools`, `history`, `events`, `run`, `life`, `def`; then `commitResult`.
5. `Event.Class()` and `MarshalEvent` for any Event; httpapi encodes with it.
6. The nine Events and five codes move to `service`; httpapi maps `service.ErrorCodes()`.
7. `internal/hostlink`, and the host calls leave the core.
