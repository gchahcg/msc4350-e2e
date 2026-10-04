#!/usr/bin/env bash
# Copyright (c) 2026 gchahcg
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.
# Orchestration for the MSC4350 end-to-end tests. See README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$ROOT/artifacts/run"
LAST="$ROOT/artifacts/last"
BIN="$ROOT/bin/fakebridge"
HS=http://127.0.0.1:18008
INJECT=127.0.0.1:29400
COMPOSE=(docker compose -f "$ROOT/compose.yml")
SYNAPSE_IMAGE="$(sed -n 's/^ *image: \(matrixdotorg\/synapse@.*\)$/\1/p' "$ROOT/compose.yml")"
export E2E_UID="$(id -u)" E2E_GID="$(id -g)"

log() { echo "[run.sh] $*" >&2; }

build() {
  log "building fakebridge"
  (cd "$ROOT" && go build -o "$BIN" ./fakebridge)
}

# variant -> "msc4190=.. msc4350=.."
variant_opts() {
  case "${1:-default}" in
    default) echo "msc4190=true msc4350=true" ;;
    nomsc4350) echo "msc4190=true msc4350=false" ;;
    nomsc4190) echo "msc4190=false msc4350=true" ;;
    *) log "unknown variant: $1"; exit 2 ;;
  esac
}

render_config() {
  local variant="$1"
  if [[ ! -f "$RUN/example.yaml" ]]; then
    "$BIN" -e -c "$RUN/example.yaml" >/dev/null
  fi
  # shellcheck disable=SC2046
  python3 "$ROOT/render_bridge_config.py" "$RUN/example.yaml" "$RUN/config.yaml" "$RUN" $(variant_opts "$variant")
}

bridge_stop() {
  if [[ -f "$RUN/bridge.pid" ]]; then
    local pid
    pid="$(cat "$RUN/bridge.pid")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.2; done
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$RUN/bridge.pid"
  fi
}

bridge_start() {
  local variant="${1:-default}"
  bridge_stop
  render_config "$variant"
  log "starting fakebridge (variant: $variant)"
  cd "$RUN"
  FAKEBRIDGE_INJECT_ADDR="$INJECT" setsid "$BIN" -c "$RUN/config.yaml" -n >>"$RUN/bridge.stdout" 2>&1 </dev/null &
  echo $! >"$RUN/bridge.pid"
  cd "$ROOT"
  for _ in $(seq 1 150); do
    if curl -fsS "http://$INJECT/health" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "$(cat "$RUN/bridge.pid")" 2>/dev/null; then
      log "bridge exited during startup, tail of output:"
      tail -n 30 "$RUN/bridge.stdout" >&2
      exit 1
    fi
    sleep 0.2
  done
  log "bridge did not become healthy"
  tail -n 30 "$RUN/bridge.stdout" >&2
  exit 1
}

wait_synapse() {
  for _ in $(seq 1 150); do
    if curl -fsS "$HS/_matrix/client/versions" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  log "synapse did not become healthy"
  "${COMPOSE[@]}" logs --tail 50 synapse >&2 || true
  exit 1
}

cmd_up() {
  local variant="${1:-default}"
  if [[ -d "$RUN" ]]; then
    log "$RUN already exists, run '$0 down' first"
    exit 1
  fi
  mkdir -p "$RUN/synapse"
  build

  # Bridge config + registration come first, the registration is loaded by Synapse at startup.
  render_config "$variant"
  "$BIN" -g -c "$RUN/config.yaml" -r "$RUN/registration.yaml" >/dev/null
  cp "$RUN/registration.yaml" "$RUN/synapse/registration.yaml"

  log "generating synapse config"
  docker run --rm -e SYNAPSE_SERVER_NAME=test.local -e SYNAPSE_REPORT_STATS=no \
    -e UID="$E2E_UID" -e GID="$E2E_GID" -v "$RUN/synapse:/data" "$SYNAPSE_IMAGE" generate >/dev/null
  python3 "$ROOT/patch_synapse_config.py" "$RUN/synapse/homeserver.yaml"

  log "starting synapse"
  "${COMPOSE[@]}" up -d synapse
  wait_synapse
  bridge_start "$variant"
  log "stack is up (homeserver $HS, inject http://$INJECT)"
}

# synapse <default|legacy>: switch msc4190 on or off on the homeserver side and restart synapse.
# The appservice registration carries an msc4190 flag too, so it is regenerated to match.
cmd_synapse() {
  local mode="${1:-default}" variant
  case "$mode" in
    default) variant=default; python3 "$ROOT/patch_synapse_config.py" "$RUN/synapse/homeserver.yaml" ;;
    legacy) variant=nomsc4190; python3 "$ROOT/patch_synapse_config.py" "$RUN/synapse/homeserver.yaml" legacy ;;
    *) log "unknown synapse mode: $mode"; exit 2 ;;
  esac
  render_config "$variant"
  "$BIN" -g -c "$RUN/config.yaml" -r "$RUN/registration.yaml" >/dev/null
  cp "$RUN/registration.yaml" "$RUN/synapse/registration.yaml"
  "${COMPOSE[@]}" restart synapse >&2
  wait_synapse
}

