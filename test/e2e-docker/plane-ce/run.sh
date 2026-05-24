#!/usr/bin/env bash
# Bring up the Plane CE v1.3.1 stack used by the bridge's real-Plane
# round-trip integration test, seed an admin user / workspace / project /
# API token, and emit a single JSON line with the connection details for
# callers to source.
#
# Usage:
#   bash test/e2e-docker/plane-ce/run.sh up      # bring up + seed; print JSON
#   bash test/e2e-docker/plane-ce/run.sh down    # tear down
#   bash test/e2e-docker/plane-ce/run.sh seed    # re-run the seed (idempotent)
#
# The "up" form is the entry point for CI: it blocks until plane-api is
# accepting requests, runs the seed, and prints
#   {"workspace_slug":"...","project_id":"...","api_token":"...", ...}
# on the last stdout line. Earlier lines are progress diagnostics.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yaml"
SEED_SCRIPT="${SCRIPT_DIR}/seed.py"

API_HOST_PORT="${PFB_PLANE_HOST_PORT:-8765}"
API_HEALTH_URL="http://localhost:${API_HOST_PORT}/api/instances/"

# Compose helper. Prefer the v2 plugin (`docker compose`); fall back to
# the standalone `docker-compose` so CI images without the plugin work.
compose() {
  if docker compose version >/dev/null 2>&1; then
    docker compose -f "$COMPOSE_FILE" "$@"
  else
    docker-compose -f "$COMPOSE_FILE" "$@"
  fi
}

log() { echo "pfb-e2e-plane:" "$@" >&2; }

wait_for_api() {
  local i max=60
  for ((i=1; i<=max; i++)); do
    # /api/instances/ returns 200 even before configure_instance — the
    # endpoint always renders the Instance record (or its absence) as JSON.
    if curl -fsS "$API_HEALTH_URL" >/dev/null 2>&1; then
      log "plane-api healthy after ${i}s"
      return 0
    fi
    sleep 5
  done
  log "ERROR: plane-api never became healthy"
  compose logs --tail=80 plane-api >&2
  return 1
}

cmd_up() {
  log "bringing up Plane CE v1.3.1 stack (this takes ~90s on a cold cache)"
  compose up -d
  wait_for_api
  cmd_seed
}

cmd_seed() {
  log "seeding admin user + workspace + project + API token"
  # Pass the desired API token through if the caller wants a stable value
  # (CI sets PFB_PLANE_API_KEY=ci-plane-key-<run-id> for grep-ability in
  # logs); otherwise seed.py generates a random one.
  local token_env=""
  if [[ -n "${PFB_PLANE_API_KEY:-}" ]]; then
    token_env="-e PFB_PLANE_API_KEY=${PFB_PLANE_API_KEY}"
  fi

  # Copy the seed script into the container, then run it with the plane
  # codebase on PYTHONPATH. We don't bind-mount because compose-managed
  # containers in CI may not have host paths accessible.
  docker cp "$SEED_SCRIPT" plane-ce-plane-api-1:/tmp/seed.py
  # shellcheck disable=SC2086
  compose exec -T $token_env -e PYTHONPATH=/code plane-api \
    python /tmp/seed.py
}

cmd_down() {
  log "tearing down Plane CE stack"
  compose down -v --remove-orphans
}

case "${1:-up}" in
  up)   cmd_up ;;
  seed) cmd_seed ;;
  down) cmd_down ;;
  *)
    echo "usage: $0 {up|seed|down}" >&2
    exit 64
    ;;
esac
