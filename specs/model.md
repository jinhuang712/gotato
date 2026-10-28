# Model

An agent is only as steady as the call to its model, and that call crosses a network Gotato does not control. Providers shed load, cut streams, refuse long requests and word the same failure in different ways. The Model seam keeps all of that outside the core. The core sees one small interface and one kind of error answer, whichever provider is behind it.

## TL;DR
- A Model is one method that returns a stream of events; the core reads the stream and builds the reply from it.
- The gateway implements each wire API once, and it owns the retries, the reasoning replay and the reading of provider errors. A provider is configuration.
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

## One implementation per wire API

A provider is a configuration entry, and the Client behind it is one of three encoders.

```text
 config entry                       gateway.Client
 ┌──────────────────┐   ┌───────────────────────────────────────────┐
 │ api, base_url,   │──▶│ openai-chat-completions ─┐                │
 │ key, model, ...  │   │ openai-responses ────────┼─▶ shared: send,│
 └──────────────────┘   │ anthropic-messages ──────┘   retry, SSE,  │
                        │                              errors       │
                        │ side doors the core may ask:             │
                        │  ContextWindowTokens  CacheRetention     │
                        │  RoundReasoningOnly   ContextOverflow()  │
                        └───────────────────────────────────────────┘
```

Any vendor that speaks one of the three wire APIs needs no new code, and no vendor SDK enters your service. The three encoders differ only in how they write the request and read the stream; the sending, retrying and error reading are shared. The side doors are small interfaces a Model may add (a capability). The core and the compaction code ask for them and fall back to "unknown" when a Model has none. A wrapper Model keeps them visible by implementing `Unwrap`; one that does not hides them, and the window reads as unknown.

## Retries live in three places

Each layer repairs the failure it can see, and no layer repairs another's.

```text
 layer              sees                          does
 gateway, send      no response yet               send again: transport error, 429 (not a hard
                                                  quota), 500 to 504, 529, "overloaded"
 gateway, stream    a stream cut                  open the stream again with the same request
 Session, Turn      "request too long", before    ask the TurnPreparer to shorten it,
                    any output                    then call once more (once per Turn)
 caller             everything else               reads Retryable and decides
```

Both gateway layers report each repeat with `ReportModelRetry`, so the Session stores a `model_retried` Event with the attempt, the wait and a safe reason. A provider's `Retry-After` wins over the backoff, and a cap keeps it from holding a Run for an hour. A certificate the client distrusts, or a host that does not exist, is a mistake in the settings and fails at once. An error the provider sends in the middle of a stream is marked Retryable but is not sent again by the gateway: the stream began, so the caller decides.

## A stream cut after output

A cut after output is sent again only when a Session listens, and the Session drops what it had.

```text
 reply_started A ─ text_delta ─ text_delta ─ ✂ cut
                                              │ gateway: ReportModelRetry
                                              ▼
                  model_retried ─▶ reply_cut_off A ─▶ reply_started B ─ text_delta ─ ... ─ reply_ended B
                                    (text kept as a log)   (the whole new reply)
```

The first reply stays on the Record as a Cut-off reply, only as a log of what happened; the Context never sends it to a model. The new reply has its own start, so a reader that sees `model_retried` throws away the deltas since the last `reply_started`. Only text is kept in the log: a half tool call or half reasoning is void, and a cut with no text writes `model_retried` and a new `reply_started` and nothing else. A caller that reads the Client with no Session gets a Retryable error instead, because sending again would show it the same output twice. When the attempts run out, the Run fails with `model_failure` and the Record keeps the last Cut-off reply.

## Reasoning goes back for the current Round

Replay is decided from the Record alone, so every Instance sends the same bytes.

```text
 Round 1                          Round 2 (the current Round)
 user ─ reply+reasoning ─ tool    user ─ reply+reasoning ─ tool result ─ reply+reasoning
        └── sent WITHOUT it ──┘          └──────────── sent WITH it ─────────────────┘
        (Anthropic, OpenAI)              (any other provider gets all reasoning, always)
```

A Round starts at the last user Message; Tool Results and notices never start one, and a Steer does. A provider that needs reasoning only inside the tool loop in progress gets only that, so a Session that lives for weeks does not carry every thought it ever had. The cut is always everything before the Round, and it never moves back, so the bytes already sent stay the same and the cache holds. A provider the gateway does not recognize gets all of it, because some providers refuse a request that leaves it out. The same rule tells compaction to leave reasoning out of the Messages it keeps.

## How an error is classified

The gateway answers two questions by method, and the core reads only those.

```text
 error from Stream or Recv
   │ ctx ended?      ─▶ canceled / deadline_exceeded   (Run's ctx, or the call's deadline)
   │ stream ended without done ─▶ model_protocol_failure
   │ coded protocol_failure or not_supported ─▶ kept as it is
   │ ContextOverflow() true ─▶ context_overflow        (400/413 whose words say "too long")
   └ anything else ─▶ model_failure, Retryable = err.Retryable()
```

"Too long" is read out of a short list of phrases, plus the team's own, because providers publish no stable code for it. A request whose `max_tokens` alone is too large is not an overflow, since compaction cannot shorten it. The Message of the error is the public text only; the provider's own words and any host name stay on the Cause. The Model was called, so the error is never "not run".

## What this buys you
- A new vendor is a few lines of configuration, and a vendor SDK never enters your service.
- A flaky network costs a repeated call and a `model_retried` Event, not a failed Run.
- A Model of your own plugs in by one method, and gains retries, overflow handling and capabilities by saying so.

## See also
[FEATURES.md → G-F07](../FEATURES.md#g-f07--models-and-providers-) for the settings and defaults, [G-F31](../FEATURES.md#g-f31--recovering-a-cut-stream-), decisions [001](../plan/decisions/001-one-implementation-per-wire-api.md), [016](../plan/decisions/016-reasoning-goes-back-for-the-current-round.md) and [020](../plan/decisions/020-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md), [GLOSSARY.md](../GLOSSARY.md). The specs [errors.md](errors.md) and [context.md](context.md) cover codes and the Context in full.
