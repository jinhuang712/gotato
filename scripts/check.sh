#!/usr/bin/env bash
# Checks the Go modules of the repository the way CI does: gofmt, go build,
# go vet, go test -race and golangci-lint.
#
# Usage: scripts/check.sh [module-path ...]
#   With no argument it checks every module; with paths (for example
#   adapter/http) it checks only those. The root module is ".".
#
# It runs every step of every chosen module, prints one line per step, and
# exits 1 if any step failed. golangci-lint is skipped, with a note, when it
# is not installed or when it was built with an older Go than the module
# targets; any other lint failure fails the run.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# Each checkout lints with a cache of its own: a shared cache keeps the
# results of a worktree by its absolute paths, so once that worktree is
# removed another checkout reports its files and cannot read their nolint.
export GOLANGCI_LINT_CACHE="$root/.cache/golangci-lint"

all_modules=(. adapter/http adapter/mcp internal/cmd/gotato examples/larkbot bench/refservice)

if [ "$#" -gt 0 ]; then
  modules=("$@")
else
  modules=("${all_modules[@]}")
fi

for m in "${modules[@]}"; do
  if [ ! -f "${m%/}/go.mod" ]; then
    echo "check: ${m%/} is not a module (no go.mod); the modules are: ${all_modules[*]}" >&2
    exit 2
  fi
done

failed=0

# step NAME CMD...: run CMD, and print its output only when it fails.
step() {
  local name=$1 out
  shift
  if out=$("$@" 2>&1); then
    echo "ok    $name"
  else
    echo "FAIL  $name"
    sed 's/^/      /' <<<"$out"
    failed=1
  fi
}

# gofmt_clean: fail if gofmt -l lists a tracked .go file under the current
# directory, outside notes/. A nested module is listed again on its own turn.
gofmt_clean() {
  local unformatted
  unformatted=$(git ls-files -z -- '*.go' ':(exclude,top)notes' | xargs -0 gofmt -l)
  if [ -n "$unformatted" ]; then
    echo "gofmt needed on:"
    echo "$unformatted"
    return 1
  fi
}

# lint NAME: golangci-lint, skipped when missing or too old for the module.
lint() {
  local out
  if ! command -v golangci-lint >/dev/null 2>&1; then
    echo "skip  $1 golangci-lint: not installed"
    return
  fi
  if out=$(golangci-lint run ./... 2>&1); then
    echo "ok    $1 golangci-lint"
  elif grep -q 'used to build golangci-lint is lower than the targeted Go version' <<<"$out"; then
    echo "skip  $1 golangci-lint: built with an older Go than the module targets"
  else
    echo "FAIL  $1 golangci-lint"
    sed 's/^/      /' <<<"$out"
    failed=1
  fi
}

for m in "${modules[@]}"; do
  m=${m%/}
  echo "== $m"
  pushd "$m" >/dev/null
  step "$m gofmt" gofmt_clean
  step "$m go build" go build ./...
  step "$m go vet" go vet ./...
  step "$m go test -race" go test -race ./...
  lint "$m"
  popd >/dev/null
done

if [ "$failed" -ne 0 ]; then
  echo "check: FAILED"
  exit 1
fi
echo "check: all passed"
