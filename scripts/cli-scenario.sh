#!/usr/bin/env bash
# A smoke run of the test CLI (internal/cmd/gotato): it builds the CLI into
# bin/, makes a Session, runs a prompt with the demo model, reads the Events,
# inspects the Context and compacts it. Every step must exit 0, and the JSON
# steps must print valid JSON. Needs jq.
#
# Usage: scripts/cli-scenario.sh
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# The CLI is a module of its own.
(cd internal/cmd/gotato && go build -o "$root/bin/gotato" .)

GOTATO_HOME=$(mktemp -d)
export GOTATO_HOME
trap 'rm -rf "$GOTATO_HOME"' EXIT

gotato=$root/bin/gotato

"$gotato" doctor --json | jq -e . >/dev/null
echo "ok    doctor"

id=$("$gotato" session create --json | jq -er .id)
echo "ok    session create ($id)"

"$gotato" run --session "$id" --model demo --json "use-tool" | jq -e . >/dev/null
echo "ok    run"

"$gotato" events --session "$id" >/dev/null
echo "ok    events"

"$gotato" context inspect "$id" --json | jq -e . >/dev/null
echo "ok    context inspect"

"$gotato" context compact "$id" --keep-messages 2 --json | jq -e . >/dev/null
echo "ok    context compact"

echo "cli-scenario: all passed"
