# plane-forge-bridge

**Status: v0.1.2** — multi-arch image (linux/amd64 + linux/arm64) at
`ghcr.io/hstern/plane-forge-bridge:0.1.2` (also `:0.1`, `:0`, `:latest`).
Patch release fixing two real-deployment bugs in the plane → forge path
that v0.1.0 shipped silently broken — see CHANGELOG.

Bidirectional webhook bridge between [Plane](https://plane.so) and any
Gitea-API-compatible git forge ([Forgejo](https://forgejo.org/),
[Gitea](https://about.gitea.com/)). Closes parity with Plane's
first-party GitHub/GitLab integration for the parts most operators need:
issue / comment / label / state sync, plus PR-driven work-item state
automation.

## Why this exists

Plane's first-party GitHub and GitLab connectors live in a closed-source
service called **silo** (commercial, distributed via
`artifacts.plane.so/makeplane/silo-commercial`). The OSS `makeplane/plane`
repository contains no connector code, so a PR adding native Forgejo support
upstream is not actually possible —
[makeplane/plane#1495](https://github.com/makeplane/plane/issues/1495)
has been open since 2023 with no maintainer response. This repo is the OSS
satellite that fills that gap: an MIT-licensed alternative to the existing
paid third-party plugin discussed in
[makeplane discussion #8796](https://github.com/orgs/makeplane/discussions/8796).

## Design at a glance

- Single static Go binary, stateless, one process. Distroless runtime.
- Two HTTP webhook endpoints (`/forge/webhook`, `/plane/webhook`) with HMAC
  verification at the handler boundary, plus outbound REST clients to both sides.
- Forgejo and Gitea share the v1 REST API and webhook payload format
  (`X-Gitea-Signature` HMAC-SHA256, `X-Gitea-Event`) — one internal `forge`
  package speaks both. CI tests against both as a matrix on every PR.
- Loop-break marker on every outbound write:
  `<!-- pfb:src=<forge|plane>,evt=<event-id> -->`. Inbound events carrying
  our marker are dropped. Belt-and-braces: an in-memory LRU of recently-seen
  `(source_event_id, target_object_id)` pairs.
- Identity mapping is a config map first, email-based match second (works
  against Gitea/Forgejo's webhook noreply placeholder by re-resolving via
  the user-search API), bridge bot as the fallback.

See [docs/design.md](docs/design.md) for the full design + open questions.

## Quickstart

1. **Pull the image**

   ```sh
   docker pull ghcr.io/hstern/plane-forge-bridge:0.1.2
   ```

2. **Write a `config.yaml`** — copy [`config.example.yaml`](config.example.yaml)
   and fill in the placeholders. The config names *environment variables* that
   hold the secrets (it does not store the secrets themselves), so the file is
   safe to commit alongside infrastructure code.

3. **Run it**

   ```sh
   docker run -d --name pfb \
     -p 8080:8080 \
     -v $PWD/config.yaml:/etc/pfb/config.yaml:ro \
     -e PFB_FORGE_TOKEN=...        \
     -e PFB_FORGE_WEBHOOK_SECRET=... \
     -e PFB_PLANE_API_KEY=...      \
     -e PFB_PLANE_WEBHOOK_SECRET=... \
     ghcr.io/hstern/plane-forge-bridge:0.1.2 \
     --config /etc/pfb/config.yaml
   ```

4. **Register the webhook on the forge side**: `POST` to
   `/api/v1/repos/<owner>/<repo>/hooks` with `type: "gitea"`,
   `url: https://<bridge-host>/forge/webhook`, the same HMAC secret as
   `PFB_FORGE_WEBHOOK_SECRET`, and the events you want
   (`issues`, `issue_comment`, `pull_request`, `pull_request_review`).

5. **Register the webhook on the Plane side**: workspace settings →
   Webhooks → add `https://<bridge-host>/plane/webhook` with the same HMAC
   secret as `PFB_PLANE_WEBHOOK_SECRET` and the relevant event types.

6. **Tail the logs**: `docker logs -f pfb`. JSON structured (`log/slog`);
   each accepted webhook is one info-level line with `kind`, `delivery_id`,
   and the resulting outbound action.

## What ships in v0.1

**Bidirectional**

- Issues create/update/close/reopen ↔ work items (title, description, state, assignee, labels)
- Comments create both ways (edit/delete deferred)
- Labels both ways with auto-create on either side
- State mapping per-link (forge `open`/`closed` ↔ configurable plane states)

**One-way (forge → plane)**

- PR open/reopen/close/merge → state automation on the linked work item via
  `[PROJ-123]` bracket refs in title/body or `proj-123-foo` head-branch
  names (review-state semantics deferred)
- Plane → forge issue creation closes the loop (work_item.created → forge
  issue created with both loop-break and plane-ref markers; plane updates +
  deletes deferred)

**Identity**

- Static `forge_username → plane_member_uuid` config map (operator override)
- Email-based matching as fallback — defeats Gitea/Forgejo's webhook noreply
  placeholder by re-resolving through the user-search API
- Configurable bridge bot as the final fallback

**Out of v0.1**: comment edit/delete identity persistence, plane
work_item update/delete reverse lookup, PR-review-state automation,
plane→forge label sync wire-up, custom OAuth flow (only needed for
multi-tenant heterogeneous-email deployments), label cache invalidation
on rename, PR state no-op suppression, plane→forge body author preface.

## Build / test

```sh
make build              # go build ./...
make test               # go test ./...
make race               # go test -race ./...
make lint               # golangci-lint run ./...
make vuln               # govulncheck ./...
make image              # docker buildx build
make e2e                # e2e against Forgejo + Gitea + plane-stub containers
```

CI runs all of these on every PR; the matrix e2e brings up real Forgejo 15
and Gitea 1.22 service containers, drives them via REST, and asserts on
plane-stub's recorded calls.

## License

[MIT](LICENSE).
