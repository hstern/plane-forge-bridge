# AGENTS.md

Guidance for AI agents (and humans) working in this repository.

## What this is

`plane-forge-bridge` is a bidirectional webhook bridge between
[Plane](https://plane.so) and any Gitea-API-compatible forge
(Forgejo, Gitea). Single stateless Go binary, MIT, published to
`ghcr.io/hstern/plane-forge-bridge`. See [README.md](README.md) and
`docs/design.md`.

## Build, test, lint

```sh
make build              # go build ./...
make test               # go test ./...
make race               # go test -race ./...
make lint               # golangci-lint run ./...  (config in .golangci.yml)
make vuln               # govulncheck ./...
make image              # docker buildx build (test stage runs inside)
make e2e                # test/e2e-docker against Forgejo + Gitea + plane-stub
```

CI runs the same in a single workflow at `.github/workflows/ci.yaml`. There is
no `.forgejo/workflows/` — one workflow runs on both GitHub-hosted runners and
any self-hosted Forgejo Actions runner because `runs-on` is parameterized via
`${{ vars.RUNNER_LABEL || 'ubuntu-latest' }}`.

## Conventions (please keep)

- **Every package has a `README.md`.** Add one when you add a package.
- **Unit tests for everything.** New behavior comes with tests; keep them fast
  and hermetic (no real network — use the in-package fakes / `httptest`).
- **Interfaces have hand-written fakes** under a sibling `mock/` package
  (func-field fakes, call recording, concurrency-safe). No codegen tool
  dependency.
- **Standard library `log/slog`** for logging; no logging framework.
- **No secrets, account IDs, or homelab details** in code, tests, docs, or
  examples. Generic placeholders only. CI image names derive from
  `github.repository` (ghcr), never a hardcoded account string.
- `//nolint` directives must name the linter and give a reason
  (`nolintlint` enforces this).

## Architecture invariants (don't break these)

- **One forge package speaks both Forgejo and Gitea.** They share the v1 REST
  API and webhook payload format (`X-Gitea-Signature` HMAC-SHA256,
  `X-Gitea-Event`). Don't fork into two packages — CI proves the contract by
  running e2e against both images as a matrix.
- **Loop-break marker on every outbound write.** Every comment/description we
  write to either side ends with `<!-- pfb:src=<forge|plane>,evt=<id> -->`.
  Inbound events carrying our marker are dropped *before* any work, and a
  short LRU of `(source_event_id, target_object_id)` pairs catches the case
  where the marker was stripped. Both layers must stay — they cover different
  failure modes.
  - **Plane CE v1.3.1 strips HTML comments from `description_html`** during
    ProseMirror sanitization (verified against plane.stern.ca — POST body
    `<p>x</p>\n\n<!-- ... -->` round-trips as `<div><p>x</p>\n\n</div>`).
    Comment bodies (`comment_html`) preserve the marker; work item
    descriptions do not. The marker check is therefore non-functional on
    inbound `work_item.*` echoes. The durable defence is the
    `external_source` round-trip: the bridge stamps
    `external_source="forge:owner/repo"` on every forge→plane create, and
    `handlePlaneWorkItemCreated` skips any inbound `work_item.created`
    whose `WorkItem.ExternalSource` starts with `"forge:"`. See PFB-27.
- **HMAC verification happens at the HTTP handler boundary**, before the body
  is parsed. Constant-time compare. Reject on missing header.
- **Stateless process.** No on-disk state, no DB. Config + env vars in, HTTP
  out. The LRU is in-memory and can be lost on restart — the marker is the
  durable defense; the LRU is the optimization.
- **Identity mapping is config in v1.** `forge_username → plane_member_uuid`
  static map. Unmapped users post as the configured bridge bot with the real
  author named in the body. Do not silently drop unmapped events.
- **The bridge is the single writer of its own outbound calls.** Don't add
  retry loops that could double-write before the loop-break check on the
  return path — idempotency relies on the marker being present on the first
  write.

## Hosting

Public repo: <https://github.com/hstern/plane-forge-bridge>. Container image:
`ghcr.io/hstern/plane-forge-bridge`. There is no other remote; develop
directly against GitHub. Use `gh` (already authenticated as `hstern`) for repo
operations — not `fj`, which is unrelated.

## Build order

Tracked in the prompt that scaffolded this repo, summarized:

1. Skeleton — LICENSE, README, AGENTS.md.  ← *we are here*
2. `cmd/main.go` + minimal HTTP server with HMAC-verified webhook endpoints.
3. `internal/forge` + `internal/plane` parse + verify, unit tests against
   captured payload fixtures.
4. Multi-stage Dockerfile + `test/e2e-docker/plane-stub`.
5. `.github/workflows/ci.yaml` modeled on
   [`hstern/fj-bellows`](https://github.com/hstern/fj-bellows)
   (`build-image` → `lint` ∥ `e2e-docker` → `publish`-by-retag, matrix over
   Forgejo + Gitea).
6. Issue create/update/close translation (forge → plane).
7. Comments both ways with loop-break marker.
8. Labels + state mapping.
9. PR / branch → work-item state automation.
10. Plane → forge direction.
11. v2: OAuth identity handshake.
