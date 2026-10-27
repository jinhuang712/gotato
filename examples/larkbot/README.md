# larkbot

A Lark chat bot built on [Gotato](../../README.md), and a local cluster to try it on. It is an example of an application that embeds the runtime. It is not a product, and not a client Gotato ships.

## What it demonstrates

- **Many instances behind a chat platform.** Lark delivers each event to one random instance of the app. A message for a thread can land on an instance that is not running that thread's Run.
- **`not_writer` forwarding.** That instance's `Runner.Send` answers `not_writer` with the writer's address, and the bot posts the message there as a `follow_up`.
- **Ordered retry.** A message that was known not to have run (`not_writer`, `busy`, `draining`, a forward that could not connect) is sent again with backoff, in order per Session.
- **Cards.** Every reply is a Lark interactive card whose footer names the instance that answered.
- **Warm Sessions.** One Gotato Session per message thread. A new message starts a Session; a reply continues the Session of its thread. Any instance can pick a Session up from the shared store.
- **Tools in a multi-instance service.** An MCP server and tool search, each checked across a takeover or a restart.

What it is not: a product. It has no users, no persona beyond one constant, no idempotency across instances, and no real model. Replace what you need.

## How it works

The diagram shows one message through the bot.

```text
Lark ──(long connection, im.message.receive_v1)──► onMessage ──ack at once──► goroutine
                                                                                │
                              runner.CreateSessionExclusive (chat_id, thread in metadata)
                              runner.Send(SessionID, follow_up, text)
                                                                                ▼
Lark ◄── reply to the user's message ◄── replyLoop ◄── runner.SubscribeInstance (reply_ended, run_ended)
```

- The thread key is the event's `root_id` if set, else `parent_id` if set, else the message's own `message_id`. The Session ID is `lark-<thread key>` (a hash when the key has unusual characters). The Session's metadata keeps `lark.chat_id` and `lark.thread`, so any instance can find the chat and the message to reply to.
- The reply is read from the Instance's Event stream, not from `Send`'s return: a message sent while a Run is going is queued into that Run, and the Run's `run_ended` tells the bot to answer.
- A reply posted by an instance that did not see the Lark message (the writer, after a forward) replies to the thread's root message.
- Non-text messages and the bot's own messages are ignored.
- The persona is one constant, `instruction` in `main.go`. Replace it with your own.
- User-visible strings (the busy notice) are constants in `bot.go`.

## Run it locally with no secrets

You need Go. The fake Lark and the `echo` model need no credentials and no network.

```sh
go build -o bin/larkbot . && go build -o bin/fakelark ./cmd/fakelark
export LARK_APP_ID=fake LARK_APP_SECRET=fake
for p in 8080 8081; do
  ./bin/larkbot -model echo -delay 3s -store ./data/sessions -addr 127.0.0.1:$p -instance-label pod-$p \
    -lark-events http -lark-base-url http://127.0.0.1:9000 &
done
./bin/fakelark -addr 127.0.0.1:9000 -bots http://127.0.0.1:8080,http://127.0.0.1:8081 &
curl -s localhost:9000/send -d '{"chat":"oc_x","text":"hi"}'
curl -s 'localhost:9000/cards?chat=oc_x'
```

Without any Lark at all, one prompt through the Runner: `./bin/larkbot -model echo -once "hello"`.

`cmd/fakelark` plays Lark with the standard library only. The bot has two switches for it and no other change: `-lark-base-url` sends the Lark client to the fake, and `-lark-events http` serves `POST /lark/event` on `-addr` instead of opening the long connection. The fake delivers each event to one random bot, like Lark's cluster mode, records every card, and, with `-redeliver` (default on), delivers an event again when it is not acked within 3 s. More ways to drive it:

