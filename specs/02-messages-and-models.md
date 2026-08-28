# 02. Messages and Models

**Status:** Draft

> Runtime Messages are provider-neutral transcript values. A Model receives a converted request and returns a normalized stream that the Runtime assembles exactly once.

## 1. Runtime Message shape

The identifier aliases used by this contract are:

```go
type MessageID string
type ToolCallID string
```

The Runtime MUST support these roles:

```go
type Role string

const (
    RoleUser       Role = "user"
    RoleAssistant  Role = "assistant"
    RoleToolResult Role = "tool_result"
)

type Message struct {
    ID         MessageID
    Role       Role
    Parts      []ContentPart
    ToolCalls  []ToolCall
    ToolResult *ToolResult
    Usage      Usage
    StopReason StopReason
}
```

A `user` Message MUST contain at least one meaningful content part. An `assistant` Message MAY contain text, reasoning, Tool Calls, or any combination supported by the configured Model. A `tool_result` Message MUST contain a Tool Call ID and a finalized Tool Result.

The Runtime MUST reject a Message with an unknown core role, an empty user input, duplicate Tool Call IDs within one assistant Message, or a Tool Result that has no originating Tool Call.

## 2. Content parts

Content is represented independently of provider SDK types:

```go
type ContentKind string

const (
    ContentText      ContentKind = "text"
    ContentReasoning ContentKind = "reasoning"
    ContentImage     ContentKind = "image"
    ContentJSON      ContentKind = "json"
)

type ContentPart struct {
    Kind     ContentKind
    Text     string
    Data     []byte
    MIMEType string
    Metadata map[string]string
}
```

`ContentText` and `ContentReasoning` use `Text`. Binary parts use `Data` and MUST declare `MIMEType`. A converter MAY reject a part unsupported by its provider, but it MUST NOT silently reinterpret one content kind as another.

Messages, parts, metadata maps, and byte slices crossing a Runtime boundary MUST be copied or treated as immutable. Provider-specific content belongs in the converter or adapter, never in the Runtime Message type.

## 3. Tool Calls and Results

```go
type ToolCall struct {
    ID        ToolCallID
    ToolID    string
    Arguments []byte
}

type ToolResultStatus string

const (
    ToolSucceeded ToolResultStatus = "succeeded"
    ToolFailed    ToolResultStatus = "failed"
    ToolBlocked   ToolResultStatus = "blocked"
    ToolCancelled ToolResultStatus = "cancelled"
)

type ToolResult struct {
    CallID    ToolCallID
    Status    ToolResultStatus
    Content   []ContentPart
    Metadata  map[string]string
    SafeError string
    Executed  bool
}
```

`ToolCall.Arguments` is complete JSON only after Model stream assembly. A partial argument value MUST NOT enter the transcript or Tool resolver.

`ToolResult.Executed` is authoritative. A blocked result has `false`; a result from an invoked executor has `true` even when that executor returns an error or is cancelled after starting.

## 4. Usage and stop values

Normalized usage is provider-neutral:

```go
type Usage struct {
    InputTokens      uint64
    OutputTokens     uint64
    CachedInputTokens uint64
    ReasoningTokens  uint64
}

type StopReason string

const (
    StopNone       StopReason = "none"
    StopEndTurn    StopReason = "end_turn"
    StopToolCalls  StopReason = "tool_calls"
    StopMaxTokens  StopReason = "max_tokens"
    StopCancelled  StopReason = "cancelled"
    StopError      StopReason = "error"
)
```

Adapters MAY leave unavailable usage fields at zero and MUST NOT fabricate provider-specific counts. Negative values are invalid. Usage updates are observational until Model completion, at which point the final usage is attached to the assistant Message and Run accounting.

## 5. Context transformation

Before every Model call the Runtime performs:

```text
Agent snapshot
    ↓
ContextTransformer
    ↓ new runtime Message sequence
MessageConverter
    ↓ portable Model Message sequence
ModelRequest
```

A Transformer receives a read-only snapshot and the Run Context. It MUST return a new sequence or an error. It MUST NOT mutate committed Agent history, ToolSet activation, queue state, or Run identity.

A converter receives the transformed sequence and produces the representation accepted by the Model adapter. It MUST preserve role order, Tool Call identity, Tool Result identity, and the distinction between committed and in-progress values.

