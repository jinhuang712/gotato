#!/bin/sh
# Builds the bot and runs it with the credentials in .env.
# Usage: ./run.sh [flags]   e.g. MODEL=gateway ./run.sh  (MODEL defaults to echo)
#        ADDR=127.0.0.1:8081 MODEL=demo DELAY=8s LABEL=pod-b ./run.sh
set -eu
cd "$(dirname "$0")"

if [ ! -f .env ]; then
	echo "run.sh: .env is missing; copy .env.example and fill it in" >&2
	exit 1
fi

go build -o bin/larkbot .

set -a
. ./.env
set +a

# ADDR, STORE, MODEL, DELAY and LABEL pick the listen address, the shared store, the model,
# a per-model-call delay and the name shown in cards. -advertise follows ADDR.
set -- -model "${MODEL:-echo}" -store "${STORE:-./data/sessions}" -addr "${ADDR:-127.0.0.1:8080}" -delay "${DELAY:-0}" "$@"
if [ -n "${LABEL:-}" ]; then set -- -instance-label "$LABEL" "$@"; fi
exec ./bin/larkbot "$@"
