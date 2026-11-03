# Change what the model sees

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Change what the model sees

By the end of this page you have chosen what the model reads on each Turn: your own builder, a transformer, a block of the system prompt, or a rewrite before the call. You need the [first page](../tutorials/getting-started.md).

## Choose what the model sees

A builder turns the Transcript into the Context of one Turn. The default, `gotato.FullHistoryContext()`, sends everything, which keeps the request prefix identical from Turn to Turn so the provider's prompt cache hits. Write your own only when you can say what the model should see instead. A function is enough:

```go
// lastTen shows the model only the last ten Messages, and a block for the date.
func lastTen() gotato.ContextBuilder {
	return gotato.ContextBuilderFunc(func(_ context.Context, s gotato.ContextSnapshot) (gotato.ModelContext, error) {
		msgs := s.Messages
		if len(msgs) > 10 {
			msgs = msgs[len(msgs)-10:]
		}
		return gotato.ModelContext{
			SystemInstructions: s.SystemInstructions,
			System:             []gotato.ContextBlock{{Tag: "today", Text: "2026-10-28"}},
			Messages:           msgs,
		}, nil
	})
}
```

Cutting history like this breaks the prompt cache. For a long Session, prefer Compaction, which closes a stretch into a Segment the model can read back.

## Reshape the Messages after the builder

Both are Extension stages that run after the builder. A transformer selects, adds or prunes Messages; a converter adapts their shape for one provider. They change what the model sees on this Turn only, never the Transcript.

## Add a tagged block to the system prompt

A tagged piece of the system prompt, rendered as `<tag attr="value">text</tag>`. A `ContextBuilder` fills `ModelContext.System` with them. The tag marks a boundary or where the text came from.

## Rewrite the Transcript before a call

An Extension stage before every model call, with no tool open. It may rewrite the Transcript and say so. The Runner's own compaction works this way.

## See also

- [How the Context is built](../../specs/context.md)
- [Context, as the user sees it](../../FEATURES.md#f4-context)
- [Tune compaction](tune-compaction.md)
