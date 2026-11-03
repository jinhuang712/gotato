# bench/refservice

The reference service that [GOALS.md](../../plan/GOALS.md) measures for [B10: Startup time](../../plan/GOALS.md#b10-startup-time) and [C1: Footprint](../../plan/GOALS.md#c1-footprint): the smallest program a team would write to embed Gotato in its own Go service. It is a nested Go module, so the root `go test ./...` does not build it; it requires the root module and `adapter/http` (a module of its own), both by `replace`.

Run both from the repository root:

```bash
(cd bench/refservice && go test -run '^$' -bench Startup -benchtime 20x .)
bench/refservice/footprint.sh
```

The benchmark reports `ms/start`, from process start to the first 200 from `/healthz`. The script prints one JSON object with module counts, binary size, Go version and platform.
