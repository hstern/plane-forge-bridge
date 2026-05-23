# `internal/server`

HTTP front-end of the bridge. Wires three routes:

| Method | Path             | Behavior                                                                                                                                                                       |
| ------ | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `POST` | `/forge/webhook` | `MaxBytesReader(1 MiB)` → `forge.VerifyAndParse` → log → check loop-break marker → `202 Accepted` (or `200 OK` if marker matched and event was dropped).                       |
| `POST` | `/plane/webhook` | `MaxBytesReader(1 MiB)` → `plane.VerifyAndParse` → log → check loop-break marker → `202 Accepted` (or `200 OK` if marker matched and event was dropped).                       |
| `GET`  | `/healthz`       | `200 OK` body `ok\n`. Used by the container healthcheck.                                                                                                                       |

HMAC verification happens at the handler boundary before the body is parsed (constant-time compare lives in the dialect packages). Body size cap is defence-in-depth against a misconfigured proxy.

For issue events on the forge side (`issues.opened`, `issues.edited`, `issues.closed`, `issues.reopened`), the handler hands the parsed event to a `Translator` (production: `*sync.Engine`) which decides whether to create or update a Plane work item. Other event kinds (push, pull_request_review, etc.) are accepted but not dispatched in step 6.

The `Translator` field is optional. When nil, the server verifies + logs + returns 202 without outbound calls — useful for smoke deploys and tests that want to exercise just the HTTP surface.

After a successful translation, the server records `(forge_delivery_id, plane_work_item_id)` in the loop-break LRU so an echoed Plane webhook caused by that write can be dropped via the LRU even if the marker is stripped downstream.

## Response status conventions

- `202 Accepted` — verified, parsed, will be acted on (once translation exists).
- `200 OK` — verified, parsed, dropped because the body carried our own loop-break marker.
- `204 No Content` — verified, but the event type isn't one we handle. Tells the forge to stop retrying.
- `400 Bad Request` — missing event header, malformed JSON, unreadable body.
- `401 Unauthorized` — missing or invalid signature.
- `413 Request Entity Too Large` — body exceeded 1 MiB.
- `500 Internal Server Error` — anything unexpected (slogged at error level).

## Public API

```go
type Translator interface {
    HandleForgeIssue(ctx context.Context, evt *forge.Event) (*sync.Outcome, error)
}

func New(cfg *mapping.Resolved, log *slog.Logger, translator Translator) *Server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (s *Server) ListenAndServe(ctx context.Context) error
```

`ListenAndServe` blocks until `ctx` is cancelled, then shuts down gracefully with a 15 s timeout.

## Loop-break check

The check reads the event's textual body (`Issue.Body` / `Comment.Body` / `PullRequest.Body` for forge; `WorkItem.Description` / `Comment.CommentHTML` for plane) and runs `idemp.Extract` on it. If a marker is present we drop the event — it's our own echo coming back.

The LRU layer activates on the outbound side: after a successful translation the server records `(forge_delivery_id, plane_work_item_id)`. The plane→forge inbound path (step 10) will consult that LRU to drop echoed events whose marker got stripped between the bridge and Plane.
