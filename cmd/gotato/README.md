# `gotato` CLI Contract

The `gotato` command is the official runtime interface for humans, shell automation, and coding agents. It drives a `service.Runner` in-process: the same object `gotato serve` exposes over HTTP and `gotato-grpc` over gRPC. Nothing below is CLI-only behavior.

```bash
go build -o bin/gotato ./cmd/gotato
```

## Conventions

| Rule | Behavior |
|---|---|
| stdout | requested data only |
| stderr | diagnostics, warnings, human status lines |
| `--json` | one pretty-printed JSON document on stdout |
| `--jsonl` | one JSON object per line on stdout |
| `--quiet` | suppress human status lines that are not errors |
| `--no-color` | accepted; output never contains ANSI color |
| `--timeout D` | for `run`: the run's deadline (`30s`, `2m`); it settles as `deadline_exceeded` and exits 4. For `serve`: a lifetime bound (`30s`): the server drains and shuts down when it elapses. For other commands: the command deadline |
| `--store DIR` | session store directory; default `$GOTATO_HOME/sessions`, else `~/.gotato/sessions`. Each session is a directory with one file per write, `<id>/<position>.json`; a session file of the earlier format, `<id>.json`, is read as it is and moved into its directory by its first write, so an older `gotato` no longer finds it |
| failures in machine mode | stderr gets the message **and** stdout gets `{"error": "...", "exit_code": N, "code": "...", "not_run": false, "retryable": false}`; this includes usage errors. `code` is the runtime's error code and is absent when the failure has none. `not_run: true` means nothing with side effects happened, so the same command may run again elsewhere; `retryable: true` means the same command, run again later, may succeed. The runtime decides both; the CLI only prints them |
| flags | accepted before or after positional arguments |
| `--help` | any command or subcommand prints usage on stdout and exits 0 |

### Exit codes

| Code | Meaning |
|---|---|
| `0` | success; for `run`, the run completed |
| `1` | runtime error (store, provider, encoding), also a run that failed because its reply or its end could not be written, so the session does not hold it (the JSON outcome is still printed, with `status: "failed"` and the `store_failure` error) |
| `2` | usage error (unknown command, missing argument, invalid flag value, invalid model or strategy) |
| `3` | not found (session id, tool id): the error code `not_found` |
| `4` | the run did not complete: `failed`, `cancelled`, or `deadline_exceeded` (the JSON outcome is still printed) |

### Environment

| Variable | Effect |
|---|---|
| `GOTATO_HOME` | base directory; sessions live in `$GOTATO_HOME/sessions` |
| `GOTATO_MODEL` | default `--model` |
| `GOTATO_GATEWAY_CONFIG` | default `--gateway-config` (else `gateway.yaml`) |

## Models

| `--model` | Behavior | Credentials |
|---|---|---|
| `echo` (default) | answers `echo: <prompt>` | none |
| `demo` | when the prompt is `use-tool`, calls `demo.echo` then answers `demo response: use-tool`; otherwise `demo response: <prompt>` | none |
| `gateway` | OpenAI-compatible Chat Completions or Responses provider configured by YAML | API key per YAML |

The model used for a session is remembered in the session (`gotato.model` metadata) and reused by later runs unless `--model` is given.

## Commands

### `gotato run`

```text
gotato run [--session ID] [--model M] [--instruction S] [--compact-ceiling N] [--json | --events jsonl] [--continue] [--no-save] "prompt" | -
```

Creates a session when `--session` is omitted. `-` reads the prompt from stdin. The model sees the whole session history (append-only). `--compact-ceiling N` compacts the session automatically at the start of a run when its estimated tokens exceed N, and is remembered in the session. `--continue` resumes the loop without a new prompt (valid only when the history ends in a user or tool-result message). `--no-save` runs without writing to the store: with no `--session` nothing is persisted at all, and with an existing session the history and its settings stay as they were (`--instruction` and `--compact-ceiling` apply to that run only). Without `--no-save`, settings given with `--session` are the run's first write. Only one process runs a session at a time: a run on a session that another `gotato` process is running waits up to 5 seconds for it, then exits `1` with `busy`. Each process keeps a liveness row under `<store>/.instances/` while it writes, and ends it when it exits, so the next process takes the session over at once; a process that was killed holds its sessions until its row expires, about 10 seconds later.

