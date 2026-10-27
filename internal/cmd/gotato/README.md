# `gotato`, the maintainers' test tool

`gotato` is a small command-line tool for the maintainers of this repository. It drives a `service.Runner` in-process, the same object `gotato serve` exposes over HTTP, so it exercises the real behavior of the packages by hand and in tests. It is not shipped and has no contract: its commands, flags, JSON fields and exit codes change freely. It carries no runtime logic of its own; a behavior it needs lives in a package first.

Build and run it from the repository root:

```bash
go run ./internal/cmd/gotato doctor --json
go build -o "$(mktemp -d)/gotato" ./internal/cmd/gotato     # or build it outside the tree
```

## Conventions

- stdout holds the requested data, stderr the diagnostics.
- `--json` prints one JSON document; `--jsonl` prints one JSON object per line.
- In machine mode a failure also writes `{"error", "exit_code", "code", "not_run", "retryable"}` on stdout.
- Exit codes today: `0` ok, `1` runtime error, `2` usage error, `3` not found, `4` the run did not complete.
- `--store DIR` sets the session store; the default is `$GOTATO_HOME/sessions`, else `~/.gotato/sessions`.
- `GOTATO_MODEL` and `GOTATO_GATEWAY_CONFIG` set the defaults of `--model` and `--gateway-config`.
- `--help` on any command prints its usage.

## Models

| `--model` | Behavior |
|---|---|
| `echo` (default) | answers `echo: <prompt>`; no credentials |
| `demo` | prompt `use-tool` calls `demo.echo` then answers; `use-big-tool` returns a large result; no credentials |
| `gateway` | a provider configured by the gateway YAML file |

## Commands

Run `gotato <command> --help` for the flags.

| Command | What it does |
|---|---|
| `run [--session ID] "prompt"` | runs a prompt on a new or existing Session; `--events jsonl` streams its Events |
| `session create\|list\|show\|fork\|delete` | manage Sessions |
| `session events\|resume\|message\|notice\|cancel <id>` | read the log, continue a Session, send it a message or a notice, stop its Run |
| `context inspect\|build\|compact <id>` | see what the model would receive, and compact the Session |
| `tools list\|describe\|active\|activate\|deactivate` | list and switch the tools of a Session |
| `events --session <id> [--kind K]` | the Session's Events, one per line |
| `serve [--addr HOST:PORT]` | serves the Runner over HTTP (`service/httpapi`) |
| `doctor` | checks the store, the models, the gateway configuration and the tools |

## A smoke run

```bash
id=$(gotato session create --json | jq -r .id)
gotato run --session "$id" --model demo --json "use-tool" | jq .status
gotato context inspect "$id" --json | jq '{approx_tokens, prefix_hash}'
gotato context compact "$id" --keep 2 --json | jq .messages_after
gotato events --session "$id" | jq -r .kind | sort | uniq -c
```
