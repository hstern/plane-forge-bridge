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

The minimal `Client` struct (`BaseURL`, `Token`, `*http.Client`) + `NewClient`
constructor is here as a stub; the outbound REST methods (create issue, post
comment, set labels, ...) land in a later step. Keeping it in this package
preserves the "one package, one forge contract" invariant from `AGENTS.md`.

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
- The outbound REST client methods. The `Client` struct is a placeholder.
- Identity mapping (`forge_username → plane_member_uuid`) — config concern.
- Anything Plane-specific. Plane payloads are handled by `internal/plane`.