`--json` outcome:

```json
{
  "session_id": "…", "run_id": "…", "status": "completed",
  "agent": "demo", "model": "demo", "compacted": false,
  "final_text": "…", "final_message": { … },
  "usage": {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
  "metrics": {"elapsed_ms": 0, "turns": 2, "tool_calls": 1, "text_bytes": 23, "reasoning_bytes": 0},
  "error": {"code": "…", "message": "…"},
  "messages": 4, "events": 18,
  "record_epoch": "…"
}
```

`--events jsonl` streams the Run's Events as one JSON line each while the run executes, and ends with one line `{"kind":"run_result","result":{…outcome…}}`. A run on a new session starts with `session_created` and `session_claimed`; every run then goes through `run_started`, `message_committed`, `turn_started`, `context_built`, `reply_started`, `text_delta`, `reply_ended`, `tool_started`, `tool_ended`, `tool_result_committed`, `turn_ended` and `run_ended`. Each line is one flat object whose `kind` names the Event (see `events.go` in the root package). Stored Events carry `number`, their position in the session's Record; deltas (`text_delta`, `reasoning_delta`, `tool_progress`) carry the number of the Event they follow. Instance Events such as `session_loaded` and `session_saved` are not in this stream.

Human mode prints the final text on stdout and a one-line status on stderr.

### `gotato session`

| Command | Output (`--json`) |
|---|---|
| `session create [--id ID] [--meta k=v,…] [--instruction S] [--compact-ceiling N]` | summary `{id, created_at, updated_at, messages, runs, usage, metadata}` |
| `session list` | array of summaries, newest first, each with its `record_epoch` (`--jsonl`: one per line) |
| `session show <id>` | the full session document: `schema_version, id, parent_id, created_at, updated_at, messages[], runs[], events[] (the last 1000 stored Events, each a flat object with `kind`), usage, compactions[], metadata` |
| `session fork <id> [--id ID]` | summary of the new session; `parent_id` names the source; an `--id` that already exists is rejected (exit 2) |
| `session events <id>` | same as `events --session <id>` |
| `session resume <id> "prompt"` | same as `run --session <id> "prompt"` |
| `session delete <id>` | `{"id": "…", "deleted": true}` |

### `gotato context`

| Command | Output (`--json`) |
|---|---|
| `context inspect <id> [--instruction S]` | report: `session_id, strategy, source_messages, selected_messages, approx_tokens, system_bytes, prefix_hash, compaction_pending, compact_ceiling?, metadata, compactions[], context{…}, request{system_instructions, messages[], tools[], options?, cache_breakpoints[], cache_key?}`; `compaction_pending` is true when the next run compacts the session first, past `compact_ceiling`; `--instruction` overrides the session's stored instruction for this inspection only and is not persisted |
| `context build <id> [--instruction S]` | the exact `ModelRequest` the agent would send now (same overrides, not persisted) |
| `context compact <id> [--keep N] [--min-tokens N] [--recap none\|truncate\|model] [--recap-max-tokens N] [--model M]` | `{session_id, replaced, reason?, messages_before, messages_after, tokens_before, tokens_after, compaction?}`; `compaction` (`at, replaced_messages, from_message_id, to_message_id, recap, bytes_before, bytes_after, segment, tokens_before, tokens_after, ceiling?, tool_sets?`) is present only when `replaced` is true, and `reason` (`below_min_tokens` or `nothing_to_close`) only when it is false |

The agent builds the report with the code its turns run, so the request includes its extensions, its tools and its model options, and `prefix_hash` equals the one the next turn's `context_built` event reports. The request is laid out for prompt caching: system prompt, sorted tools, then the append-only history. `prefix_hash` covers everything but the last message; two inspections (or two consecutive turns) with the same hash present an identical cacheable prefix to the provider. `cache_breakpoints` are provider-neutral hints (`after: system | tools | message`).

