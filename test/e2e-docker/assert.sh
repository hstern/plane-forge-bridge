#!/usr/bin/env bash
# Assert that the bridge successfully translated a forge issue.opened event
# into a Plane create-issue POST recorded by plane-stub.
#
# Required env:
#   PLANE_STUB_URL    plane-stub control plane (e.g. http://localhost:8081)
#   FORGE_ORG         org name used during seeding (asserter checks external_source)
#   FORGE_REPO        repo name used during seeding
#   FORGE_ISSUE_ID    forge issue ID we created
#   ISSUE_WAIT_SEC    how long to wait for the bridge to post (default 60)

set -euo pipefail
log() { printf '[assert] %s\n' "$*" >&2; }

: "${PLANE_STUB_URL:?}"
: "${FORGE_ORG:?}"
: "${FORGE_REPO:?}"
: "${FORGE_ISSUE_ID:?}"

wait_sec=${ISSUE_WAIT_SEC:-60}

expected_source="forge:${FORGE_ORG}/${FORGE_REPO}"
expected_id="${FORGE_ISSUE_ID}"

# Poll /_/recorded for a POST that has external_source matching the link.
log "polling $PLANE_STUB_URL/_/recorded for ~${wait_sec}s for the create POST"
found_json=""
for i in $(seq 1 "$wait_sec"); do
  body=$(curl -fsS "$PLANE_STUB_URL/_/recorded" 2>/dev/null || echo '[]')
  found_json=$(printf '%s' "$body" | jq -c --arg src "$expected_source" --arg eid "$expected_id" '
    .[] |
    select(.method == "POST") |
    select(.path | test("/api/v1/workspaces/[^/]+/projects/[^/]+/issues/?$")) |
    (.body | try fromjson) as $b |
    select($b.external_source == $src and ($b.external_id|tostring) == $eid) |
    {path: .path, body: $b}
  ' | head -n1)
  if [ -n "$found_json" ]; then
    log "matched a recorded create after ${i}s"
    break
  fi
  sleep 1
done

if [ -z "$found_json" ]; then
  log "ERROR: no matching create POST recorded within ${wait_sec}s"
  log "all recorded calls so far:"
  curl -fsS "$PLANE_STUB_URL/_/recorded" | jq . >&2 || true
  exit 1
fi

log "matched payload:"
printf '%s\n' "$found_json" | jq . >&2

# Assert the loop-break marker is in the description_html. This is the
# durable defense against echo loops; without it the round-trip via Plane
# would re-fire as a new forge event when step 10 lands.
desc=$(printf '%s' "$found_json" | jq -r '.body.description_html // ""')
if ! printf '%s' "$desc" | grep -q "<!-- pfb:src=forge,evt="; then
  log "ERROR: description_html missing loop-break marker"
  log "description_html=$desc"
  exit 1
fi
log "loop-break marker present in description_html"

# Assert the name field was populated from the issue title.
name=$(printf '%s' "$found_json" | jq -r '.body.name // ""')
if [ -z "$name" ]; then
  log "ERROR: name field empty in recorded create"
  exit 1
fi
log "issue title carried through: name=$name"

log "all assertions passed"
