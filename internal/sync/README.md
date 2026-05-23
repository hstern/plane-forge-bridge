# internal/sync

Bidirectional translation between parsed forge webhook events and Plane
REST API calls. Covers issues (step 6) and comments (step 7) in the
[build order](../../AGENTS.md#build-order).

## What it does

Takes a parsed `*forge.Event` or `*plane.Event` and turns it into the
right REST call on the OTHER side, idempotently, with the loop-break
marker baked into every body it writes.

```
forge webhook → server (HMAC + LRU) → sync.Engine.HandleForgeIssue   → plane REST
                                    → sync.Engine.HandleForgeComment → plane REST
plane webhook → server (HMAC + LRU) → sync.Engine.HandlePlaneComment → forge REST
```

The engine is a pure translator. It does **not** own the loop-break LRU —
the server consults the LRU before calling a `Handle*` method and records
the returned `Outcome` afterwards. Keeping the LRU out of the engine makes
the translator testable in isolation and matches the "single writer"
invariant in [AGENTS.md](../../AGENTS.md#architecture-invariants).

## Public API

```go
type PlaneClient interface {
    GetIssue(ctx, projectID, issueID string) (*plane.WorkItem, error)
    GetIssueByExternalRef(ctx, projectID, source, externalID string) (*plane.WorkItem, error)
    CreateIssue(ctx, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
    UpdateIssue(ctx, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
    ListProjectStates(ctx, projectID string) ([]plane.State, error)
    CreateComment(ctx, projectID, issueID string, req plane.CreateCommentRequest) (*plane.Comment, error)
    UpdateComment(ctx, projectID, issueID, commentID string, req plane.UpdateCommentRequest) (*plane.Comment, error)
    DeleteComment(ctx, projectID, issueID, commentID string) error
}

type ForgeClient interface {
    GetIssue(ctx, owner, repo string, number int64) (*forge.Issue, error)
    CreateComment(ctx, owner, repo string, issueNumber int64, req forge.CreateCommentRequest) (*forge.Comment, error)
    UpdateComment(ctx, owner, repo string, commentID int64, req forge.UpdateCommentRequest) (*forge.Comment, error)
    DeleteComment(ctx, owner, repo string, commentID int64) error
}

type BridgeBot struct {
    ForgeUsername string
    PlaneMemberID string
}

type Engine struct {
    Client      PlaneClient
    ForgeClient ForgeClient
    Links       []mapping.Link
    Users       map[string]string
    Bot         BridgeBot
    Log         *slog.Logger
    // + an unexported state cache
}

func NewEngine(c PlaneClient, cfg *mapping.Resolved, log *slog.Logger) *Engine
func (e *Engine) HandleForgeIssue(ctx context.Context, evt *forge.Event) (*Outcome, error)
func (e *Engine) HandleForgeComment(ctx context.Context, evt *forge.Event) (*Outcome, error)
func (e *Engine) HandlePlaneComment(ctx context.Context, evt *plane.Event) (*Outcome, error)

type Outcome struct {
    Action     Action
    WorkItemID string         // Created/Updated issue events; carries the plane
                              // work-item ID the comment is attached to on
                              // comment outcomes
    CommentID  string         // Created/Updated comment events; the comment's
                              // ID on the OTHER side
    Reason     string
    Link       *mapping.Link
}

type Action int
const (
    ActionSkipped Action = iota
    ActionCreated
    ActionUpdated
)

// Body rendering helpers, exported so they can be reused if the
// translation path moves.
func RenderDescription(forgeBody, senderLogin, senderHTMLURL, repoFullName, deliveryID string, mapped bool) string
func RenderComment(originalBody, senderLogin, senderHTMLURL, repoFullName, deliveryID string, src idemp.Source, mapped bool) string

// parseExternalRef is unexported but documented here so the contract is
// visible. It validates the (external_source, external_id) pair the bridge
// stamps on every work item and is the plane→forge inverse of externalRef.
//   source=="forge:owner/repo", externalID=="<positive int64>"
```

## Decision tree

### forge → plane issues (`HandleForgeIssue`)

Branches on `evt.Kind` after first checking that `evt.Repo.FullName` is in
the configured link list:

| evt.Kind              | If found on Plane           | If not found on Plane               |
|-----------------------|-----------------------------|-------------------------------------|
| `IssueOpened`         | `UpdateIssue` (reconcile)   | `CreateIssue`                       |
| `IssueEdited`         | `UpdateIssue` (title+body)  | log warn, `ActionSkipped`           |
| `IssueClosed`         | `UpdateIssue` (state=closed)| `ActionSkipped`                     |
| `IssueReopened`       | `UpdateIssue` (state=open)  | `ActionSkipped`                     |
| anything else         | n/a                         | `ActionSkipped` ("unsupported")     |

### forge → plane comments (`HandleForgeComment`)

| evt.Kind                    | Behaviour                                                                                                |
|-----------------------------|----------------------------------------------------------------------------------------------------------|
| `IssueCommentCreated`       | `GetIssueByExternalRef`, then `CreateComment` with marker-wrapped body. Skip if the issue isn't mirrored. |
| `IssueCommentEdited`        | `ActionSkipped` — see "Open questions" below                                                              |
| `IssueCommentDeleted`       | `ActionSkipped` — see "Open questions" below                                                              |
| anything else               | `ActionSkipped` ("unsupported")                                                                          |

### plane → forge comments (`HandlePlaneComment`)

| evt.Kind                    | Behaviour                                                                                                |
|-----------------------------|----------------------------------------------------------------------------------------------------------|
| `EventCommentCreated`       | `plane.GetIssue` → `parseExternalRef` → `forge.CreateComment` with marker-wrapped body                    |
| `EventCommentUpdated`       | `ActionSkipped` — see "Open questions" below                                                              |
| `EventCommentDeleted`       | `ActionSkipped` — see "Open questions" below                                                              |
| anything else               | `ActionSkipped` ("unsupported")                                                                          |

Repos with no link in config short-circuit to `ActionSkipped` with
`Reason="no link configured for repo"` and zero API calls — the bridge
only acts on what it has been explicitly told to mirror.

The "edit without prior open" case for issues is deliberately not a
create: an edit arrived without us seeing the open, which means we missed
the open. The resulting work item would have a wrong open timestamp and
no creation attribution. We log a warning and surface the gap rather than
paper over it.

The "comment for an un-mirrored issue" case on the forge→plane path
returns `ActionSkipped` with `Reason="comment fired before plane issue
was mirrored"`. The forge webhook ordering doesn't guarantee that the
`issue.opened` event for a brand-new issue arrives before the
`issue_comment.created` event for its first comment — but the bridge can
recover on the next forge → plane delivery once the issue is mirrored.

## External reference convention

Every work item the bridge creates from a forge issue carries:

| Field             | Value                                                  |
|-------------------|--------------------------------------------------------|
| `external_source` | `"forge:" + repo.FullName` (e.g. `forge:acme/widgets`) |
| `external_id`     | `strconv.FormatInt(issue.Number, 10)`                  |

**Step 6 → step 7 contract change:** `external_id` is now the forge issue
NUMBER (per-repo monotonic), not the internal database ID. This lets the
plane → forge inbound comment path resolve the forge issue with
`forge.GetIssue(owner, repo, number)`. Looking up by DB id requires an
endpoint Forgejo doesn't reliably expose. The change is backwards-
incompatible with any work items created during step 6, but step 6 only
just landed; there is no migration shim.

The pair is stable across re-deliveries of the same forge issue —
`GetIssueByExternalRef` on this pair is what makes `HandleForgeIssue`
idempotent: a redelivered open finds the existing work item and falls
through to the update branch instead of creating a duplicate.

`parseExternalRef(source, externalID)` is the inverse used on the
plane → forge path: it pulls the parsed `(owner, repo, number)` back out
of a plane work item so we know which forge issue to comment on.

## Body rendering

Both `RenderDescription` and `RenderComment` produce the body for every
write the engine performs.

```
[optional unmapped-author preface]

<original body>

<!-- pfb:src=<forge|plane>,evt=<delivery-id> -->
```

- The trailing loop-break marker is always present and is the last
  non-whitespace content. Emission is delegated to `idemp.Wrap`, so the
  canonical marker format and idempotency semantics live in one place.
- The unmapped-author preface fires only when the sender's username has
  no entry in `users` (identity is config in v1; see
  [design.md](../../docs/design.md#identity-mapping)). The preface
  string is built by the shared `unmappedAuthorPreface` helper so the
  format only lives in one place; the regression guard
  `TestRenderComment_PrefaceHelperSharedWithRenderDescription` catches
  any drift.
- `RenderComment` takes the source side as an explicit parameter, since
  comments flow in both directions; `RenderDescription` is hard-wired to
  `idemp.SourceForge` because in step 6 / step 7 descriptions only flow
  forge → plane.

## State mapping

Per-link configuration translates forge's two-state world (`open`,
`closed`) to Plane's named workflow states (`Todo`, `Done`, ...).
`ResolveStateID`:

1. Looks up `link.StateMap[forgeState]` to get the Plane state *name*.
2. Resolves the name to a UUID via `ListProjectStates`, with results
   cached per project for at most 5 minutes.

Returns `""` (with no error) when the link has no mapping for that forge
state — callers treat that as "don't change state". That matches the
design doc's "we do not invent transitions" promise. Returns an error
when the mapped name doesn't exist in the project; that's an operator
config bug we surface rather than silently drop.

The state cache is concurrency-safe. Concurrent cold-fill calls for the
same project coalesce on a per-project mutex, so the engine doesn't fan
out N parallel `ListProjectStates` requests under bursty webhook load.

## Idempotency

Two redeliveries of the same forge open event produce the same Plane work
item:

1. First call: `GetIssueByExternalRef` → `ErrNotFound` → `CreateIssue`.
2. Second call: `GetIssueByExternalRef` → returns the existing work item →
   `UpdateIssue` (reconcile branch).

For comments, the marker on the body is the per-delivery defence: a
redelivered comment write produces the same marker, and the loop-break
LRU described in [AGENTS.md](../../AGENTS.md#architecture-invariants)
short-circuits the second delivery before it even reaches the engine.

The LRU is the server's job — not this package's — because the LRU's key
is `(source_event_id, target_object_id)` and the engine doesn't know the
target object ID until *after* it has run. The server records the
`Outcome.WorkItemID` / `Outcome.CommentID` it gets back.

## Open questions

### Comment identity mapping

There is no `external_id` field on Plane comments (only work items
support it). When `issue_comment.edited` fires on the forge side, we get
the forge comment ID; we need to map that to a Plane comment ID to call
`plane.UpdateComment`. The stateless bridge has no persisted side-table,
so today we cannot.

**Step 7 deferral:** `HandleForgeComment` and `HandlePlaneComment` return
`ActionSkipped` on the `*Edited`/`*Updated` and `*Deleted` branches with
a clear `Reason` (`"forge comment update/delete needs identity mapping
(deferred to a later step)"` and the symmetric `"plane comment ..."`).
Only the **Created** branches make a write. This is the minimum useful
path — any comment added on either side appears on the other.

Sketch of follow-ups, ordered by cost:

1. **Small persistent KV.** A SQLite or BoltDB side-table keyed on the
   composite `(side, comment_id) → (other_side, comment_id)`. Two-row
   write per mirrored Created, single-row read per Edited/Deleted.
   Breaks the "stateless process" invariant in AGENTS.md but only for
   this one mapping; the rest of the bridge remains stateless.
2. **Stuff the foreign ID into the body.** Encode the
   forge↔plane comment-ID pair inside an HTML comment beside the
   loop-break marker (e.g. `<!-- pfb:rev=<other-id> -->`). Re-derive on
   Edit/Delete by parsing the body of the comment we're trying to
   mirror. Cute, but Plane allows the user to delete that line.
3. **Content-fingerprint matching.** On Edit, list comments on the
   destination side, compute a marker+body fingerprint, find the
   matching one. Brittle, but doesn't need persistence.

(1) is the most likely choice and is tracked outside this package, in
the build-order roadmap.

## Not yet implemented

This package covers issues (step 6) and the forward/backward comment
paths (step 7). Remaining gaps live in later steps:

- **Labels** (step 8) — currently the engine ignores `Issue.Labels`.
- **PR / branch → work-item state automation** (step 9) — pull request
  events are short-circuited as `ActionSkipped` for now.
- **Plane → forge issue translation** (step 10) — a separate handler that
  the server will dispatch from `/plane/webhook`. This package's step-7
  `HandlePlaneComment` is the prototype for how that dispatch will work.
- **Comment update/delete in both directions** — see "Open questions".

## Dependencies

Standard library only.
