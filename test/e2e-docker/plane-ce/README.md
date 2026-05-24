# test/e2e-docker/plane-ce — real Plane CE in CI

A minimal Plane CE v1.3.1 stack the bridge spins up to round-trip a
real forge webhook → translator → POST `/issues/` → assertion exchange
against the actual Plane backend (not the recording stub). This is the
structural fix from PFB-28 for the class of bug that produced PFB-22,
PFB-24, and PFB-25 — testdata that diverged from real Plane wire
shape, with the existing e2e stub happy to agree with synthetic data.

## What runs

`docker-compose.yaml` brings up the minimum Plane CE topology needed
for the REST API + webhook delivery worker:

- `plane-db` — postgres 15 (ephemeral, no volume)
- `plane-redis` — valkey 7
- `plane-mq` — rabbitmq 3.13 with management plugin
- `plane-minio` — S3-compatible storage for attachments (Plane requires
  it even if nothing uses it)
- `plane-migrator` — one-shot django migrations
- `plane-api` — gunicorn serving `/api/v1/*` and `/auth/*`
- `plane-worker` — celery worker (processes webhook deliveries)
- `plane-beat-worker` — celery beat scheduler

Trimmed from the upstream `makeplane/plane` compose by dropping the
frontends (web, space, admin, live), the proxy, and all persistent
volumes — none are needed for headless REST + webhook coverage.

## What CI does

The `plane-1.3` leg of the `e2e` matrix in `.github/workflows/ci.yaml`
(alongside `e2e-forgejo-15` and `e2e-gitea-1.22`):

1. Brings up this stack via `run.sh up` (~90s on a cold cache)
2. Seeds an admin user + workspace + project + API token via direct
   Django ORM (see `seed.py`)
3. Pulls **the bridge image built by `build-image`** (not the source —
   this is the artifact that's about to be published)
4. Runs that image as a sibling container on the `pfb-e2e-plane-ce`
   network, configured to talk to `plane-api:8000/api/v1`
5. POSTs a synthetic Forgejo `issues.opened` webhook
   (`internal/forge/testdata/issues_opened.json`) at the bridge
6. Polls real Plane via REST for the work item the bridge should
   have created, looking it up by `external_source=forge:acme/widget`
   + `external_id=42`. A 200 with a populated `state` field proves
   the full round-trip (forge webhook → bridge decode → translator →
   bridge POST → real Plane decode → REST GET round-trip) — exactly
   the path PFB-25 broke in production.
7. Tears down on success or failure (`if: always()`).

The `publish` job depends on the entire `e2e` matrix succeeding,
alongside `lint`.

## Why these versions

The Plane backend is pinned to `v1.3.1` because that is the version
`plane.stern.ca` runs and that `internal/plane/testdata/` is locked to
(see `internal/plane/testdata/README.md`). Bumping the version requires:

1. Re-capturing the webhook payloads in `internal/plane/testdata/`
   against the new version
2. Verifying that `seed.py` still finds the expected ORM surface (Plane
   has moved User/Workspace models across module boundaries before)
3. Verifying that the `register_instance` and `configure_instance`
   management commands still exist with the same arguments

The `valkey:7.2.11-alpine` / `postgres:15.7-alpine` / `rabbitmq:3.13.6-management-alpine`
pins match the upstream compose at the same Plane release; no reason to
drift them independently.

## Local run

```bash
# Bring the stack up + seed admin/workspace/project/token (~90s cold cache):
bash test/e2e-docker/plane-ce/run.sh up
# Last stdout line is JSON:
# {"workspace_slug":"pfb-ci","project_id":"...","api_token":"...", ...}

# Inspect the seeded REST surface:
TOKEN=<api_token from above>
curl -H "X-API-Key: $TOKEN" \
  http://localhost:8765/api/v1/workspaces/pfb-ci/projects/ | jq

# Tear down:
bash test/e2e-docker/plane-ce/run.sh down
```

The seed is idempotent — re-running it returns the same workspace,
project, and token; pass `PFB_PLANE_API_KEY=<known-value>` to lock the
token to a stable string (CI does this for log grep-ability).

To exercise the full bridge round-trip locally, follow the same
sequence the CI job does (compose up + seed → run bridge image with
config pointing at `plane-api:8000` on the compose network → POST
signed webhook → assert via REST). See the `e2e-real-plane` block in
`.github/workflows/ci.yaml` for the exact commands.

### Podman wart

On rootless podman, `rabbitmq:3.13.6-management-alpine` fails to start
with `eacces` on `/var/lib/rabbitmq/.erlang.cookie` because the
container runs as root, which maps to a UID that lacks read access to
the in-image `/var/lib/rabbitmq/` (owned by uid 100). The compose pins
`user: "100:101"` on the rabbitmq service to work around this. Docker
(in CI) is unaffected either way.

Separately, rootless podman occasionally interprets `-v file:file`
bind mounts as directories (the destination doesn't exist yet, so
podman creates a directory at that path before the source is mounted).
The Linux Docker daemon in GitHub Actions doesn't have this quirk;
the e2e-real-plane CI job uses the same `-v config.yaml:...` pattern
the existing e2e-docker job uses without issue.

## Why the healthcheck uses 127.0.0.1, not localhost

`plane-api` gunicorn binds `0.0.0.0:8000` (IPv4 only). On some rootless
podman setups, `localhost` inside the container resolves to `::1`
first, and the wget healthcheck gets ECONNREFUSED. `127.0.0.1` is
unambiguous and works across Docker and Podman.

## Bootstrap details (seed.py)

Plane CE has no documented headless seed path. The browser flow
(register instance → sign up admin → create workspace → create project →
mint API token) is human-oriented and runs through Django sessions
with CSRF cookies; driving it from a script is more wire than it's
worth. `seed.py` runs inside the `plane-api` container and goes
directly through Django ORM:

- `Instance` row with `is_setup_done=True` (the gate the auth views
  check before allowing sign-up)
- `User` with a hashed password + `is_email_verified=True`
- `InstanceAdmin` linking the user to the instance as Owner (role 20)
- `Workspace` (slug `pfb-ci`) + `WorkspaceMember`
- `Project` (identifier `PFB`) + `ProjectIdentifier` + `ProjectMember`
- Default `State` rows (Backlog/Todo/In Progress/Done/Cancelled) — Plane
  normally seeds these via a post-create signal, but we create them
  explicitly so state-map tests are deterministic
- `APIToken` with a known value (env override or random UUID hex)

Every step is `get_or_create`, so the seed is safe to re-run.
