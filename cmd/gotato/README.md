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
| `--store DIR` | session store directory; default `$GOTATO_HOME/sessions`, else `~/.gotato/sessions` |
| failures in machine mode | stderr gets the message **and** stdout gets `{"error": "...", "exit_code": N}`; this includes usage errors |
| flags | accepted before or after positional arguments |
| `--help` | any command or subcommand prints usage on stdout and exits 0 |

### Exit codes

| Code | Meaning |
|---|---|
| `0` | success; for `run`, the run completed |
| `1` | runtime error (store, provider, encoding) |
| `2` | usage error (unknown command, missing argument, invalid flag value, invalid model or strategy) |
| `3` | not found (session id, tool id) |
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
| `gateway` | OpenAI-compatible or Codex provider configured by YAML | per YAML |

The model used for a session is remembered in the session (`gotato.model` metadata) and reused by later runs unless `--model` is given.

## Commands

### `gotato run`

```text
gotato run [--session ID] [--model M] [--instruction S] [--panel time,cwd] [--compact-ceiling N] [--json | --events jsonl] [--continue] [--no-save] "prompt" | -
```

Creates a session when `--session` is omitted. `-` reads the prompt from stdin. The model sees the whole session history (append-only). `--panel` appends a dynamic `<panel>` with the listed items to the tail of each request; `--compact-ceiling N` compacts the session automatically at the start of a run when its estimated tokens exceed N. Both are remembered in the session. `--continue` resumes the loop without a new prompt (valid only when the history ends in a user or tool-result message). `--no-save` runs without writing the run's messages to the store: with no `--session` nothing is persisted at all, and with an existing session the history stays as it was (settings given alongside are still stored).

`--json` outcome:

```json
{
  "session_id": "…", "run_id": "…", "status": "completed",
  "agent": "demo", "model": "demo", "compacted": false,
  "final_text": "…", "final_message": { … },
  "usage": {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
  "metrics": {"elapsed_ms": 0, "turns": 2, "tool_calls": 1, "text_bytes": 23, "reasoning_bytes": 0},
  "error": {"code": "…", "message": "…"},
  "messages": 4, "events": 18
}
```

`--events jsonl` streams every runtime event as one JSON line while the run executes (`agent_start`, `context_built`, `message_*`, `tool_execution_*`, `turn_end`, `agent_end`, …) and ends with one line `{"kind":"run_result","result":{…outcome…}}`.

Human mode prints the final text on stdout and a one-line status on stderr.

### `gotato session`

| Command | Output (`--json`) |
|---|---|
| `session create [--id ID] [--meta k=v,…] [--instruction S] [--panel time,cwd] [--compact-ceiling N]` | summary `{id, created_at, updated_at, messages, runs, usage, metadata}` |
| `session list` | array of summaries, newest first (`--jsonl`: one per line) |
| `session show <id>` | the full session document: `schema_version, id, parent_id, created_at, updated_at, messages[], runs[], events[], usage, compactions[], metadata` |
| `session fork <id> [--id ID]` | summary of the new session; `parent_id` names the source; an `--id` that already exists is rejected (exit 2) |
| `session events <id>` | same as `events --session <id>` |
| `session resume <id> "prompt"` | same as `run --session <id> "prompt"` |
| `session delete <id>` | `{"id": "…", "deleted": true}` |

### `gotato context`

| Command | Output (`--json`) |
|---|---|
| `context inspect <id> [--panel ITEMS] [--instruction S]` | report: `session_id, strategy, source_messages, selected_messages, approx_tokens, system_bytes, panel_bytes, prefix_hash, metadata, compactions[], context{…}, request{system_instructions, messages[], tools[], cache_breakpoints[]}`; `--panel`/`--instruction` override the session's stored settings for this inspection only and are not persisted |
| `context build <id> [--panel ITEMS] [--instruction S]` | the exact `ModelRequest` the agent would send now (same overrides, not persisted) |
| `context compact <id> [--keep N] [--summarizer truncate\|model] [--model M]` | `{session_id, replaced, messages_before, messages_after, tokens_before, tokens_after, compaction?}`; `compaction` is present only when `replaced` is true |

