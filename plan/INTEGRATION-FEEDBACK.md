# Integration feedback: first real adoption (a Lark bot service running on several pods)

Source: the downstream team's append-only log (entries `D-001` to `D-012`; the team calls them G-NNN, renamed here so they do not clash with GOALS.md goals). Each entry was checked against this repository on 2026-10-29. The log is still growing: add new entries below, do not rewrite old ones.

Verdict: **confirmed** (the claim holds), **partly** (part of it holds, see the note), **wrong** (the repository already covers it).

Remove an item from *Backlog* in the commit that resolves it, as in [TODO.md](TODO.md).

## Backlog

### P0

- [ ] **D-009 · The `WithForwarding` example can be bypassed.** Verdict: confirmed, but the risk is in the example, not the docs. `nil` allow forwards nowhere and redirects are not followed, and the docs say so. The example uses `strings.HasPrefix(address, "http://10.0.")`, which `http://10.0.evil.com` passes. `serve --forward` allows every address; its code comment says "test tool", its help text does not. Fix: parse the URL and check the host IP against a CIDR (or an instance allow-list) in the example and in `guides/http-in-five-minutes.md`; say "tests only" in the `--forward` help.
- [ ] **D-001 · Release order and proxy warm-up.** Verdict: partly, now healed. Tags and module paths are right. The root tag `v0.2.0` was made 20 seconds before `adapter/mcp/v0.2.0`, and proxy.golang.org likely cached "not found" for a few minutes, so `go get` reported "found, but does not contain package". It no longer reproduces. Fix: in the release notes, push nested-module tags first (or together), then warm the proxy with a `.info` request; document that a new nested tag can lag by minutes, and that `GOPROXY=direct` is the fallback.

### P1

