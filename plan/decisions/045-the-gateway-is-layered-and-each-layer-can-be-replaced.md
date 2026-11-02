# 045. The gateway is four layers a team can replace, over one core description of the model

Decided: 2026-11-02    Status: current

## The problem
`gateway.Client` is closed ([DESIGNS → 10](../../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)). The wire API is an enum checked in nine places, and the retry, restart, SSE reader and error tables are unexported, so a new wire API means editing Gotato. Provider rules come from names and tables of hosts and models. Auth is a static key. One model is described twice: [042](042-a-models-profile-is-its-own-value-apart-from-the-connection.md)'s `gateway.ModelProfile`, mixed with facts of the server, and `gotato.ModelConfig`'s sampling. The core finds the window through probes a wrapper can hide.

## The choice
**One model, one core type.** `gotato.ModelConfig{Name string; Facts ModelFacts; Sampling Sampling}`, and `Model` gains `Config() ModelConfig`.
- `ModelFacts{Window Window; CacheRetention time.Duration; RoundReasoning, ThinkingBinding bool}` is what the model is. Nothing overrides it, and nothing is guessed from a name. `Window` is a number or `"unknown"`, written out, and `gateway.New` refuses one not stated. `RoundReasoning`: the provider takes back only the current Round's reasoning. `ThinkingBinding`: it checks a thinking block's prefix.
- `Sampling{Temperature *float64; MaxTokens uint32; ReasoningEffort, ReasoningSummary string}` holds defaults. `WithSampling` (was `WithModelConfig`) overrides them field by field into `ModelRequest.Sampling`. Who may override is [044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)'s.

**Four gateway layers**, joined by `gateway.New(wire WireAPI, conn Connection, model gotato.ModelConfig, opts ...Option)`, each with a shipped default:
- **Wire API**: `Encode(Call) (*Request, error)`. `Call` holds the ModelConfig, the `ModelRequest`, the key and the cache key. `Request` holds `Body`, `Header` and `NewDecoder func() Decoder`, fresh per attempt, with `Decode(SSEEvent) ([]gotato.ModelEvent, bool, error)`. Shipped: `ChatCompletions{}`, `Responses{}`, `AnthropicMessages{}`, whose fields are the wire's own options (cache key field, deferred form, API version) and whose zero value is the default.
- **Transport and auth**: `Connection{Endpoint, APIKey, CacheKeyHeader string; Headers map[string]string; HTTPClient *http.Client}`. Other auth goes in the `RoundTripper`, as for MCP ([025](025-callers-credentials-and-quotas-belong-to-the-service.md)). `NewHTTPClient(connect, header time.Duration)` (10s, 60s) puts timeouts on the client itself.
- **Retry**: `Retry{Max int; Backoff, MaxWait time.Duration}` via `WithRetry`, for a refused request and a cut stream. `DefaultRetry` is 2, 200ms, 60s; `Retry{}` is off.
- **Error meaning**: `Classifier func(Failure) Verdict`, `Verdict{Retryable, Overflow bool; Limit int}`. `DefaultClassifier` holds today's tables; a team wraps it with `WithClassifier`.

A wire never sends, retries or classifies, and nothing under it reads its bytes, so the restart guard against repeated output holds for any wire.

## Not chosen
- More enum cases: every new wire API waits for a release.
- Export `do`, the restarter and the SSE reader: a larger surface, and every wire repeats the assembly.
- A credential interface: a `RoundTripper` does it, signing too.
- `Provider` names as a registry: still a table by name.
- Presets per provider, or a table of models: they go stale (042).
- Keep the capability probes: a wrapper that forgets `Unwrap` hides the window, and the ceiling with it.
- A retry interface: a value covers every case so far.
- The flat `Config` as a shim: two ways to build a Client, and 0.3.0 breaks anyway.
- `LoadYAML` in the library: a file format and yaml.v3 in every service.

## Consequences
A new wire API is one type outside Gotato, reusing the other layers. Any auth is an `http.Client`. A wrapper that embeds a Model forwards `Config` for free. We accept that `RoundReasoning` and `ThinkingBinding` steer what a wire sends: they are facts of the model, not of the protocol. Every caller and every `gotato.Model`, fakes included, changes once. A team that omits `RoundReasoning` or `CacheRetention` sends more reasoning or gets the default retention, and the provider accepts both. An outside wire keeps [DESIGNS → 2](../../DESIGNS.md#2-a-request-only-grows-at-its-end) on its own. This supersedes 042 and [001](001-one-implementation-per-wire-api.md)'s "may also name the provider".

1. Export `Failure`, `Verdict`, `Classifier`, `DefaultClassifier`; every error path goes through it.
2. `Retry` and `WithRetry` replace `MaxRetries` (and its -1), `RetryBackoff`, `MaxRetryWait`.
3. One stream loop owns the SSE reader and restart and calls a `Decoder`. The three wires become `WireAPI` values; the checks in `normalizeAPI`, `resolveEndpoint`, `resolveCacheKey`, `reservedBodyField`, `resolveDeferredTools`, `setProtocolHeaders`, `CacheRetention` and `Stream` go.
4. `gotato`: the three model types, `Window` (from `gateway`, with `MarshalText`/`UnmarshalText`), `Model.Config`, `WithSampling`, `ModelRequest.Sampling`. `modelctx` deletes `ContextWindower`, `CacheRetainer`, `RoundReasoner`, `Unwrapper`, `WindowOf`, `RetentionOf`, `DropsReasoning`; compaction and the Runner read `Config().Facts`.
5. Delete `provider.go`, `officialHosts`, `thinkingPrefixModels`, `takesPromptCacheOptions`, `NoDropBlock`.
6. Shrink `Connection`, export `NewHTTPClient`; drop `BaseURL`, the timeout fields, the Responses refusal of an empty key.
7. Remove `Config`, `New(Config)`, `NewWithProfile`, `ModelProfile`, `ProfileSet`, `Provider`, `Connection.Profile`, `LoadYAML`; the YAML moves under the CLI's `internal/`, the one place a wire's name maps to a value. Update the CLI, workload tool, bench, tests, `gateway.example.yaml`, model spec and glossary.
