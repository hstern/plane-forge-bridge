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

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// PlaneClient is the subset of the plane.Client REST API this package
// depends on. Declaring the interface here (rather than in internal/plane)
// lets tests substitute a hand-written fake without dragging the HTTP
// transport into every assertion. The concrete plane.Client is expected to
// satisfy this interface; the wiring lives in internal/server.
type PlaneClient interface {
	GetIssueByExternalRef(ctx context.Context, projectID, source, externalID string) (*plane.WorkItem, error)
	CreateIssue(ctx context.Context, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
	UpdateIssue(ctx context.Context, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
	ListProjectStates(ctx context.Context, projectID string) ([]plane.State, error)
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

// Outcome captures what HandleForgeIssue did so the caller (server) can
// record it in the loop-break LRU and log it. WorkItemID is populated on
// Created and Updated. Reason is populated on Skipped (for debug logging).
// Link points to the matched mapping.Link on any Outcome that did link
// resolution; it is nil when the repo had no configured link at all.
type Outcome struct {
	Action     Action
	WorkItemID string
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
	Client PlaneClient
	Links  []mapping.Link
	Users  map[string]string
	Bot    BridgeBot
	Log    *slog.Logger

	stateCache stateCache
}

// NewEngine constructs an Engine from a PlaneClient and the resolved
// configuration. If log is nil, slog.Default() is used.
func NewEngine(c PlaneClient, cfg *mapping.Resolved, log *slog.Logger) *Engine {
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
		Client: c,
		Links:  links,
		Users:  users,
		Bot:    bot,
		Log:    log,
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
func externalRef(repo forge.Repository, issue forge.Issue) (source, id string) {
	return "forge:" + repo.FullName, strconv.FormatInt(issue.ID, 10)
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
		return nil, errors.New("sync: IssueOpened with nil Issue")
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

	mapped, assignee := e.mappedAssignee(evt.Sender.Login)
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
	if assignee != "" {
		req.Assignees = []string{assignee}
	}
	if stateID, err := e.ResolveStateID(ctx, link, "open"); err != nil {
		return nil, fmt.Errorf("sync: resolve open state: %w", err)
	} else if stateID != "" {
		req.StateID = stateID
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
		return nil, errors.New("sync: IssueEdited with nil Issue")
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
		return nil, fmt.Errorf("sync: %s with nil Issue", evt.Kind)
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

	wi, err := e.Client.UpdateIssue(ctx, link.PlaneProjectID, existing.ID, req)
	if err != nil {
		return nil, fmt.Errorf("sync: reconcile update: %w", err)
	}
	return &Outcome{Action: ActionUpdated, WorkItemID: wi.ID, Link: link}, nil
}

// mappedAssignee returns (true, planeMemberID) if the forge username has a
// mapping, otherwise (false, bot.PlaneMemberID). The bool tells callers
// whether to attach the unmapped-author preface to the body.
func (e *Engine) mappedAssignee(forgeUsername string) (mapped bool, planeMemberID string) {
	if id, ok := e.Users[forgeUsername]; ok && id != "" {
		return true, id
	}
	return false, e.Bot.PlaneMemberID
}
