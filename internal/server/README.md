# `internal/server`

HTTP front-end of the bridge. Wires three routes:

| Method | Path             | Behavior                                                                                                                                                                       |
| ------ | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `POST` | `/forge/webhook` | `MaxBytesReader(1 MiB)` → `forge.VerifyAndParse` → log → check loop-break marker → `202 Accepted` (or `200 OK` if marker matched and event was dropped).                       |
| `POST` | `/plane/webhook` | `MaxBytesReader(1 MiB)` → `plane.VerifyAndParse` → log → check loop-break marker → `202 Accepted` (or `200 OK` if marker matched and event was dropped).                       |
| `GET`  | `/healthz`       | `200 OK` body `ok\n`. Used by the container healthcheck.                                                                                                                       |

HMAC verification happens at the handler boundary before the body is parsed (constant-time compare lives in the dialect packages). Body size cap is defence-in-depth against a misconfigured proxy.

This package does **not** translate events into outbound calls — that's the job of `internal/sync` (added in step 6+). For now, handlers log the parsed event and return.

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
func New(cfg *mapping.Resolved, log *slog.Logger) *Server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (s *Server) ListenAndServe(ctx context.Context) error
```

`ListenAndServe` blocks until `ctx` is cancelled, then shuts down gracefully with a 15 s timeout.

## Loop-break check

The check reads the event's textual body (`Issue.Body` / `Comment.Body` / `PullRequest.Body` for forge; `WorkItem.Description` / `Comment.CommentHTML` for plane) and runs `idemp.Extract` on it. If a marker is present we drop the event — it's our own echo coming back. The LRU is allocated but not yet consulted on the handler path; it comes into play in step 6+ when translation knows the target object ID.
