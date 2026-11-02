# Model

An agent is only as steady as the call to its model, and that call crosses a network Gotato does not control. Providers shed load, cut streams, refuse long requests and word the same failure in different ways. The Model seam keeps all of that outside the core. The core sees one small interface and one kind of error answer, whichever provider is behind it.

## TL;DR
- A Model is two methods: `Stream` returns a stream of events, and `Config` says what the model is. The core reads the stream and builds the reply from it.
- The gateway is four layers, each with a default a team can replace: the wire API, the connection, the retry and the meaning of an error. A provider is a few Go values, or a file the test tools read.
- A model is described once, by a `gotato.ModelConfig`: its name, its facts (window, cache retention, how it takes reasoning) and its default sampling. The team states the facts; Gotato guesses none from an address or a name.
- The core asks the error two questions, "was this too long?" and "may a retry pass?", so it never reads a provider's words.

## A reply is a stream

The Session starts a call, reads events until `done`, and always closes the stream.

```text
 Session                         Model                          provider
    │ Stream(ctx, ModelRequest) ──▶│ ──────── request ──────────▶ │
    │ ◀── ModelStream, or an error │                               │
    │ Recv ──▶ text_delta, reasoning_delta, reasoning_done         │
    │ Recv ──▶ tool_call (whole call, with its ID)                 │
    │ Recv ──▶ usage                                               │
    │ Recv ──▶ done (stop_reason)      ◀──── end of stream ─────── │
    │ Close                                                        │
```

The request is the built Context plus the tools to offer. The reply is a Message put together event by event, and each delta is also sent on as an Event, so a client sees text as it arrives. A stream that ends with no `done`, a tool call with no ID, or an event of an unknown kind is a protocol failure, never a finished answer. Watch for the size: a reply that grows past the Message limit is stopped while it streams, not after.

## A model is described once

`Model.Config()` returns a `gotato.ModelConfig`. The runtime reads it; no side door and no table is behind it.

```text
 ModelConfig
 ├── Name        the id the provider knows
 ├── Facts       what the model is, as the team states it
 │    ├── Window            tokens, or "unknown", written out
 │    ├── CacheRetention    how long the provider keeps the prompt cache (0 = unknown)
 │    ├── RoundReasoning    the provider takes back only the current Round's reasoning
 │    └── ThinkingBinding   the provider checks the prefix a thinking block was written under
 └── Sampling    the model's default Temperature, MaxTokens, ReasoningEffort, ReasoningSummary
```

- **Facts are not overridden and not guessed.** Nothing is derived from a provider's address or a model's name. A team that omits `RoundReasoning` sends more reasoning than the provider takes back, and one that omits `CacheRetention` gets the provider's default retention; a provider accepts both.
- **The window is stated.** `gotato.WindowTokens(n)` with n above 0, or `gotato.WindowUnknown()`. `gateway.New` refuses a window left out, because leaving it out is never an accident. An unknown window leaves compaction with no ceiling, so it runs only after a refusal as too long.
- **Sampling holds defaults.** A Session's `gotato.WithSampling` overrides them field by field, and the result is `ModelRequest.Sampling`. A field the Session leaves unset keeps the Model's default.
- **How the core reads the facts.** Compaction takes its ceiling from `Facts.Window`, and leaves reasoning out of the Messages it keeps when `Facts.RoundReasoning` is set. The Runner times a warm Session's idle compaction from `Facts.CacheRetention`. Each reads `Model.Config()` and nothing else.
- **A wrapper forwards it for free.** A wrapper that embeds a `gotato.Model` has `Config` already, so no wrapper hides a fact. A Model written by hand that states nothing returns the zero `ModelConfig`: an unknown window, so no ceiling trigger; no retention; no default sampling.

## The gateway is four layers

`gateway.New(wire, connection, model, opts...)` joins four layers, and each has a default a team can replace without copying the code around it ([DESIGNS → 10](../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)).

```text
 gateway.New( wire,                connection,           model,                  opts... )
                │                      │                   │                       │
                ▼                      ▼                   ▼                       ▼
        ┌──────────────┐      ┌────────────────┐   gotato.ModelConfig     WithRetry(Retry)
        │ WireAPI      │      │ Connection     │                          WithClassifier(f)
        │ Encode(Call) │─────▶│ Endpoint       │
        │  → Request   │      │ APIKey, Headers│       gateway.Client
        │ Decoder      │      │ HTTPClient     │       send, retry, restart, read SSE,
        └──────────────┘      └────────────────┘       classify errors
```

