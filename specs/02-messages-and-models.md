# 02. Messages and Models

**Status:** Draft

> **Core keeps Messages provider-neutral; Models return normalized streams.**

## 1. Message

```go
type Role string
const (
    RoleUser Role = "user"
    RoleAssistant Role = "assistant"
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

A user Message needs meaningful content. An assistant Message may contain text, reasoning, or Tool Calls. A Tool Result Message needs a valid originating Call ID and finalized result. Unknown roles, empty user input, duplicate Call IDs, and orphan results are invalid.

## 2. Content

```go
type ContentKind string
const (
    ContentText ContentKind = "text"
    ContentReasoning ContentKind = "reasoning"
    ContentImage ContentKind = "image"
    ContentJSON ContentKind = "json"
)

type ContentPart struct {
    Kind ContentKind
    Text string
    Data []byte
    MIMEType string
    Metadata map[string]string
}
```

Binary content declares MIME type. A converter may reject unsupported content but may not silently reinterpret it. Values crossing a boundary are copied or immutable.

## 3. Tool values

```go
type ToolCall struct { ID ToolCallID; ToolID string; Arguments []byte }
type ToolResult struct {
    CallID ToolCallID
    Status ToolResultStatus
    Content []ContentPart
    Metadata map[string]string
    SafeError string
    Executed bool
}
```

Arguments are complete JSON only after assembly. `Executed` is authoritative: blocked uses are false; an invoked executor is true even if it fails or is cancelled later.

## 4. Usage and stop

Usage is provider-neutral; unavailable counts remain zero and are not fabricated. Stop values include `none`, `end_turn`, `tool_calls`, `max_tokens`, `cancelled`, and `error`.

## 5. Model request

```go
type ModelRequest struct {
    SystemInstructions string
    Messages []ModelMessage
    Tools []ToolSpec
    Options ModelOptions
}
```

Core builds a fresh request for each Model call. Options have provider-neutral meaning. Provider-specific options belong in adapter configuration.

## 6. Model contract

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}

type ModelStream interface {
    Recv(context.Context) (ModelEvent, error)
    Close() error
}
```

`Recv` honors Context, returns source order, returns `io.EOF` only after completion, returns typed failure on provider failure, and never returns a second completion. `Close` is idempotent and Core calls it on completion, cancellation, protocol error, and panic recovery.

The contract exposes no provider SDK or Protobuf values.

## 7. Normalized stream

Model Events include text/reasoning deltas, Tool Call start, argument deltas, usage updates, completion, and failure. Call starts precede their argument deltas; Call IDs are not reused; argument bytes append byte-for-byte; completion includes stop reason; failure is terminal.

## 8. Assembly

The assembler maintains one in-progress assistant Message:

```text
text/reasoning delta → content part
Tool start → Call slot
argument delta → Call bytes
completion → finalized Message
```

At completion every Tool argument must parse as one JSON value, the assistant Message is committed once, and only then may Tool resolution begin. Malformed JSON, duplicate IDs, post-terminal stream Events, or missing completion are Model protocol failures. Partial failed responses are discarded by default.

## 9. LLM Adapter boundary

An LLM Adapter exposes the provider-neutral Model contract to Core. It owns provider protocol, authentication, provider-specific options, usage mapping, and provider-level failure policy. A Model Router may select among adapters.

An adapter cannot mutate Core transcript state, execute Tools, or create a second Agent Loop. Run-level retry is admitted by Core policy and remains inside the same Run.
