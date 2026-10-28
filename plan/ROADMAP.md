<!-- what-we-build: drafted 2026-10-29 -->
# Gotato — Roadmap

What comes next, and how we know it is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures and what [FEATURES.md](../FEATURES.md) marks as planned (⬜) or names as a gap (🚧). The principles are in [DESIGNS.md](../DESIGNS.md), the forks already decided are in [decisions](decisions/README.md), and terms are in [GLOSSARY.md](../GLOSSARY.md).

Gotato has made no release yet. The first release is named 0.2.0 (versions 0.1.0 and 0.1.1 of the module path were published by mistake before it, hold other code, and are retracted), so there are no point releases before it. Every feature in FEATURES.md is built; what stands between the code and the release is the release itself. No step has a date.

## Next: the 0.2.0 release

The maintainer approves the reviewed documents. The MCP adapter is a module of its own that reaches the root through `replace`, which `go get` ignores, so the root is tagged `v0.2.0` first, the adapter then requires that version and is tagged with its directory (`adapter/mcp/v0.2.0`). The `retract` directives in both `go.mod` files keep the stray 0.1.x versions from being chosen. A module outside the repository, with no `replace`, follows the README from nothing to a running agent with an MCP ToolSet. The release notes name what was never checked against a real provider.

Done when:

- the outside-module check passes against proxy.golang.org with the real tags;
- `go get github.com/jinhuang712/gotato@latest` resolves to v0.2.0.

## Every release, from the first on

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again, and compared with the previous release (for the first release, with the values in GOALS.md) and with what its row in GOALS.md names.
- G-G06 is checked: each break has a before-and-after entry in [CHANGELOG.md](../CHANGELOG.md).

## Not scheduled

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](../FEATURES.md#later);
- the other items in [TODO.md](TODO.md), which are fixed or settled outside any release.
