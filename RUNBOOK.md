<!-- what-we-build: drafted 2026-10-28 -->
# Gotato — Runbook

What to do when a service built on Gotato misbehaves, found by what you see. Gotato is a library and sends no alerts of its own, so this runbook starts from the code, status or Event you see, not from an alert name. Your team owns on-call and paging.

This is a first version. No team has run Gotato in production yet, so it holds only what the code does today. Each row was copied from [FEATURES.md](FEATURES.md) and has **not yet been verified by running it**. Add a row when a real incident needs one.

**What you see**

| You see | Go to |
|---|---|
| An error answer with `Gotato-Not-Run` or `Gotato-Retryable` | [A request is refused](#a-request-is-refused) |
| `/readyz` answers 503 | [An instance is draining](#an-instance-is-draining) |
| A Session stays `busy` after its instance died | [An instance died](#an-instance-died) |
| A Run ended `failed` with the cause `interrupted` | [A Run was interrupted](#a-run-was-interrupted) |
| A service that does not start or answer | [Check the setup](#check-the-setup) |

## A request is refused

An error carries two answers from the runtime, over HTTP as headers and in the body, in the CLI's JSON as fields:

- **Not run** (`Gotato-Not-Run: true`, `"not_run"`): nothing with side effects happened, so sending the same request again cannot run it twice. It does not say that sending it again helps: a `404` or a `400` is not run and fails the same on every instance.
- **Retryable** (`Gotato-Retryable: true`, `"retryable"`): the same request, sent again later, may succeed.

Send a request to another instance only when it is both not run and retryable, except `not_writer`, which goes to the address in the `Gotato-Writer` header. A `Retry-After` header says when to try again. The status follows the code, never the other way: a code has the same status on every route, and a run, a streamed run and a message answer a refusal the same.

This is the one table of every error code. `Not run` and `Retryable` are what the runtime sets for the code (`gotato/errors.go`); "by cause" means the place that makes the error decides. The HTTP column is `httpapi.StatusFor`, the exit column is the test tool's `internal/cmd/gotato`, and a test fails when this table and the code differ.

| Code | Not run | Retryable | HTTP | Exit | Produced by | Do |
|---|---|---|---|---|---|---|
| `invalid_argument` | by cause | no | 400 | 2 | the core and `service` when a request is malformed; `session` for a bad Record or ID | fix the request; a Session whose Agent is gone answers it too, repair it with `agent` in the run body |
| `invalid_state` | by cause | no | 409 | 1 | the core (`Continue` on a Transcript that cannot continue, `Interrupt` with no Run); `service` (no Run to abort, the Run is over); `session` | read the message: the code is shared by these cases |
| `busy` | yes | yes | 409 | 1 | `service` (a Session with a Run, here or on another instance); the core (a second Prompt) | retry after the Run in flight ends (`Retry-After`) |
| `session_closed` | yes | no | 409 | 1 | the core, `service` messages | the Session is closed; start another |
| `session_closing` | yes | no | 409 | 1 | the core, `service` messages | the Session is closing; start another |
| `model_failure` | no | by cause | 500 | 1 | the core, from the Model | read `retryable`; the provider decides |
| `context_overflow` | no | no | 422 | 1 | the core, from the Model | compact the Session; as a Run's result it answers 200 |
| `model_protocol_failure` | no | no | 500 | 1 | the core, from the Model | a bug in the provider adapter |
| `tool_resolution_failure` | no | no | 500 | 1 | the core, from a ToolSet | check the ToolSet |
| `tool_argument_failure` | no | no | 500 | 1 | the core | the model sent bad arguments |
| `tool_execution_failure` | no | no | 500 | 1 | the core, from a Tool | read the Tool's message |
| `extension_failure` | no | no | 500 | 1 | the core, from a hook | check the extension |
| `limit_exceeded` | by cause | by cause | 429 | 1 | `service` (`MaxActiveRuns`, marked not run and retryable; a full notice queue, not retryable); the core (a limit of a Run or a queue); `modelctx` | when marked, retry on another instance; otherwise read the message |
| `canceled` | no | no | 408 | 1 | the core, `service` | the caller left; as a Run's result it answers 200 |
| `deadline_exceeded` | no | no | 504 | 1 | the core, `service` | the Run's deadline passed; as a Run's result it answers 200 |
| `internal_invariant` | no | no | 500 | 1 | the core, `session` | a bug; keep the Session's Events and report it |
| `not_supported` | no | no | 400 | 1 | `session` | the Store does not do this |
| `conflict` | no | no | 409 | 1 | `session`, `service` | another writer wrote first; read the Session again |
| `store_failure` | no | yes | 503 | 1 | `session`, `service` | the store failed; it may have run; a Run whose save failed answers 500 `not saved` |
| `overloaded` | yes | yes | 503 | 1 | `service` (`MaxActiveSessions`) | retry on another instance; if every instance answers it, raise `--max-sessions` or add instances |
| `draining` | yes | yes | 503 | 1 | `service` | send the request to another instance; your load balancer should do it on its own once `/readyz` answers 503 |
| `not_found` | yes | no | 404 | 3 | `session`, `service` | the Session does not exist; do not retry |
| `cannot_resume` | by cause | no | 409 | 1 | `service` (a stream) | start over with `after` 0; the body has the current `record_epoch` |
| `not_writer` | yes | yes | 409 | 1 | `service` (a message, notice or abort for a Run another instance writes) | go to the `Gotato-Writer` address, or enable forwarding |

The HTTP layer adds three codes to a body where the runtime has none or shares one: `session_exists` (409, a create with an ID that is taken), `body_too_large` (413, a body over the cap, `httpapi.WithMaxBody`) and `forward_failed` (502, a forward to the writer failed, so the request may have reached it). A 400 from the HTTP layer itself (bad JSON, an unknown field, a `session-id` header that disagrees) carries `invalid_argument`.

## An instance is draining

On SIGTERM an instance marks itself draining, answers 503 on `/readyz`, takes no new Runs, lets each Run finish, then saves and releases its Sessions. This is the normal way to stop one.

- **Give it time.** `--drain-timeout` of the test CLI, which sets `service.Config.DrainTimeout` (10 seconds by default), is how long it waits before it stops the Runs still going. Set your platform's termination grace period above it.
- **A stopped Run is not lost.** Its stream ends with its result and then a reconnect code. The caller reconnects, and a Session that no instance uses can be taken over by any instance.
- **If `/readyz` stays 503** after a restart, the process is still draining. Look at the instance's Events for `session_unloaded` with `drain`.

## An instance died

A dead instance cannot release its Sessions, but it holds them only through its Liveness row. The row expires on its own; when it does, every Claim of that instance ends and another instance may take its Sessions over.

- **Wait for the row to expire.** An instance renews its row every third of the row's TTL, so the row lapses one TTL after the last renewal. A request to a Session whose writer is gone is served by the next instance that gets it.
- **A Session whose writer is alive** makes a request wait for it, up to a bound, then claim it or answer `busy`.
- **If `/readyz` answers 503 with `"lapsed": true`**, the instance could not renew its row in time, usually because the store was out of reach. It refuses writes, and answers every Run as not run, so the load balancer can send it elsewhere. When the store answers again it registers a new name by itself and `/readyz` returns to 200; no restart is needed.
- **A write from the old instance cannot corrupt the Record.** It lost its Claim, so the Store refuses it (the Epoch).

## A Run was interrupted

A `run_ended` with the status `failed` and the cause `interrupted` means the Run never ended on its own: its instance stopped, for example by a crash, before it finished. The next instance that opens the Session writes that `run_ended` for it. A tool call that had started is answered in the Transcript with a result that says it stopped with an unknown outcome, so a tool may have done only part of its work. Gotato sends the model no notice about it.

- **Read the Record** with `GET /v1/sessions/{id}/events` and check what the last tool call did before you start the Run again.
- **A tool with an external effect** should be idempotent or check before it writes. Gotato does not undo a tool.

## Check the setup

A service checks itself over HTTP.

- **`GET /readyz`** answers 503 while the instance drains.
- **`GET /healthz`** answers with the contract version. An instance on an older contract refuses what it does not know during a rolling deploy, until it is replaced.
