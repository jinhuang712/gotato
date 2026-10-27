<!-- what-we-build: approved 2026-10-25 -->
# Gotato — Runbook

What to do when a service built on Gotato misbehaves, found by what you see. Gotato is a library and sends no alerts of its own, so this runbook starts from the code, status or Event you see, not from an alert name. Your team owns on-call and paging.

This is a first version. No team has run Gotato in production yet, so it holds only what the code does today. Each row was copied from [FEATURES.md](FEATURES.md) and [cmd/gotato/README.md](cmd/gotato/README.md) and has **not yet been verified by running it**. Add a row when a real incident needs one.

**What you see**

| You see | Go to |
|---|---|
| HTTP 503 marked "not run" | [A request is refused](#a-request-is-refused) |
| `/readyz` answers 503 | [An instance is draining](#an-instance-is-draining) |
| A Session stays `busy` after its instance died | [An instance died](#an-instance-died) |
| A Run ended `failed` with the cause `interrupted` | [A Run was interrupted](#a-run-was-interrupted) |
| `gotato` exits with a non-zero code | [CLI exit codes](#cli-exit-codes) |
| A service that does not start or answer | [Check the setup](#check-the-setup) |

## A request is refused

An error marked "not run" means nothing with side effects happened, so sending the same request to another instance cannot run it twice. Over HTTP it is a 503 with the header `Gotato-Not-Run: true`.

| Code | Meaning | Do |
|---|---|---|
| `draining` | the instance is shutting down | send the request to another instance; your load balancer should do it on its own once `/readyz` answers 503 |
| `overloaded` | the instance holds `MaxActiveSessions` Sessions and every one is running a Run | retry on another instance; if every instance answers it, raise `--max-sessions` or add instances |
| `limit_exceeded` | the instance is at `MaxActiveRuns` | same as `overloaded` |
| `busy` | the Session already has a Run, here or on another instance | retry after the Run in flight ends |

## An instance is draining

On SIGTERM an instance marks itself draining, answers 503 on `/readyz`, takes no new Runs, lets each Run finish, then saves and releases its Sessions. This is the normal way to stop one.

- **Give it time.** `--drain-timeout` (10 seconds by default) is how long it waits before it stops the Runs still going. Set your platform's termination grace period above it.
- **A stopped Run is not lost.** Its stream ends with its result and then a reconnect code. The caller reconnects, and a Session that no instance uses can be taken over by any instance.
- **If `/readyz` stays 503** after a restart, the process is still draining. Look at the instance's Events for `session_unloaded` with `drain`.

## An instance died

A dead instance cannot release its Sessions, but it holds them only through its Liveness row. The row expires on its own; when it does, every Claim of that instance ends and another instance may take its Sessions over.

- **Wait for the row to expire.** An instance renews its row every third of the row's TTL, so the row lapses one TTL after the last renewal. A request to a Session whose writer is gone is served by the next instance that gets it.
- **A Session whose writer is alive** makes a request wait for it, up to a bound, then claim it or answer `busy`.
- **A write from the old instance cannot corrupt the Record.** It lost its Claim, so the Store refuses it (the Epoch).

## A Run was interrupted

A `run_ended` with the status `failed` and the cause `interrupted` means the Run never ended on its own: its instance stopped, for example by a crash, before it finished. The next instance that opens the Session writes that `run_ended` for it. A tool call that had started is answered in the Transcript with a result that says it stopped with an unknown outcome, so a tool may have done only part of its work. Gotato sends the model no notice about it.

- **Read the Record** with `gotato events --session ID` and check what the last tool call did before you start the Run again.
- **A tool with an external effect** should be idempotent or check before it writes. Gotato does not undo a tool.

## CLI exit codes

| Code | Meaning | Do |
|---|---|---|
| `0` | success | nothing |
| `1` | runtime error: store, provider, encoding; or a Run whose reply could not be written | read stderr; check the store and the provider |
| `2` | usage error | read the usage text |
| `3` | not found (a Session or a tool ID) | check the ID |
| `4` | the Run did not complete: `failed`, `cancelled`, `deadline_exceeded` | the JSON on stdout still has the outcome; read its `error` |

## Check the setup

`gotato doctor --json` checks the store, the models, the gateway configuration and the tools. It exits 1 only when a check that is not a warning fails.

- **`model.gateway` warning**: `gateway.yaml` was not found. Pass `--gateway-config PATH` or set `GOTATO_GATEWAY_CONFIG`.
- **`store` fails**: the directory under `GOTATO_HOME` is missing or not writable.
- **`GET /healthz`** answers with the contract version. An instance on an older contract refuses what it does not know during a rolling deploy, until it is replaced.
