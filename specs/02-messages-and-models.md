# 02. Messages and Models

**Status:** Draft

> The Agent retains application Messages; the Model receives a deliberate provider-compatible conversion.

```text
Agent Messages → Transform → Convert → Model Request → Model Stream
```

## 1. Message roles

Core MUST support:

```text
user
assistant
tool_result
```

Content MUST support text and MAY support structured parts such as images.

An assistant Message MUST represent text, optional reasoning content, zero or more Tool Calls, usage, and stop reason.

## 2. Application Messages

Applications MAY add Message forms for their own state. Before each Model call:

1. context Transformers receive a snapshot;
2. the converter produces provider-compatible Messages;
3. the Runtime builds the Model request.

Transformers MUST honor cancellation and return a new context value.

## 3. Model contract

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}
```

A `ModelStream` MUST provide ordered events, terminal completion, close semantics, and Context cancellation. A `Recv`-style API is the preferred phase-one shape.

## 4. Model request

A request MUST contain:

```text
system instructions
converted Messages
visible Tool specifications
portable model options
```

Provider adapters own provider-specific options and wire formats.

## 5. Stream events

Normalized Model events MUST represent:

```text
text delta
reasoning delta when available
Tool Call start and argument delta
usage update
completion
failure
```

## 6. Assembly

- Text deltas MUST preserve source order.
- Tool Call deltas MUST group by call identity.
- Complete Tool arguments MUST parse as JSON before Schema validation.
- Model completion MUST produce one committed assistant Message.
- Usage SHOULD be attached to the assistant Message and Model completion Event.

## 7. Model failure

A stream failure before assistant completion terminates the current Run with a typed Model error. Retry behavior is supplied by an explicit adapter or Extension policy.
