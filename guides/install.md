# Install

By the end of this page you have Gotato in your `go.mod`. Gotato has no release yet, so a version is a commit: pin one you have tested.

## The runtime

The root module holds the core, the store, the Runner, and the providers. It needs Go 1.22.

```sh
go get github.com/jinhuang712/gotato@<commit>
```

`go get` turns the commit into a pseudo-version (`v0.0.0-<time>-<commit>`) in your `go.mod`. Import the core under the name `gotato`, which is the name of its package:

```go
import (
    "github.com/jinhuang712/gotato/gotato"
    "github.com/jinhuang712/gotato/session"
)
```

Do not use `@main` in a service: it moves each time you run it. Until the first release a name may change, with the before and after in [CHANGELOG.md](../CHANGELOG.md); read its *Unreleased* section when you move the commit.

## The MCP adapter

`adapter/mcp` is a module of its own. It requires the runtime at `v0.0.0`, which does not exist as a tag, so point that requirement at the same commit with one `replace`:

```sh
go mod edit -replace=github.com/jinhuang712/gotato@v0.0.0=github.com/jinhuang712/gotato@<commit>
go get github.com/jinhuang712/gotato/adapter/mcp@<commit>
go mod tidy
```

Use the same commit for the runtime and the adapter. The adapter needs Go 1.25, and `go get` raises the `go` line of your module to match. Once Gotato has a release, the `replace` goes away.

## Check it

```go
s, err := gotato.NewSession(gotato.WithModel(myModel))
```

If this builds, you are ready for [Getting started](getting-started.md).
