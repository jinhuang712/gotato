# bench/refservice

The reference service that GOALS.md G-B10 (startup time) and G-B12 (footprint) measure: the smallest program a team would write to embed Gotato in its own Go service. It is a nested Go module, so the root `go test ./...` does not build it.

Run both from the repository root:

```bash
(cd bench/refservice && go test -run '^$' -bench Startup -benchtime 20x .)
bench/refservice/footprint.sh
```

The benchmark reports `ms/start`, from process start to the first 200 from `/healthz`. The script prints one JSON object with module counts, binary size, Go version and platform.
