# 06. Extensions and Moving Parts

**Status:** Draft

> Extensions implement small behavior capabilities at explicit lifecycle boundaries.

## 1. Moving Parts

```text
ContextTransformer
MessageConverter
PreToolUse
PostToolUse
EventObserver
TurnStopper
```

An Extension implements one or more small interfaces corresponding to these capabilities.

## 2. Installation

Extensions MUST be installed explicitly on Agent construction. Installation order MUST be deterministic.

## 3. Context transformation

A `ContextTransformer` receives a context snapshot and returns the Messages used by the next conversion stage. It MUST honor the Run Context.

A `MessageConverter` converts transformed Gotato Messages into the portable Model request representation.

## 4. Pre-Tool-Use

`PreToolUse` runs after Tool resolution, complete argument assembly, and Schema validation.

It MAY return:

```text
Proceed
Block with a Tool Result
termination hint
```

Pre-Tool-Use observes the validated arguments and does not replace them.

## 5. Tool Use

The Runtime invokes the resolved Tool executor at most once after all Pre-Tool-Use components proceed.

Tool execution remains a Moving Part through the Tool interface while invocation count, Context propagation, and outcome commitment remain fixed Core rails.

## 6. Post-Tool-Use

`PostToolUse` receives every finalized Tool outcome, including executed and blocked outcomes.

It MAY:

```text
transform the model-facing Result
add typed metadata
redact or limit content
attach a termination hint
```

The outcome records whether execution occurred.

## 7. Event observation

An `EventObserver` receives canonical Core Events. Its interface or configuration MUST express blocking or advisory failure behavior.

An observer is in-process, fast, and Context-aware. It MUST NOT block on a network peer, a remote lock, or any wait without a bound of its own. Remote consumers receive Events through a bounded service boundary instead.

Event projection, filtering, redaction, delivery, and sinks use focused Event Moving Parts rather than changing canonical Event production.

## 8. Turn stopping

A `TurnStopper` runs after `turn_end` and before Steering, Tool continuation, Follow-up, or another Model request.

## 9. Ordering

```text
Installed: A → B → C
Pre:       A → B → C
Tool Use:  at most once
Post:      C → B → A
Observers: registration order
```

## 10. Failure modes

Each capability MUST use one explicit mode:

```text
blocking    current operation returns an error
transform   returned value enters the next stage
advisory    failure is reported while execution continues
```

A blocking Pre- or Post-Tool-Use Extension error terminates the Run. A Tool execution error becomes a failed Tool Result when the Runtime can continue.

## 11. Distribution

Extensions are compiled Go packages installed through constructors and options. Official tracing, logging, compaction, routing, retry, and policy integrations depend on these public contracts.
