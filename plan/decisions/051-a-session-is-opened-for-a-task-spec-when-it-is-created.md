# 051. A Session is opened for a TaskSpec when it is created

Decided: 2026-11-03    Status: current

## The problem
[044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md) lets a Run open a Session for a TaskSpec, but only `RunRequest.Task`, which takes no Session ID. Over HTTP every run route acts on a Session that exists, so a TaskSpec cannot be run at all; and a caller who retries a Run that opened a Session cannot tell whether the first one did, so a retry opens a second.

## The choice
Whatever creates a Session may name a TaskSpec. `host.CreateSessionRequest` gains `Task` and `TaskSpec`, as `RunRequest` has them, and both go through one function: resolve the TaskSpec, check it narrows its profile, add the Background once after the instruction, keep the snapshot in the Record. A TaskSpec Session is then run like any other, by its ID. A caller-chosen `ID` makes the create the retry point: a second create of the same ID fails with `already_exists`, as today.

Over HTTP, `POST /v1/sessions` takes `task`, a registered TaskSpec name, beside `agent`, `id` and `metadata`, and excludes `agent`. The body carries a name only, never a TaskSpec value: a definition reaches a service through its code or its store ([044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)), not through a request. `GET /v1/tasks` lists the registered TaskSpec names with the AgentProfile each runs, as `GET /v1/agents` lists the profiles.

## Not chosen
- A route `POST /v1/tasks/{name}/runs`: a second way to create a Session over HTTP, with no ID to retry by.
- A TaskSpec value in the HTTP body: any caller could then shape a Session's bounds and Background; narrowing keeps the cap, not the instruction.
- `RunRequest.Task` removed, so only create opens a TaskSpec Session: a Run with no Session ID already creates one in Go, and the two share one path, so the shortcut costs nothing.
- A TaskSpec set on an existing Session: the Background belongs at the start ([006](006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md)).

## Consequences
A scheduler creates a Session for its TaskSpec with an ID it derives (the job and the day, say), then runs it; a retry of either call is safe. HTTP gains one field and one route, no new run route.

1. `CreateSessionRequest.Task` and `.TaskSpec`, through the function `RunRequest.Task` uses; tests that both paths write the same Record.
2. `task` on `POST /v1/sessions`; `GET /v1/tasks`; the HTTP spec, the guides and the CHANGELOG.