Compaction closes everything since the last checkpoint, less a kept tail, into a segment that code builds: the user's messages and each run's final reply word for word (a message past 2K tokens is cut, and a line says where), one line per tool call (`10:02 payments.get_batch {…} → ok`), and a recap when `--recap` asks for one. Tool results, reasoning and a run's other text are left out. Without `--keep`, the tail is the latest run's turns, up to 20K tokens; `--keep N` keeps the last N messages instead, and the cut always keeps a tool call with its result. `--min-tokens N` skips a stretch since the last checkpoint estimated below N tokens (`replaced: false`, `reason: below_min_tokens`), so a timer can call it for every idle session. `--recap` defaults to `none`; `truncate` needs no model. `--recap model` has the session's agent write the recap: its request is the context the next model call would send plus one plain ask, so the provider reads the cached prefix, and the reply is kept only if it starts with one of the five `## ` headings, has `## What happened`, uses no other `## ` heading, calls no tool and is not cut off at the output limit (`--recap-max-tokens`, default 8192). A reply that fails leaves the segment without a recap (`compaction.recap` is `none`; `compaction.recap_usage` still shows the call). Run the payments check on your own gateway first: `gotato-workload -recap-check -live -gateway-config gateway.yaml -min-share 0.8`. The model's recap is never the default. The command stores `compaction_started`, `segment_closed` and `compaction_ended` events: the closed messages stay in the store, and a load starts after them. A session whose last checkpoint holds one summary from before segments exits `1` with `invalid_state`.

### `gotato tools`

| Command | Output (`--json`) |
|---|---|
| `tools list [--session ID] [--model M]` | `{session_id?, agent?, max_effect?, tools:[{id, name, description, input_schema, sequential, effect, active}]}` |
| `tools describe <id> [--session ID]` | one tool entry |
| `tools active [--session ID]` | active tools only |
| `tools activate <id> --session ID` | updated entry; stored as session metadata `gotato.tool.<id>` |
| `tools deactivate <id> --session ID` | updated entry |

Tool activation is session state: `run --session ID` honors it. Without `--session`, `list`/`describe`/`active` describe the default surface; `activate`/`deactivate` require `--session` (exit 2).

`max_effect` is the agent's Effect cap, present only when the agent has one. A tool above the cap is not listed, `describe` reports it as not found (exit 3), and `activate` cannot switch it on. Without `--json`, `list` prints `max effect: <cap>` as its first line when the agent has a cap.

Builtin tools: `demo.echo` (returns its `value`), `time.now` (RFC 3339 UTC).

### `gotato events`

```text
gotato events --session <id> [--kind KIND] [--json]
```

Default output is JSON Lines, one stored `Event` per line, in the order of the Record. The Events are the session's Record: its messages, its runs, its claims, its compactions. Each is one flat object:

```json
{"kind":"context_built","time":"…","session_id":"…","number":7,"run_id":"…","turn":1,"strategy":"full_history","messages":1,"source_messages":1,"selected_messages":1,"prefix_hash":"…","prefix_messages":0,"system_bytes":0,"tools":1}
```

`--json` prints one array instead. The fields of each kind are the fields of its type in `events.go` of the root package. The command reads every Event of the Record; deltas and Instance Events are not stored.

### `gotato serve`

```text
gotato serve [--addr HOST:PORT] [--max-runs N] [--max-sessions N] [--queue reject|wait] [--drain-timeout D] [--model M] [--gateway-config PATH]
```

Serves the same runner over HTTP (`service/httpapi`, contract `4`). Sessions live in the same store the CLI uses, so `gotato run --session ID` and `POST /v1/sessions/ID/runs` continue the same history. Routes:

```text
GET    /healthz  /readyz  /v1/agents
POST   /v1/sessions            GET /v1/sessions            GET|DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/fork  GET /v1/sessions/{id}/events?kind=   GET /v1/sessions/{id}/context
GET    /v1/sessions/{id}/stream?after=N&record_epoch=E   → SSE: event: stream, one event per stored Event (id: its number), event: reconnect at a drain
POST   /v1/sessions/{id}/compact {"keep":N?,"min_tokens":N?}
POST   /v1/sessions/{id}/runs {"prompt"|"continue":true,"timeout_ms"?}     → run result
POST   /v1/sessions/{id}/runs/stream                                          → SSE: event: <kind> …, event: result[, event: reconnect]
POST   /v1/sessions/{id}/cancel   POST /v1/runs/{run_id}/cancel
POST   /v1/runs {"prompt","agent"?,"metadata"?}   POST /v1/runs/stream        → create session and run
```

