// Package sync translates parsed forge webhook events into Plane REST API
// calls. This is the one-way forge → plane path for issues (step 6 in the
// build order). Comments (step 7), labels (step 8), PR/branch state
// automation (step 9), and the reverse plane → forge direction (step 10)
// live in later steps and other packages.
//
// The Engine is intentionally a pure translator: it knows the per-link
// configuration (state mapping, identity mapping, bridge bot) and the
// PlaneClient interface, but it owns no loop-break state. The caller (the
// HTTP server) is responsible for the LRU lookup before dispatching to the
// engine and for recording the engine's Outcome afterwards. Keeping the LRU
// out of the engine keeps it testable in isolation and matches the
// invariant in AGENTS.md that the bridge is the single writer of its own
// outbound calls.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/idemp"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// ErrMalformedEvent is returned by the Handle* methods when an HMAC-verified
// webhook event is structurally invalid — e.g. an issue.opened event with no
// issue payload, a plane work_item.created with no data object, an
// external_source/external_id pair that can't be parsed.
//
// The server uses this to distinguish bad-input (HTTP 422 — well-formed JSON,
// malformed semantics) from transient upstream errors (HTTP 500 — should
// retry). Without it, every shape error was a 500, and the forge/Plane side
// would retry the same malformed delivery indefinitely.
//
// Programmer bugs (nil event, ForgeClient not configured) are NOT wrapped
// with this sentinel — those are 500-class.
var ErrMalformedEvent = errors.New("sync: malformed event")