```sh
# a reply inside a thread: root_id and parent_id are reply_to
curl -s localhost:9000/send -d '{"chat":"oc_x","text":"and then?","reply_to":"om_fake_1"}'
# several new messages at once
curl -s localhost:9000/send -d '{"chat":"oc_x","text":"hi","count":5,"parallel":true}'
# a user thread: first message, wait for the card, then a burst of replies
curl -s localhost:9000/conversation -d '{"chat":"oc_x","turns":["hi","a","b","c"],"gap_ms":50}'
curl -s -X POST localhost:9000/reset
```

## Run it against real Lark

1. Create a custom app in the Lark developer console and enable the **Bot** capability.
2. Permissions: `im:message` (receive) and `im:message:send_as_bot` (send).
3. Events and callbacks: choose **long connection** and add the event `im.message.receive_v1`.
4. Publish a version of the app and add the bot to a chat.
5. `cp .env.example .env`, fill `LARK_APP_ID` and `LARK_APP_SECRET` (`.env` is git-ignored), then `./run.sh`.

`./run.sh` reads `$MODEL` (default `echo`), `$ADDR`, `$STORE`, `$DELAY` and `$LABEL`. For a real model use `-model gateway` with a [gateway](../../gateway) config (`$GOTATO_GATEWAY_CONFIG`, default `gateway.yaml`).

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-store DIR` | `./data/sessions` | Gotato file store |
| `-model` | `echo` | `echo`, `demo`, or `gateway` |
| `-once PROMPT` | | run one prompt through the Runner (in-memory store), print the reply, exit |
| `-retry-budget` | `20s` | how long a message that was not run is retried; `0` turns it off |
| `-addr` | `127.0.0.1:8080` | internal HTTP address: `/healthz`, `/readyz` (503 while draining) and Gotato's HTTP handler, which peers post forwarded messages to |
| `-advertise URL` | `http://<addr>` | the address peers reach this instance at |
| `-instance NAME` | generated | the Gotato instance name in the store |
| `-instance-label` | `$POD_NAME`, else the host name | the name shown in cards and logs |
| `-delay DURATION` | `0` | sleep in every model call, to keep a Run in flight |
| `-lark-base-url URL` | real Lark | the Lark Open API base URL; point it at `fakelark` |
| `-lark-events ws\|http` | `ws` | `ws`: long connection. `http`: `POST /lark/event` on `-addr` |
| `-tools LIST` | none | extra tools: `export`, `mcp`, `mcp-search` (see Tools) |

SIGINT or SIGTERM closes the connection, drains the Runner (running Runs finish and are answered), and closes it.

## Retry

A message is sent again only when it is known not to have run:

- `Runner.Send` answers `not_writer`, `busy`, `draining` or another `NotRun` refusal (`gotato.NotRun`);
- a forward is answered `409` or `503` with `Gotato-Not-Run: true`;
- a forward could not connect (nothing reached the peer).

Each retry calls `Runner.Send` again, so it finds out who the writer is anew: when a killed writer's Liveness row expires, this instance takes the Session over and starts the Run. The wait starts at 250 ms and doubles to 2 s, within `-retry-budget`. A failure after the connection was made may have been accepted, so it is not retried: it is logged as `delivery unknown`, and the user gets the busy notice. The retry runs in the message's own goroutine, so the Lark event is acknowledged at once; a later message of the same Session waits for an earlier one that is retrying, so order is kept.

Duplicates are left to you. Each instance keeps an in-memory set of message IDs and drops a repeat it has seen; a repeat that lands on another instance is handled as new. Marking an ID before the work loses the message if the instance dies in between, and marking it after lets two instances race, so the choice depends on your service. If you want one, keep the `message_id` in your own shared store.

## Tools

`-tools` installs extra tools; the default is none.

| `-tools` | What the Agent gets |
|---|---|
| `export` | test only: a tool whose 1.2 MB result the core spills |
| `mcp` | the MCP server at `-mcp-url` as a ToolSet, always visible, through `adapter/mcp` over streamable HTTP |
| `mcp-search` | the same ToolSet in the catalog: the model loads a tool with `search_tools` |