`--max-sessions N` is the hard limit on the sessions this process has active (0, the default, has none). Past it, a run that would activate another session is refused before anything is loaded or created, with `503`, the header `Gotato-Not-Run: true` and `"not_run": true` in the body, and nothing is written, so a load balancer may send it to another instance once. It is off by default, because a single process has nowhere else to send the run. `--max-runs` bounds runs, with `429`, and counts runs on sessions already active too.

A load balancer hashes on the `session-id` header. The server checks it against the session ID in the path or the body (`400` when they differ), and answers `POST /v1/sessions`, `POST /v1/runs` and a fork with the new session's ID in a `session-id` header, to send next time. `GET /v1/sessions/{id}/stream` sends the session's stored events after `after` (or the `Last-Event-ID` header a browser's `EventSource` sends by itself), then the live ones; a `record_epoch` that is not the session's, or an `after` past its end, answers `409` with code `cannot_resume`. A stream follows the session's record it opened: if the session is deleted, or deleted and created again under the same ID, it sends none of the new record's events and ends with `event: error`: code `not_found` for a session that is gone, and code `cannot_resume` for one created again. Every stored event on that stream and on the run streams has its number as the SSE `id:`.

Errors are `{"error","code","message","not_run","retryable"}` (the last two when true) with 400 (invalid argument), 404 (unknown session, `not_found`, marked not run), 409 (session busy / run not active / stream cannot resume), 429 (capacity, marked not run), 500 (a settled run could not be saved), 503 (draining or past `--max-sessions`, marked not run; or the store failed), 504 (request deadline). `Gotato-Not-Run: true` is set whenever the runtime says the request did not run, on any status. A Run that settles as `failed`, `cancelled`, or `deadline_exceeded` still returns HTTP 200 with the outcome in the body (`result.status`); only request-level failures use those status codes. On the streaming routes a bad request and a refusal at admission (draining 503 and past `--max-sessions` 503, both marked not run; capacity 429, marked not run) get the status `/v1/runs` gives, before the stream starts; every failure after admission is delivered in-band as `event: error`. `GET /readyz` returns 503 while draining so a rolling deployment can stop sending traffic. Authentication and logging are the embedding application's: wrap the handler. On SIGINT/SIGTERM the server drains: it marks its Liveness row draining, `/readyz` answers 503, it admits no new run, and it waits `--drain-timeout` for active runs to finish and save. Runs still going then are stopped (including runs queued behind a busy session), saved and released, and each stopped stream ends with `event: result` and then `event: reconnect`, whose `retry_after_ms` says how long to wait before continuing the session. The server then removes its Liveness row and exits 0; stderr says `draining (up to …)` and `stopped`.

### `gotato doctor`

```text
gotato doctor [--json] [--gateway-config PATH]
```

```json
{"ok": true, "version": "2", "go": "go1.26.1",
 "checks": [
   {"name": "store", "ok": true, "detail": "/…/sessions (3 sessions)"},
   {"name": "model.echo", "ok": true, "detail": "deterministic, no credentials"},
   {"name": "model.demo", "ok": true, "detail": "deterministic tool loop, no credentials"},
   {"name": "model.gateway", "ok": false, "warning": true, "detail": "gateway.yaml not found; --model gateway unavailable"},
   {"name": "tools", "ok": true, "detail": "demo.echo, time.now"}
 ]}
```

`ok` is false (exit 1) only when a non-warning check fails.

## Scenario

```bash
id=$(gotato session create --json | jq -r .id)
gotato run --session "$id" --model demo --json "use-tool" | jq .status      # "completed"
gotato run --session "$id" --json "and again" | jq .messages                # 6
gotato context inspect "$id" --json | jq '{approx_tokens, prefix_hash}'
gotato context compact "$id" --keep 2 --json | jq .messages_after           # 4
gotato events --session "$id" | jq -r .kind | sort | uniq -c
gotato tools deactivate time.now --session "$id" --json | jq .tool.active   # false
gotato session fork "$id" --json | jq .parent_id
```

## Compatibility

Command names, flag names, JSON field names, JSONL shapes, and exit codes are part of the runtime contract. Fields may be added; renames and removals require a breaking-change entry in `CHANGELOG.md`.
