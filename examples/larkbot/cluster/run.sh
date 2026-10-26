#!/bin/sh
# A local kind cluster with three bot replicas, a fake Lark in front and Gotato's fake gateway as the model.
#   cluster/run.sh up       create the cluster (if missing), build and load the images, apply, wait ready
#   cluster/run.sh smoke    one message, one card, one Transcript
#   cluster/run.sh s1       rolling deploy with Runs in flight
#   cluster/run.sh s2 [delete|crash]   kill the pod that writes a Run (delete: kubectl delete --force; crash: SIGKILL)
#   cluster/run.sh s3       scale the bot Deployment 3 -> 1 -> 5 with Runs in flight and between messages
#   cluster/run.sh s4       two or more bots race for the same Session (bursts into one thread)
#   ACK_DELAY=4s cluster/run.sh up; cluster/run.sh s5   bots ack late (> fakelark's 3 s), so events are redelivered
#   RETRY_BUDGET=0 cluster/run.sh up   the bot's -retry-budget (default 20s; 0 = no retry); `up` again re-applies it
#   ACK_DELAY=4s cluster/run.sh up     the bot's test-only -ack-delay (default 0s); `up` again re-applies it
#   cluster/run.sh s6       bash tool: a spilled result is read with bash by the pod that takes the Session over (writer killed mid-Run)
#   cluster/run.sh s7       MCP tools in flight during a rolling deploy of the bot and a restart of the MCP server
#   MCP_STATELESS=true cluster/run.sh s7   the MCP server answers statelessly (no Mcp-Session-Id); default false = the SDK's default
#   cluster/run.sh s8       tool search: is a tool the model loaded still loaded on the pod that takes the Session over?
#   cluster/run.sh s9       compaction after a takeover: the loaded tool is unloaded, read_segment works from the other pod (bot -recap truncate)
#   cluster/run.sh s10      graceful roll of the MCP server (SIGTERM, fakemcp -drain 20s) with calls in flight, next to a SIGKILL of the container (MCP_STATELESS=true for a stateless server)
#   cluster/run.sh tools LIST   restart the bot with -tools LIST (bash,export,mcp,mcp-search) and the scripted model; no list = the defaults
#   (s6-s8 restart the bot with their own -tools and -script flags; `up` again restores the defaults for smoke and s1-s5)
#   cluster/run.sh status | logs [larkbot|fakelark|fakemcp|fakegateway] | down
# Run it from a clean clone of Gotato: the image build takes the repo root as its context, because go.mod replaces Gotato
# and its adapters with the local checkout. Nothing connects to the real Lark. Your own kubectl context is never touched: the cluster has its own
# kubeconfig file (cluster/.state/kubeconfig), and every kubectl call here names it and the context kind-gotato.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
appdir=$(dirname "$here")          # examples/larkbot
root=$(cd "$appdir/../.." && pwd)    # the Gotato repo root: the image build context
name=gotato
ns=gotato
state="$here/.state"
KCFG="$state/kubeconfig"
export KCFG

k() { kubectl --kubeconfig "$KCFG" --context "kind-$name" "$@"; }
kn() { k -n "$ns" "$@"; }
say() { printf '\n== %s\n' "$*"; }

tag() {
	sha=$(git -C "$root" rev-parse --short HEAD)
	if [ -n "$(git -C "$root" status --porcelain)" ]; then
		sum=$(cd "$root" && { git diff HEAD; git ls-files -o --exclude-standard | xargs cat 2>/dev/null; } | shasum | cut -c1-6)
		sha="$sha-dirty$sum"
	fi
	echo "dev-$sha"
}

need() { command -v "$1" >/dev/null 2>&1 || { echo "run.sh: $1 is not installed" >&2; exit 1; }; }

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "$name"; }

