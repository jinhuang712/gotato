# 04. Events

**Status:** Draft

> One Event model serves embedded consumers, Extensions, Agent Routines, transports, observability, and tests.

```text
Runtime ──Events──┬──► embedded subscriber
                  ├──► Extension
                  ├──► Agent Routine observer
                  ├──► gRPC bridge
                  ├──► logger / tracer
                  └──► test recorder
```

## 1. Event kinds

Phase one MUST expose semantic equivalents of:

```text
agent_start
agent_end
turn_start
turn_end
message_start
message_update
message_end
tool_execution_start
tool_execution_update
tool_execution_end
toolset_activated
routine_started
routine_completed
routine_failed
routine_cancelled
```

Go names and payload types MUST preserve these lifecycle meanings.

## 2. Ordering

A text-only Run emits:

```text
agent_start
turn_start
user Message lifecycle when Prompt supplied input
assistant message_start
assistant message_update...
assistant message_end
turn_end
agent_end
```

Tool execution and Tool Result Message Events occur after assistant `message_end` and before `turn_end`.

Routine lifecycle Events occur in the parent Run Event stream. Detailed child Events preserve the child Run sequence and Routine correlation.

## 3. Partial Messages

Assistant `message_update` Events identify the Message under construction and carry a delta or partial value. The committed transcript changes at `message_end`.

## 4. Tool and Routine progress

Tools and Agent Routines MAY emit progress updates. Progress remains observational; final Tool and Routine Results carry settled outcomes.

## 5. Subscribers

```go
type EventHandler func(Event) error
```

The exact type MAY evolve while preserving these semantics:

- registration returns an unsubscribe function;
- subscribers run in registration order;
- subscribers are awaited;
- subscriber errors follow a configured blocking or advisory policy;
- panics are recovered at the subscriber boundary.

## 6. Canonical facts and Moving Parts

Core fixes Event kind, production point, ordering, correlation, and terminal barriers.

Moving Parts MAY provide:

```text
Event enrichment
consumer filtering
projection
redaction
delivery policy
sinks
```

Each consumer receives its own projection of the canonical immutable Event.

## 7. Backpressure

Awaited subscribers provide explicit embedded backpressure. Service transports and Routine bridges MUST use bounded buffering and define their slow-consumer policy.

## 8. Terminal barrier

The terminal `agent_end` Event acts as a settlement barrier. No loop Events follow it for the Run.

## 9. Service projection

The gRPC adapter maps Core Event kinds and portable payloads to `RunEvent`. Transport envelopes add remote correlation without changing Core lifecycle semantics.
