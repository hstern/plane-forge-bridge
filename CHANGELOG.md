# Changelog

All notable changes to plane-forge-bridge are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Changed

- **CI gains a real-Plane round-trip job (PFB-28).** The
  `e2e-real-plane` workflow stage brings up a minimum Plane CE v1.3.1
  stack (api+worker+postgres+redis+rabbitmq+minio) via
  `test/e2e-docker/plane-ce/docker-compose.yaml`, seeds an admin user /
  workspace / project / API token via direct Django ORM
  (`seed.py` — Plane CE has no headless signup flow), and runs the
  bridge's `plane` package integration test (`go test
  -tags=integration ./internal/plane`) against real Plane over the
  REST API. The publish gate now requires this job to pass alongside
  `e2e-docker` and `lint`. Goal: future Plane wire-shape changes (the
  PFB-22 / PFB-24 / PFB-25 class) fail in CI instead of in production —
  the existing `plane-stub` happily agrees with synthetic testdata, so
  it can't catch upstream drift on its own.

## [0.1.4] — 2026-05-24

### Fixed

- **Plane → forge: drop work_item.created echoes via external_source
  (PFB-27).** The bridge stamps `external_source="forge:owner/repo"`
  on every forge→plane create; Plane echoes that back on its own
  `work_item.created` webhook. The HTML-comment loop-break marker is
  supposed to catch the echo, but Plane CE v1.3.1 strips HTML comments
  out of `description_html` during ProseMirror sanitization (verified
  against plane.stern.ca — `<p>x</p>\n\n<!-- ... -->` round-trips as
  `<div><p>x</p>\n\n</div>`; comment bodies preserve the marker). With
  no marker on the echo, `handlePlaneWorkItemCreated` would create a
  duplicate forge issue — the failure mode PFB-25 exposed in
  production. The handler now gates on `WorkItem.ExternalSource`
  starting with the bridge's `"forge:"` prefix and skips with
  `reasonPlaneCreatedEchoExternalSource`. Non-bridge external sources
  (e.g. an operator importing from GitHub) are unaffected. AGENTS.md
  updated to document the ProseMirror sanitization finding so the next
  feature work doesn't reach for the marker as the durable
  description-side defence.

## [0.1.3] — 2026-05-24

### Fixed

- **Plane REST `POST /issues/` response decode regression from v0.1.2.**
  v0.1.2 modelled `WorkItem.state` / `labels` / `assignees` as object
  refs to match the webhook wire shape (PFB-24), but Plane CE v1.3.1's
  REST surface returns the same fields as bare UUID strings (state) and
  arrays of bare UUID strings (labels, assignees). Every forge→plane
  create call decoded the POST response with
  `json: cannot unmarshal string into Go struct field WorkItem.state of
  type plane.StateRef`, leaving the bridge with no record of the
  just-created Plane work item and causing a duplicate Forgejo issue
  when Plane's webhook for the new work item came back. Added custom
  `UnmarshalJSON` on `StateRef` / `LabelRef` / `AssigneeRef` accepting
  both the bare-UUID and object forms; webhook decode is unchanged.
  Regression tests pin both shapes against verbatim captures from
  plane.stern.ca so the next contract drift fails in CI. See PFB-25.

## [0.1.2] — 2026-05-24

### Fixed

- **Plane → forge: `WorkItem.state`, `labels`, `assignees` are objects,
  not bare UUIDs.** The bridge's `WorkItem` struct modelled them as
  `string` / `[]string`; real Plane (CE v1.3.1+) serializes them as
  nested objects (`{id, name, color, group}` for `state`, etc.). Every
  real Plane webhook 400'd on payload decode after the v0.1.1
  action-tense fix. Added `StateRef`, `LabelRef`, `AssigneeRef` types
  and pointed the fields at them; the bridge's sync layer only reads
  `Labels[i].ID` today, so the user-visible behaviour change is exactly:
  decode no longer fails. Regression test pins decoding of a captured
  real Plane payload so the next struct-shape drift fails in CI instead
  of in production.

## [0.1.1] — 2026-05-24

### Fixed

- **Plane → forge: real Plane sends past-tense action verbs (`created` /
  `updated` / `deleted`).** v0.1.0's parser only recognised present-tense
  (`create` / `update` / `delete`), so every real Plane webhook hit
  `ErrUnsupportedEvent` → 204 No Content + log at DEBUG. plane → forge
  was broken on every v0.1.0 deployment; the silent 204 made Plane think
  the delivery succeeded and the DEBUG log was suppressed at the default
  `log_level=info`, so operators saw nothing. Parser now accepts both
  spellings. Testdata fixtures swapped to past-tense (the wire reality).
  `/plane/webhook` `ErrUnsupportedEvent` logs promoted from DEBUG to INFO
  so a future contract drift can't be silent again — `/forge/webhook`
  stays at DEBUG since forges legitimately fan out events the operator
  didn't subscribe to.
  ([#10](https://github.com/hstern/plane-forge-bridge/issues/10))

## [0.1.0] — 2026-05-23

First release. Bidirectional webhook bridge between Plane and any
Gitea-API-compatible forge (Forgejo, Gitea):

- Issues create/update/close/reopen ↔ work items
- Comments create both ways (edit/delete deferred)
- Labels both ways with auto-create
- State mapping per-link
- PR/branch refs (`[PROJ-123]` or `proj-123-foo`) → work-item state
  automation
- Plane → forge issue creation closes the loop (work_item.created → forge
  issue; updates and deletes deferred)
- Identity: config map → email match → bridge bot, with auto-fallback
  through `forge.SearchUsers` to defeat Gitea/Forgejo's webhook noreply
  placeholder

Loop-break: HTML-comment marker + in-memory LRU. Single static binary,
distroless runtime, stateless. CI matrix tests against real Forgejo 15
and Gitea 1.22 service containers end-to-end on every PR.

[0.1.4]: https://github.com/hstern/plane-forge-bridge/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/hstern/plane-forge-bridge/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/hstern/plane-forge-bridge/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/hstern/plane-forge-bridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/hstern/plane-forge-bridge/releases/tag/v0.1.0
