# Wrap a model

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Wrap a model

By the end of this page you have wrapped a Model to log, count or rewrite a call, and made its failures say what to do. You need the [first page](../tutorials/getting-started.md).

## What a Model is

A Model is two methods: `Stream(ctx, ModelRequest) (ModelStream, error)` starts a call, and `Config() ModelConfig` says what the model is: its name, its facts (the context window, how long its provider keeps the prompt cache, whether it takes back only the current Round's reasoning) and its default sampling. The `model` package implements it for OpenAI Chat Completions, OpenAI Responses and Anthropic Messages. You write your own Model to reach a provider it does not cover, or you wrap one. A Model that states nothing returns the zero `gotato.ModelConfig`: an unknown window, so compaction has no ceiling and runs only after the provider refuses a request as too long.

## Wrap a Model to log, count or rewrite

A wrapper holds another Model and passes the call on. It sees every request and every event, so it can log, count, trace, or rewrite.

```go
// Logged wraps a Model and sees every request and every event.
type Logged struct {
	Inner gotato.Model
	Log   func(string)
}

// Config passes the model's description on, so the window and the other facts stay visible.
func (m Logged) Config() gotato.ModelConfig { return m.Inner.Config() }

func (m Logged) Stream(ctx context.Context, req gotato.ModelRequest) (gotato.ModelStream, error) {
	m.Log(fmt.Sprintf("request: %d messages, %d tools", len(req.Messages), len(req.Tools)))
	stream, err := m.Inner.Stream(ctx, req) // pass the whole request on, unchanged
	if err != nil {
		return nil, err
	}
	return &loggedStream{ModelStream: stream, log: m.Log}, nil
}

type loggedStream struct {
	gotato.ModelStream
	log func(string)
}

func (s *loggedStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	ev, err := s.ModelStream.Recv(ctx)
	if err == nil {
		s.log("event: " + string(ev.Kind))
	}
	return ev, err
}

// Close is always called, also when the Session stops reading before io.EOF.
func (s *loggedStream) Close() error {
	s.log("closed")
	return s.ModelStream.Close()
}
```

Used as `gotato.WithModel(Logged{Inner: model, Log: ...})`, one prompt logs `request: 1 messages, 0 tools | event: text_delta | event: done | closed`.

What a Model keeps to:

- **One goroutine reads a `ModelStream`.** `Recv` returns the next `ModelEvent` and `io.EOF` after the last. The caller always calls `Close`.
- **A reply is a stream** of `text_delta` and `reasoning_delta` events and `tool_call` events, then `usage`, then `done` with a `StopReason`. The Session may stop reading at `done`, so do your closing work in `Close`, not on `io.EOF`.
- **Honor `ctx`.** It ends the call, and a Model that ignores it holds the Run.
- **A wrapper forwards `Config`.** The runtime reads the window, the cache retention and the reasoning rule from `Model.Config().Facts`. A wrapper that embeds the `gotato.Model` has `Config` for free; one that holds it in a field returns `Inner.Config()`, or the facts are lost.
- **A wrapper passes the whole `ModelRequest` on.** Besides `Messages` and `Tools` it carries `SystemInstructions`, `Sampling` (sampling and reasoning hints), `CacheBreakpoints` and `CacheKey` (prompt-cache hints), and `LoadedTools` and `UnloadedTools` (the Tool catalog). A wrapper that copies only the fields it knows silently drops the rest. Change a field by copying the request, not by building a new one.

## Make a failure say what to do

A Model tells the Session about a failure with a method on the error it returns. (What the Model knows about itself, such as its context window or how long its provider keeps the prompt cache, is a separate matter: it is the `Facts` of the `ModelConfig` that `Config()` returns.)

| Method on the error | What Gotato does | Resulting code |
|---|---|---|
| `Retryable() bool` returning true | marks the failure retryable: the same request, sent again later, may succeed | `model_failure`, retryable |
| `ContextOverflow() bool` returning true | treats the request as too long. When the refusal comes before any output, the Session builds a shorter Context and calls once more in that Turn | `context_overflow` |
| a `*gotato.RuntimeError` with `CodeNotSupported` or `CodeModelProtocolFailure` | keeps your code as it is | as given |
| any other error | a failure of the Model | `model_failure`, not retryable |

```go
// RateLimited is a Model failure that says it is worth retrying.
type RateLimited struct{}

func (RateLimited) Error() string   { return "provider: rate limited" }
func (RateLimited) Retryable() bool { return true }

// TooLong is a Model failure that says the request did not fit.
type TooLong struct{}

func (TooLong) Error() string         { return "provider: prompt too long" }
func (TooLong) ContextOverflow() bool { return true }
```

A Model that retries inside itself, as the `model` package's wire APIs do, calls `gotato.ReportModelRetry(ctx, ...)` before it waits, so the Session stores a `ModelRetried` Event and discards the reply so far.

A context error is the caller's only when the Run's context really ended; one the Model produced itself (an HTTP client timeout) is a retryable `model_failure`.

## See also

- [State what your model is](state-what-your-model-is.md)
- [Customize the model bridge](customize-the-model-bridge.md)
- [Test a tool and a model](test-a-tool-and-a-model.md)
- [How the model bridge works](../../specs/model.md)
- [Model, as the user sees it](../../FEATURES.md#f7-model)
