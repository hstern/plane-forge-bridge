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

type Client struct {
    BaseURL, WorkspaceSlug, APIKey, UserAgent string
    HTTPClient *http.Client
}
func NewClient(baseURL, workspaceSlug, apiKey string, hc *http.Client) *Client

func (c *Client) CreateIssue(ctx context.Context, projectID string, req CreateIssueRequest) (*WorkItem, error)
func (c *Client) UpdateIssue(ctx context.Context, projectID, issueID string, req UpdateIssueRequest) (*WorkItem, error)
func (c *Client) GetIssue(ctx context.Context, projectID, issueID string) (*WorkItem, error)
func (c *Client) GetIssueByExternalRef(ctx context.Context, projectID, source, externalID string) (*WorkItem, error)
func (c *Client) GetIssueBySequenceID(ctx context.Context, projectID string, sequenceID int) (*WorkItem, error)
func (c *Client) ListProjectStates(ctx context.Context, projectID string) ([]State, error)
func (c *Client) ListProjectLabels(ctx context.Context, projectID string) ([]Label, error)
func (c *Client) CreateProjectLabel(ctx context.Context, projectID string, req CreateLabelRequest) (*Label, error)
func (c *Client) CreateComment(ctx context.Context, projectID, issueID string, req CreateCommentRequest) (*CommentResponse, error)
func (c *Client) UpdateComment(ctx context.Context, projectID, issueID, commentID string, req UpdateCommentRequest) (*CommentResponse, error)
func (c *Client) DeleteComment(ctx context.Context, projectID, issueID, commentID string) error
```

Sentinel errors:

- `ErrMissingSignature`
- `ErrInvalidSignature`
- `ErrMissingEventHeader`
- `ErrUnsupportedEvent`
- `ErrMalformedPayload`
- `ErrNotFound` — returned by `GetIssueByExternalRef` when no work item
  matches the `(source, externalID)` pair. This is the normal "we have
  not yet mirrored this forge issue" case; callers should treat it as a
  signal to create rather than as a failure.

## Client (outbound REST)

`Client` speaks Plane's "v1" REST API and is the bridge's outbound write
path. All four methods take a `context.Context`, set both
`X-Api-Key: <APIKey>` (workspace/personal API tokens) and
`Authorization: Bearer <APIKey>` (OAuth access tokens), `Accept:
application/json`, `User-Agent: <Client.UserAgent or
"plane-forge-bridge">`, and `Content-Type: application/json` on bodies. Responses are size-capped at
4 MiB; non-2xx responses become `*APIError` with up to 4 KiB of the
response body preserved for diagnosis.

| Method                   | HTTP   | Path                                                                          | Returns                |
| ------------------------ | ------ | ----------------------------------------------------------------------------- | ---------------------- |
| `CreateIssue`            | POST   | `/workspaces/{slug}/projects/{pid}/issues/`                                   | `*WorkItem`            |
| `UpdateIssue`            | PATCH  | `/workspaces/{slug}/projects/{pid}/issues/{iid}/`                             | `*WorkItem`            |
| `GetIssue`               | GET    | `/workspaces/{slug}/projects/{pid}/issues/{iid}/`                             | `*WorkItem` / `ErrNotFound` |
| `GetIssueByExternalRef`  | GET    | `/workspaces/{slug}/projects/{pid}/issues/?external_source=…&external_id=…`   | `*WorkItem` / `ErrNotFound` |
| `GetIssueBySequenceID`   | GET    | `/workspaces/{slug}/work-items/{project_identifier}-{sequence_id}/`           | `*WorkItem` / `ErrNotFound` |
| `ListProjectStates`      | GET    | `/workspaces/{slug}/projects/{pid}/states/`                                   | `[]State`              |
| `ListProjectLabels`      | GET    | `/workspaces/{slug}/projects/{pid}/labels/`                                   | `[]Label`              |
| `CreateProjectLabel`     | POST   | `/workspaces/{slug}/projects/{pid}/labels/`                                   | `*Label`               |
| `CreateComment`          | POST   | `/workspaces/{slug}/projects/{pid}/issues/{iid}/comments/`                    | `*CommentResponse`     |
| `UpdateComment`          | PATCH  | `/workspaces/{slug}/projects/{pid}/issues/{iid}/comments/{cid}/`              | `*CommentResponse`     |
| `DeleteComment`          | DELETE | `/workspaces/{slug}/projects/{pid}/issues/{iid}/comments/{cid}/`              | `error` / `ErrNotFound` |

### Comments

Comments are scoped to a work item. Plane stores rich text in
`comment_html` and identifies each comment by a server-assigned UUID
(`comment-uuid` in the table above) that is independent of the forge's
own comment ID — the mapping between the two lives in the bridge's
loop-break marker, not in either system's primary key. `Access` is
either `"INTERNAL"` (visible only to workspace members) or `"EXTERNAL"`
(the default; visible to guests). `CreateComment` returns the created
comment so the caller can read back the assigned UUID before recording
it in the LRU.

`CommentResponse` is a type alias for the existing `Comment` type used
by the inbound webhook decoder — Plane's `IssueCommentSerializer` is the
same serializer for the webhook payload and the REST response, so the
two shapes do not need to be tracked separately.

### Error model

- `ErrNotFound` — the "not yet mirrored" signal from
  `GetIssueByExternalRef`. Use `errors.Is(err, plane.ErrNotFound)`.
- `*APIError` — any other non-2xx response. `errors.As(err, &apiErr)`
  exposes `StatusCode`, `Method`, `Path`, and the truncated `Body`.

### Plane API notes (from research at implementation time)

These are the contract details we relied on; they came from reading the
upstream source at `makeplane/plane` rather than the docs site, which
under-specifies the list endpoint:

1. **Filter parameter names.** Plane's work-item list endpoint accepts
   `external_id` and `external_source` as query parameters. When both
   are present the view short-circuits to `Issue.objects.get(...)` and
   returns the bare serialized object (HTTP 200) — not a paginated
   `{"results": [...]}` envelope. A miss raises `DoesNotExist` which
   DRF turns into HTTP 404. `GetIssueByExternalRef` therefore treats
   404 as `ErrNotFound`. Source:
   `apps/api/plane/api/views/issue.py`, `IssueListCreateAPIEndpoint.get`,
   lines around the `request.GET.get("external_id")` /
   `request.GET.get("external_source")` block. As a belt-and-braces
   guard the client also returns `ErrNotFound` if a 200 ever arrives
   with an empty `id` field, so a future Plane-side change to the
   pagination contract won't silently return zero-value WorkItems.
2. **State list pagination.** `/states/` is paginated through Plane's
   `BasePaginator`, which wraps rows in
   `{"results": [...], "next_cursor": ..., ...}`. `ListProjectStates`
   decodes only `results`; we read the first page only because Plane
   projects rarely have more than a handful of states. Source:
   `apps/api/plane/utils/paginator.py`.
3. **Authentication headers.** Plane's API supports two auth schemes:
   `X-Api-Key: <token>` for personal/workspace API tokens (the bridge's
   usual deployment) and `Authorization: Bearer <token>` for OAuth
   access tokens. The client sends both on every request so the same
   `Client.APIKey` works against either deployment shape without a
   config switch. Source: `makeplane/plane`
   `apps/api/plane/api/middleware/api_authentication.py`.
4. **Trailing slashes are mandatory.** Plane's URLConf does not redirect
   missing trailing slashes; the client constructs every path with one.
5. **Label endpoints.** Labels are scoped to a project and share Plane's
   `BasePaginator` envelope on list (`{"results": [...], ...}`), so
   `ListProjectLabels` decodes only the rows — same pattern as
   `ListProjectStates`. Two quirks worth recording in case a caller needs
   to handle them: (a) Plane returns **HTTP 409 Conflict** (not 422) when a
   label with the same name already exists in the project, and the
   response body includes the existing label's `id` so a "get-or-create"
   caller can recover without a second list; (b) the `LabelDetailAPIEndpoint`
   subclasses `LabelListCreateAPIEndpoint` so update/delete reuse the same
   queryset filter (`project__archived_at__isnull=True`) — archived
   projects' labels are invisible to the API. Source:
   `apps/api/plane/api/urls/label.py` (the two `path(...)` entries) and
   `apps/api/plane/api/views/issue.py` (`LabelListCreateAPIEndpoint` and
   `LabelDetailAPIEndpoint`; the 409 branches live in the `post` body and
   the `IntegrityError` except clause).
6. **Comment endpoints.** The work-item detail endpoint is
   `GET /workspaces/{slug}/projects/{pid}/issues/{iid}/` and comments
   are nested under the work item:
   `POST/GET .../issues/{iid}/comments/` and
   `PATCH/DELETE .../issues/{iid}/comments/{cid}/`. The detail view also
   accepts `GET` per `IssueCommentDetailAPIEndpoint`'s `http_method_names`
   list. Source: `apps/api/plane/api/urls/work_item.py` (the
   `IssueDetailAPIEndpoint` and `IssueCommentListCreateAPIEndpoint` /
   `IssueCommentDetailAPIEndpoint` path entries) and
   `apps/api/plane/api/serializers/issue.py` (`IssueCommentSerializer`,
   which is also the serializer Plane uses for the webhook payload — so
   the REST and webhook comment shapes match field-for-field).

7. **Sequence-ID lookup.** Plane's UI displays each work item as
   `<PROJECT_IDENTIFIER>-<SEQUENCE_ID>` (e.g. `PFB-123`), but the public
   v1 list endpoint at `/workspaces/{slug}/projects/{pid}/issues/` does
   **not** accept a `?sequence_id=` filter — only `external_id` +
   `external_source` short-circuit there. The only public endpoint that
   looks up by sequence_id is `WorkspaceIssueAPIEndpoint`, mounted at
   both `/workspaces/{slug}/work-items/{project_identifier}-{issue_identifier}/`
   (current) and the deprecated `/workspaces/{slug}/issues/{project_identifier}-{issue_identifier}/`
   alias. The view executes `Issue.objects.get(workspace__slug=slug,
   project__identifier=project_identifier, sequence_id=issue_identifier)`
   and returns the bare serialized object on hit (HTTP 200) or 404 on
   miss — same response shape as `GetIssueByExternalRef`. Because the
   lookup keys on the project's *short identifier code* (the "PFB" in
   "PFB-123"), the `projectID` parameter to `GetIssueBySequenceID` is
   the identifier string, not the project UUID accepted by the other
   methods. As a belt-and-braces guard the client also returns
   `ErrNotFound` if a 200 ever arrives with an empty `id` field.
   Source: `apps/api/plane/api/views/issue.py`
   (`WorkspaceIssueAPIEndpoint.get`, lines ~228–248) and
   `apps/api/plane/api/urls/work_item.py` (the
   `work-item-by-identifier` / `issue-by-identifier` path entries).

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
5. **REST API client extras.** `Client` covers the v1 issue
   create/update/lookup + state list surface and the comment
   create/update/delete surface (see "Client (outbound REST)" above).

## Fixtures

Under `testdata/`:

- `work_item_created.json`
- `work_item_updated.json`
- `work_item_deleted.json`
- `comment_created.json`
- `comment_updated.json`

All fixtures use placeholder UUIDs; no real workspace data is checked
in.
