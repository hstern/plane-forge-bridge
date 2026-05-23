# internal/sync

Bidirectional translation between parsed forge webhook events and Plane
REST API calls. Covers issues (step 6), comments (step 7), labels +
state-map coverage on issue writes (step 8), and PR/branch → work-item
state automation (step 9) in the
[build order](../../AGENTS.md#build-order).

## What it does

Takes a parsed `*forge.Event` or `*plane.Event` and turns it into the
right REST call on the OTHER side, idempotently, with the loop-break
marker baked into every body it writes.

```
forge webhook → server (HMAC + LRU) → sync.Engine.HandleForgeIssue       → plane REST
                                    → sync.Engine.HandleForgeComment     → plane REST
                                    → sync.Engine.HandleForgePullRequest → plane REST
plane webhook → server (HMAC + LRU) → sync.Engine.HandlePlaneComment     → forge REST
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
    GetIssueBySequenceID(ctx, projectID string, sequenceID int) (*plane.WorkItem, error)
    CreateIssue(ctx, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
    UpdateIssue(ctx, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
    ListProjectStates(ctx, projectID string) ([]plane.State, error)
    ListProjectLabels(ctx, projectID string) ([]plane.Label, error)
    CreateProjectLabel(ctx, projectID string, req plane.CreateLabelRequest) (*plane.Label, error)
    CreateComment(ctx, projectID, issueID string, req plane.CreateCommentRequest) (*plane.Comment, error)
    UpdateComment(ctx, projectID, issueID, commentID string, req plane.UpdateCommentRequest) (*plane.Comment, error)
    DeleteComment(ctx, projectID, issueID, commentID string) error
}

type ForgeClient interface {
    GetIssue(ctx, owner, repo string, number int64) (*forge.Issue, error)
    ListRepoLabels(ctx, owner, repo string) ([]forge.Label, error)
    CreateRepoLabel(ctx, owner, repo string, req forge.CreateLabelRequest) (*forge.Label, error)
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
func (e *Engine) HandleForgePullRequest(ctx context.Context, evt *forge.Event) (*Outcome, error)
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

| evt.Kind              | If found on Plane                       | If not found on Plane               |
|-----------------------|-----------------------------------------|-------------------------------------|
| `IssueOpened`         | `UpdateIssue` (reconcile: title+body+labels+open state) | `CreateIssue` (title+body+labels+open state) |
| `IssueEdited`         | `UpdateIssue` (title+body+labels; state untouched)      | log warn, `ActionSkipped`           |
| `IssueClosed`         | `UpdateIssue` (state=closed; labels untouched)          | `ActionSkipped`                     |
| `IssueReopened`       | `UpdateIssue` (state=open; labels untouched)            | `ActionSkipped`                     |
| anything else         | n/a                                     | `ActionSkipped` ("unsupported")     |

Why `IssueEdited` does NOT touch state: forge fires `issues.edited` for any
property change — title, body, assignee, labels — and the payload doesn't
reliably signal whether the state changed. The explicit transitions arrive
as `IssueClosed` / `IssueReopened` where we DO translate via
`link.StateMap`. Touching state on edit risks moving Plane backwards when a
user edits the title of a closed forge issue.

Why `IssueClosed` / `IssueReopened` do NOT touch labels: those events are
pure state transitions. The latest labels arrive via the next
`IssueOpened` (on reconcile redelivery) or `IssueEdited`. Reading label
state on every close/reopen would add a wasted resolver pass on the hot
path.

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

### forge → plane PR/branch state (`HandleForgePullRequest`)

Step 9. Pull request lifecycle events nudge the linked Plane work item's
state — opening a PR moves it to "In Progress", merging to "Done",
closing without merge to "Cancelled", etc. The exact state names are
operator-supplied via `link.pr_state_map`.

PR automation is **opt-in per link**: a link with no `project_identifier`
or empty `pr_state_map` produces `ActionSkipped` with
`Reason="no PR automation configured for this link"` and zero API calls.
This keeps existing issue/comment-only deployments unaffected.

Decision tree (after `linkForRepo` matches and PR automation is enabled):

| evt.Kind                | Merged | Behaviour                                                                              |
|-------------------------|--------|----------------------------------------------------------------------------------------|
| `PullRequestOpened`     | n/a    | parse ref → `GetIssueBySequenceID` → `UpdateIssue` with `pr_state_map["opened"]`       |
| `PullRequestReopened`   | n/a    | as above, also keyed on `"opened"` — reopening lands in the same lane as opening       |
| `PullRequestClosed`     | true   | parse ref → `GetIssueBySequenceID` → `UpdateIssue` with `pr_state_map["merged"]`       |
| `PullRequestClosed`     | false  | parse ref → `GetIssueBySequenceID` → `UpdateIssue` with `pr_state_map["closed"]`       |
| `PullRequestEdited`     | n/a    | `ActionSkipped`, `Reason="PR action \"…\" does not map to a state transition"`         |
| `PullRequestReview`     | n/a    | `ActionSkipped`, `Reason="review event handling deferred to a later step"`             |
| anything else           | n/a    | `ActionSkipped` (same "no state transition" reason)                                    |

Skip reasons (each surfaces a specific operator-facing failure mode so
the logs are actionable):

- `no link configured for repo` — repo not in `links:`.
- `no PR automation configured for this link` — link present but no
  `project_identifier` + `pr_state_map`.
- `no [<IDENT>-N] ref found in PR title/body or branch name` — the PR
  doesn't reference a Plane work item we can target.
- `[<IDENT>-N] does not exist on the configured Plane project` — operator
  typoed the ref, the work item was deleted, or the link points at a
  different project than the PR thinks.
- `no state transition configured for action "<action>" on this link` —
  the action key isn't present in `pr_state_map`.
- `PR action "<kind>" does not map to a state transition` — edit /
  unknown action.
- `review event handling deferred to a later step` — `pull_request_review`.

A misconfigured state NAME (mapping pointing at a state Plane doesn't
have) returns an **error**, not a skip — that's a config bug the
operator should see loudly. Updates issue a PATCH unconditionally even
when the current state already matches the target; the no-op
optimisation can land later behind a benchmark.

### PR ref grammar

Two surfaces, tried in this order; the first hit wins:

| Source       | Pattern (regex)                            | Case sensitivity            |
|--------------|--------------------------------------------|-----------------------------|
| PR title     | `` \[<IDENT>-([0-9]+)\] ``                 | identifier exact            |
| PR body      | `` \[<IDENT>-([0-9]+)\] `` (same regex)    | identifier exact            |
| Head branch  | ``(?i)^<ident>-([0-9]+)(?:-\|$)``          | identifier case-insensitive |

- `<IDENT>` is the link's `project_identifier` (e.g. `PFB`), regex-escaped.
- Title and body use the canonical UPPERCASE form Plane displays —
  `[pfb-42]` does NOT match in the title/body. Branch names follow
  the lowercase-with-hyphens convention, so the head-branch surface is
  case-insensitive on the identifier.
- The digit run must be followed by either `-` (a slug suffix) or
  end-of-string. `pfb-42abc` does NOT match as 42 — that prevents
  accidental routing when the slug accidentally collides with digits.
- If multiple matches exist in the title or body (or in the branch),
  the FIRST match in source order wins.
- Sequence IDs that overflow `int` (e.g. a 21-digit literal in the body)
  are rejected so they don't wrap to garbage; parsing falls through to
  the next source.
- The identifier is required to be the SAME identifier configured on
  the link — we deliberately don't scan for arbitrary `[XXX-N]` so we
  can't mis-route to the wrong Plane project.

### Supported `pr_state_map` keys

| Key      | Triggered by                                                              |
|----------|---------------------------------------------------------------------------|
| `opened` | `pull_request.opened`, `pull_request.reopened`                            |
| `merged` | `pull_request.closed` with `merged=true`                                  |
| `closed` | `pull_request.closed` with `merged=false`                                 |
| `reviewed` | reserved — review-event handling is deferred (see below)                |

The loader rejects any other keys; this lets operators learn about typos
at config-load time rather than via a missing PR transition six weeks
later. The `reviewed` key is accepted by the loader (so configs are
forward-compatible) but does nothing in step 9: review-state →
work-item-state mapping needs more thought (does "approved" advance to
"In Review"? "In QA"? operator preference, no canonical mapping). When
that question firms up, `HandleForgePullRequest` will switch from
returning `Reason="review event handling deferred to a later step"` to
applying the configured transition.

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

## Label translation

`labelResolver` (in `labels.go`) maps forge label *names* onto Plane label
*UUIDs* scoped to a single Plane project. The resolver is wired into the
issue create, reconcile, and edit paths so any labels carried on the forge
event land on the Plane work item.

```
forge labels → resolveLabels(projectID, names) → []plane-label-UUID
```

Resolution:

1. First call for a project (or after the 5-minute TTL expires) →
   `ListProjectLabels`, build a `name → UUID` map.
2. Subsequent calls → serve from the cache.
3. Name miss → `CreateProjectLabel`, fold the resulting UUID into the cache
   so the next lookup hits.

Like the state cache, refresh and create attempts serialise on a
per-project mutex so N concurrent webhooks for the same project trigger at
most one `ListProjectLabels`. The thundering-herd guard is covered by
`TestResolveLabels_ConcurrentSameProject_OneList`.

Empty input → empty output, zero API calls. Errors from `ListProjectLabels`
or `CreateProjectLabel` propagate; the engine fails the event rather than
silently dropping labels.

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

### Label cache invalidation

The label resolver does not invalidate on rename. If an operator renames a
label in Plane while the cache is warm, the engine keeps returning the
cached UUID for the OLD name and will auto-create a duplicate under the
NEW name for any forge event that arrives before the 5-minute TTL elapses.

The blast radius is a name collision in the Plane project, not data loss,
and operators can wait out the TTL. Options if we need to do better:

1. Cache-bust on `CreateProjectLabel` failure when Plane returns a
   uniqueness conflict (today we propagate the error).
2. Subscribe to Plane label.* webhooks once they exist and invalidate
   reactively.
3. Shorten the TTL. The cost is more `ListProjectLabels` traffic; the
   five-minute value is conservative and matches `stateCacheTTL`.

## Not yet implemented

This package covers issues (step 6), the forward/backward comment paths
(step 7), label translation + complete state-map coverage on issue
writes (step 8), and PR/branch → work-item state automation (step 9).
Remaining gaps live in later steps:

- **PR review state → work-item state** — `pull_request_review` events
  are short-circuited as `ActionSkipped` for now (see the PR section
  above). The `reviewed` key in `pr_state_map` is reserved.
- **Plane → forge issue translation** (step 10) — a separate handler that
  the server will dispatch from `/plane/webhook`. The forge-side label
  helpers (`ListRepoLabels` / `CreateRepoLabel`) are wired into the
  `ForgeClient` interface and fake in this step so step 10 can consume
  them without touching the contract.
- **Comment update/delete in both directions** — see "Open questions".

## Dependencies

Standard library only.
