# plane-forge-bridge

**Status: pre-alpha — nothing works yet.** Skeleton commit only.

Bidirectional webhook bridge between [Plane](https://plane.so) and any
Gitea-API-compatible git forge ([Forgejo](https://forgejo.org/),
[Gitea](https://about.gitea.com/)). Aims at feature parity with Plane's
first-party GitHub/GitLab integration: issue / comment / label / state sync,
plus PR → work-item state automation and branch-name linking.

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

- Single static Go binary, stateless, one process.
- Two HTTP webhook endpoints (`/forge/webhook`, `/plane/webhook`) with HMAC
  verification, plus outbound REST clients to both sides.
- Forgejo and Gitea share the v1 REST API and webhook payload format
  (`X-Gitea-Signature` HMAC-SHA256, `X-Gitea-Event`) — one internal `forge`
  package speaks both. CI exercises both as a matrix.
- Loop-break marker on every outbound write:
  `<!-- pfb:src=<forge|plane>,evt=<event-id> -->`. Inbound events carrying
  our marker are dropped. Belt-and-braces: an LRU of recently-seen
  `(source_event_id, target_object_id)` pairs.
- Identity mapping in v1 is a static `forge_username → plane_member_uuid`
  table in config. Unmapped users post as the configured bridge bot with the
  original author noted in the body. v2 will add an OAuth handshake.

See [docs/design.md](docs/design.md) for the full design (added in step 2+).

## Scope

**v1 — bidirectional**

- Issue ↔ work item (title, description, state, assignee, labels)
- Comment ↔ comment
- Label ↔ label (auto-create on either side)
- State ↔ state (configurable mapping)

**v1 — one-way (forge → plane)**

- PR open/edit/merge/close/review → state automation on the linked work item
  when the title or body references `[PROJ-123]` (Plane's own bracket
  syntax)
- Branch name like `proj-123-foo` → link to work item

**Out of v1**: cross-repo issue links, @-mentions.

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

## License

[MIT](LICENSE).