// PlaneClient is the subset of the plane.Client REST API this package
// depends on. Declaring the interface here (rather than in internal/plane)
// lets tests substitute a hand-written fake without dragging the HTTP
// transport into every assertion. The concrete plane.Client is expected to
// satisfy this interface; the wiring lives in internal/server.
type PlaneClient interface {
	GetIssue(ctx context.Context, projectID, issueID string) (*plane.WorkItem, error)
	GetIssueByExternalRef(ctx context.Context, projectID, source, externalID string) (*plane.WorkItem, error)
	GetIssueBySequenceID(ctx context.Context, projectIdentifier string, sequenceID int) (*plane.WorkItem, error)
	CreateIssue(ctx context.Context, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
	UpdateIssue(ctx context.Context, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
	ListProjectStates(ctx context.Context, projectID string) ([]plane.State, error)
	ListProjectLabels(ctx context.Context, projectID string) ([]plane.Label, error)
	CreateProjectLabel(ctx context.Context, projectID string, req plane.CreateLabelRequest) (*plane.Label, error)
	CreateComment(ctx context.Context, projectID, issueID string, req plane.CreateCommentRequest) (*plane.Comment, error)
	UpdateComment(ctx context.Context, projectID, issueID, commentID string, req plane.UpdateCommentRequest) (*plane.Comment, error)
	DeleteComment(ctx context.Context, projectID, issueID, commentID string) error
	// ListWorkspaceMembers powers the v2 identity resolver. Returns the
	// workspace-scoped member list (small enough that fetching all is
	// cheaper than per-email queries). The sync-local plane.Member type is
	// defined in identity.go; the production *plane.Client returns its
	// sibling-package equivalent and the wiring layer adapts.
	ListWorkspaceMembers(ctx context.Context) ([]plane.Member, error)
}

// ForgeClient is the subset of the forge.Client REST API the sync engine
// needs for the plane → forge direction. The concrete forge.Client is
// expected to satisfy this interface; tests substitute a hand-written fake.
//
// Issue write methods (CreateIssue, UpdateIssue) live on a separate
// optional interface — ForgeIssueWriter — so the bridge can be wired
// against a forge.Client build that does not yet implement them. The
// HandlePlaneWorkItem handler type-asserts into ForgeIssueWriter at
// dispatch time and skips with a clear reason when the assertion
// fails.
type ForgeClient interface {
	GetIssue(ctx context.Context, owner, repo string, number int64) (*forge.Issue, error)
	ListRepoLabels(ctx context.Context, owner, repo string) ([]forge.Label, error)
	CreateRepoLabel(ctx context.Context, owner, repo string, req forge.CreateLabelRequest) (*forge.Label, error)
	CreateComment(ctx context.Context, owner, repo string, issueNumber int64, req forge.CreateCommentRequest) (*forge.Comment, error)
	UpdateComment(ctx context.Context, owner, repo string, commentID int64, req forge.UpdateCommentRequest) (*forge.Comment, error)
	DeleteComment(ctx context.Context, owner, repo string, commentID int64) error
	// SearchUsers powers the v2 identity resolver's plane→forge path.
	// Upstream is a substring `?q=` search, so callers must filter the
	// response for exact-email matches.
	SearchUsers(ctx context.Context, query string) ([]forge.User, error)
}

// ForgeIssueWriter is the plane → forge issue write surface used by
// HandlePlaneWorkItem. Kept separate from ForgeClient so a forge.Client
// build that does not implement these methods can still satisfy the
// rest of the bridge's wiring; HandlePlaneWorkItem type-asserts at
// dispatch time and surfaces a clear ActionSkipped when the assertion
// fails. The production *forge.Client implements both methods.
type ForgeIssueWriter interface {
	CreateIssue(ctx context.Context, owner, repo string, req forge.CreateIssueRequest) (*forge.Issue, error)
	UpdateIssue(ctx context.Context, owner, repo string, number int64, req forge.UpdateIssueRequest) (*forge.Issue, error)
}

// BridgeBot identifies the configured bridge bot account on both sides.
// When a forge sender has no entry in Users, the bridge attributes the
// resulting Plane work item to the bot's PlaneMemberID and prepends an
// in-body attribution preface so the real author is still visible.
type BridgeBot struct {
	ForgeUsername string
	PlaneMemberID string
}

// Action names what HandleForgeIssue did with an event.
type Action int

// Outcome action values. ActionSkipped is the zero value so a zero Outcome
// is a no-op result that callers can pass through without checking.
const (
	ActionSkipped Action = iota
	ActionCreated
	ActionUpdated
)

// String renders Action for log lines.
func (a Action) String() string {
	switch a {
	case ActionCreated:
		return "created"
	case ActionUpdated:
		return "updated"
	case ActionSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Outcome captures what an engine Handle* call did so the caller (server)
// can record it in the loop-break LRU and log it.
//
// WorkItemID is populated on Created/Updated for issue events; it carries
// the plane work-item ID on the forge→plane path and the plane work-item ID
// the comment is attached to on the plane→forge path.
//
// CommentID is populated on Created/Updated for comment events. It is the
// comment's ID on the OTHER side: the plane comment ID when the forge→plane
// comment write succeeded, the forge comment ID when the plane→forge write
// succeeded. The server uses this to record (sourceEventID, targetObjID)
// in the LRU.
//
// Reason is populated on Skipped (for debug logging). Link points to the
// matched mapping.Link on any Outcome that did link resolution; it is nil
// when the repo had no configured link at all.
type Outcome struct {
	Action     Action
	WorkItemID string
	CommentID  string
	Reason     string
	Link       *mapping.Link
}

// Engine translates forge events into Plane API calls.
//
// Fields are exported so the construction site (typically internal/server)
// can wire dependencies in directly without an opaque option struct. The
// stateCache field is unexported because callers should never touch it; it
// is populated lazily by ResolveStateID.
type Engine struct {
	Client      PlaneClient
	ForgeClient ForgeClient
	Links       []mapping.Link
	Users       map[string]string
	Bot         BridgeBot
	Log         *slog.Logger

	stateCache      stateCache
	labelCache      labelCache
	forgeLabelCache forgeLabelCache
	identityCache   identityCache
}

// NewEngine constructs an Engine from a PlaneClient, a ForgeClient (which
// may be nil if the deployment only mirrors forge→plane), and the resolved
// configuration. If log is nil, slog.Default() is used.
func NewEngine(plane PlaneClient, forgeC ForgeClient, cfg *mapping.Resolved, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	var bot BridgeBot
	var links []mapping.Link
	var users map[string]string
	if cfg != nil {
		bot = BridgeBot{
			ForgeUsername: cfg.BridgeBot.ForgeUsername,
			PlaneMemberID: cfg.BridgeBot.PlaneMemberID,
		}
		links = cfg.Links
		users = cfg.Users
	}
	return &Engine{
		Client:      plane,
		ForgeClient: forgeC,
		Links:       links,
		Users:       users,
		Bot:         bot,
		Log:         log,
	}
}

// linkForRepo returns the configured link for a forge repo full_name, or
// nil if none. The bridge only acts on events for repos it has been
// explicitly configured to mirror; unmatched repos are skipped at the
// engine boundary.
func (e *Engine) linkForRepo(fullName string) *mapping.Link {
	for i := range e.Links {
		if e.Links[i].ForgeRepo == fullName {
			return &e.Links[i]
		}
	}
	return nil
}

// externalRef returns the (source, id) pair the bridge stamps on every
// work item it creates from a forge issue. The pair is stable across
// re-deliveries of the same forge issue, which is what makes
// GetIssueByExternalRef a reliable idempotency key.
//
// The external_id is the forge issue NUMBER (per-repo monotonic), not the
// internal database ID. This lets the plane→forge inbound path resolve the
// forge issue with forge.GetIssue(owner, repo, number) — Forgejo's REST
// API exposes lookup by number but not always by DB id. The change is
// incompatible with step-6 work items, which used issue.ID; nothing is
// running in production yet, so there's no migration shim.
func externalRef(repo forge.Repository, issue forge.Issue) (source, id string) {
	return "forge:" + repo.FullName, strconv.FormatInt(issue.Number, 10)
}

// parseExternalRef extracts (owner, repo, issueNumber) from a plane
// WorkItem's external_source / external_id pair. Returns an error if either
// field is missing or malformed. Used on the plane→forge inbound path to
// figure out which forge issue a plane event refers to.
//
// Acceptable shapes:
//   - source must start with "forge:" and contain exactly one "/" in the
//     remainder, giving owner and repo.
//   - externalID must be a positive int64 parseable by strconv.ParseInt.
func parseExternalRef(source, externalID string) (owner, repo string, number int64, err error) {
	const prefix = "forge:"
	if source == "" {
		return "", "", 0, fmt.Errorf("%w: empty external_source", ErrMalformedEvent)
	}
	if !strings.HasPrefix(source, prefix) {
		return "", "", 0, fmt.Errorf("sync: external_source %q missing %q prefix", source, prefix)
	}
	rest := source[len(prefix):]
	o, r, ok := strings.Cut(rest, "/")
	if !ok || o == "" || r == "" || strings.Contains(r, "/") {
		return "", "", 0, fmt.Errorf("sync: external_source %q not of form forge:owner/repo", source)
	}
	owner, repo = o, r
	if externalID == "" {
		return "", "", 0, fmt.Errorf("%w: empty external_id", ErrMalformedEvent)
	}
	n, perr := strconv.ParseInt(externalID, 10, 64)
	if perr != nil {
		return "", "", 0, fmt.Errorf("sync: external_id %q not numeric: %w", externalID, perr)
	}
	if n <= 0 {
		return "", "", 0, fmt.Errorf("sync: external_id %q not positive", externalID)
	}
	return owner, repo, n, nil
}

// HandleForgeIssue translates a single forge issue event into the
// appropriate Plane API call.
//
// The function is idempotent: calling it twice with the same event yields
// the same Outcome on the same target work item. On the second call the
// existing work item is found via GetIssueByExternalRef and the path falls
// through to UpdateIssue, so no duplicate is created.
//
// Behaviour by evt.Kind:
//
//   - IssueOpened: look up by external ref. If absent, create. If present,
//     reconcile (defensive update of title, description, and state).
//   - IssueEdited: look up. If present, update title and description. If
//     absent, log a warning and skip — we never saw the open event, and
//     creating from an edit would lose history (no original timestamp,
//     incomplete metadata).
//   - IssueClosed: look up. If present, update with state translated through
//     link.StateMap["closed"]. If absent, skip.
//   - IssueReopened: same as Closed but with link.StateMap["open"].
//   - Anything else: skipped with Reason="unsupported event".
func (e *Engine) HandleForgeIssue(ctx context.Context, evt *forge.Event) (*Outcome, error) {
	if evt == nil {
		return nil, errors.New("sync: nil event")
	}

	link := e.linkForRepo(evt.Repo.FullName)
	if link == nil {
		e.Log.Debug("skipping forge event: no link configured",
			"repo", evt.Repo.FullName, "kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{Action: ActionSkipped, Reason: "no link configured for repo"}, nil
	}

	switch evt.Kind {
	case forge.EventIssueOpened:
		return e.handleOpened(ctx, evt, link)
	case forge.EventIssueEdited:
		return e.handleEdited(ctx, evt, link)
	case forge.EventIssueClosed:
		return e.handleStateChange(ctx, evt, link, "closed")
	case forge.EventIssueReopened:
		return e.handleStateChange(ctx, evt, link, "open")
	default:
		return &Outcome{Action: ActionSkipped, Reason: "unsupported event", Link: link}, nil
	}
}

// handleOpened implements the IssueOpened branch. It looks up the work item
// by external ref; if found it reconciles (an open of an already-present
// item is a redelivery, so we update defensively rather than creating a
// duplicate). If absent, it creates.
func (e *Engine) handleOpened(ctx context.Context, evt *forge.Event, link *mapping.Link) (*Outcome, error) {
	if evt.Issue == nil {
		return nil, fmt.Errorf("%w: issue.opened payload has no issue", ErrMalformedEvent)
	}
	source, id := externalRef(evt.Repo, *evt.Issue)

	existing, err := e.Client.GetIssueByExternalRef(ctx, link.PlaneProjectID, source, id)
	switch {
	case err == nil && existing != nil:
		// Already exists — reconcile fields. This branch fires on redelivery
		// of the open event, which the forge will do retry-on-failure.
		return e.reconcile(ctx, evt, link, existing, "open")
	case errors.Is(err, plane.ErrNotFound):
		// Fall through to create.
	default:
		return nil, fmt.Errorf("sync: lookup before create: %w", err)
	}

	mapped, _ := e.mappedAssignee(evt.Sender.Login)
	desc := RenderDescription(
		evt.Issue.Body,
		evt.Sender.Login, evt.Sender.HTMLURL, evt.Repo.FullName,
		evt.DeliveryID, mapped,
	)
	req := plane.CreateIssueRequest{
		Name:            evt.Issue.Title,
		DescriptionHTML: desc,
		ExternalSource:  source,
		ExternalID:      id,
	}
	// v2 identity resolution: static config → email match → "". When the
	// resolver returns "" the request omits Assignees, which lets Plane
	// leave the work item unassigned (matching the v1 behaviour for
	// unmapped users — the bridge bot is still the API caller, but it
	// doesn't impose itself as the assignee).
	assignee, _ := e.resolvePlaneMember(ctx, evt.Sender.Login, evt.Sender.Email)
	if assignee != "" {
		req.Assignees = []string{assignee}
	}
	if stateID, err := e.ResolveStateID(ctx, link, "open"); err != nil {
		return nil, fmt.Errorf("sync: resolve open state: %w", err)
	} else if stateID != "" {
		req.StateID = stateID
	}
	labels, err := e.resolveLabels(ctx, link.PlaneProjectID, forgeLabelNames(evt.Issue.Labels))
	if err != nil {
		return nil, fmt.Errorf("sync: resolve labels: %w", err)
	}
	if len(labels) > 0 {
		req.Labels = labels
	}

	wi, err := e.Client.CreateIssue(ctx, link.PlaneProjectID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: create issue: %w", err)
	}
	e.Log.Info("created plane work item from forge issue",
		"repo", evt.Repo.FullName, "issue", evt.Issue.Number,
		"work_item", wi.ID, "delivery", evt.DeliveryID)
	return &Outcome{Action: ActionCreated, WorkItemID: wi.ID, Link: link}, nil
}

// handleEdited implements the IssueEdited branch. We intentionally do NOT
// create from an edit: an edit event arriving without a prior open means
// the open was missed (HMAC failure, downtime, …) and the resulting work
// item would have an incorrect open timestamp and no creation attribution.
// Better to surface the gap in logs than paper over it.
func (e *Engine) handleEdited(ctx context.Context, evt *forge.Event, link *mapping.Link) (*Outcome, error) {
	if evt.Issue == nil {
		return nil, fmt.Errorf("%w: issue.edited payload has no issue", ErrMalformedEvent)
	}
	source, id := externalRef(evt.Repo, *evt.Issue)

	existing, err := e.Client.GetIssueByExternalRef(ctx, link.PlaneProjectID, source, id)
	if errors.Is(err, plane.ErrNotFound) {
		e.Log.Warn("forge issue edit with no prior open in Plane; dropping",
			"repo", evt.Repo.FullName, "issue", evt.Issue.Number,
			"delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: "issue edit with no prior open in Plane",
			Link:   link,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: lookup before edit: %w", err)
	}

	mapped, _ := e.mappedAssignee(evt.Sender.Login)
	desc := RenderDescription(
		evt.Issue.Body,
		evt.Sender.Login, evt.Sender.HTMLURL, evt.Repo.FullName,
		evt.DeliveryID, mapped,
	)
	name := evt.Issue.Title
	req := plane.UpdateIssueRequest{
		Name:            &name,
		DescriptionHTML: &desc,
	}
	// We deliberately do NOT set StateID on edits. forge fires
	// `issues.edited` for any property change (title, body, assignee,
	// labels), and the event payload doesn't reliably tell us whether the
	// state changed. The explicit state transitions arrive as IssueClosed /
	// IssueReopened where we DO translate via link.StateMap. Touching state
	// on edit risks moving Plane backwards when a user edits the title of a
	// closed forge issue.
	labels, err := e.resolveLabels(ctx, link.PlaneProjectID, forgeLabelNames(evt.Issue.Labels))
	if err != nil {
		return nil, fmt.Errorf("sync: resolve labels: %w", err)
	}
	// Only assign when we actually resolved a non-empty set. The plane
	// CreateIssueRequest.Labels tag is omitempty, so a nil/empty slice is
	// indistinguishable on the wire and leaves Plane's labels alone. An
	// operator removing every forge label is therefore NOT mirrored to
	// Plane today — a deliberate v1 limitation, since the alternative
	// would require a separate "clear labels" PATCH that Plane's API
	// shape doesn't cleanly support via this omitempty field.
	if len(labels) > 0 {
		req.Labels = labels
	}
	wi, err := e.Client.UpdateIssue(ctx, link.PlaneProjectID, existing.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: update issue: %w", err)
	}
	return &Outcome{Action: ActionUpdated, WorkItemID: wi.ID, Link: link}, nil
}

// handleStateChange implements the IssueClosed and IssueReopened branches.
// forgeState is "open" or "closed". If the link's StateMap has no mapping
// for that state, the bridge updates the work item without StateID — i.e.
// it leaves Plane's workflow state alone — rather than inventing a
// transition (matches the design doc's "we do not invent transitions"
// guarantee).
func (e *Engine) handleStateChange(ctx context.Context, evt *forge.Event, link *mapping.Link, forgeState string) (*Outcome, error) {
	if evt.Issue == nil {
		return nil, fmt.Errorf("%w: %s payload has no issue", ErrMalformedEvent, evt.Kind)
	}
	source, id := externalRef(evt.Repo, *evt.Issue)

	existing, err := e.Client.GetIssueByExternalRef(ctx, link.PlaneProjectID, source, id)
	if errors.Is(err, plane.ErrNotFound) {
		e.Log.Warn("forge state change with no Plane work item; dropping",
			"repo", evt.Repo.FullName, "issue", evt.Issue.Number,
			"forge_state", forgeState, "delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: "state change with no Plane work item",
			Link:   link,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: lookup before state change: %w", err)
	}

	stateID, err := e.ResolveStateID(ctx, link, forgeState)
	if err != nil {
		return nil, fmt.Errorf("sync: resolve %s state: %w", forgeState, err)
	}

	req := plane.UpdateIssueRequest{}
	if stateID != "" {
		req.StateID = &stateID
	}

	wi, err := e.Client.UpdateIssue(ctx, link.PlaneProjectID, existing.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: update issue state: %w", err)
	}
	return &Outcome{Action: ActionUpdated, WorkItemID: wi.ID, Link: link}, nil
}

// reconcile updates an existing work item when an open event is redelivered.
// Title, description, and the open state are all reconciled. This is the
// safe redelivery path: the create branch would fail with a uniqueness
// error if Plane enforces external-ref uniqueness, and we don't want to
// rely on that.
func (e *Engine) reconcile(ctx context.Context, evt *forge.Event, link *mapping.Link, existing *plane.WorkItem, forgeState string) (*Outcome, error) {
	mapped, _ := e.mappedAssignee(evt.Sender.Login)
	desc := RenderDescription(
		evt.Issue.Body,
		evt.Sender.Login, evt.Sender.HTMLURL, evt.Repo.FullName,
		evt.DeliveryID, mapped,
	)
	name := evt.Issue.Title
	req := plane.UpdateIssueRequest{
		Name:            &name,
		DescriptionHTML: &desc,
	}
	if stateID, err := e.ResolveStateID(ctx, link, forgeState); err != nil {
		return nil, fmt.Errorf("sync: resolve %s state: %w", forgeState, err)
	} else if stateID != "" {
		req.StateID = &stateID
	}
	labels, err := e.resolveLabels(ctx, link.PlaneProjectID, forgeLabelNames(evt.Issue.Labels))
	if err != nil {
		return nil, fmt.Errorf("sync: resolve labels: %w", err)
	}
	if len(labels) > 0 {
		req.Labels = labels
	}

	wi, err := e.Client.UpdateIssue(ctx, link.PlaneProjectID, existing.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: reconcile update: %w", err)
	}
	return &Outcome{Action: ActionUpdated, WorkItemID: wi.ID, Link: link}, nil
}

// mappedAssignee is the v1 static-map-only identity check. It is kept as
// the source of truth for the unmapped-author PREFACE: the preface fires
// when the forge sender has no entry in the operator's static config,
// regardless of whether the v2 email resolver later finds a Plane member
// for them. This keeps the body attribution honest — the operator
// configured the static map deliberately, the email match is a best-
// effort fallback. Callers that want an assignee Plane member UUID
// should use resolvePlaneMember (v1 + v2 + bot fallback) instead.
func (e *Engine) mappedAssignee(forgeUsername string) (mapped bool, planeMemberID string) {
	if id, ok := e.Users[forgeUsername]; ok && id != "" {
		return true, id
	}
	return false, e.Bot.PlaneMemberID
}

// reasonCommentIdentityDeferredForge is the Outcome.Reason returned for
// forge → plane issue_comment.edited and issue_comment.deleted events.
// We skip in step 7 because there is no persistent mapping from forge
// comment IDs to plane comment IDs (Plane comments don't carry an
// external_id field), so we can't address the right plane comment for an
// update or delete. See README "Open questions".
const reasonCommentIdentityDeferredForge = "forge comment update/delete needs identity mapping (deferred to a later step)"

// reasonCommentIdentityDeferredPlane is the symmetric Reason on the
// plane → forge path. Same root cause.
const reasonCommentIdentityDeferredPlane = "plane comment update/delete needs identity mapping (deferred to a later step)"

// HandleForgeComment translates a forge issue_comment.* event into the
// matching Plane comment API call.
//
// Flow:
//
//  1. linkForRepo(evt.Repo.FullName); skip if no link configured.
//  2. external_ref lookup: find the plane WorkItem mirroring this forge
//     issue. Required — comments are scoped to an issue, so we need the
//     plane issue ID to call CreateComment. Comments may fire before the
//     issue mirror catches up; we skip with a clear Reason in that case.
//  3. By kind:
//     - EventIssueCommentCreated: marker-wrap the body, CreateComment.
//     - EventIssueCommentEdited / EventIssueCommentDeleted: skipped in
//     step 7 with Reason=reasonCommentIdentityDeferredForge. The
//     identity mapping from forge comment id → plane comment id is a
//     follow-up; we don't have persistent storage yet.
//
// Other kinds: ActionSkipped, Reason="unsupported event".
func (e *Engine) HandleForgeComment(ctx context.Context, evt *forge.Event) (*Outcome, error) {
	if evt == nil {
		return nil, errors.New("sync: nil event")
	}

	link := e.linkForRepo(evt.Repo.FullName)
	if link == nil {
		e.Log.Debug("skipping forge comment: no link configured",
			"repo", evt.Repo.FullName, "kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{Action: ActionSkipped, Reason: "no link configured for repo"}, nil
	}

	switch evt.Kind {
	case forge.EventIssueCommentCreated:
		return e.handleForgeCommentCreated(ctx, evt, link)
	case forge.EventIssueCommentEdited, forge.EventIssueCommentDeleted:
		e.Log.Info("forge comment update/delete deferred",
			"repo", evt.Repo.FullName, "kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonCommentIdentityDeferredForge,
			Link:   link,
		}, nil
	default:
		return &Outcome{Action: ActionSkipped, Reason: "unsupported event", Link: link}, nil
	}
}

// handleForgeCommentCreated implements the EventIssueCommentCreated branch
// of HandleForgeComment. It resolves the plane work-item that mirrors the
// forge issue via the external ref, then posts the marker-wrapped comment.
func (e *Engine) handleForgeCommentCreated(ctx context.Context, evt *forge.Event, link *mapping.Link) (*Outcome, error) {
	if evt.Issue == nil {
		return nil, fmt.Errorf("%w: issue_comment.created payload has no issue", ErrMalformedEvent)
	}
	if evt.Comment == nil {
		return nil, fmt.Errorf("%w: issue_comment.created payload has no comment", ErrMalformedEvent)
	}
	source, id := externalRef(evt.Repo, *evt.Issue)

	existing, err := e.Client.GetIssueByExternalRef(ctx, link.PlaneProjectID, source, id)
	if errors.Is(err, plane.ErrNotFound) {
		e.Log.Warn("forge comment for un-mirrored issue; dropping",
			"repo", evt.Repo.FullName, "issue", evt.Issue.Number,
			"delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: "comment fired before plane issue was mirrored",
			Link:   link,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: lookup before comment create: %w", err)
	}

	mapped, _ := e.mappedAssignee(evt.Sender.Login)
	body := RenderComment(
		evt.Comment.Body,
		evt.Sender.Login, evt.Sender.HTMLURL, evt.Repo.FullName,
		evt.DeliveryID, idemp.SourceForge, mapped,
	)
	req := plane.CreateCommentRequest{CommentHTML: body}

	c, err := e.Client.CreateComment(ctx, link.PlaneProjectID, existing.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: create plane comment: %w", err)
	}
	e.Log.Info("created plane comment from forge comment",
		"repo", evt.Repo.FullName, "issue", evt.Issue.Number,
		"work_item", existing.ID, "comment", c.ID, "delivery", evt.DeliveryID)
	return &Outcome{
		Action:     ActionCreated,
		WorkItemID: existing.ID,
		CommentID:  c.ID,
		Link:       link,
	}, nil
}

// HandlePlaneComment translates a plane comment.* event into the matching
// forge comment API call.
//
// Flow:
//
//  1. plane.GetIssue(evt.Comment.Project, evt.Comment.IssueID) to read
//     external_source / external_id.
//  2. parseExternalRef → (owner, repo, number). Malformed refs cause
//     ActionSkipped with a clear Reason — these are operator-config or
//     non-mirrored issues, not crashes.
//  3. By kind:
//     - EventCommentCreated: marker-wrap, forge.CreateComment.
//     - EventCommentUpdated / EventCommentDeleted: skipped in step 7 with
//     Reason=reasonCommentIdentityDeferredPlane. Same identity-mapping
//     reason as the forge → plane direction.
//
// Other kinds: ActionSkipped, Reason="unsupported event".
func (e *Engine) HandlePlaneComment(ctx context.Context, evt *plane.Event) (*Outcome, error) {
	if evt == nil {
		return nil, errors.New("sync: nil event")
	}

	switch evt.Kind {
	case plane.EventCommentCreated:
		return e.handlePlaneCommentCreated(ctx, evt)
	case plane.EventCommentUpdated, plane.EventCommentDeleted:
		e.Log.Info("plane comment update/delete deferred",
			"kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonCommentIdentityDeferredPlane,
		}, nil
	default:
		return &Outcome{Action: ActionSkipped, Reason: "unsupported event"}, nil
	}
}

// handlePlaneCommentCreated implements the EventCommentCreated branch of
// HandlePlaneComment. It resolves the forge issue the plane work item
// mirrors via its external_source/external_id pair, then posts the
// marker-wrapped comment.
func (e *Engine) handlePlaneCommentCreated(ctx context.Context, evt *plane.Event) (*Outcome, error) {
	if evt.Comment == nil {
		return nil, fmt.Errorf("%w: comment.created payload has no comment", ErrMalformedEvent)
	}
	if e.ForgeClient == nil {
		return nil, errors.New("sync: ForgeClient not configured")
	}

	// Read the parent work-item to discover which forge issue it mirrors.
	wi, err := e.Client.GetIssue(ctx, evt.Comment.Project, evt.Comment.IssueID)
	if err != nil {
		return nil, fmt.Errorf("sync: lookup plane issue for comment: %w", err)
	}
	source, externalID := wi.ExternalSource, wi.ExternalID

	owner, repo, number, err := parseExternalRef(source, externalID)
	if err != nil {
		e.Log.Warn("plane comment on non-mirrored or malformed work item; dropping",
			"work_item", wi.ID, "external_source", source, "external_id", externalID,
			"delivery", evt.DeliveryID, "err", err)
		return &Outcome{
			Action:     ActionSkipped,
			Reason:     "plane comment on work item with no forge mirror: " + err.Error(),
			WorkItemID: wi.ID,
			Link:       e.linkForRepo(repo),
		}, nil
	}

	link := e.linkForRepo(owner + "/" + repo)

	body := RenderComment(
		evt.Comment.CommentHTML,
		evt.Actor.DisplayName, "", owner+"/"+repo,
		evt.DeliveryID, idemp.SourcePlane,
		// Plane→forge does not need the unmapped-author preface for v1: the
		// bridge bot writes on the forge side regardless, and the actor's
		// display name is taken from the plane event. Mark as mapped to
		// suppress the preface; the marker is what matters for loop-break.
		true,
	)
	req := forge.CreateCommentRequest{Body: body}

	c, err := e.ForgeClient.CreateComment(ctx, owner, repo, number, req)
	if err != nil {
		return nil, fmt.Errorf("sync: create forge comment: %w", err)
	}
	e.Log.Info("created forge comment from plane comment",
		"owner", owner, "repo", repo, "issue", number,
		"comment", c.ID, "delivery", evt.DeliveryID)
	return &Outcome{
		Action:     ActionCreated,
		WorkItemID: wi.ID,
		CommentID:  strconv.FormatInt(c.ID, 10),
		Link:       link,
	}, nil
}

// reasonPRReviewDeferred is the Outcome.Reason for pull_request_review
// events. Step 9 v1 ships only the open/merged/closed transitions; review
// state → work-item state will land in a later step when the requirements
// firm up (does "approved" advance to "In Review"? "In QA"? operator
// preference, no canonical mapping).
const reasonPRReviewDeferred = "review event handling deferred to a later step"

// HandleForgePullRequest translates forge pull_request.* events into a
// state update on the linked Plane work item.
//
// Flow:
//
//  1. linkForRepo(evt.Repo.FullName); skip if no link configured.
//  2. Skip if PR automation is not opted into on this link
//     (link.ProjectIdentifier or link.PRStateMap empty).
//  3. parsePRRef on title + body + head branch ref; skip if no ref found.
//  4. plane.GetIssueBySequenceID; skip on ErrNotFound (operator typoed the
//     ref or the work item was deleted — not a bridge failure).
//  5. Map evt.Kind + evt.PullRequest.Merged → PRStateMap key:
//     opened/reopened → "opened", closed+merged → "merged",
//     closed+unmerged → "closed", anything else (edited, review) → skip.
//  6. ResolveStateID on the mapped state name. Lookup miss surfaces as an
//     error — that's an operator config bug we shouldn't paper over.
//  7. UpdateIssue with only StateID set. The PATCH is unconditional even
//     when the state already matches; the optimisation (skip on no-op)
//     can land later when we have a benchmark to justify it.
//
// Outcome.WorkItemID is the resolved plane work item ID on ActionUpdated.
// On every skip path, Outcome.Reason names the specific reason so
// operators can tell "config gap" apart from "no match in this PR" in
// the logs.
func (e *Engine) HandleForgePullRequest(ctx context.Context, evt *forge.Event) (*Outcome, error) {
	if evt == nil {
		return nil, errors.New("sync: nil event")
	}

	link := e.linkForRepo(evt.Repo.FullName)
	if link == nil {
		e.Log.Debug("skipping forge PR event: no link configured",
			"repo", evt.Repo.FullName, "kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{Action: ActionSkipped, Reason: "no link configured for repo"}, nil
	}

	// PR automation is opt-in per link. A link with no ProjectIdentifier
	// or an empty PRStateMap means the operator has not asked the bridge
	// to move work-item state on PR events for this repo — issue/comment
	// translation still works, but PRs are a no-op.
	if link.ProjectIdentifier == "" || len(link.PRStateMap) == 0 {
		return &Outcome{
			Action: ActionSkipped,
			Reason: "no PR automation configured for this link",
			Link:   link,
		}, nil
	}

	// pull_request_review is the one branch that isn't a state-affecting
	// PR action under step 9 — review events are deferred. We check it
	// before parsing the ref so the skip reason is "review deferred"
	// rather than "no ref found" when the review payload happens to lack
	// a ref. Same for plain edited.
	if evt.Kind == forge.EventPullRequestReview {
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonPRReviewDeferred,
			Link:   link,
		}, nil
	}

	if evt.PullRequest == nil {
		return nil, fmt.Errorf("%w: %s payload has no pull_request", ErrMalformedEvent, evt.Kind)
	}

	seq, ok := parsePRRef(
		link.ProjectIdentifier,
		evt.PullRequest.Title,
		evt.PullRequest.Body,
		evt.PullRequest.Head.Ref,
	)
	if !ok {
		return &Outcome{
			Action: ActionSkipped,
			Reason: fmt.Sprintf("no [%s-N] ref found in PR title/body or branch name", link.ProjectIdentifier),
			Link:   link,
		}, nil
	}

	actionKey, ok := prActionKey(evt.Kind, evt.PullRequest.Merged)
	if !ok {
		// EventPullRequestEdited and anything else not in the
		// open/reopen/close set: edits shouldn't move state (the title or
		// body changed, not the PR's lifecycle). Skip with a reason
		// pointing at the action so the log line is useful.
		return &Outcome{
			Action: ActionSkipped,
			Reason: fmt.Sprintf("PR action %q does not map to a state transition", evt.Kind),
			Link:   link,
		}, nil
	}

	stateName, ok := link.PRStateMap[actionKey]
	if !ok || stateName == "" {
		return &Outcome{
			Action: ActionSkipped,
			Reason: fmt.Sprintf("no state transition configured for action %q on this link", actionKey),
			Link:   link,
		}, nil
	}

	// The plane endpoint takes the project's short identifier code (e.g.
	// "PFB"), not the project UUID. See plane.Client.GetIssueBySequenceID.
	wi, err := e.Client.GetIssueBySequenceID(ctx, link.ProjectIdentifier, seq)
	if errors.Is(err, plane.ErrNotFound) {
		return &Outcome{
			Action: ActionSkipped,
			Reason: fmt.Sprintf("[%s-%d] does not exist on the configured Plane project", link.ProjectIdentifier, seq),
			Link:   link,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: lookup work item by sequence id: %w", err)
	}

	// ResolveStateID surfaces the lookup miss as an error — the operator
	// has named a Plane state in pr_state_map that doesn't exist on the
	// project. We don't want to silently no-op.
	stateID, err := e.resolvePRStateName(ctx, link, stateName)
	if err != nil {
		return nil, fmt.Errorf("sync: resolve PR state %q: %w", stateName, err)
	}

	req := plane.UpdateIssueRequest{StateID: &stateID}
	updated, err := e.Client.UpdateIssue(ctx, link.PlaneProjectID, wi.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: update PR-driven state: %w", err)
	}
	e.Log.Info("updated plane work item state from forge PR",
		"repo", evt.Repo.FullName, "pr", evt.PullRequest.Number,
		"work_item", updated.ID, "sequence", seq,
		"action", actionKey, "state", stateName, "delivery", evt.DeliveryID)
	return &Outcome{
		Action:     ActionUpdated,
		WorkItemID: updated.ID,
		Link:       link,
	}, nil
}

// prActionKey maps a (Kind, Merged) pair to the PRStateMap key. Returns
// ok=false for actions that should NOT move state (edited, anything
// unknown). pull_request_review is filtered out by the caller before this
// runs.
func prActionKey(kind forge.EventKind, merged bool) (string, bool) {
	switch kind {
	case forge.EventPullRequestOpened:
		return "opened", true
	case forge.EventPullRequestReopened:
		return "opened", true
	case forge.EventPullRequestClosed:
		if merged {
			return "merged", true
		}
		return "closed", true
	default:
		return "", false
	}
}

// resolvePRStateName resolves a PR-state name (the value side of
// link.PRStateMap) to a Plane state UUID through the shared state cache.
// Unlike ResolveStateID this does NOT short-circuit on missing names —
// the caller has already confirmed PRStateMap has an entry, so a state
// that isn't on the project is a hard config bug we surface.
func (e *Engine) resolvePRStateName(ctx context.Context, link *mapping.Link, name string) (string, error) {
	states, err := e.listStates(ctx, link.PlaneProjectID)
	if err != nil {
		return "", fmt.Errorf("list states for project %s: %w", link.PlaneProjectID, err)
	}
	for _, s := range states {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("state %q not found in project %s", name, link.PlaneProjectID)
}
