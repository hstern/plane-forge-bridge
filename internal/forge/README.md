# internal/forge

Webhook signature verification and payload parsing for Gitea-API-compatible
forges — [Forgejo](https://forgejo.org/) and [Gitea](https://about.gitea.com/).

## Why one package speaks both

Forgejo forked from Gitea and the two projects still share the v1 webhook
contract byte-for-byte for the events this bridge cares about:

| Header | Meaning |
|---|---|
| `X-Gitea-Signature` | HMAC-SHA256 of the raw body, hex-encoded |
| `X-Gitea-Event`     | Event name (`issues`, `issue_comment`, `pull_request`, `pull_request_review`, `push`) |
| `X-Gitea-Delivery`  | Unique delivery UUID — used as the loop-break event ID |

Splitting into `forgejo/` and `gitea/` subpackages would duplicate every
struct and decode path with no payoff. The matrix CI job exercises both
forge images against this single package to keep that assumption honest.

## Public API

```go
// Verify, then parse — the convenience most HTTP handlers want.
func VerifyAndParse(secret string, headers http.Header, body []byte) (*Event, error)

// Or call them individually.
func VerifySignature(secret string, headers http.Header, body []byte) error
func Parse(headers http.Header, body []byte) (*Event, error)

// Sentinel errors — match with errors.Is.
var (
    ErrEmptySecret        // empty secret is a config bug; refuse to verify.
    ErrMissingSignature   // map to HTTP 401
    ErrInvalidSignature   // map to HTTP 401
    ErrMissingEventHeader // map to HTTP 400
    ErrUnsupportedEvent   // ack-and-drop with HTTP 204
    ErrMalformedPayload   // map to HTTP 400
    ErrNotFound           // outbound: GetIssue / DeleteComment got HTTP 404
)

// EventKind names a parsed event we care about.
type EventKind string
// ... EventIssueOpened, EventIssueClosed, EventPullRequestClosed, etc.

// Event is the parsed delivery; only the fields for the matching family are set.
type Event struct {
    Kind        EventKind
    DeliveryID  string
    Repo        Repository
    Sender      User
    Issue       *Issue
    Comment     *Comment
    PullRequest *PullRequest
    Review      *Review
    Push        *Push
    Raw         []byte
}
```

The `Client` struct + `NewClient` constructor live in the same package as
the inbound webhook code; the outbound REST surface (`GetIssue`, comment
CRUD) is documented in the "Client (outbound REST)" section below.
Keeping both directions in one package preserves the "one package, one
forge contract" invariant from `AGENTS.md`.

## Client (outbound REST)

`Client` speaks the Gitea/Forgejo v1 REST API and is the bridge's
outbound write path on the forge side. It is the sibling of
`internal/plane`'s `Client` — same `do` plumbing, same `*APIError` /
`ErrNotFound` error model, just different auth and path conventions.

All methods take a `context.Context`, set
`Authorization: token <Token>` (the "token" scheme is canonical for
Forgejo and Gitea — NOT "Bearer"), `Accept: application/json`,
`User-Agent: <Client.UserAgent or "plane-forge-bridge">`, and
`Content-Type: application/json` on bodies. Responses are size-capped at
4 MiB; non-2xx responses become `*APIError` with up to 4 KiB of the
response body preserved for diagnosis.

```go
type Client struct {
    BaseURL, Token, UserAgent string
    HTTPClient *http.Client
}
func NewClient(baseURL, token string, hc *http.Client) *Client

func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int64) (*Issue, error)
func (c *Client) CreateComment(ctx context.Context, owner, repo string, issueNumber int64, req CreateCommentRequest) (*Comment, error)
func (c *Client) UpdateComment(ctx context.Context, owner, repo string, commentID int64, req UpdateCommentRequest) (*Comment, error)
func (c *Client) DeleteComment(ctx context.Context, owner, repo string, commentID int64) error
```

| Method          | HTTP   | Path                                                       | Returns                |
| --------------- | ------ | ---------------------------------------------------------- | ---------------------- |
| `GetIssue`      | GET    | `/api/v1/repos/{owner}/{repo}/issues/{number}`             | `*Issue` / `ErrNotFound` |
| `CreateComment` | POST   | `/api/v1/repos/{owner}/{repo}/issues/{index}/comments`     | `*Comment`             |
| `UpdateComment` | PATCH  | `/api/v1/repos/{owner}/{repo}/issues/comments/{id}`        | `*Comment`             |
| `DeleteComment` | DELETE | `/api/v1/repos/{owner}/{repo}/issues/comments/{id}`        | `nil` / `ErrNotFound`  |

Request bodies:

```go
type CreateCommentRequest struct { Body string `json:"body"` }
type UpdateCommentRequest struct { Body string `json:"body"` }
```

The comment endpoints today only accept `body`; the request types stay
as structs (rather than `map[string]string`) so future optional fields
can be added without churning every caller.

### Error model

- `ErrNotFound` — the "no such resource" signal from `GetIssue` and
  `DeleteComment`. Use `errors.Is(err, forge.ErrNotFound)`. Distinct
  from the webhook sentinels in `errors.go`, which are inbound concerns.
- `*APIError` — any other non-2xx response. `errors.As(err, &apiErr)`
  exposes `StatusCode`, `Method`, `Path`, and the truncated `Body`.

### Forge API notes

1. **Auth scheme is "token", not "Bearer".** Forgejo and Gitea both
   document `Authorization: token <pat>` for personal access tokens.
   Sending `Bearer ...` silently fails on some versions; the
   `TestClient_AuthorizationToken` regression test pins the exact
   header.
2. **No trailing slashes.** The forge accepts paths with or without a
   trailing slash, but its docs use the unslashed form — the client
   matches that, which is the opposite of Plane's convention.
3. **Comment paths.** Create takes the issue *number* (`.../issues/{n}/
   comments`); update/delete take the comment *ID*
   (`.../issues/comments/{id}`). The two namespaces are intentional in
   the upstream API.

## Handler skeleton

```go
body, _ := io.ReadAll(r.Body)
evt, err := forge.VerifyAndParse(secret, r.Header, body)
switch {
case errors.Is(err, forge.ErrMissingSignature),
     errors.Is(err, forge.ErrInvalidSignature):
    http.Error(w, "bad signature", http.StatusUnauthorized)
case errors.Is(err, forge.ErrUnsupportedEvent):
    w.WriteHeader(http.StatusNoContent) // ack-and-drop
case err != nil:
    http.Error(w, "bad request", http.StatusBadRequest)
default:
    dispatch(evt) // evt.DeliveryID is the loop-break marker ID
}
```

## Fixtures

Captured webhook bodies live in `testdata/`:

- `issues_opened.json`
- `issues_edited.json`
- `issues_closed.json`
- `issue_comment_created.json`
- `pull_request_opened.json`
- `pull_request_closed_merged.json`
- `pull_request_closed_unmerged.json`
- `pull_request_review_submitted.json`
- `push.json`

These are hand-built from the documented Forgejo/Gitea webhook schema —
realistic enough to exercise the parser but free of any real account data.
To add a new fixture:

1. Drop the JSON in `testdata/<event>_<action>.json`.
2. Add a row to the `TestParse` table in `parse_test.go` with the matching
   `X-Gitea-Event` header and a `check` closure asserting at least one
   field per typed payload pointer the event populates.
3. `go test ./internal/forge/... -race`.

When real captures from a live forge become available (e.g. from the e2e
docker matrix in step 5), replace the hand-built fixtures in-place — the
parser does not enable `DisallowUnknownFields`, so extra forge-specific
fields are tolerated.

## What is NOT in scope here

- The HTTP handler itself (lives in `cmd/`).
- The loop-break marker emit/strip logic (lives in `internal/loopbreak/`,
  added later — this package only surfaces `DeliveryID` so the caller can
  build the marker).
- Issue create/update/labels on the outbound side — only `GetIssue` plus
  comment CRUD ship today; broader issue mutation lands with later
  build-order steps.
- Identity mapping (`forge_username → plane_member_uuid`) — config concern.
- Anything Plane-specific. Plane payloads are handled by `internal/plane`.
