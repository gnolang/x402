#!/usr/bin/env bash
# The weather demo in one command: a local gnodev node, the facilitator and the
# seller, each started once the one before it answers, all stopped together on
# Ctrl-C. `make demo` runs it with the Makefile's settings, which arrive in the
# environment; `make pay` then pays the seller from another terminal.
#
# Written for bash 3.2, the one macOS ships.
set -o errexit
set -o nounset
set -o pipefail

readonly startup_seconds=120

workdir="$(mktemp -d)"
readonly workdir
pids=()

stop_all() {
  if ((${#pids[@]} > 0)); then
    kill "${pids[@]}" 2>/dev/null || true
    wait "${pids[@]}" 2>/dev/null || true
  fi
}
trap stop_all EXIT

# start runs a command in the background, its output in $workdir/<name>.log.
start() {
  local name="$1"
  shift
  "$@" >"${workdir}/${name}.log" 2>&1 &
  pids+=("$!")
}

# await waits until url answers over HTTP, or fails naming the log of the
# process started last, the one meant to serve it.
await() {
  local name="$1" url="$2" pid="${pids[${#pids[@]} - 1]}" waited
  for ((waited = 0; waited < startup_seconds; waited++)); do
    if curl -s -o /dev/null "${url}"; then
      echo "${name} is up"
      return
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      echo "Error: ${name} exited; see ${workdir}/${name}.log" >&2
      exit 1
    fi
    sleep 1
  done
  echo "Error: ${name} did not answer at ${url} within ${startup_seconds}s; see ${workdir}/${name}.log" >&2
  exit 1
}

for url in "${RPC}/status" "${FACILITATOR}/supported" "${SELLER_URL}"; do
  if curl -s -o /dev/null "${url}"; then
    echo "Error: something already answers at ${url}; stop it or override the address" >&2
    exit 1
  fi
done

read -r -a facilitator_args <<<"${FACILITATOR_ARGS}"
read -r -a seller_args <<<"${SELLER_ARGS}"

echo "building the facilitator and the seller"
"${GO:-go}" build -o "${workdir}/gnofacilitator" ../../cmd/gnofacilitator
"${GO:-go}" build -o "${workdir}/weather" .

start gnodev gnodev local -chain-id "${NETWORK#gno:}" -node-rpc-listener "${RPC#*://}" -no-web -no-watch
await gnodev "${RPC}/status"
start facilitator "${workdir}/gnofacilitator" "${facilitator_args[@]}"
await facilitator "${FACILITATOR}/supported"
start seller "${workdir}/weather" "${seller_args[@]}"
await seller "${SELLER_URL}"

cat <<EOF

The weather demo is up. Logs are in ${workdir}.
  open ${SELLER_URL} in a browser
  run  make pay      in another terminal to buy the weather, as gno's test1 key
Ctrl-C stops all three.
EOF
wait
