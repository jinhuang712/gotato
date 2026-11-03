# Gotato — Runbook

For the person keeping a service built on Gotato running. Gotato is a library and sends no alerts of its own, so this runbook starts from what you see: a status, a code, a header or an Event. Your team owns the paging and the on-call rota.

This is a first version. No team has run Gotato in production yet, so it holds what the code does today, read from the code on 2026-11-03. It has not been verified by an incident. Add a row when a real one needs it.

**What you see**

| You see | Go to |
|---|---|
| An error answer with `Gotato-Not-Run` or `Gotato-Retryable` | [A request is refused](#a-request-is-refused) |
| `/readyz` answers 503 with `draining` | [An instance is draining](#an-instance-is-draining) |
| `/readyz` answers 503 with `lapsed` | [An instance lost its liveness row](#an-instance-lost-its-liveness-row) |
| A Session stays `busy` after its instance died | [An instance died](#an-instance-died) |
| A Run ended `failed` with the cause `interrupted` | [A Run was interrupted](#a-run-was-interrupted) |
| A service that does not start or answer | [Start an instance](#start-an-instance) |

## Deploy

Deploy an instance like any other copy of your service: start it with a Store and its definitions, and stop it with a drain, so a deploy loses no work.

### Start an instance

An instance is one copy of your service holding a `host.Runner`. It needs a Store and a Registry with at least one AgentProfile registered in code. Everything else has a default. These are the settings of `host.Config` an operator touches.

| Setting | Default | Does |
|---|---|---|
| `Heartbeat.TTL` | 10 s | how long the instance's liveness row lives after each renewal; renewed every third of it |
| `Heartbeat.Wait` | 5 s | how long a Run waits for a Session another live instance is running, before `busy` |
| `Bounds.MaxActiveSessions` | no limit | the hard limit on Sessions in memory; at it, the longest-idle warm copy is evicted, then Runs are refused as `overloaded` |
| `Bounds.MaxWarmSessions` | 10,000 | the most idle copies kept; negative means no bound |
| `Bounds.Queue` | reject | a second Run on a Session this instance runs fails `busy`, or waits when set to wait |
| `Admitter` | none | the service's own policy for whether a Run may start; `host.Limits{MaxActiveRuns: n}` bounds the Runs in flight |
| `Warm.TTL` | 60 min | how long an idle Session stays in memory; negative keeps none |
| `Warm.IdleLead`, `Warm.IdleMinTokens` | 5 min, 20,000 tokens | when an idle Session is compacted before its cache expires, and the least stretch worth it |
| `TimeAwayAfter` | 30 min | idle time after which a message is followed by a `time_away` notice; negative turns it off |
| `DrainTimeout` | 10 s | how long a drain lets Runs finish before it stops them |
| `Address` | none | where this instance can be reached; without it a `not_writer` answer cannot name the writer |
| `StreamPoll` | 1 s | how often an idle Session stream looks in the store for another instance's Events |

- **Probes.** `GET /healthz` is 200 while the process is up, also while it drains. `GET /readyz` is 200, or 503 while the instance drains or its liveness row lapsed; send traffic by it.
- **Route by the session ID.** Hash the `session-id` header at your load balancer. It is routing and not correctness: a miss still works.
- **The test CLI.** `gotato serve` has `--addr`, `--max-runs`, `--max-sessions`, `--queue`, `--warm-ttl`, `--drain-timeout` and `--advertise`. It is a test tool with no contract.

### Stop an instance

On SIGTERM drain first, then stop the HTTP server, then close the Runner. `Drain` marks the instance draining, so `/readyz` answers 503 and the load balancer stops sending work. It takes no new Run and no Session over, lets each Run finish, and saves and releases its Sessions. A Run still going at `DrainTimeout` is stopped, saved and released. `Close` last removes the liveness row.

- **Give it time.** Set the platform's termination grace period above `DrainTimeout` plus about 25 seconds: a stopped Run is waited for up to 15 seconds, and the server shutdown and the close take up to 5 seconds each.
- **A stopped Run is not lost.** Its stream ends with its result and a reconnect hint. The caller reconnects, and any instance can take the Session over.
- **A drain is never undone.** A restarted process is a new Runner.

## Roll back

Roll back the way you deploy, one instance at a time, with drain.

- **Do not mix 0.2.0 and 0.3.0 over one store, and do not roll back across them.** 0.3.0 changed what a Store saves: the Header carries the Record's epoch, the listing columns and Gotato's own state apart, and the file store writes a different batch file, so a directory written by 0.2.0 is not read. An adapter of your own has to be rewritten to the new Records seam. The changelog gives no migration, so read its Store entries before you move.
- **Between versions that keep the stored form,** old and new instances share the store while the roll goes on. Mixing versions in one fleet has not been tried: roll one instance first and watch it. `GET /healthz` returns the HTTP contract version, so you can see which instances run which.
- **A Run in flight finishes** on the instance that has it, or ends `interrupted` if that instance dies.
- **A break in Go code or a Store adapter** comes with a before-and-after note in the changelog. Read it before you move a version.

## Alerts

Gotato raises no alert itself. Each section below starts from what you see, says what it means and what to do, and *On call* says when to escalate.

### A request is refused

An error carries two answers from the runtime, over HTTP as headers and in the body:

- **Not run** (`Gotato-Not-Run: true`, `"not_run"`): nothing with side effects happened, so sending the same request again cannot run it twice. It does not say that sending it again helps: a `404` or a `400` is not run and fails the same on every instance.
- **Retryable** (`Gotato-Retryable: true`, `"retryable"`): the same request, sent again later, may succeed.

Send a request to another instance only when it is both not run and retryable, except `not_writer`, which goes to the address in the `Gotato-Writer` header. `Retry-After` is sent only when a Run found its Session busy on another instance, after the wait (5 seconds by default). The status follows the code, never the other way: a code has the same status on every route.

This is the one table of every error code. "By cause" means the place that makes the error decides. The HTTP column is the status `adapter/http` gives, and the exit column is the test CLI's.

| Code | Not run | Retryable | HTTP | Exit | Produced by | Do |
|---|---|---|---|---|---|---|
| `invalid_argument` | by cause | no | 400 | 2 | the core and `host` for a malformed request; `record` for a bad Record, `store` for a bad ID | fix the request; a Session whose Agent is no longer registered answers it too, repair it with `agent` in the run body |
| `invalid_state` | by cause | no | 409 | 1 | the core, `host`, `store` | read the message: the code is shared by several cases |
| `busy` | yes | yes | 409 | 1 | `host` (a Session with a Run, here or on another instance); the core (a second Prompt) | retry after the Run ends |
| `session_closed` | yes | no | 409 | 1 | the core, `host` messages | the Session is closed; start another |
| `session_closing` | yes | no | 409 | 1 | the core, `host` messages | the Session is closing; start another |
| `model_failure` | no | by cause | 500 | 1 | the core, from the Model | read `retryable`; the provider decides |
| `context_overflow` | no | no | 422 | 1 | the core, from the Model | compact the Session; as a Run's result it answers 200 |
| `model_protocol_failure` | no | no | 500 | 1 | the core, from the Model | a bug in the provider adapter |
| `tool_resolution_failure` | no | no | 500 | 1 | the core, from a ToolSet | check the ToolSet |
| `tool_argument_failure` | no | no | 500 | 1 | the core | the model sent bad arguments |
| `tool_execution_failure` | no | no | 500 | 1 | the core, from a Tool | read the Tool's message |
| `tool_not_loaded` | no | no | 500 | 1 | the core | the model called a catalog Tool before it was loaded; it is loaded now. It is the `code` of a failed Tool Result and never ends a Run |
| `extension_failure` | no | no | 500 | 1 | the core, from a hook | check the extension |
| `limit_exceeded` | by cause | by cause | 429 | 1 | `host` (`Limits{MaxActiveRuns}` or the service's own `Admitter`, not run and retryable; a full notice queue, not retryable); the core (a limit of a Run or a queue); `record` | when marked, retry on another instance; otherwise read the message |
| `canceled` | no | no | 408 | 1 | the core, `host` | the caller left; as a Run's result it answers 200 |
| `deadline_exceeded` | no | no | 504 | 1 | the core, `host` | the Run's deadline passed; as a Run's result it answers 200 |
| `internal_invariant` | no | no | 500 | 1 | the core, `record` | a bug; keep the Session's Events and report it |
| `not_supported` | no | no | 400 | 1 | `host`, `record`, `model` | the Store or wire does not do this, or does not know a stored Record's kind or schema |
| `already_exists` | no | no | 409 | 1 | `host` | the Session ID the caller chose is taken; choose another or read the Session that has it |
| `conflict` | by cause | no | 409 | 1 | `store`, `host` | another writer wrote first, or this instance's row lapsed; read the Session again |
| `store_failure` | by cause | yes | 503 | 1 | `store`, `host` | the store failed; a failed load or claim did not run, a failed write of a Run's step may have; a Run whose save failed answers 500, or 409 when another writer took the Session, with a generic message and the detail in the server log |
| `overloaded` | yes | yes | 503 | 1 | `host` (`Bounds.MaxActiveSessions`) | retry on another instance; if every instance answers it, raise the limit or add instances |
| `draining` | yes | yes | 503 | 1 | `host` | send the request to another instance; your load balancer should do it once `/readyz` answers 503 |
| `not_found` | yes | no | 404 | 3 | `record`, `host` | the Session does not exist; do not retry |
| `cannot_resume` | no | no | 409 | 1 | `host` (a stream) | start over with `after` 0; the body has the current `record_epoch` |
| `not_writer` | yes | yes | 409 | 1 | `host` (a message, notice, abort or delete for a Session another instance writes) | go to the `Gotato-Writer` address, or enable forwarding |

The HTTP layer adds two codes to a body: `body_too_large` (413, a body over 1 MiB by default, `WithMaxBody`) and `forward_failed` (502, a forward to the writer failed, so the request may have reached it). A 400 from the HTTP layer itself (bad JSON, an unknown field, a `session-id` header that disagrees) carries `invalid_argument`. The test CLI also exits 4 when a Run did not complete.

### An instance is draining

`/readyz` answers 503 with `"draining": true`: someone called `Drain`, as the stop procedure does.

- **Leave it.** It is stopping. Replace it, do not wait for it to recover: a drain is never undone.
- **If it should not be draining,** look for what sent the signal (a deploy, a platform eviction) and restart the process.

### An instance lost its liveness row

`/readyz` answers 503 with `"lapsed": true`: the instance could not renew its row in time, usually because the store was out of reach. It refuses Runs with `conflict`, marked not run and not retryable, so `/readyz` is what moves traffic away.

- **Check the store first.** When it answers again the instance registers a new name by itself and `/readyz` returns to 200. No restart is needed.
- **Its old writes cannot land.** Another instance may hold its Sessions by now, and the store refuses any write from the old name.
- **If it does not recover** within a few TTLs, restart it and look at the store's logs.

### An instance died

A dead instance cannot release its Sessions, but it holds them only through its liveness row. The row expires on its own; then every Claim of that instance ends and another instance may take its Sessions over.

- **Wait for the row to expire.** It lapses one TTL (10 seconds by default) after the last renewal. A request to a Session whose writer is gone is served by the next instance that gets it.
- **A Session whose writer is alive** makes a request wait up to `Heartbeat.Wait`, then answers `busy`.
- **A write from the old instance cannot corrupt the Record.** It wrote after a head that is no longer the last, so the store refuses it with a conflict.

### A Run was interrupted

A `run_ended` with the status `failed` and the cause `interrupted` means the Run never ended on its own: its instance stopped, for example by a crash, before it finished. The next instance that opens the Session writes that `run_ended`. A tool call that had started is answered in the Transcript with a result that says the instance stopped and the outcome is unknown, so a tool may have done only part of its work. Gotato sends the model no notice about it.

- **Read the Record** with `GET /v1/sessions/{id}/events` and check what the last tool call did before you start the Run again.
- **A tool with an external effect** should be idempotent or check before it writes. Gotato does not undo a tool.

## On call

Your team owns who is on call and how they are paged. Gotato gives you what to watch, and it ships no metrics or alerting.

- **Page on:** `/readyz` at 503 for longer than a deploy takes, a rise in `store_failure`, and `overloaded` from every instance.
- **Look at:** Runs and Sessions in flight (`Runner.ActiveRuns`, `ActiveSessions`), `Runner.Draining` and `Runner.Lapsed`, and the instance Events of `Runner.SubscribeInstance` (`run_rejected`, `session_fenced`, `liveness_lapsed`, `store_write_failed`), which only Go code can read: forward them to your own logs.
- **Escalate** to the maintainers when a code is `internal_invariant` or `model_protocol_failure`, or when a Record looks wrong. Send the Session's Events and the code.
