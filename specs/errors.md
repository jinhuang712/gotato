# Errors

A caller that gets an error has questions before it reads the code: what happened, did anything run, and may I try again. In a service with many Instances the second one matters most, because it decides whether the same request can go to another Instance without running twice. Gotato answers all three in the error itself, where the error is made, and every surface only translates. This spec follows one thread through the layers of the [map](ARCHITECTURE.md): the core defines the codes, the host adds five, and the adapters translate them.

## TL;DR
- Every error a Gotato layer returns is a `RuntimeError`: a `Code`, plus two facts set by the place that made it, `NotRun` and `Retryable`.
- A caller reads the two facts with `gotato.NotRun(err)` and `gotato.Retryable(err)`. HTTP and the CLI show the same answers and never work them out themselves.
- Text from a foreign error never reaches a client. It stays on `Cause`, and the message is a public one.

## What an error holds

One error carries a code, two facts and a cause.

```text
RuntimeError
┌─────────────────────────────────────────────────────┐
│ Code       what happened          "busy"            │
│ Message    text a client may read                   │
│ NotRun     nothing with effects happened            │
│ Retryable  the same request, later, may succeed     │
│ Details    small extras: writer, retry_after_ms     │
│ Operation  where it was made, shown in Error()       │
│ Cause      the foreign error, for logs only         │
└─────────────────────────────────────────────────────┘
```

The codes are two lists. `gotato.ErrorCodes()` lists the core's, and `host.ErrorCodes()` the five only a Runner makes: `not_writer`, `draining`, `overloaded`, `cannot_resume` and `already_exists` ([decision 048](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)). Their strings, their JSON and their HTTP statuses did not change. A layer that maps every code, a transport or a CLI, ranges over both. `gotato.ErrorOf` knows the defaults of the core's codes only; the service says for its own that draining, overloaded and not_writer are not run and retryable.

`NotRun` and `Retryable` are separate on purpose. A 404 did not run, yet it fails the same everywhere, so it is not worth sending again. A transient model failure did run, so it is retryable but not safe to send to another Instance blindly. The codes whose answer is the same wherever they are made get it by default; any other error gets its answer from the place that knows better. Both functions look through wrapped errors.

## Where the answers come from

The answers are set at the place of the failure, along the path of a request.

```text
 request ─▶ admission ─▶ claim ─▶ Run (Turns) ─▶ write ─▶ reply

 admission  draining, overloaded, invalid_argument (a reserved
            metadata key among them), not_found,
            limit_exceeded (the Runner's capacity)         ─ not run
 claim      busy, not_writer, session_closing              ─ not run
 Run        model_failure (Retryable if the Model says so),
            canceled, deadline_exceeded                    ─ it ran
 write      store_failure (Retryable; ran is unknown)      ─ it may have run
```

Admission and claim refuse before the Run starts, so it is marked not run and a load balancer may try another Instance. Admission has three steps, in order: a draining Instance refuses, the memory bound (`Bounds.MaxActiveSessions`, which evicts a warm copy first) refuses, and then the service's own `Admitter` decides; a refusal it returns is marked not run too. So is a request whose Agent is not registered (`ErrUnknownAgent`) or whose TaskSpec is unknown, invalid or widens its AgentProfile (`invalid_argument`). After the Run begins, a failure may have had effects, so only the cases that are known stay marked. A store failure on a write of a Run's step is not marked not run, because the write may have landed. Watch `limit_exceeded`: from the Runner's capacity it is not run and retryable, while the same code that ends a Run at a limit of its own ran and is neither.

## What a caller does

The two facts give four answers, and one code has a place to go.

```text
                      Retryable
                  no               yes
            ┌───────────────┬──────────────────────┐
  NotRun    │ fix the call  │ another Instance,    │
  yes       │ (not_found,   │ or later             │
            │  invalid_arg) │ (draining, busy)     │
            ├───────────────┼──────────────────────┤
  NotRun    │ it ran and    │ it ran; try again    │
  no        │ failed        │ later (model_failure │
            │               │ the model calls      │
            │               │ transient)           │
            └───────────────┴──────────────────────┘
  not_writer: not run and retryable, but retry at Details["writer"], not just anywhere
```

Send a request to another Instance only when it is both not run and retryable. The exception is `not_writer`, which names the Instance that writes the Session. An embedding service reads these answers from the error, and a client behind a load balancer reads them from the headers below. Both see the same thing.

## What ends a Run, and what does not

A failure inside a Run is handled where it is, and only some end the Run.

```text
 tool error ────────▶ failed Tool Result ("tool failed", a code) ──▶ the model reads it, Run goes on
 tool panic ────────▶ same
 bad tool arguments ▶ refused Tool Result ─────────────────────────▶ the model corrects itself
 model failure ─────▶ Run ends: status failed, Error = model_failure
 context ended ─────▶ Run ends: canceled or deadline_exceeded
 write fails ───────▶ Run ends: failed, store_failure
```

A tool is something the model uses, so its failure is information for the model, not a reason to stop. The model, the store and the caller's own context are the Run's foundations, so a failure there ends it, and the result carries the code. A Run that started and settled is returned together with its error: a caller that returns on `err != nil` would throw a settled Run away.

## How HTTP and the CLI translate

Each surface keeps one table from code to its own form, and a test fails when a code is missing. HTTP is the optional `adapter/http` module and the CLI is `internal/cmd/gotato`.

```text
 RuntimeError ──▶ HTTP   status from the code (one table)
                         header  Gotato-Not-Run: true     when NotRun
                         header  Gotato-Retryable: true   when Retryable
                         header  Gotato-Writer (not_writer only), Retry-After (from retry_after_ms,
                                 rounded up to seconds)
                         body    {code, message, not_run, retryable, writer?, record_epoch?}
              ──▶ CLI    exit code from the code (one table)
                         with --json: {error, exit_code, code, not_run, retryable}
```

The status is a view of the code and decides nothing: whether a request ran and whether to retry are the runtime's words, copied to the headers and body. A load balancer reads the headers and does not need to know a single code. On the CLI, the exit codes belong to the test tool and carry no contract; its JSON repeats the same two facts.

## Foreign text stays on the Cause

An error from outside Gotato may carry a host, a query or a secret, so its text is not passed on.

```text
 foreign error ──▶ PublicText(err, fallback)
   │                   │ implements Public()?   yes ─▶ that text
   │                   │                         no ─▶ the fixed fallback ("model call failed")
   ▼                   ▼
 Cause (logs only)   Message (clients, the model, every subscriber)

 HTTP 5xx, request ran:   Message becomes the status text; detail goes to the server log
 HTTP 5xx, not run:       Message is kept; it is a refusal, written to be read
```

The rule has one door: an error is public only if its maker says so by implementing `Public()`. A tool that wants the model to read why it failed returns such an error, or a `RuntimeError`, whose Message is written to be read and whose code the failed result keeps; any other error becomes `tool_execution_failure: tool failed`. The same holds for a model's error, a store's and a panic, whose stack stays in `Error()` on the Cause. Logs keep the detail, so nothing is lost for those who run the service.

## What this buys you
- A new code is classified once, where it is made, and every surface follows.
- A load balancer retries safely on headers alone; an embedding service gets the same answers in Go.
- A secret in a foreign error cannot leak to a client, the model or a subscriber by accident.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the guideline [a failure stays local](../DESIGNS.md#4-a-failure-stays-local), [FEATURES.md](../FEATURES.md) (F1: Runs, F12.2: Status codes and errors, and "When something fails"), decisions [035](../plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md), [037](../plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md), [038](../plan/decisions/038-a-limit-guards-a-real-failure.md) and [048](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md).