```text
 layer            default                          replace it with
 wire API         ChatCompletions, Responses,      your own WireAPI value
                  AnthropicMessages
 connection       Connection{Endpoint, APIKey,     an HTTPClient whose RoundTripper signs
                  ...}, NewHTTPClient(10s, 60s)    or refreshes a credential
 retry            DefaultRetry: 2, 200ms, 60s      WithRetry(Retry{...}); Retry{} is off
 error meaning    DefaultClassifier                WithClassifier, usually wrapping the default
```

### The wire API

A `WireAPI` writes one request and reads its reply, and does nothing else: it never sends, retries or classifies.

- `Encode(Call) (*Request, error)` takes the `Call` (the `ModelConfig`, the `ModelRequest`, the key and the cache key) and returns a `Request`: the `Body`, the `Header` it sets, and `NewDecoder`, which gives a fresh `Decoder` for each attempt.
- `Decoder.Decode(SSEEvent)` turns one Server-Sent Event into `ModelEvent`s and says when the reply is done. An error the stream carries is a `Failure`, which the Classifier reads.
- The shipped wires are `ChatCompletions{CacheKeyField}`, `Responses{CacheKeyField, Deferred}` and `AnthropicMessages{Version, Deferred}`. Their fields are the wire's own options, and the zero value is the default.
- A new protocol is one type outside Gotato, passed to `gateway.New`. It reuses the connection, the retry and the classifier, and no vendor SDK enters your service. Nothing under the wire reads its bytes, so the rule that a request only grows at its end holds for any wire; the wire keeps it for its own bytes.
- A wire may add `Validate() error`; `gateway.New` calls it, so a mistake in its settings is refused before the first call.

How the facts steer a shipped wire:

| Fact | Chat Completions | Responses | Anthropic Messages |
|---|---|---|---|
| `RoundReasoning` | earlier Rounds' reasoning left out | earlier Rounds' reasoning left out | earlier Rounds' reasoning left out, and a cache marker where the last Round started |
| `CacheRetention` | not used | `prompt_cache_options` with ttl 30m, only when exactly 30m | `cache_control` ttl 1h on every marker, only when 1h or more |
| `ThinkingBinding` | not used | not used | `thinking` block_binding `drop_block`, with its beta header |

### The connection

`Connection` says where to send the request and as whom, whatever wire is spoken over it.

- `Endpoint` is the complete URL, path included. Gotato keeps no table of providers' addresses or paths.
- `APIKey` goes where the wire's protocol wants a credential; empty sends none. `Headers` go with every request, under the wire's own headers, so none can replace the credential. `CacheKeyHeader` sends the cache key in a header for a provider that routes its cache by one.
- `HTTPClient` sends the requests. Nil means `gateway.NewHTTPClient(10s, 60s)`: 10 seconds to open a connection, 60 to wait for the response headers. Any other authentication, a signed request or a token that expires, goes in the client's `RoundTripper`, as for MCP ([decision 025](../plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)).

### The retry and the meaning of an error

Retry says how often, and the Classifier says what a failure means. Both are below, in *Retries live in three places* and *How an error is classified*.

## Retries live in three places

Each layer repairs the failure it can see, and no layer repairs another's. The gateway's two layers act within its `Retry`: `Max` repeats after the first attempt, a first wait of `Backoff` that grows with the attempt and carries up to 25% of jitter, and `MaxWait` as the cap. `DefaultRetry` is 2, 200 milliseconds and 60 seconds; `gateway.WithRetry(gateway.Retry{...})` sets another, and `Retry{}` turns retries off. Whether a failure is worth repeating is the Classifier's answer, not the Retry's.

```text
 layer              sees                          does
 gateway, send      no response yet               send again: transport error, 429 (not a hard
                                                  quota), 500 to 504, 529, "overloaded"
 gateway, stream    a stream cut                  open the stream again with the same request
 Session, Turn      "request too long", before    ask the TurnPreparer to shorten it,
                    any output                    then call once more (once per Turn)
 caller             everything else               reads Retryable and decides
```

Both gateway layers report each repeat with `ReportModelRetry`, so the Session stores a `model_retried` Event with the attempt, the wait and a safe reason. A provider's `Retry-After` wins over the backoff, and `MaxWait` keeps it from holding a Run for an hour. A certificate the client distrusts, or a host that does not exist, is a mistake in the settings and fails at once. An error the provider sends in the middle of a stream is marked Retryable when its code, type or text says the failure is the provider's own and may pass (an overload, a rate limit, a service that is unavailable), like a refused request. The gateway does not send it again: the stream began, so the caller decides.

## A stream cut after output

