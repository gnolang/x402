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
# How long one HTTP probe waits: a listener that accepts and never answers, such
# as a suspended process, would otherwise hang the script.
readonly probe_seconds=2

workdir="$(mktemp -d)"
readonly workdir
pids=()
names=()

# stop_all stops what start started. The logs stay for a look afterwards; the
# binaries go, and the directory too when nothing ran.
stop_all() {
  if ((${#pids[@]} > 0)); then
    kill "${pids[@]}" 2>/dev/null || true
    wait "${pids[@]}" 2>/dev/null || true
  fi
  rm -f "${workdir}/gnofacilitator" "${workdir}/weather"
  rmdir "${workdir}" 2>/dev/null || true
}
trap stop_all EXIT

# start runs a command in the background, its output in $workdir/<name>.log.
start() {
  local name="$1"
  shift
  "$@" >"${workdir}/${name}.log" 2>&1 &
  pids+=("$!")
  names+=("${name}")
}

# answers reports whether url answers over HTTP.
answers() {
  curl -s -o /dev/null --max-time "${probe_seconds}" "$1"
}

# await waits until url answers over HTTP, or fails naming the log of the
# process started last, the one meant to serve it.
await() {
  local name="$1" url="$2" pid="${pids[${#pids[@]} - 1]}" waited
  for ((waited = 0; waited < startup_seconds; waited++)); do
    if answers "${url}"; then
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

# refuse_taken stops the demo when something already answers at url, naming the
# make variable that moves the demo's own process elsewhere.
refuse_taken() {
  local url="$1" knob="$2"
  if answers "${url}"; then
    echo "Error: something already answers at ${url}; stop it, or move the demo's with ${knob}" >&2
    exit 1
  fi
}

: "${RPC:?run through make demo}" "${FACILITATOR:?run through make demo}" "${SELLER_URL:?run through make demo}"
: "${NETWORK:?run through make demo}" "${FACILITATOR_ARGS:?run through make demo}" "${SELLER_ARGS:?run through make demo}"

refuse_taken "${RPC}/status" RPC
refuse_taken "${FACILITATOR}/supported" FACILITATOR_LISTEN
refuse_taken "${SELLER_URL}" LISTEN

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

# bash 3.2 has no wait -n, so each process is checked in turn: one that dies
# stops the demo rather than leaving the other two serving.
while :; do
  for ((i = 0; i < ${#pids[@]}; i++)); do
    if ! kill -0 "${pids[i]}" 2>/dev/null; then
      echo "Error: ${names[i]} exited; see ${workdir}/${names[i]}.log" >&2
      exit 1
    fi
  done
  sleep 1
done