For tests, `-script` plays `[script:NAME]` directives in a message with a scripted model (`script.go`) that decides from the request alone, so any instance continues a Run the same way. `cmd/fakemcp` is a small MCP server with fixed answers, a `-stateless` mode and a graceful `-drain`.

## Run it on a local cluster

Three bot replicas on a local [kind](https://kind.sigs.k8s.io/) cluster, one shared file store, a fake Lark in front (random delivery and redelivery) and Gotato's fake gateway (`internal/fakegateway`) as the model. Nothing connects to the real Lark or to a real model, and your own kubectl context is not touched: the cluster has its own kubeconfig in `cluster/.state/`, and every command names the context `kind-gotato`.

You need Docker, `kind`, `kubectl` and Go. Any Docker works. From a clean clone of Gotato:

```sh
cd examples/larkbot
cluster/run.sh up       # create the cluster, build 4 images, load, apply, wait ready
cluster/run.sh smoke    # one message, one card, one Transcript
cluster/run.sh s1       # a scenario (below)
cluster/run.sh down     # delete the cluster
```

The image build takes the repository root as its context, because `go.mod` replaces Gotato and its adapters with the local checkout. The manifests are plain kubectl files in `cluster/*.yaml`. Image tags are `dev-<git sha>`, with `-dirty<hash>` for uncommitted changes. `cluster/run.sh status` shows the pods and `cluster/run.sh logs [larkbot|fakelark|fakemcp|fakegateway]` the logs.

### Scenarios

Each scenario runs against a cold cluster and checks the Records afterwards. The driver (`cluster/driver`, standard library only) reaches the bots through `kubectl proxy`, and tags every message so it can be found in the Transcript. A scenario exits 1 only on a `GOTATO-FAULT`.

| Scenario | What it does | What it proves |
|---|---|---|
| `smoke` | one message | one card, one Transcript |
| `s1` | rolling deploy with Runs in flight | no message lost, each once in its Transcript |
| `s2 [delete\|crash]` | kill the pod that writes a Run (deleted, or SIGKILL) | another pod takes the Session over after the Liveness TTL |
| `s3` | scale 3 to 1 to 5 with Runs in flight | pods added by the scale-up take Sessions over |
| `s4` | bursts into one thread from several pods | `not_writer` forwarding keeps order and the Records stay consistent |
| `s5` | `ACK_DELAY=4s cluster/run.sh up` first: bots ack late | measures redelivered events; the bot has no cross-instance idempotency |
| `s7` | MCP calls in flight while the MCP server is killed (SIGKILL), the bot rolls, and the MCP server rolls gracefully (SIGTERM, drain) | tool errors are visible, no Run is stuck; in-flight calls survive a drain, while a stateful MCP server still fails some (`MCP_STATELESS=true` for a stateless server) |
| `s8` | tool search, writer killed | a tool loaded by search is still loaded after a takeover |

How to read the output:

| Line | Meaning |
|---|---|
| `messages sent N, acked by a bot, in a Transcript once, missing, more than once` | per user message: did a pod take the event, and how often does its tag appear in its Session's Transcript |
| `Runs N, reply cards N (extra, missing), busy notices` | cards posted to the fake Lark against Runs in the Records |
| `GOTATO-FAULT` | a gap in the Record's event numbers, a claim that does not account for the holder before it, or a message the bot's `Send` accepted that is not in the Transcript |
| `KNOWN-BOT-GAP` | a loss or duplicate the bot is known to have: a refusal while draining, a failed forward, a message in the memory of a writer that was killed, a duplicate from a redelivery to another pod |
| `UNEXPLAINED` | not classified; read the logs |

## Tests

```sh
gofmt -l . && go vet ./... && go test -race ./... && golangci-lint run ./...
```

No network and no credentials are needed.
