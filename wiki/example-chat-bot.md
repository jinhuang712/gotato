# Example: a chat bot on several pods

By the end of this page you have run the chat bot in [`examples/larkbot`](../examples/larkbot/README.md) on your machine, with two instances, a fake chat platform and no secrets, and you know where to look for each Gotato behavior it uses. You need Go and the root module checked out (see [Install](install.md)). The cluster step also needs Docker, `kind` and `kubectl`.

## What the bot shows

A chat platform delivers each event to one random instance. A message for a thread can land on an instance that does not run that thread's Run, so the bot does what [Embed Gotato in a service](embed-in-a-service.md) says a caller that cannot route by Session does:

- one Session per message thread, kept in a store every instance shares;
- a `not_writer` answer forwards the message to the writer's address;
- a message that was known not to have run is sent again with backoff, in order per Session;
- each reply is a card that names the instance that answered.

## 1. Run two instances

The fake Lark delivers events to the bots at random and records the cards they post. The `echo` model needs no key.

```sh
cd examples/larkbot
go build -o bin/larkbot . && go build -o bin/fakelark ./cmd/fakelark
export LARK_APP_ID=fake LARK_APP_SECRET=fake
for p in 8080 8081; do
  ./bin/larkbot -model echo -delay 3s -store ./data/sessions -addr 127.0.0.1:$p -instance-label pod-$p \
    -lark-events http -lark-base-url http://127.0.0.1:9000 &
done
./bin/fakelark -addr 127.0.0.1:9000 -bots http://127.0.0.1:8080,http://127.0.0.1:8081 &
```

## 2. Send a message and a reply

The first call starts a Session. A reply in the same thread continues that Session, whichever instance receives it.

```sh
curl -s localhost:9000/send -d '{"chat":"oc_x","text":"hi"}'
curl -s 'localhost:9000/cards?chat=oc_x'
```

Each card has a footer with the Session and the instance. The reply below goes into the thread of the first message (`om_fake_1`, the `message_id` of the first answer). Send it three times inside the 3 s delay, and the bots log `send not_writer, forwarding` when one lands on the instance that does not run the Run.

```sh
curl -s localhost:9000/send -d '{"chat":"oc_x","text":"more","reply_to":"om_fake_1"}'
```

## A streaming card

To update one card while the model writes, run the Run with `runner.StreamRun` (or follow the Session with `runner.StreamSession`) and read `text_delta` Events in its sink. `StreamRun` hands the sink every Event of the Run: deltas, `tool_started`, `tool_ended` and `tool_progress`.

```go
resp, err := runner.StreamRun(ctx, req, func(e gotato.Event) error {
    if d, ok := e.(gotato.TextDelta); ok {
        card.Append(d) // buffer by RunID and update the card about every 500 ms
    }
    return nil
})
```

Four limits:

- **A queued message has no deltas.** A message sent with `runner.Send` into a Run that is going is queued in that Run, and its caller gets no sink. [`examples/larkbot`](../examples/larkbot/README.md) streams only a Run it starts itself (with `StreamRun`); a queued message is answered by the final text of the Run it joined, read from `SubscribeInstance`.
- **Deltas are sent, not stored.** A subscriber that reads too slowly skips only deltas, and the final text is in the `reply_ended` Event, which is stored.
- **Deltas are visible only on the writer Instance.** `StreamSession` on another instance sees the stored Events at its next look and no deltas.
- **A writer switch needs the card's own id.** If another pod takes the Session over, it must edit the same card, so store the id of the card (not the id of the user's message) in the Session's metadata. `examples/larkbot` stores the card's id in `lark.card` next to the thread root, but does not read it back after a takeover: a Run another pod finishes is answered with a new card.

### Process text and the answer

Treat `text_delta` as a draft: the model does not yet know whether the reply will ask for tools. The order is `reply_started`, `text_delta` (sent only), `reply_ended` (stored, with `tool_calls` and `stop_reason`), then `tool_started`. On `reply_ended`, fold the text into the process part of the card when `tool_calls > 0`, and keep it as the answer when `tool_calls` is 0. The last reply of a Run is its final text, which is what `examples/larkbot` posts.

## Known gap: no de-duplication across pods

Duplicates are left to you. `examples/larkbot` keeps an in-memory set of message IDs on each instance and drops a repeat it has seen; a repeat that lands on another instance is handled as new (the cluster scenario `s5` measures it). Marking an ID before the work loses the message if the instance dies in between, and marking it after lets two instances race, so the choice depends on your service. Gotato itself does not de-duplicate `RequestID` in 0.2.0. Pass the chat platform's message id as `MessageRequest.RequestID` (`examples/larkbot` does, for a Run its message starts), so that a repeat can be found in the Record's `run_started` Event, or keep the id in your own shared store.

## 3. Try the cluster

Three pods, a rolling deploy, a killed writer and a scale-down run on a local kind cluster. Every scenario checks the Records afterwards.

```sh
cd examples/larkbot
cluster/run.sh up && cluster/run.sh s1 && cluster/run.sh down
```

The example's [README](../examples/larkbot/README.md) lists the scenarios and what each proves. The bot is an application, not a client Gotato ships: copy what you need, and replace the persona in `main.go`.