## 6. Model request

```go
type ModelRequest struct {
    SystemInstructions string
    Messages           []ModelMessage
    Tools              []ToolSpec
    Options            ModelOptions
}

type ModelMessage struct {
    Role       Role
    Parts      []ContentPart
    ToolCalls  []ToolCall
    ToolResult *ToolResult
}
```

A request MUST contain the system instructions, the converted transcript, the Tool specifications visible for this Turn, and portable options. The Runtime MUST build a fresh request for each Model call; adapters MAY reuse provider clients internally.

`ModelOptions` contains only options with provider-neutral semantics. Provider-specific sampling, headers, safety switches, and transport settings belong in the adapter configuration.

## 7. Model and stream contracts

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}

type ModelStream interface {
    Recv(context.Context) (ModelEvent, error)
    Close() error
}
```

`Recv` MUST:

- return events in source order;
- honor the supplied Context while waiting;
- return `io.EOF` only after a completion event has been delivered;
- return a typed failure when the provider stream fails;
- never return a second completion event;
- be safe to call until completion or failure, but not concurrently unless the adapter explicitly documents it.

`Close` MUST be idempotent and MUST release provider resources. The Runtime MUST call it on normal completion, cancellation, protocol failure, and panic recovery.

The Model API MUST NOT expose provider SDK Messages or generated Protobuf types.

## 8. Normalized Model events

```go
type ModelEventKind string

const (
    ModelTextDelta             ModelEventKind = "text_delta"
    ModelReasoningDelta        ModelEventKind = "reasoning_delta"
    ModelToolCallStart         ModelEventKind = "tool_call_start"
    ModelToolArgumentsDelta    ModelEventKind = "tool_arguments_delta"
    ModelUsageUpdate           ModelEventKind = "usage_update"
    ModelCompleted              ModelEventKind = "completed"
    ModelFailed                ModelEventKind = "failed"
)

type ModelEvent struct {
    Kind           ModelEventKind
    Text           string
    CallID         ToolCallID
    ToolID         string
    ArgumentsDelta []byte
    Usage          Usage
    StopReason     StopReason
    Error          error
}
```

Required event rules:

- A text or reasoning delta MUST have the corresponding content kind.
- `ModelToolCallStart` MUST precede all argument deltas for its Call ID.
- A Call ID MUST NOT be reused for another Tool Call in the same stream.
- An argument delta is appended byte-for-byte; the assembler MUST NOT parse a prefix.
- A completion event MUST contain the final stop reason and MAY contain final usage.
- A failure event MUST contain a typed Model error and MUST be terminal for that stream.
- Events after completion or failure are protocol errors.

## 9. Assembly

The Stream Assembler maintains one in-progress assistant Message:

```text
text delta             → append text part
reasoning delta        → append reasoning part
Tool Call start        → allocate call slot
argument delta         → append bytes to call slot
usage update           → replace provisional usage
completion             → finalize Message
```

Text and reasoning deltas preserve source order. Tool Calls retain the first-seen source position even if their argument deltas are interleaved.

At completion:

1. every Tool Call argument buffer MUST parse as one JSON value;
2. the assistant Message MUST be created with stable Call IDs and source order;
3. the Message MUST be committed once;
4. Tool resolution and Schema validation MAY begin only after commitment.

Malformed JSON, duplicate Call IDs, an event after completion, or a missing completion event is a Model protocol failure. A partial assistant Message from a failed stream is not committed unless an explicit adapter policy says the provider completed it; the default is to discard partial transcript state.

## 10. Model failure and retry

A stream failure before completion terminates the current Model call with a typed Model error. The Run MAY retry only inside its own loop and only when:

```text
the error is classified transient;
the retry budget has remaining attempts;
the Run Context is still active;
no Tool executor from that Model response has run;
and the configured policy permits replay of the Model request.
```

The default retry count is zero unless an Agent definition installs a policy. Every retry is observable through usage and diagnostic metadata, but it MUST NOT emit a second `agent_end`.

## 11. Provider boundary

Provider adapters own:

```text
provider request and response encoding
provider authentication and transport
provider-specific options
provider rate-limit and retry classification
provider SDK resource release
```

They MUST map provider failures into safe typed Model errors and MUST leave Runtime correlation values intact.
