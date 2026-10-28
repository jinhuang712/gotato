# Errors

A caller that gets an error has questions before it reads the code: what happened, did anything run, and may I try again. In a service with many Instances the second one matters most, because it decides whether the same request can go to another Instance without running twice. Gotato answers all three in the error itself, where the error is made, and every surface only translates.

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
│ Cause      the foreign error, for logs only         │
└─────────────────────────────────────────────────────┘
```

`NotRun` and `Retryable` are separate on purpose. A 404 did not run, yet it fails the same everywhere, so it is not worth sending again. A transient model failure did run, so it is retryable but not safe to send to another Instance blindly. The codes whose answer is the same wherever they are made get it by default; any other error gets its answer from the place that knows better. Both functions look through wrapped errors.

## Where the answers come from

The answers are set at the place of the failure, along the path of a request.

```text
 request ─▶ admission ─▶ claim ─▶ Run (Turns) ─▶ write ─▶ reply

 admission  draining, overloaded, invalid_argument, not_found,
            limit_exceeded (the Runner's capacity)         ─ not run
 claim      busy, not_writer, session_closing              ─ not run
 Run        model_failure (Retryable if the Model says so),
            canceled, deadline_exceeded                    ─ it ran
 write      store_failure (Retryable; ran is unknown)      ─ it may have run
```

Admission and claim refuse before the Run starts, so it is marked not run and a load balancer may try another Instance. After the Run begins, a failure may have had effects, so only the cases that are known stay marked. A store failure on a write of a Run's step is not marked not run, because the write may have landed. Watch `limit_exceeded`: from the Runner's capacity it is not run and retryable, while the same code that ends a Run at a limit of its own ran and is neither.

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

Each surface keeps one table from code to its own form, and a test fails when a code is missing.

```text
 RuntimeError ──▶ HTTP   status from the code (one table)
                         header  Gotato-Not-Run: true     when NotRun
                         header  Gotato-Retryable: true   when Retryable
                         header  Gotato-Writer, Retry-After   when Details has them
                         body    {code, message, not_run, retryable, writer?}
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

The rule has one door: an error is public only if its maker says so by implementing `Public()`. A tool that wants the model to read why it failed returns such an error; any other becomes `tool_execution_failure: tool failed`. The same holds for a model's error, a store's and a panic, whose stack stays in `Error()` on the Cause. Logs keep the detail, so nothing is lost for those who run the service.

## What this buys you
- A new code is classified once, where it is made, and every surface follows.
- A load balancer retries safely on headers alone; an embedding service gets the same answers in Go.
- A secret in a foreign error cannot leak to a client, the model or a subscriber by accident.

## See also
[FEATURES.md → G-F01](../FEATURES.md#g-f01--agents-and-runs-) (the errors section and the table of codes), [G-F14](../FEATURES.md#g-f14--http-and-streaming-), decisions [020](../plan/decisions/020-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md), [022](../plan/decisions/022-when-the-hash-missed-the-answer-names-the-writer.md) and [023](../plan/decisions/023-a-limit-guards-a-real-failure.md), [RUNBOOK.md](../RUNBOOK.md), [GLOSSARY.md](../GLOSSARY.md). Related specs: [model.md](model.md) and [multi-instance.md](multi-instance.md).
