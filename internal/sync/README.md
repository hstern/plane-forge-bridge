# internal/sync

One-way translation from parsed forge webhook events to Plane REST API
calls for issues. Step 6 in the [build order](../../AGENTS.md#build-order).

## What it does

Takes a `*forge.Event` for an `issue.*` delivery and turns it into the
right `CreateIssue` / `UpdateIssue` call on the Plane side, idempotently,
with the loop-break marker baked into every body it writes.

```
forge webhook → server (HMAC + LRU) → sync.Engine.HandleForgeIssue → plane REST
```

The engine is a pure translator. It does **not** own the loop-break LRU —
the server consults the LRU before calling `HandleForgeIssue` and records
the returned `Outcome` afterwards. Keeping the LRU out of the engine makes
the translator testable in isolation and matches the "single writer"
invariant in [AGENTS.md](../../AGENTS.md#architecture-invariants).

## Public API

```go
type PlaneClient interface {
    GetIssueByExternalRef(ctx, projectID, source, externalID string) (*plane.WorkItem, error)
    CreateIssue(ctx, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
    UpdateIssue(ctx, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
    ListProjectStates(ctx, projectID string) ([]plane.State, error)
}

type BridgeBot struct {
    ForgeUsername string
    PlaneMemberID string
}

type Engine struct {
    Client PlaneClient
    Links  []mapping.Link
    Users  map[string]string
    Bot    BridgeBot
    Log    *slog.Logger
    // + an unexported state cache
}

func NewEngine(c PlaneClient, cfg *mapping.Resolved, log *slog.Logger) *Engine
func (e *Engine) HandleForgeIssue(ctx context.Context, evt *forge.Event) (*Outcome, error)

type Outcome struct {
    Action     Action
    WorkItemID string
    Reason     string
    Link       *mapping.Link
}

type Action int
const (
    ActionSkipped Action = iota
    ActionCreated
    ActionUpdated
)

// Body rendering, exported so the future comments path (step 7) can reuse it.
func RenderDescription(forgeBody, senderLogin, senderHTMLURL, repoFullName, deliveryID string, mapped bool) string
```

## Decision tree

`HandleForgeIssue` branches on `evt.Kind` after first checking that
`evt.Repo.FullName` is in the configured link list:

| evt.Kind              | If found on Plane           | If not found on Plane               |
|-----------------------|-----------------------------|-------------------------------------|
| `IssueOpened`         | `UpdateIssue` (reconcile)   | `CreateIssue`                       |
| `IssueEdited`         | `UpdateIssue` (title+body)  | log warn, `ActionSkipped`           |
| `IssueClosed`         | `UpdateIssue` (state=closed)| `ActionSkipped`                     |
| `IssueReopened`       | `UpdateIssue` (state=open)  | `ActionSkipped`                     |
| anything else         | n/a                         | `ActionSkipped` ("unsupported")     |

Repos with no link in config short-circuit to `ActionSkipped` with
`Reason="no link configured for repo"` and zero API calls — the bridge
only acts on what it has been explicitly told to mirror.

The "edit without prior open" case is deliberately not a create: an edit
arrived without us seeing the open, which means we missed the open. The
resulting work item would have a wrong open timestamp and no creation
attribution. We log a warning and surface the gap rather than paper over
it.

## External reference convention

Every work item the bridge creates carries:

| Field             | Value                                     |
|-------------------|-------------------------------------------|
| `external_source` | `"forge:" + repo.FullName` (e.g. `forge:acme/widgets`) |
| `external_id`     | `strconv.FormatInt(issue.ID, 10)`         |

Stable across re-deliveries of the same forge issue — `GetIssueByExternalRef`
on this pair is what makes `HandleForgeIssue` idempotent: a redelivered
open finds the existing work item and falls through to the update branch
instead of creating a duplicate.

## Body rendering

`RenderDescription` produces the `description_html` for every create or
update the engine performs.

```
[optional unmapped-author preface]

<original forge body>

<!-- pfb:src=forge,evt=<delivery-id> -->
```

- The trailing loop-break marker is always present and is the last
  non-whitespace content. Emission is delegated to `idemp.Wrap`, so the
  canonical marker format and idempotency semantics live in one place.
- The unmapped-author preface fires only when the forge sender's username
  has no entry in `users` (identity is config in v1; see
  [design.md](../../docs/design.md#identity-mapping)). When the user *is*
  mapped, the preface is omitted — Plane will attribute via the
  `created_by` field the engine populates from the Users map.

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

The LRU described in [AGENTS.md](../../AGENTS.md#architecture-invariants)
is the server's job — not this package's — because the LRU's key is
`(source_event_id, target_object_id)` and the engine doesn't know the
target object ID until *after* it has run. The server records the
`Outcome.WorkItemID` it gets back.

## Not yet implemented

This package is the issue create/update/close path. Other forge → plane
behaviour, and the reverse direction, live in later steps:

- **Comments** (step 7) — body translation here will be reused.
- **Labels** (step 8) — currently the engine ignores `Issue.Labels`.
- **PR / branch → work-item state automation** (step 9) — pull request
  events are short-circuited as `ActionSkipped` for now.
- **Plane → forge direction** (step 10) — a separate handler that the
  server will dispatch from `/plane/webhook`.

## Dependencies

Standard library only.
