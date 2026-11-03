# Customize the model bridge

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Customize the model bridge

By the end of this page you have changed one layer of the model bridge: the wire API, the connection, the retry or what an error means. You need the [first page](../tutorials/getting-started.md).

## Speak a wire API Gotato does not ship

Replaces the wire API of the model bridge (package `model`): how a request is written and its reply read. Implement `Encode(Call) (*Request, error)`: the `Call` holds the `ModelConfig`, the `ModelRequest`, the key and the cache key, and the `Request` holds the `Body`, the `Header` the wire sets and `NewDecoder`, which returns a fresh `Decoder` for each attempt. `Decoder.Decode(SSEEvent)` turns one event into `ModelEvent`s and says when the reply is done. The wire never sends, retries or classifies, so it reuses the Connection, the Retry and the Classifier as they are. Pass it as the first argument of `model.New`; the shipped ones are `ChatCompletions{}`, `Responses{}` and `AnthropicMessages{}`. A wire may add `Validate() error`, which `model.New` calls.

## Authenticate and set timeouts

`model.Connection` holds the complete `Endpoint`, an `APIKey`, `Headers`, a `CacheKeyHeader` and an `HTTPClient`. Any authentication beyond a static key goes in the `RoundTripper` of the client you pass: sign the request or add a token that expires there. With no `HTTPClient` the model bridge uses `model.NewHTTPClient(10*time.Second, 60*time.Second)`; call it yourself for other timeouts.

## Change how often a call is sent again

`model.Retry{Max, Backoff, MaxWait}` says how often a refused request or a cut stream is sent again, how long to wait and the longest wait. Pass it with `model.WithRetry`; the default is `model.DefaultRetry` (2, 200 ms, 60 s), and `model.Retry{}` turns retries off. Whether a failure may be repeated is the Classifier's answer.

## Change what a failed call means

`model.Classifier` is `func(Failure) Verdict`. A `Failure` is what one attempt showed (the status, the message, the code and type, the headers, the error under it), and a `Verdict{Retryable, Overflow, Limit}` says what it means. `model.DefaultClassifier` holds the model bridge's own tables. To add to it, write a function that calls it and changes the Verdict, and pass that with `model.WithClassifier`:

```go
func(f model.Failure) model.Verdict {
	v := model.DefaultClassifier(f)
	if strings.Contains(f.Message, "request_too_big") { // your provider's wording for "too long"
		v.Overflow, v.Retryable = true, false
	}
	return v
}
```

## See also

- [How the model bridge works](../../specs/model.md)
- [Model, as the user sees it](../../FEATURES.md#f7-model)
- [State what your model is](state-what-your-model-is.md)
