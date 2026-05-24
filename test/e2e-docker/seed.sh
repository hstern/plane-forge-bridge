#!/usr/bin/env bash
# Seed a freshly-started forge (Forgejo or Gitea — same API) with:
#   - an admin user (the install-lock env var is set in the workflow so the
#     container starts already installed; `forgejo admin user create` works
#     without going through the web wizard)
#   - an API token (printed on stdout)
#   - an organization
#   - a repository in that organization
#   - a webhook on that repository pointed at the bridge with the shared
#     HMAC secret
#
# Required env:
#   FORGE_URL          base URL reachable from the runner (e.g. http://localhost:3000)
#   FORGE_CONTAINER    name of the running forge container (for `docker exec`)
#   FORGE_FLAVOR       "forgejo" or "gitea" — chooses the admin CLI binary
#   FORGE_ADMIN_USER   admin username to create
#   FORGE_ADMIN_PASS   admin password
#   FORGE_ADMIN_EMAIL  admin email
#   FORGE_ORG          org to create
#   FORGE_REPO         repo name to create under the org
#   BRIDGE_WEBHOOK_URL webhook target (e.g. http://bridge:8080/forge/webhook)
#   PFB_FORGE_WEBHOOK_SECRET  HMAC secret to register with the webhook
#
# Prints the API token on stdout. Logs go to stderr.

set -euo pipefail

log() { printf '[seed] %s\n' "$*" >&2; }

: "${FORGE_URL:?}"
: "${FORGE_CONTAINER:?}"
: "${FORGE_FLAVOR:?}"
: "${FORGE_ADMIN_USER:?}"
: "${FORGE_ADMIN_PASS:?}"
: "${FORGE_ADMIN_EMAIL:?}"
: "${FORGE_ORG:?}"
: "${FORGE_REPO:?}"
: "${BRIDGE_WEBHOOK_URL:?}"
: "${PFB_FORGE_WEBHOOK_SECRET:?}"

# Wait for the forge HTTP API.
log "waiting for $FORGE_URL/api/v1/version"
ready=0
for i in $(seq 1 120); do
  code=$(curl -fsS -o /dev/null -w '%{http_code}' "$FORGE_URL/api/v1/version" 2>/dev/null || true)
  if [ "$code" = "200" ]; then
    log "$FORGE_FLAVOR ready after ${i}s"
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  log "ERROR: $FORGE_FLAVOR did not become ready within 120s"
  exit 1
fi

# Admin user — both Forgejo and Gitea ship the same admin subcommand.
log "creating admin user $FORGE_ADMIN_USER"
if ! docker exec --user git "$FORGE_CONTAINER" \
      "$FORGE_FLAVOR" admin user create \
        --username "$FORGE_ADMIN_USER" \
        --password "$FORGE_ADMIN_PASS" \
        --email "$FORGE_ADMIN_EMAIL" \
        --admin \
        --must-change-password=false >&2; then
  log "admin user create returned non-zero (likely already exists); continuing"
fi

# Mint an API token. Names must be unique per-user; suffix with epoch nanos.
token_name="pfb-integ-$(date +%s%N)-$$"
log "minting API token $token_name"
token_response=$(
  curl -fsS -u "$FORGE_ADMIN_USER:$FORGE_ADMIN_PASS" \
    -H 'Content-Type: application/json' \
    -X POST \
    -d "{\"name\":\"$token_name\",\"scopes\":[\"write:admin\",\"write:repository\",\"write:issue\",\"write:organization\",\"write:user\"]}" \
    "$FORGE_URL/api/v1/users/$FORGE_ADMIN_USER/tokens"
)
api_token=$(printf '%s' "$token_response" | jq -r .sha1)
if [ -z "$api_token" ] || [ "$api_token" = "null" ]; then
  log "ERROR: failed to mint token: $token_response"
  exit 1
fi
log "minted token (masked in logs)"

