# 05. Tools and ToolSets

**Status:** Draft

> Tool is the execution unit. ToolSet is the capability-discovery unit.

## 1. Tool specification

A Tool MUST provide:

```text
stable local name
description
input JSON Schema
execution function
```

It MAY provide output Schema, display metadata, execution mode, and adapter metadata.

## 2. Execution contract

A Tool receives Context, call identity, validated arguments, and a progress reporter. It returns a final result or error.

The Runtime MUST:

```text
assemble complete arguments
validate input Schema
recover Tool panics
bound progress and result size
convert errors into failed Tool Results
```

## 3. ToolSet contract

```go
type ToolSet interface {
    Spec() ToolSetSpec
    Tools(context.Context) ([]Tool, error)
}
```

A ToolSet specification MUST contain a stable name and model-facing capability description. `Tools` MUST resolve a deterministic collection.

## 4. Composition

The public API MUST support:

```text
WithTool
WithTools
WithToolSet
WithToolSets
```

Construction validates names, Schemas, nil implementations, ToolSet uniqueness, qualified Tool uniqueness, and deterministic order.

## 5. Qualified identity

Core identity MUST combine ToolSet and local Tool name:

```text
grafana.view_dashboard
grafana.edit_panel
```

Provider adapters MAY encode this identity for provider syntax. The mapping MUST remain stable and reversible within the request.

Individual Tools use a reserved or explicit root namespace and remain always visible.

## 6. Staged visibility

The Agent MUST distinguish:

```text
registered ToolSets
active ToolSets
visible Tools for the next Model request
```

Agent construction MAY mark selected ToolSets active. Inactive ToolSets are discoverable through the built-in activation Tool.

## 7. Activation Tool

When inactive ToolSets exist, the Runtime MUST expose an activation Tool whose specification includes their names and descriptions.

```text
activate_toolset(name="grafana")
```

Activation MUST be:

```text
validated against registered ToolSets
idempotent
deterministically ordered
bounded by active ToolSet and visible Tool limits
committed between Turns
observable through Tool and ToolSet Events
```

The activated Tools MUST appear in the next Model request and remain active until Agent reset or explicit configuration change.

## 8. Registration helpers

An official typed-function adapter SHOULD derive Schema and execution glue from Go input and output types. Source generation MAY assemble package-level ToolSets.

Registration uses explicit constructors and imports.

## 9. Capability adapters

Local functions, HTTP, gRPC, MCP, workflows, scripts, and remote Agents MAY implement Tools or ToolSets through adapter packages.
