# Serve over HTTP

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Serve over HTTP

By the end of this page your service serves its Runner over HTTP. You need [Run Sessions on many pods](run-sessions-on-many-pods.md) and [Install](install.md).

## Mount the handler

```go
mux.Handle("/agent/", http.StripPrefix("/agent", httpapi.New(runner)))
```

`httpapi` is `github.com/jinhuang712/gotato/adapter/http`, a module of its own (`go get github.com/jinhuang712/gotato/adapter/http`; see [Install](install.md)), imported with an alias because `net/http` is imported too.

The handler serves the same Runner over HTTP, with a stream for each Run and each Session. A service that wants another transport maps the same Runner itself.

## See also

- [HTTP in five minutes: a run you can curl](../tutorials/http-in-five-minutes.md)
- [How the HTTP API works](../../specs/http.md)
- [The routes](../../FEATURES.md#f121-routes)
