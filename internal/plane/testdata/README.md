# internal/plane/testdata

Fixtures used by `parse_test.go`. Each file is a single webhook delivery
body, the exact bytes Plane sends to a `webhook_url` over HTTP.

## Pinned Plane version

**Plane CE v1.3.1** (`makeplane/plane-backend:v1.3.1`). Wire-shape drift
between Plane versions has burned us three times — PFB-22 (action verb
tense), PFB-24 (object-form state/labels/assignees), and PFB-25 (REST
response uses bare UUIDs while webhooks use objects). Every captured
payload below is locked to this version.

Pinning the testdata to v1.3.1 is a snapshot, not a guarantee — when
Plane upstream ships a new minor, recapture the payloads against the new
version, bump this line, and run the suite to surface any decode breaks
before they hit production.

## Provenance

| File | Source | Notes |
|---|---|---|
| `work_item_created.json` | verbatim capture from `plane.stern.ca` (2026-05-24) | Real Plane CE v1.3.1 webhook delivery body. Captured via `psql webhook_logs.request_body`. Exercises object-form state (PFB-24), past-tense action verb (PFB-22), full activity.actor (richer than the bridge models), and the `description`/`description_json`/`description_stripped` fields the bridge currently ignores. |
| `work_item_updated.json` | **structurally derived** — original synthetic shape, augmented to match the real v1.3.1 envelope | Awaiting verbatim capture. Uses the same field layout as the verbatim `work_item_created.json` but with synthetic UUIDs and content for "Investigate flaky CI runner". |
| `work_item_deleted.json` | **structurally derived** — minimal | Awaiting verbatim capture. Deletes carry only `data.id` in the original modelling; verify against a real capture whether Plane v1.3.1 ships richer delete payloads (it likely does — at minimum `project`, `workspace`, `created_by`). |
| `comment_created.json`, `comment_updated.json` | **synthetic** — pre-PFB-24, never recaptured | Awaiting verbatim capture. The shape currently checks only into the fields the bridge reads (`id`, `issue`, `comment_html`, `actor`, `created_by`, `access`); verify whether real Plane comment payloads carry the same actor-as-string + activity.actor-as-object split as work item payloads. |

`comment_deleted.json` is intentionally absent — the bridge doesn't
parse comment deletes from the webhook side today (the Plane→forge
direction is read-only for now), so there's no fixture-decoded path
to pin.

## Capture procedure

Plane stores every delivered webhook in postgres `webhook_logs`. To
recapture for a new version (or to fill one of the synthetic slots above
with a real capture):

```bash
# On the Plane host:
PGPW=$(sudo grep '^POSTGRES_PASSWORD=' /etc/plane/plane.env | cut -d= -f2)

# 1. Trigger the event on Plane (UI or REST). The webhook delivers immediately.
# 2. Find the delivery (most recent matching event):
sudo podman exec -e PGPASSWORD="$PGPW" plane-postgres psql -U plane -d plane -P pager=off -c \
  "SELECT id, event_type, created_at
   FROM webhook_logs
   WHERE event_type = 'issue' OR event_type = 'issue_comment'
   ORDER BY created_at DESC LIMIT 5;"

# 3. Pull the verbatim body (cast bytea → text and pipe to jq):
sudo podman exec -e PGPASSWORD="$PGPW" plane-postgres psql -U plane -d plane -tA -P pager=off -c \
  "SELECT convert_from(request_body, 'UTF-8') FROM webhook_logs WHERE id = '<delivery-uuid>';" \
  | jq . > internal/plane/testdata/<event>.json
```

The capture is purely structural — no secrets, but it does contain
workspace/project/user UUIDs from the source environment. Sanitize if
the source workspace isn't already public.

## What the fixtures pin

`parse_test.go::TestParse_Fixtures` exercises each fixture against the
bridge's `Parse` function and asserts the high-value fields decoded
successfully. The PFB-22, PFB-24, and PFB-25 regressions all manifested
as decode errors that this layer would catch — adding a recaptured
fixture for each event type is the durable defence against the same
class of bug.

For deeper coverage, two inline tests pin specific known-real shapes:

- `TestParse_RealPlaneWorkItemPayload_PFB24` — object-form state + label
  in a webhook body
- `TestWorkItem_UnmarshalJSON_RESTShape_PFB25` /
  `TestCreateIssue_RealPlaneRESTShape_PFB25` — bare-UUID state /
  labels / assignees in a REST POST response (the opposite shape from
  webhooks; see PFB-25 for the trap)

A real-Plane-container end-to-end test is tracked separately as the
next phase of the audit, so future contract drift is caught in CI
against the real backend rather than against captured fixtures alone.