- [ ] **D-004 · An MCP server without annotations hides every tool under `WithMaxEffect`.** Verdict: confirmed (docs only; the core's `WithMaxEffect` code was not read). `WithToolEffect` matches the exact MCP name only. Fix: add `WithDefaultEffect(ToolEffect)` for tools with no annotations, or `WithToolEffectFunc`; add the consequence to the MCP section of FEATURES.md.
- [ ] **D-002 · `adapter/mcp` cannot filter or rename tools.** Verdict: confirmed. The only cleaning is empty and repeated names, recorded in `Skipped()`. Fix: `WithToolFilter(func(*sdk.Tool) bool)` (rejected tools go to `Skipped` as "filtered") and `WithToolRename(func(string) string)`; add both to the options table.
- [ ] **D-006 · A streamed card across pods needs the card's id, and nothing says so.** Verdict: partly. Deltas are sent, not stored; a slow subscriber skips only deltas; `StreamSession` on another instance sees no deltas. Wrong in the log: the card is updated by the writer's own `SubscribeInstance`, so the *message* id need not be shared; what a writer switch needs is the **card's** own id. `examples/larkbot` stores only the thread root. Fix: one sentence in FEATURES.md "deltas are visible only on the writer Instance"; a streaming card in the example (buffer by RunID, throttle about 500 ms, store the card id in Session metadata, final text from `RunEnded` when `events_skipped` fires).
- [ ] **D-007 · `larkbot` does not de-duplicate across pods.** Verdict: confirmed. Wrong in the log: `examples/larkbot/README.md` and a test already call it a known gap; the two guides do not mention it. Fix: copy the README note into `guides/example-chat-bot.md` and `guides/embed-in-a-service.md`; pass Lark's `message_id` as `MessageRequest.RequestID` so a repeat can be found in `run_started`. Gotato itself does not de-duplicate `RequestID` in 0.2.0 (roadmap item).

### P2

- [ ] **D-003 · Nothing warns against `http.Client.Timeout` on the MCP transport.** Verdict: partly. Headers are covered by a `RoundTripper` (FEATURES.md says so), but no text says that `Client.Timeout` cuts the standalone SSE stream, and the `WithHTTPClient` comment invites setting a timeout. Fix: one sentence in the option comment and FEATURES.md (use `Transport.ResponseHeaderTimeout` and the Run's context). Optional: `WithHeaders(func(ctx) http.Header)`.
- [ ] **D-008 · No Mongo guidance for a custom `session.Store`.** Verdict: partly. The requirements hold (atomic batch plus head update, unique `(id, seq)`, epoch rule, 24 backing stores). Wrong in the log: `storetest` has an epoch case that names the rule, and later abilities can come as optional interfaces. Fix: add a Mongo section to `guides/write-a-store.md` (unique index, `E11000` to `ErrConflict`, epoch in the head document's filter, replica set for a transaction or a head-document CAS otherwise).
- [ ] **D-010 · Reasoning effort cannot be set through a self-hosted gateway on chat/anthropic wires.** Verdict: partly. `refuseReasoningConfig` rejects it on both; only Responses sends it. Wrong in the log: an unrecognised host already returns every reasoning artifact, so no `provider: deepseek` is needed. Fix: document how to set `provider` for a LiteLLM host (cache TTL optimizations need it); consider a pass-through extra body field.
- [ ] **D-005 · No cost field or price table.** Verdict: confirmed, and a gap in the docs, not a stated non-goal. Fix: say in FEATURES.md that cost is the team's own conversion of `Usage`; add a worked example. No core change.

### New since the first pass

- [ ] **D-013 · No guide shows a streaming card, and the example uses `SubscribeInstance`, not `StreamRun`.** Verdict: partly. The downstream claim holds: a `StreamRun` sink gets every Event of the Run (deltas, `tool_started`, `tool_ended`, `tool_progress`), and no other Instance sees them. Not so: the docs never say "use `StreamRun` for a streaming card" (`guides/` only uses it to collect Events in tests), and `examples/larkbot` uses `SubscribeInstance` because a message sent into a running Run is queued there and its caller gets no sink. This refines D-006: a card needs `StreamRun` (or `StreamSession`), and a queued message has no deltas. Fix: a "streaming card" section in `guides/example-chat-bot.md` with that limit.
- [ ] **D-014 · `guides/write-a-store.md` does not say how to run `storetest` without a database.** Verdict: partly. Wrong in the log: `storetest.Run` needs no external service and no transaction; `MemoryStore`, `FileStore` and the guide's `mystore` all pass it, and the contract is one conditional write with a unique `(id, seq)`. A multi-document transaction is a cost of the Mongo design chosen, not a Gotato rule. Fix: a "no database at hand" section (point to the three above and `storetest.Check`); a CI example that starts a real backing store; say that a single-document atomic write plus the unique key is enough.
- [ ] **D-015 · A per-Run system prompt exists as `Update`, but no guide shows it.** Verdict: partly. There is no per-Run instruction field, but `Update: {"gotato.instruction": ...}` sets the Session's instruction as the Run's first write, `buildSession` re-reads it on every Run, `SkipSave` makes it last for one Run, and an empty value falls back to `Agent.Instruction` (code and FEATURES.md agree). It replaces, it does not append. Fix: a guide example; a test that pins the override (not checked whether one exists).

### P3 (docs)

- [ ] **D-011 · Small doc gaps.** Verdict: partly. Unverified `native` deferred loading and the missing catalog budget are already stated in FEATURES.md. `guides/install.md` does say the adapter needs Go 1.25, but its opening line says only 1.24. Fix: add "1.25 with `adapter/mcp`" beside the opening line.
- [ ] **D-012 · No recommended `WithListCache` TTL.** Verdict: confirmed, and by design (default 0 sees changes at once, the code comment and FEATURES.md say why). Do not change the default. Fix: suggest a value in FEATURES.md, for example `WithListCache(5*time.Minute)` for a server that sends `list_changed`.

## Corrections to send back to the downstream log

New entries that cite the old ones; do not edit them.

| Entry | Correction |
|---|---|
| D-006 | The card id, not the message id, is what a writer switch needs. |
| D-007 | The README documents the gap; the guides do not. |
| D-009 | Docs warn and the default is safe; the example's prefix check is the weak point. |
| D-010 | An unrecognised host returns reasoning by default; no `provider` needed. |
| D-006 | A streamed card needs `StreamRun`, not `SubscribeInstance` (see D-013). |
| D-011 | `install.md` already states the Go 1.25 need for the adapter. |

## Open checks for the downstream team

- D-004: does their MCP gateway send annotations? (They planned to test it in a staging environment.) That decides whether D-004 hurts them at all.
- D-001: which time did their failure happen? It would confirm the proxy-lag explanation, which is still an inference.
- D-014: their own Store has not run the conformance suite yet, so it should not be treated as trustworthy until it has.
