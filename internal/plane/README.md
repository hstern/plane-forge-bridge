# internal/plane

Parse and verify inbound webhook deliveries from a Plane instance
(<https://plane.so>) and expose a minimal typed surface for the bridge.

This package is the Plane-side counterpart of `internal/forge`. It does
not (yet) speak Plane's REST API — a placeholder `Client` is exposed so
later steps can hang methods off it without churning every importer.

## Webhook contract

Plane signs the raw request body with HMAC-SHA256 keyed by the
per-webhook secret and sends three headers:

| Header              | Meaning                                              |
| ------------------- | ---------------------------------------------------- |
| `X-Plane-Delivery`  | Unique UUID for the delivery (used as event id).     |
| `X-Plane-Event`     | Event type: `issue`, `issue_comment`, `project`, ... |
| `X-Plane-Signature` | `hex(HMAC-SHA256(secret, raw_body))`, no prefix.     |

The body envelope is:

```json
{
  "event":        "<event-type>",
  "action":       "create" | "update" | "delete",
  "webhook_id":   "<uuid>",
  "workspace_id": "<uuid>",
  "data":         { ... entity-specific payload ... },
  "activity":     { "actor": { "id": "<uuid>", "display_name": "..." } }
}
```

For `action: "delete"` the `data` object contains only the id of the
deleted entity; other fields are absent. The package handles that by
producing a `WorkItem` / `Comment` with only `ID` populated.

### Citations

The signature scheme and header names are taken from the upstream
source rather than the docs site, because the docs only show fragments
of the contract:

- `apps/api/plane/bgtasks/webhook_task.py` in
  [`makeplane/plane`](https://github.com/makeplane/plane) — sets the
  three headers, computes
  `hmac.new(secret, json.dumps(payload), hashlib.sha256).hexdigest()`,
  and constructs the envelope shown above.
- `apps/api/plane/api/serializers/issue.py` —
  `IssueExpandSerializer` and `IssueCommentSerializer` describe the
  field layout of `data` for issue and comment events.
- `apps/api/plane/db/models/issue.py` — model fields for `Issue` and
  `IssueComment`, including `description_html`, `comment_html`,
  `priority`, `sequence_id`, `access`.

Plane's public docs corroborate the headers and algorithm at a
high level:

- <https://developers.plane.so/dev-tools/intro-webhooks>
- <https://developers.plane.so/dev-tools/build-plane-app/webhooks>

## Public API

```go
type EventKind string

const (
    EventWorkItemCreated EventKind = "work_item.created"
    EventWorkItemUpdated EventKind = "work_item.updated"
    EventWorkItemDeleted EventKind = "work_item.deleted"
    EventCommentCreated  EventKind = "comment.created"
    EventCommentUpdated  EventKind = "comment.updated"
    EventCommentDeleted  EventKind = "comment.deleted"
)

type Event struct {
    Kind        EventKind
    DeliveryID  string
    Action      string
    WorkspaceID string
    WebhookID   string
    Actor       Actor
    WorkItem    *WorkItem
    Comment     *Comment
    Raw         []byte
}

func VerifySignature(secret string, headers http.Header, body []byte) error
func Parse(headers http.Header, body []byte) (*Event, error)
func VerifyAndParse(secret string, headers http.Header, body []byte) (*Event, error)

type Client struct { BaseURL, WorkspaceSlug, APIKey string; HTTPClient *http.Client }
func NewClient(baseURL, workspaceSlug, apiKey string, hc *http.Client) *Client
```

Sentinel errors:

- `ErrMissingSignature`
- `ErrInvalidSignature`
- `ErrMissingEventHeader`
- `ErrUnsupportedEvent`
- `ErrMalformedPayload`

## Open questions

These need to be confirmed against a live Plane instance — none of
them block the parse/verify happy path but each is a known unknown:

1. **Comment delete payload.** The OSS source serializes
   `issue_comment` through `IssueCommentSerializer` for create and
   update, but the on-the-wire shape for `action: "delete"` is not
   explicitly tested in upstream. We assume it mirrors the issue
   delete shape (`data: { "id": "..." }`).
2. **`description_html` vs `description_json`.**
   `IssueExpandSerializer` declares `description = source=
   description_json`, but the model also carries `description_html`.
   Empirically Plane's webhook ships `description_html`; we map that
   field. If a live capture shows otherwise we'll add a sibling.
3. **`state` field shape.** The serializer uses
   `StateLiteSerializer(read_only=True)` which embeds the state object,
   but in the webhook payload we have observed it serialized as a UUID
   string. We decode as a string for now; promote to a struct if a
   capture shows the nested form.
4. **No comment_deleted fixture.** Added the kind for symmetry; we
   should capture a real payload before relying on it.
5. **REST API client.** `Client` is a stub; method surface to be
   designed alongside the v1 issue create/update/close translation
   step.

## Fixtures

Under `testdata/`:

- `work_item_created.json`
- `work_item_updated.json`
- `work_item_deleted.json`
- `comment_created.json`
- `comment_updated.json`

All fixtures use placeholder UUIDs; no real workspace data is checked
in.
