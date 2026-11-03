# Contributing

Gotato is a small library with a versioned contract, so a change is welcome when it keeps it small and keeps the contract. This page is for people who change Gotato itself; how to use it is in the README and the wiki.

## Before you start

A change passes one test first: would a Go service that embeds an agent want this on its own? Anything else belongs to an application built on Gotato, and *Not planned* in [FEATURES.md](FEATURES.md) lists what was already ruled out, with the reason.

- **Ask first** in an issue for anything bigger than a fix, so the design is agreed before the code.
- **A choice with another way open** becomes a decision in `plan/decisions/`, written when the choice is made.
- **A break** of the Go API, the HTTP contract or a stored shape needs a before-and-after note in [CHANGELOG.md](CHANGELOG.md).

## Set up

You need the Go versions in [wiki/how-to/install.md](wiki/how-to/install.md), and the newest of them to run the checks in every module. The repository holds six Go modules: the root, `adapter/http`, `adapter/mcp`, `internal/cmd/gotato`, `examples/larkbot` and `bench/refservice`. Nothing else is needed: the tests run with no network and no model provider.

## Make a change

- **One concern per branch,** named with a type prefix such as `fix/` or `feat/`, and one change per commit where it is practical.
- **A fix comes with a test** that fails before it.
- **The docs move with the code:** a change to what a user sees updates [FEATURES.md](FEATURES.md) and [CHANGELOG.md](CHANGELOG.md) in the same change, and a change to how a part works updates its spec.
- **Never rewrite shared history** or force-push `main`.

AGENTS.md holds the full rules for this repository, for people and coding agents alike.

## Checks

`scripts/check.sh` runs every check: gofmt, build, vet, tests with the race detector, and the linter where it is installed, in every module. `scripts/check.sh adapter/http` runs one module. A change is ready when the modules it touched pass.

## Cut a release

The root module and the two adapters a service imports, `adapter/mcp` and `adapter/http`, are tagged together; each adapter's tag carries its path, such as `adapter/mcp/v0.3.0`. The test CLI, the example and the benchmark are modules too, and they are not for `go get`. No release script exists yet, so the steps are manual.

1. **Set the adapters' `require`.** In the release commit, the `require` of the root in each adapter's `go.mod` names the new root version, since a consumer ignores the `replace` that points at `../..`.
2. **Push the root tag first,** since each adapter requires the root at a release.
3. **Push the adapter tags** at once after it, on the same commit, and warm the proxy with a `.info` request for each tag, such as `curl https://proxy.golang.org/github.com/<owner>/gotato/adapter/mcp/@v/v0.3.0.info`.
4. **Check from a clean module cache.** In a new module, with `GOMODCACHE` set to a temporary directory, run `go get` for the root and for each adapter at the new tag. Announce the release only when this passes.
5. **Expect lag.** A new nested tag can take minutes, sometimes about an hour, to appear on proxy.golang.org; a consumer can set `GOPROXY=direct` until it does.
