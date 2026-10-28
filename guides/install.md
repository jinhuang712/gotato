# Install

By the end of this page you have Gotato in your `go.mod`. Pin a release: `v0.2.0` is the first.

## The runtime

The root module holds the core, the store, the Runner, and the providers. It needs Go 1.24.

```sh
go get github.com/jinhuang712/gotato@v0.2.0
```

Import the core under the name `gotato`, which is the name of its package:

```go
import (
    "github.com/jinhuang712/gotato/gotato"
    "github.com/jinhuang712/gotato/session"
)
```

Do not use `@main` in a service: it moves each time you run it. From 0.2.0 on, a break has an entry in [CHANGELOG.md](../CHANGELOG.md), with the before and after; read it when you move to a new version.

## The MCP adapter

`adapter/mcp` is a module of its own, tagged `adapter/mcp/v0.2.0`. It requires the runtime at the same version, so one command adds it:

```sh
go get github.com/jinhuang712/gotato/adapter/mcp@v0.2.0
```

The adapter needs Go 1.25 (the MCP SDK it uses requires it), and `go get` raises the `go` line of your module to match.

## Check it

```go
ctx := context.Background()
s, err := gotato.NewSession(ctx, gotato.WithModel(myModel))
```

If this builds, you are ready for [Getting started](getting-started.md).
