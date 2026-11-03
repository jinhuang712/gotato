# State what your model is

> [Wiki](../README.md) › [How-to](../README.md#how-to) › State what your model is

By the end of this page the runtime knows your model's window, cache retention and reasoning rule, because you said so. You need the [first page](../tutorials/getting-started.md).

## State the model's facts

`Model.Config()` returns the facts the runtime reads, so compaction knows its limits. Gotato keeps no table of windows, and it guesses nothing from a model's name or address: the team states them.

| Field of `gotato.ModelFacts` | Used for |
|---|---|
| `Window` (`gotato.WindowTokens(n)` or `gotato.WindowUnknown()`) | the Compaction ceiling |
| `CacheRetention` | when a warm Session compacts, before the cache expires |
| `RoundReasoning` | leaving reasoning out of the Messages compaction keeps, and out of earlier Rounds in the request |
| `ThinkingBinding` | asking the provider to drop a thinking block whose prefix changed (Anthropic wire) |

`ModelConfig.Sampling` holds the model's default sampling, which a Session's `gotato.WithSampling` overrides field by field. `model.New` takes the same `ModelConfig` and returns it from `Config()`; a wrapper that embeds the Model forwards it, so no fact is hidden.

## See also

- [How the facts are used](../../specs/model.md)
- [Customize the model bridge](customize-the-model-bridge.md)
