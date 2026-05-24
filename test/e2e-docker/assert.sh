#!/usr/bin/env bash
# Assert that the bridge successfully translated a forge issue.opened event
# into a Plane create-issue POST recorded by plane-stub. Optionally also
# asserts a comment create was translated.
#
# Required env:
#   PLANE_STUB_URL      plane-stub control plane (e.g. http://localhost:8081)
#   FORGE_ORG           org name used during seeding
#   FORGE_REPO          repo name used during seeding
#   FORGE_ISSUE_NUMBER  forge issue number we created (the user-facing number)
#   ISSUE_WAIT_SEC      how long to wait for the bridge to post (default 60)
#
# Optional env (enables the comment assertion):
#   ASSERT_COMMENT      "1" to also check that a comment POST was recorded

set -euo pipefail
log() { printf '[assert] %s\n' "$*" >&2; }

: "${PLANE_STUB_URL:?}"
: "${FORGE_ORG:?}"
: "${FORGE_REPO:?}"
: "${FORGE_ISSUE_NUMBER:?}"

wait_sec=${ISSUE_WAIT_SEC:-60}

expected_source="forge:${FORGE_ORG}/${FORGE_REPO}"
expected_id="${FORGE_ISSUE_NUMBER}"

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

# Optional: assert v2 identity resolution put the matched Plane member
# UUID into the create POST's assignees field.
if [ "${ASSERT_ASSIGNEE:-}" != "" ]; then
  expected="${ASSERT_ASSIGNEE}"
  match=$(printf '%s' "$found_json" | jq -c --arg a "$expected" '
    .body.assignees // [] | map(select(. == $a)) | first
  ')
  if [ -z "$match" ] || [ "$match" = "null" ]; then
    log "ERROR: expected assignee UUID $expected not in recorded create"
    log "actual assignees: $(printf '%s' "$found_json" | jq -c '.body.assignees // []')"
    exit 1
  fi
  log "v2 identity resolved: assignees carries $expected"
fi

# Optional: assert that the create POST carries plane label UUIDs and that
# at least one POST to /labels/ happened (the bridge's auto-create path).
if [ "${ASSERT_LABELS:-0}" = "1" ]; then
  labels_len=$(printf '%s' "$found_json" | jq -r '.body.labels | length // 0')
  if [ "$labels_len" -lt 1 ]; then
    log "ERROR: recorded create has no labels"
    printf '%s\n' "$found_json" | jq . >&2
    exit 1
  fi
  log "issue create carried ${labels_len} label UUID(s)"

  label_creates=$(curl -fsS "$PLANE_STUB_URL/_/recorded" |
    jq '[.[] | select(.method == "POST" and (.path | test("/labels/?$")))] | length')
  if [ "$label_creates" -lt 1 ]; then
    log "ERROR: no POSTs to /labels/ recorded (auto-create path didn't fire)"
    exit 1
  fi
  log "auto-create path fired (${label_creates} POSTs to /labels/)"
fi

# Optional: assert that a comment translation was also recorded.
if [ "${ASSERT_COMMENT:-0}" = "1" ]; then
  log "polling for the bridge's forge→plane comment create"
  comment_json=""
  for i in $(seq 1 "$wait_sec"); do
    body=$(curl -fsS "$PLANE_STUB_URL/_/recorded" 2>/dev/null || echo '[]')
    comment_json=$(printf '%s' "$body" | jq -c '
      .[] |
      select(.method == "POST") |
      select(.path | test("/api/v1/workspaces/[^/]+/projects/[^/]+/issues/[^/]+/comments/?$")) |
      (.body | try fromjson) as $b |
      select($b.comment_html != null) |
      {path: .path, body: $b}
    ' | head -n1)
    if [ -n "$comment_json" ]; then
      log "matched a recorded comment create after ${i}s"
      break
    fi
    sleep 1
  done
  if [ -z "$comment_json" ]; then
    log "ERROR: no matching comment POST recorded within ${wait_sec}s"
    curl -fsS "$PLANE_STUB_URL/_/recorded" | jq . >&2 || true
    exit 1
  fi
  log "matched comment payload:"
  printf '%s\n' "$comment_json" | jq . >&2
  c_html=$(printf '%s' "$comment_json" | jq -r '.body.comment_html // ""')
  if ! printf '%s' "$c_html" | grep -q "<!-- pfb:src=forge,evt="; then
    log "ERROR: comment_html missing loop-break marker"
    log "comment_html=$c_html"
    exit 1
  fi
  log "loop-break marker present in comment_html"
fi

log "all assertions passed"