cmd_up() {
	need docker; need kind; need kubectl
	mkdir -p "$state"
	if cluster_exists; then
		say "kind cluster $name exists"
		kind export kubeconfig --name "$name" --kubeconfig "$KCFG" >/dev/null
	else
		say "creating kind cluster $name"
		kind create cluster --name "$name" --kubeconfig "$KCFG" --wait 120s
	fi
	# The store is a hostPath on the one node. The images run as uid 65532, so that user must own it.
	docker exec "$name-control-plane" sh -c 'mkdir -p /var/gotato && chown 65532:65532 /var/gotato'

	t=$(tag)
	say "building images, tag $t"
	for img in larkbot fakelark fakemcp fakegateway; do
		docker build -q -f "$here/Dockerfile" --target "$img" -t "$img:$t" "$root" >/dev/null
		echo "built $img:$t ($(docker image inspect "$img:$t" --format '{{.Size}}' | awk '{printf "%.1f MB", $1/1e6}'))"
	done
	say "loading images into kind"
	kind load docker-image --name "$name" "larkbot:$t" "fakelark:$t" "fakemcp:$t" "fakegateway:$t"

	echo "$t" >"$state/tag"
	apply_all "$t" "${TOOLS:-}"
	cmd_status
}

# apply_all templates and applies every manifest; the bot gets -tools $2 (and the scripted test model when $2 is set).
apply_all() {
	t=$1; tools=$2
	script=false; [ -n "$tools" ] && script=true
	say "applying manifests (bot -tools='$tools')"
	k apply -f "$here/namespace.yaml" >/dev/null
	for f in fakegateway fakelark fakemcp larkbot; do
		sed -e "s/__TAG__/$t/g" -e "s/__RETRY_BUDGET__/${RETRY_BUDGET:-20s}/g" -e "s/__ACK_DELAY__/${ACK_DELAY:-0s}/g" \
			-e "s/__TOOLS__/$tools/g" -e "s/__SCRIPT__/$script/g" -e "s/__MCP_STATELESS__/${MCP_STATELESS:-false}/g" -e "s/__RECAP__/${RECAP:-none}/g" -e "s/__MCP_TRACE__/${MCP_TRACE:-false}/g" -e "s/__MCP_DRAIN__/${MCP_DRAIN:-0s}/g" "$here/$f.yaml" | kn apply -f -
	done
	say "waiting for rollouts"
	for d in fakegateway fakelark fakemcp larkbot; do kn rollout status "deployment/$d" --timeout=180s; done
}

# with_tools re-applies the manifests with the bot's -tools set to $1, for the scenarios that need them.
with_tools() {
	[ -f "$state/tag" ] || { echo "run.sh: no cluster; run: cluster/run.sh up" >&2; exit 1; }
	apply_all "$(cat "$state/tag")" "$1"
}

cmd_down() {
	if cluster_exists; then kind delete cluster --name "$name"; else echo "no kind cluster $name"; fi
	rm -rf "$state"
}

cmd_status() {
	kn get deploy,pods -o wide
}

cmd_logs() {
	app=${1:-larkbot}
	kn logs -l "app=$app" --prefix --tail=200 --all-containers --max-log-requests=10
}

driver() {
	[ -f "$KCFG" ] || { echo "run.sh: no cluster; run: cluster/run.sh up" >&2; exit 1; }
	need go
	(cd "$appdir" && go build -o "$state/driver" ./cluster/driver)
	"$state/driver" -kubeconfig "$KCFG" -context "kind-$name" -namespace "$ns" "$@"
}

case "${1:-}" in
up) cmd_up ;;
down) cmd_down ;;
status) cmd_status ;;
logs) shift; cmd_logs "$@" ;;
smoke | s1 | s3 | s4 | s5) driver "$1" ;;
s2) driver -kill "${2:-delete}" s2 ;;
tools) with_tools "${2:-}" ;;
s6) with_tools bash,export; driver s6 ;;
s7) with_tools mcp; driver s7 ;;
s8) with_tools mcp-search; driver s8 ;;
s9) RECAP=truncate with_tools mcp-search; driver s9 ;;
s10) MCP_DRAIN=${MCP_DRAIN:-20s} with_tools mcp; driver s10 ;;
*) sed -n '2,25p' "$0" >&2; exit 2 ;;
esac
