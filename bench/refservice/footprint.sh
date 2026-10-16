#!/usr/bin/env bash
# Measures GOALS.md G-B12: modules and stripped binary size of the reference
# service. Prints one JSON object on stdout.
set -euo pipefail

cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

go build -trimpath -ldflags='-s -w' -o "$tmp/refservice" .

modules=$(go list -m all | wc -l | tr -d ' ')
linked=$(go version -m "$tmp/refservice" | grep -cE '^[[:space:]]+(mod|dep)[[:space:]]' || true)
bytes=$(wc -c < "$tmp/refservice" | tr -d ' ')
mb=$(awk -v b="$bytes" 'BEGIN { printf "%.1f", b / 1e6 }')

printf '{"modules": %s, "linked_modules": %s, "binary_bytes": %s, "binary_mb": %s, "go": "%s", "goos": "%s", "goarch": "%s"}\n' \
  "$modules" "$linked" "$bytes" "$mb" "$(go env GOVERSION)" "$(go env GOOS)" "$(go env GOARCH)"