The request is laid out for prompt caching: system prompt, sorted tools, the append-only history, then the tail message carrying the `<panel>`. `prefix_hash` covers everything but the tail; two inspections (or two consecutive turns) with the same hash present an identical cacheable prefix to the provider. `cache_breakpoints` are provider-neutral hints (`after: system | tools | message`).

Compaction permanently replaces the messages before the last `--keep` (aligned to a user message so tool calls stay with their results) by one summary message tagged `metadata.compaction = "summary"`, records the compaction in the session, and stores a `session_compacted` event. `truncate` needs no model.

### `gotato tools`

| Command | Output (`--json`) |
|---|---|
| `tools list [--session ID] [--model M]` | `{session_id?, agent?, tools:[{id, name, description, input_schema, sequential, active}]}` |
| `tools describe <id> [--session ID]` | one tool entry |
| `tools active [--session ID]` | active tools only |
| `tools activate <id> --session ID` | updated entry; stored as session metadata `gotato.tool.<id>` |
| `tools deactivate <id> --session ID` | updated entry |

Tool activation is session state: `run --session ID` honors it. Without `--session`, `list`/`describe`/`active` describe the default surface; `activate`/`deactivate` require `--session` (exit 2).

Builtin tools: `demo.echo` (returns its `value`), `time.now` (RFC 3339 UTC).

### `gotato events`

```text
gotato events --session <id> [--kind KIND] [--json]
```

Default output is JSON Lines, one runtime `Event` per line, in production order:

```json
{"agent_id":"…","run_id":"…","sequence":7,"kind":"context_built","event_class":"protected","turn":1,"payload":{"strategy":"full_history","messages":1,"source_messages":1,…},"timestamp":"…"}
```

`--json` prints one array instead. Event payload keys per kind are documented in `events.go` of the root package.

### `gotato serve`

```text
gotato serve [--addr HOST:PORT] [--max-runs N] [--queue reject|wait] [--drain-timeout D] [--model M] [--gateway-config PATH]
```

Serves the same runner over HTTP (`service/httpapi`, contract `2`). Sessions live in the same store the CLI uses, so `gotato run --session ID` and `POST /v1/sessions/ID/runs` continue the same history. Routes:

```text
GET    /healthz  /readyz  /v1/agents
POST   /v1/sessions            GET /v1/sessions            GET|DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/fork  GET /v1/sessions/{id}/events?kind=   GET /v1/sessions/{id}/context
POST   /v1/sessions/{id}/compact {"keep":N}
POST   /v1/sessions/{id}/runs {"prompt"|"continue":true,"timeout_ms"?}     → run result
POST   /v1/sessions/{id}/runs/stream                                          → SSE: event: <kind> …, event: result
POST   /v1/sessions/{id}/cancel   POST /v1/runs/{run_id}/cancel
POST   /v1/runs {"prompt","agent"?,"metadata"?}   POST /v1/runs/stream        → create session and run
```

Errors are `{"error","code","message"}` with 400 (invalid argument), 404 (unknown session), 409 (session busy / run not active), 429 (capacity), 500 (a settled run could not be saved), 504 (request deadline). A Run that settles as `failed`, `cancelled`, or `deadline_exceeded` still returns HTTP 200 with the outcome in the body (`result.status`); only request-level failures use those status codes. On the streaming routes every failure, including a bad request, is delivered in-band as `event: error` after the stream has started. `GET /readyz` returns 503 while draining so a rolling deployment can stop sending traffic. Authentication and logging are the embedding application's: wrap the handler. On SIGINT/SIGTERM the server stops admitting, waits `--drain-timeout` for active runs, then cancels them (including runs queued behind a busy session).

### `gotato doctor`

```text
gotato doctor [--json] [--gateway-config PATH]
```

```json
{"ok": true, "version": "1", "go": "go1.26.1",
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
gotato context compact "$id" --keep 2 --json | jq .messages_after           # 3
gotato events --session "$id" | jq -r .kind | sort | uniq -c
gotato tools deactivate time.now --session "$id" --json | jq .tool.active   # false
gotato session fork "$id" --json | jq .parent_id
```

## Compatibility

Command names, flag names, JSON field names, JSONL shapes, and exit codes are part of the runtime contract. Fields may be added; renames and removals require a `MIGRATION.md` entry.
