# Tell an error apart

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Tell an error apart

By the end of this page a client or a load balancer decides from two headers whether to send a request again, and where. You need [HTTP in five minutes](../tutorials/http-in-five-minutes.md).

## Read the two headers

Every error body is `{"code", "message", "not_run"?, "retryable"?}`, and two headers repeat the runtime's answer.

| Header | Meaning |
|---|---|
| `Gotato-Not-Run: true` | Nothing happened. The request is safe to send again, here or elsewhere. It does not say that sending it again helps: a 404 is not run and fails the same everywhere. |
| `Gotato-Retryable: true` | Sending the same request again later may succeed. |

A client or load balancer sends a request to another Instance when both headers are set. For `not_writer` it goes to the `Gotato-Writer` address. Here is a refusal while the Instance drains:

```text
POST /v1/sessions/ID/runs  {"prompt":"again"}
503  Gotato-Not-Run: true   Gotato-Retryable: true
{"code":"draining","message":"draining: host: draining","not_run":true,"retryable":true}
```

[RUNBOOK.md](../../RUNBOOK.md) has the table of every code with its status.

## See also

- [Every error code and its status](../../RUNBOOK.md)
- [How errors are classified](../../specs/errors.md)