# Flip the admin user's email visibility to public. CLI-created users
# default to keep-email-private regardless of service.DEFAULT_*, so
# without this the webhook sender.email arrives as
# <id>+<login>@noreply.localhost and the bridge's v2 identity resolver
# has nothing to match against the configured plane member.
log "setting admin user email visibility public"
http_code=$(
  curl -fsS -o /dev/null -w '%{http_code}' \
    -H "Authorization: token $api_token" \
    -H 'Content-Type: application/json' \
    -X PATCH \
    -d "{\"source_id\":0,\"login_name\":\"$FORGE_ADMIN_USER\",\"email\":\"$FORGE_ADMIN_EMAIL\",\"visibility\":\"public\"}" \
    "$FORGE_URL/api/v1/admin/users/$FORGE_ADMIN_USER" || true
)
if [ "$http_code" != "200" ]; then
  log "WARN: admin user PATCH visibility=public returned $http_code (continuing)"
fi
# The visibility=public above governs profile visibility; the
# user-settings call below is what actually flips email visibility.
http_code=$(
  curl -fsS -o /dev/null -w '%{http_code}' \
    -H "Authorization: token $api_token" \
    -H 'Content-Type: application/json' \
    -X PATCH \
    -d '{"email":"'"$FORGE_ADMIN_EMAIL"'","hide_email":false}' \
    "$FORGE_URL/api/v1/user/settings" || true
)
if [ "$http_code" != "200" ]; then
  log "WARN: user settings PATCH hide_email=false returned $http_code (continuing)"
fi

# Org. 422 = already exists; treat as success.
log "creating organization $FORGE_ORG"
http_code=$(
  curl -fsS -o /dev/null -w '%{http_code}' \
    -H "Authorization: token $api_token" \
    -H 'Content-Type: application/json' \
    -X POST \
    -d "{\"username\":\"$FORGE_ORG\",\"visibility\":\"public\"}" \
    "$FORGE_URL/api/v1/orgs" || true
)
if [ "$http_code" != "201" ] && [ "$http_code" != "422" ]; then
  log "ERROR: create org status=$http_code"
  exit 1
fi

# Repo. auto_init so there is a default branch (some flows expect one).
log "creating repo $FORGE_ORG/$FORGE_REPO"
http_code=$(
  curl -fsS -o /dev/null -w '%{http_code}' \
    -H "Authorization: token $api_token" \
    -H 'Content-Type: application/json' \
    -X POST \
    -d "{\"name\":\"$FORGE_REPO\",\"auto_init\":true,\"default_branch\":\"main\"}" \
    "$FORGE_URL/api/v1/orgs/$FORGE_ORG/repos" || true
)
if [ "$http_code" != "201" ] && [ "$http_code" != "409" ]; then
  log "ERROR: create repo status=$http_code"
  exit 1
fi

# Webhook. type=gitea works for both Forgejo and Gitea; X-Gitea-Signature is
# the header both ship. Events: issues + issue_comment + pull_request +
# pull_request_review covers step 6 + step 9 (PR-driven state automation).
log "registering webhook $BRIDGE_WEBHOOK_URL on $FORGE_ORG/$FORGE_REPO"
http_code=$(
  curl -fsS -o /dev/null -w '%{http_code}' \
    -H "Authorization: token $api_token" \
    -H 'Content-Type: application/json' \
    -X POST \
    -d "{
      \"type\":\"gitea\",
      \"config\":{\"url\":\"$BRIDGE_WEBHOOK_URL\",\"content_type\":\"json\",\"secret\":\"$PFB_FORGE_WEBHOOK_SECRET\"},
      \"events\":[\"issues\",\"issue_comment\",\"pull_request\",\"pull_request_review\"],
      \"active\":true
    }" \
    "$FORGE_URL/api/v1/repos/$FORGE_ORG/$FORGE_REPO/hooks" || true
)
if [ "$http_code" != "201" ]; then
  log "ERROR: create webhook status=$http_code"
  exit 1
fi

# Print the token on stdout so the caller can capture it.
printf '%s\n' "$api_token"