cmd_down() {
  bridge_stop
  if [[ -d "$RUN" ]]; then
    rm -rf "$LAST"
    mkdir -p "$LAST"
    cp -f "$RUN/bridge.stdout" "$LAST/" 2>/dev/null || true
    "${COMPOSE[@]}" logs --no-color synapse >"$LAST/synapse.log" 2>&1 || true
  fi
  "${COMPOSE[@]}" --profile element down -v --remove-orphans >&2 || true
  # Synapse data is written as our uid, but be safe about leftovers.
  rm -rf "$RUN" 2>/dev/null || docker run --rm -v "$ROOT/artifacts:/a" busybox rm -rf /a/run
  log "stack is down (logs of the last run are in $LAST)"
}

cmd_test() {
  (cd "$ROOT" && E2E_RUN_DIR="$RUN" E2E_HS="$HS" E2E_INJECT="http://$INJECT" E2E_SCRIPT="$ROOT/run.sh" \
    go test -count=1 -v ./tests/... "$@")
}

cmd_all() {
  local rc=0
  cmd_up default
  cmd_test "$@" || rc=$?
  cmd_down
  exit "$rc"
}

# keep: bring the stack up and add Element behind a self-signed HTTPS proxy for the manual look.
# E2E_HOST overrides the address printed in the link (default: this machine's LAN address).
cmd_keep() {
  local host="${E2E_HOST:-$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1)}"
  host="${host:-localhost}"
  export E2E_HOST="$host" E2E_BIND="$host"
  cmd_up default
  sed "s/@HOST@/$host/g" "$ROOT/element/config.json.tpl" >"$RUN/element-config.json"
  "${COMPOSE[@]}" --profile element up -d element caddy
  cmd_register alice alicepw >/dev/null
  log "------------------------------------------------------------------"
  log "Element:  https://$host:18443   (self-signed certificate: accept the browser warning)"
  log "Login:    alice / alicepw   (server test.local)"
  log "Then run: $0 say     to send an encrypted message from a bridge ghost, and read it in the room"
  log "Tear down with: $0 down"
  log "------------------------------------------------------------------"
}

# wasm <stock|FILE>: serve Element with its crypto wasm replaced by FILE (or restore the stock one).
# The replacement must export the same names as the stock module, see tools/match_wasm_exports.py.
# The override is recorded in artifacts/run/compose.wasm.yml so later compose calls keep it.
cmd_wasm() {
  local target="${1:?usage: $0 wasm <stock|FILE>}" override="$RUN/compose.wasm.yml" image path
  image="$(sed -n 's/^ *image: \(ghcr.io\/element-hq\/element-web:.*\)$/\1/p' "$ROOT/compose.yml")"
  if [[ "$target" == stock ]]; then
    rm -f "$override"
  else
    [[ -f "$target" ]] || { log "no such file: $target"; exit 2; }
    # The crypto module is the biggest wasm file in the bundle.
    path="$(docker run --rm --entrypoint sh "$image" -c 'ls -S /app/bundles/*/*.wasm | head -n1')"
    log "replacing $path in the element container"
    cat >"$override" <<EOF2
services:
  element:
    volumes:
      - $(cd "$(dirname "$target")" && pwd)/$(basename "$target"):$path:ro
EOF2
  fi
  local files=(-f "$ROOT/compose.yml")
  [[ -f "$override" ]] && files+=(-f "$override")
  docker compose "${files[@]}" --profile element up -d --force-recreate element >&2
  log "element is serving the $target wasm"
}

# say [text] [ghost]: deliver a message from a bridge ghost to alice (the manual-look user).
cmd_say() {
  local text="${1:-hello from the ghost}" ghost="${2:-bob}"
  curl -fsS -XPOST "http://$INJECT/inject" \
    -d "{\"user_mxid\":\"@alice:test.local\",\"ghost\":\"$ghost\",\"text\":\"$text\"}"
  echo
}

cmd_register() {
  (cd "$ROOT" && E2E_HS="$HS" go run ./tests/register "$@")
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  down) cmd_down ;;
  test) shift; cmd_test "$@" ;;
  all) shift; cmd_all "$@" ;;
  keep) cmd_keep ;;
  say) shift; cmd_say "$@" ;;
  wasm) shift; cmd_wasm "$@" ;;
  bridge) shift; bridge_start "${1:-default}" ;;
  bridge-stop) bridge_stop ;;
  synapse) shift; cmd_synapse "$@" ;;
  register) shift; cmd_register "$@" ;;
  *) echo "usage: $0 up [variant] | down | test [go test args] | all | keep | say [text] [ghost] | wasm <stock|file> | bridge <variant> | bridge-stop | synapse <default|legacy> | register <user> <password>" >&2; exit 2 ;;
esac
