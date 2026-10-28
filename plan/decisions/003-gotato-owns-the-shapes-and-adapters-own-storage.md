# 003. Gotato owns the shapes, and adapters own storage and telemetry

Decided: 2026-10-15    Status: current

## The problem
Databases model schemas differently, and teams run different telemetry stacks. Neither belongs in the core.

## The choice
Gotato defines stable shapes for a Session and its Events. Adapters map them to a database or a telemetry stack.

## Not chosen
- A SQL store in the core: works on day one for SQL users, but brings drivers, migrations and one schema for everyone, and no help for Mongo.
- OpenTelemetry in the core: tracing on day one, but a heavy dependency for every service, including those on another stack.

## Consequences
It works with any database and any telemetry stack and adds no dependency to the service. Each team writes or picks an adapter, and proves it with [013](013-a-conformance-suite-ships-with-the-store-contract.md).