A cut after output is sent again only when a Session listens, and the Session drops what it had.

```text
 reply_started A ─ text_delta ─ text_delta ─ ✂ cut
                                              │ gateway: ReportModelRetry
                                              ▼
                  model_retried ─▶ reply_cut_off A ─▶ reply_started B ─ text_delta ─ ... ─ reply_ended B
                                    (text kept as a log)   (the whole new reply)
```

The first reply stays on the Record as a Cut-off reply, only as a log of what happened; the Context never sends it to a model. The new reply has its own start, so a reader that sees `model_retried` throws away the deltas since the last `reply_started`. Only text is kept in the log: a half tool call or half reasoning is void, and a cut with no text writes `model_retried` and a new `reply_started` and nothing else. A caller that reads the Client with no Session gets a Retryable error, worded "stream interrupted" on every wire, instead, because sending again would show it the same output twice. When the attempts run out, the Run fails with `model_failure` and the Record keeps the last Cut-off reply.

## Reasoning goes back for the current Round

Replay is decided from the Record and the model's facts alone, so every Instance sends the same bytes.

```text
 Round 1                          Round 2 (the current Round)
 user ─ reply+reasoning ─ tool    user ─ reply+reasoning ─ tool result ─ reply+reasoning
        └── sent WITHOUT it ──┘          └──────────── sent WITH it ─────────────────┘
        (RoundReasoning set)             (RoundReasoning not set: all reasoning, always)
```

A Round starts at the last user Message; Tool Results and notices never start one, and a Steer does. A provider that needs reasoning only inside the tool loop in progress takes back only that, and the team says so with `Facts.RoundReasoning`. A Session that lives for weeks then does not carry every thought it ever had. The cut is always everything before the Round, and it never moves back, so the bytes already sent stay the same and the cache holds. A model whose team does not state it gets all of the reasoning, because some providers refuse a request that leaves it out and every provider accepts it. The same fact tells compaction to leave reasoning out of the Messages it keeps.

## How an error is classified

The gateway answers two questions by method, and the core reads only those. The Classifier is what the gateway asks to fill them.

```text
 error from Stream or Recv
   │ ctx ended?      ─▶ canceled / deadline_exceeded   (Run's ctx, or the call's deadline)
   │ stream ended without done ─▶ model_protocol_failure
   │ coded protocol_failure or not_supported ─▶ kept as it is
   │ ContextOverflow() true ─▶ context_overflow        (400/413 whose words say "too long")
   └ anything else ─▶ model_failure, Retryable = err.Retryable()
```

- **A `Failure` goes in, a `Verdict` comes out.** A Failure is what one attempt showed: the HTTP status, the provider's message, code and type, the headers, and the error under it. `Verdict{Retryable, Overflow, Limit}` is what it means: may a retry pass, was the request longer than the window, and what limit in tokens the text names. Every error path of the gateway goes through the Classifier, the refusals and the errors told in a stream alike.
- **`DefaultClassifier` holds the gateway's own tables.** It reads a short list of phrases for "too long", because providers publish no stable code for it, and it names no provider's address or model. An Overflow verdict is never Retryable. A request whose `max_tokens` alone is too large is not an overflow, since compaction cannot shorten it.
- **A team adds to it by wrapping it.** `gateway.WithClassifier(f)` where `f` calls `gateway.DefaultClassifier(failure)` and then changes the Verdict, for example to add its own wording for "too long". A nil Classifier is the default.
- **What the core sees.** The Message of the error is the public text only; the provider's own words and any host name stay on the Cause. The Model was called, so the error is never "not run".

## What this buys you
- A new vendor is a few lines of Go, and a vendor SDK never enters your service. A new protocol is one type that reuses the other layers.
- Any authentication is an `http.Client`, so a signed request or a token that expires needs no change to Gotato.
- A flaky network costs a repeated call and a `model_retried` Event, not a failed Run.
- A Model of your own plugs in by two methods, and says what it is by returning a `ModelConfig`; the core never finds a fact through a wrapper that forgot to pass it on.

## See also
[FEATURES.md → G-F07](../FEATURES.md#g-f07--models-and-providers-) for the settings and defaults, [G-F31](../FEATURES.md#g-f31--recovering-a-cut-stream-), decisions [001](../plan/decisions/001-one-implementation-per-wire-api.md), [045](../plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md), [029](../plan/decisions/029-reasoning-goes-back-for-the-current-round.md) and [035](../plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md), [GLOSSARY.md](../GLOSSARY.md). The specs [errors.md](errors.md) and [context.md](context.md) cover codes and the Context in full.
