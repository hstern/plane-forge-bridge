# plane-stub

A tiny Go HTTP service used by the `e2e-docker` CI job to stand in for a
real [Plane](https://plane.so) workspace. It does two things:

1. **Records every incoming request** — method, path, query, headers, and
   body — into an in-memory log.
2. **Serves canned JSON responses** for the subset of the Plane REST API
   that `plane-forge-bridge` is expected to hit, so the bridge gets a
   well-formed-enough reply and keeps going.

The companion `assert.sh` script (lives elsewhere) reads the recorded
calls back via `/_/recorded` and checks that the bridge made the outbound
calls the test expected.

**This is not a Plane mock for production use.** It does not validate
auth, does not enforce schemas, and does not persist anything. It only
records and echoes.

## Endpoints

### Simulated Plane API

Any `POST` / `PATCH` under `/api/v1/...` is accepted and recorded. The
following shapes return `200 OK` with a JSON body containing a fresh
UUID:

| Method | Path                                                                                          |
| ------ | --------------------------------------------------------------------------------------------- |
| POST   | `/api/v1/workspaces/{slug}/projects/{project_id}/issues/`                                     |
| PATCH  | `/api/v1/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/`                          |
| POST   | `/api/v1/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/comments/`                 |
| PATCH  | `/api/v1/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/comments/{comment_id}/`    |

The response body is shaped roughly like:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "project": "<project_id from path>",
  "name": "<echoed from request body if present>",
  "state": "<echoed from request body if present>"
}
```

Any other `/api/v1/...` path returns `404 Not Found` with a JSON body —
**but the request is still recorded** so the asserter can see that the
bridge sent something unexpected.

### Control plane (under `/_/`)

| Method | Path           | Description                                              |
| ------ | -------------- | -------------------------------------------------------- |
| GET    | `/_/recorded`  | JSON array of recorded calls (see schema below).         |
| POST   | `/_/reset`     | Clear the recorded-call log. `204 No Content` on success.|
| GET    | `/_/healthz`   | `200 OK` with body `ok`. Used by container healthchecks. |

Recorded-call schema:

```json
{
  "method": "POST",
  "path": "/api/v1/workspaces/acme/projects/p1/issues/",
  "query": "",
  "headers": {"X-Api-Key": ["..."]},
  "body": "{\"name\":\"hello\"}",
  "received_at": "2026-05-23T11:22:33Z"
}
```

`Connection`, `Host`, and `User-Agent` headers are stripped before
recording (protocol noise). Everything else is kept verbatim — the
asserter decides what to check.

## Configuration

| Flag       | Env           | Default | Description                  |
| ---------- | ------------- | ------- | ---------------------------- |
| `-listen`  | `STUB_LISTEN` | `:8080` | Address to listen on.        |

Request bodies are capped at 1 MiB.

## Building the image

The Dockerfile uses `COPY . .` and references the package at
`./test/e2e-docker/plane-stub`, so it **must be built from the repository
root**, not from this directory:

```sh
docker build -f test/e2e-docker/plane-stub/Dockerfile -t plane-stub .
```

## Running locally

For debugging by hand:

```sh
go run ./test/e2e-docker/plane-stub                       # listens on :8080
go run ./test/e2e-docker/plane-stub -listen :9000         # custom port
STUB_LISTEN=:9000 go run ./test/e2e-docker/plane-stub     # via env

# In another terminal:
curl -sX POST -H 'X-Api-Key: dev' -d '{"name":"hello","state":"backlog"}' \
    http://localhost:8080/api/v1/workspaces/acme/projects/p1/issues/

curl -s http://localhost:8080/_/recorded | jq .
curl -sX POST http://localhost:8080/_/reset
```

## Tests

```sh
go test ./test/e2e-docker/plane-stub/... -race
```
