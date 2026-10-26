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

## 3. Try the cluster

Three pods, a rolling deploy, a killed writer and a scale-down run on a local kind cluster. Every scenario checks the Records afterwards.

```sh
cd examples/larkbot
cluster/run.sh up && cluster/run.sh s1 && cluster/run.sh down
```

The example's [README](../examples/larkbot/README.md) lists the scenarios and what each proves. The bot is an application, not a client Gotato ships: copy what you need, and replace the persona in `main.go`.
