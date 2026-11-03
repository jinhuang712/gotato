# Testing

An agent that calls a paid model over a network is hard to test: the answers change, the calls cost money, and the failures that matter, a cut stream or a dead instance, rarely happen on demand. Gotato's tests run with no network and no provider, on fakes that answer the same way every time and fail on purpose. Part of this ships, so a team tests its own agent the same way; the rest are Gotato's own tools, which no shipped package imports. It is the testing part of the [map](ARCHITECTURE.md).

## Three levels of fake

Each level fakes one more layer, so a test picks the cheapest one that still exercises what it checks.

```text
 level                     what is fake                      what is real               lives in
 a fake Model              the model, in process             the Loop, tools, Record    testkit/          ships
 a fake provider           the provider, over HTTP           the model bridge too       internal/fakemodel/
 the reference workload    the model, per task               the host, the store,       internal/reftasks/,
                                                             several instances          internal/workload/
```

## The test kit

The test kit gives the doubles a team needs to test an agent with no network. A fake Model plays a fixed list of replies, each a script of the events one model call streams: text, then a tool call, then text. It repeats its last script once the list ends, so a loop always finishes; a replay Model fails instead, so a test catches a model called once too often. A function Model decides each reply from the request, and two helpers read the user's text and the tool results out of it. A fake Model can stream slowly, so a test cuts it partway; block until the test lets it go, so a test steers a Run in flight; or fail. A fake tool returns a fixed result and records each call, an Event recorder keeps every Event of a Session in order, and a ready Record starts a test from a given history.

Two models serve the test CLI and the reference service: one echoes the prompt, and one calls a demo tool when the prompt says so. The conformance suite of the store is a package of its own beside the store (`store.md`), so that importing the test kit links no `testing` package into a program.

## A fake provider over HTTP

The model bridge must be tested against the wire, not against its own decoder. The fake provider is a test server that speaks the three wire APIs, built from the providers' public formats, and answers from the request: it echoes the last user text, and calls the first tool when the text says `use-tool`.

```text
 POST /v1/messages          Anthropic Messages      ┐
 POST /v1/responses         OpenAI Responses        ├ streamed as server-sent events
 POST /v1/chat/completions  OpenAI Chat Completions ┘
```

A quirk plays a provider's failure on demand, switched on by a directive in the last user text, such as `[quirk:overflow]`, or by a request header that lists them. An unknown directive is refused, so a typo fails loudly.

| Quirk | What the fake does |
|---|---|
| `overflow` | refuses the request as too long before any output, in the provider's own words and status |
| `overflow-midstream` | streams part of the reply, then says the context window ran out, in each wire's own form |
| `cut-after-text`, `cut-in-args` | closes the connection after some text, or inside a tool call's arguments, then answers normally |
| `cached=N` | reports N cached input tokens in the provider's own usage field |
| `reasoning` | streams reasoning with its signature, and refuses a request that does not send it back as the provider does |
| `delay=D` | waits before the reply, and stops when the client goes away |
| `load=<tool>` | plays the native form of deferred tools: searches, loads, calls and answers |

Without a quirk it still refuses what a provider refuses: a request past its window, an output limit past its maximum and a malformed tool loop. Gotato runs no checks against a real provider; a team checks its own endpoint.

## The reference workload

The reference workload is a fixed set of tasks that the goals are measured on. Each task has a history, prompts, tools, a fake Model that plays it the same way every time and a check of its answers, from a single lookup to a Session of 1,000 messages. The long Sessions that compact and merge over a hundred Runs are played only when named, since they take longer.

The workload runner plays the tasks through a host, and reports from the host's Events and results alone, so a team can compute the same numbers from its own telemetry:

- every failed Run, tool call and check, with its cause;
- token usage, cache hits, and the cost at a price the caller gives;
- the time Gotato adds per Turn, beside the model's and the store's;
- the time to load a Session;
- whether each Turn's request matches an inspection of the Transcript it was built from.

A live run passes a real model and its price instead of the fakes. It costs money, so it runs on demand and never in CI, and it also runs the check of a model-written Recap.

## Breaking things on purpose

Three tests break what a deploy breaks, and check that nothing is lost.

```text
 a crash at every write    play a task; for each write it makes, kill the instance right after it;
                           another instance must open exactly what that write left, answer every
                           tool call, match the request's prefix hash, and finish the next Run
 a cut at every Event      cut a Session's stream after each Event and resume it; the result
                           equals a stream that was never cut
 several instances         a hashing proxy in front of several instances: one killed mid-traffic
                           loses only the step in flight, and messages to one Session never interleave
```

## What this buys you
- A team tests a tool-using agent with no network and no key, and the test gives the same answer every time.
- The model bridge is checked against each wire's format and its failures, not against itself.
- A release is measured on the same tasks as the last one, with the same numbers.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the guideline [a Session can be stopped and seen](../DESIGNS.md#9-a-session-can-be-stopped-and-seen), [FEATURES.md](../FEATURES.md) ([F14.6: Test kit](../FEATURES.md#f146-test-kit)), decisions [010: A light library](../plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) and [013: A conformance suite](../plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md).
