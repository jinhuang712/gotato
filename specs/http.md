# HTTP API

A bot, a job or another service should reach a Session like any other API, with no Go code of its own. The HTTP API is a protocol adapter over the host: every route maps onto one host call, the host's errors become statuses and headers, and Events become server-sent events. It adds no Agent semantics, and it is a Go module of its own, so a service that does not serve HTTP carries none of it. It is the `adapter/http/` part of the [map](ARCHITECTURE.md).

## One handler over one host

The handler is a standard `http.Handler`. A service mounts it on its own mux, behind its own middleware, which owns authentication, rate limits and logging.

```text
 client ─▶ your middleware ─▶ handler ─▶ one host call ─▶ host ─▶ store
                                  │
                                  ├─ body: JSON, unknown fields refused, 1 MiB at most
                                  ├─ an error: status + body + Gotato-Not-Run / Gotato-Retryable
                                  └─ a stream: Events as server-sent events
```

A request names its Session in the path. The `session-id` header, when present and not blank, must name the same Session, so a load balancer that hashes on the header and a handler that reads the path never disagree; a create carries no header and answers with the new ID in it. The routes are:

| Route | Host call |
|---|---|
| `GET /healthz`, `GET /readyz` | alive with the contract version; ready, or 503 while draining or lapsed, with the counts of Runs and Sessions |
| `GET /v1/agents`, `GET /v1/tasks` | the registered profiles with their Effect caps; the registered tasks with the profile each runs |
| `POST /v1/sessions` | create, with `agent` or `task`, `id` and `metadata` |
| `GET /v1/sessions` | a page, by `limit` and `cursor` |
| `GET`, `DELETE /v1/sessions/{id}` | read the Session's document; delete it |
| `POST …/fork` | fork |
| `POST …/runs`, `POST …/runs/stream` | a Run, answered when it settles, or streamed |
| `POST …/messages`, `POST …/notices`, `POST …/abort` | a Steer, Follow up or Interrupt; a notice; a Stop |
| `POST …/compact`, `GET …/context` | a compaction on demand; the report of the next request |
| `GET …/events`, `GET …/stream` | the stored Events as JSON lines; the Session's stream |

A run request carries a prompt or `continue`, may set a timeout and may name an Agent, which binds the Session to it from then on and repairs a Session whose stored Agent is no longer registered. Who may choose the Agent is the middleware's to decide.

## From an error to a status

The host's error already says what happened, whether it ran and whether a retry may pass (`errors.md`). The handler reads the code to pick the status and copies the two answers into headers, never the other way round: a status alone cannot say whether a request ran.

```text
 host error ── code ─────────────▶ status          one table, StatusFor
            ── not run ──────────▶ Gotato-Not-Run: true
            ── retryable ────────▶ Gotato-Retryable: true
            ── body ─────────────▶ {"code", "message", "not_run", "retryable"}
```

A Run that settles failed, canceled or past its deadline is not a failed request: it answers 200 with the outcome in the body. Only problems with the request itself use other statuses, such as 404 for a Session that does not exist, 409 for `busy` or `not_writer`, 429 at a limit and 503 for `draining` or `overloaded`. A client or a load balancer sends a request to another instance only when both headers are set; `not_writer` goes to the address in `Gotato-Writer` instead. A 404 or a 400 is not run too, and fails the same on every instance, which is why not run alone is not a reason to resend. The message is the host's, which is written to be read; a provider's words stay on the error's cause.

## A Run lives as long as its request

A client that starts a Run holds the request open until the Run settles, and dropping it ends the Run as `canceled`. A client cannot start a Run and walk away.

```text
 POST …/runs/stream ──────────────────────────────────────────────▶ the Run's lifetime
   200 once the Run is starting
   event: <kind> … event: <kind> … event: result                     one flat JSON object each
                                    or event: error                  a failure after the start
                                    then event: reconnect + retry:   when a drain stopped the Run

 GET …/stream (watch only) ─── may drop and resume at any time, never touches a Run
```

A request refused before the Run holds its Session, such as `busy`, an unknown Agent, a drain or a limit, gets its status and headers on both run routes, and no stream starts. Once the stream starts, a failure arrives as an error event. A message with no Run going starts one, and its response is held until that Run settles, like a run request. The handler reads a subscription to stream, so a slow client never slows the Run; an idle stream sends a comment every 15 seconds.

## Watching a Session, and resuming

The Session's stream sends the stored Events after a number, from the Record, then the live ones across Runs, on any instance. Its first frame carries the Record's identity, its epoch, and so does the `Record-Epoch` header.

```text
 GET …/stream?after=40&record_epoch=E1          or   Last-Event-ID: 40
   epoch is E1 and 40 ≤ head  ─▶  stored 41 … head, then live
   epoch is not E1, or 40 > head ─▶ 409 cannot_resume, with the current epoch and head
```

Each stored Event's number is its position in the Record and its SSE `id`, so a browser resumes on its own. Partial text is sent live and never stored, so it has no `id`, and its number repeats that of the stored Event it follows, to say where it falls; a client replaces its partial text with the stored reply when it arrives. Partial text is sent only on the writer, so a stream on another instance sees stored Events only, at its next look in the store. A drain ends a stream with a reconnect event and a wait spread between half a second and a second and a half, so clients do not return at once; any other end is an error with no reconnect.

## Forwarding to the writer

A Steer, Follow up, Interrupt, notice or Stop takes effect in the writer's process. One that reaches another instance is answered 409 `not_writer`, not run, with the writer's address. When the service turns forwarding on, the handler sends it there instead, once.

```text
 client ─▶ B (not the writer) ─ allow(A's address)? ─ no ──▶ 409 not_writer
                                     │ yes
                                     └─▶ A, same request + Gotato-Forwarded: true ─▶ A's answer, relayed
                                          A forwarded request is never forwarded again
```

The address comes from the store, and anyone who can write that row can name it, so a forward goes only to an address the service's rule accepts, and a rule that accepts nothing forwards nowhere. The forward carries the caller's headers, so the service's own authentication applies at the writer. The client never follows a redirect, and has a 30-second timeout by default, which also bounds a forwarded message that starts a Run. A forward that fails answers 502, since the message may have reached the writer.

## What this buys you
- Any caller that speaks HTTP reaches a Session, and the service's middleware decides who may.
- A client tells a safe retry from a risky one by two headers, whatever the status.
- A dropped stream resumes on any instance, by one number.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the principles [stay inside the boundary](../DESIGNS.md#6-stay-inside-the-boundary) and [no instance is special](../DESIGNS.md#8-no-instance-is-special), [FEATURES.md](../FEATURES.md) (F12: HTTP API), decisions [025](../plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md), [030](../plan/decisions/030-every-entry-is-an-event.md), [035](../plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md) and [037](../plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md).
