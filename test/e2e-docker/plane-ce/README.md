# test/e2e-docker/plane-ce — real Plane CE in CI

A minimal Plane CE v1.3.1 stack the bridge spins up to round-trip a
real REST + webhook exchange against the actual Plane backend (not the
recording stub). This is the structural fix from PFB-28 for the class
of bug that produced PFB-22, PFB-24, and PFB-25 — testdata that
diverged from real Plane wire shape, with the existing e2e stub happy
to agree with synthetic data.

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

# The last stdout line is a JSON record with all the connection details:
# {"workspace_slug": "pfb-ci", "project_id": "...", "api_token": "...", ...}

# Source it into env vars and run the integration test:
eval "$(bash test/e2e-docker/plane-ce/run.sh seed 2>/dev/null | tail -n1 \
  | jq -r 'to_entries[] | "export PFB_PLANE_TEST_\(.key|ascii_upcase)=\(.value)"')"
export PFB_PLANE_TEST_BASE_URL=http://localhost:8765/api/v1
go test -tags=integration ./internal/plane -run TestIntegration -v

# Tear down:
bash test/e2e-docker/plane-ce/run.sh down
```

The seed is idempotent — re-running it returns the same workspace,
project, and token; pass `PFB_PLANE_API_KEY=<known-value>` to lock the
token to a stable string (CI does this for log grep-ability).

### Podman wart

On rootless podman, `rabbitmq:3.13.6-management-alpine` fails to start
with `eacces` on `/var/lib/rabbitmq/.erlang.cookie` because the
container runs as root, which maps to a UID that lacks read access to
the in-image `/var/lib/rabbitmq/` (owned by uid 100). The compose pins
`user: "100:101"` on the rabbitmq service to work around this. Docker
(in CI) is unaffected either way.

## CI integration

`.github/workflows/ci.yaml` defines an `e2e-real-plane` job that:

1. Brings up this stack via `run.sh up`
2. Sources the seed JSON into env vars
3. Runs `go test -tags=integration ./internal/plane -run TestIntegration`
4. Tears down via `run.sh down` (always — `if: always()`)

The job is **separate from** the existing `e2e-docker` matrix to keep
the marginal CI time scoped: real Plane boot is ~90s on a cold cache,
and the value of running the integration test twice (once per forge
flavor) is low — the wire shape doesn't depend on which forge is
upstream.

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

## Integration test coverage

`internal/plane/integration_test.go` (build tag `integration`) calls
every `plane.Client` method the bridge uses today and asserts the
high-value fields decoded. The PFB-25 regression mode (REST `state`
field as a bare UUID) is specifically pinned at the
`CreateIssue` / `GetIssue` / `UpdateIssue` round-trip points, and
PFB-27's external_source echo path is exercised on the create side.
