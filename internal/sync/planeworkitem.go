package sync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/idemp"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// Step-10 deferral reasons — exported as constants so server-side log
// assertions and operator-facing dashboards can match them exactly.
const (
	// reasonPlaneUpdateDeferred is returned for plane work_item.updated
	// events. The reverse-lookup needed to find the mirrored forge issue
	// (forge.Client.SearchIssues over the PlaneRefMarker) is not yet
	// implemented — when it lands, this skip is replaced by an
	// UpdateIssue PATCH that mirrors title/body/labels/state.
	reasonPlaneUpdateDeferred = "update for an unmirrored work item — full reverse lookup pending forge.Client.SearchIssues"

	// reasonPlaneDeleteNotMirrored is returned for plane work_item.deleted
	// events. Forge does not expose a delete-issue endpoint; the eventual
	// mirror is a close ("close instead of delete"), which is also not in
	// v1 because it requires the SearchIssues reverse lookup.
	reasonPlaneDeleteNotMirrored = "plane delete events are not mirrored to forge (issues are closed, not deleted)"

	// reasonPlaneIssueWriterMissing is returned when the wired ForgeClient
	// does not implement ForgeIssueWriter. This happens during the
	// staggered rollout of step 10 — the engine handler is in place, but
	// the underlying forge.Client build in production has not yet been
	// rebuilt with the CreateIssue/UpdateIssue methods. The skip is
	// loud so operators can correlate the deferral with the rollout
	// state.
	reasonPlaneIssueWriterMissing = "plane → forge issue writes deferred — forge client build does not implement ForgeIssueWriter"
)

// HandlePlaneWorkItem translates a plane work_item.* event into a forge
// issue create or update. This is the inverse of HandleForgeIssue and
// closes the bidirectional loop for issues (step 10 of the build order).
//
// Flow:
//
//  1. linkForPlaneProject by matching evt.WorkItem.Project against
//     link.PlaneProjectID. Multiple links targeting the same plane
//     project from different forge repos is an unusual configuration;
//     v1 takes the first match and ignores the rest. Documented in the
//     README's "Open questions".
//
//  2. By evt.Kind:
//
//     - EventWorkItemCreated: CreateIssue on the linked forge repo.
//     Body is RenderPlaneWorkItemDescription, which is the symmetric
//     inverse of RenderDescription — original plane body, optional
//     unmapped-author preface for plane actors who have no matching
//     forge identity, the loop-break marker, AND the durable
//     PlaneRefMarker so future updates can find the mirror.
//
//     - EventWorkItemUpdated: ActionSkipped with
//     reasonPlaneUpdateDeferred. The reverse lookup needed to find
//     the mirrored forge issue (a body-text search for the
//     PlaneRefMarker) requires a forge.Client method that does not
//     yet exist. The skip is loud — operators see the deferral
//     reason in logs and can correlate.
//
//     - EventWorkItemDeleted: ActionSkipped with
//     reasonPlaneDeleteNotMirrored. Forge does not expose a delete
//     endpoint on issues; the eventual behaviour is "close instead
//     of delete", deferred for the same SearchIssues reason.
//
//     - anything else: ActionSkipped, Reason="unsupported event".
//
// Outcome.WorkItemID on a successful create carries the FORGE issue
// NUMBER (per-repo monotonic, stringified). Matches the convention used
// on the forge → plane path where the outcome carries the plane work
// item's stable ID. Outcome.CommentID is empty.
func (e *Engine) HandlePlaneWorkItem(ctx context.Context, evt *plane.Event) (*Outcome, error) {
	if evt == nil {
		return nil, errors.New("sync: nil event")
	}
	if e.ForgeClient == nil {
		return nil, errors.New("sync: ForgeClient not configured")
	}
	if evt.WorkItem == nil {
		// Plane delete events can omit the data envelope, in which case the
		// parser produces a nil WorkItem. We can't act without project +
		// id; skip with a clear reason.
		if evt.Kind == plane.EventWorkItemDeleted {
			return &Outcome{
				Action: ActionSkipped,
				Reason: reasonPlaneDeleteNotMirrored,
			}, nil
		}
		return nil, fmt.Errorf("%w: %s payload has no work_item", ErrMalformedEvent, evt.Kind)
	}

	link := e.linkForPlaneProject(evt.WorkItem.Project)
	if link == nil {
		e.Log.Debug("skipping plane work_item event: no link configured",
			"plane_project", evt.WorkItem.Project, "kind", evt.Kind, "delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: "no link configured for plane project",
		}, nil
	}

	switch evt.Kind {
	case plane.EventWorkItemCreated:
		return e.handlePlaneWorkItemCreated(ctx, evt, link)
	case plane.EventWorkItemUpdated:
		// See reasonPlaneUpdateDeferred. We log at info — this is a known
		// gap operators should be able to see in normal logs.
		e.Log.Info("plane work_item.updated deferred",
			"plane_project", evt.WorkItem.Project,
			"work_item", evt.WorkItem.ID, "delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonPlaneUpdateDeferred,
			Link:   link,
		}, nil
	case plane.EventWorkItemDeleted:
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonPlaneDeleteNotMirrored,
			Link:   link,
		}, nil
	default:
		return &Outcome{
			Action: ActionSkipped,
			Reason: "unsupported event",
			Link:   link,
		}, nil
	}
}

// handlePlaneWorkItemCreated implements the EventWorkItemCreated branch.
// CreateIssue is unconditional in v1 (no reverse lookup); the caller's
// loop-break LRU is the only defence against a re-delivered create
// producing a duplicate forge issue. The LRU is the server's job and
// fires before we get here, so v1 ships without the redundant lookup.
func (e *Engine) handlePlaneWorkItemCreated(ctx context.Context, evt *plane.Event, link *mapping.Link) (*Outcome, error) {
	writer, ok := e.ForgeClient.(ForgeIssueWriter)
	if !ok {
		e.Log.Warn("plane work_item.created received but ForgeClient cannot write issues",
			"plane_project", evt.WorkItem.Project, "work_item", evt.WorkItem.ID,
			"delivery", evt.DeliveryID)
		return &Outcome{
			Action: ActionSkipped,
			Reason: reasonPlaneIssueWriterMissing,
			Link:   link,
		}, nil
	}

	owner, repo, ok := splitForgeRepo(link.ForgeRepo)
	if !ok {
		return nil, fmt.Errorf("sync: link forge_repo %q is not of form owner/repo", link.ForgeRepo)
	}

	body := RenderPlaneWorkItemDescription(
		evt.WorkItem.Description,
		evt.Actor.DisplayName,
		evt.DeliveryID,
		PlaneRefMarker{
			WorkspaceID: evt.WorkspaceID,
			ProjectID:   evt.WorkItem.Project,
			WorkItemID:  evt.WorkItem.ID,
		},
	)

	// Plane carries label UUIDs on the work item; the forge wants integer
	// label IDs. Two cache hops: plane UUID → name via the plane label
	// cache, then name → forge integer ID (with auto-create) via the
	// forge label cache.
	//
	// As of PFB-24 the webhook payload includes the label name inline
	// (LabelRef has Name + Color alongside ID), so the UUID→name lookup
	// could be skipped here. We keep it for now so the resolver remains
	// the single source of truth for label-name resolution (in case
	// upstream ever drops .Name from a future event type).
	labelUUIDs := make([]string, len(evt.WorkItem.Labels))
	for i, l := range evt.WorkItem.Labels {
		labelUUIDs[i] = l.ID
	}
	names, err := e.resolvePlaneLabelNames(ctx, link.PlaneProjectID, labelUUIDs)
	if err != nil {
		return nil, fmt.Errorf("sync: resolve plane label names: %w", err)
	}
	forgeLabelIDs, err := e.resolveForgeLabels(ctx, owner, repo, names)
	if err != nil {
		return nil, fmt.Errorf("sync: resolve forge labels: %w", err)
	}

	req := forge.CreateIssueRequest{
		Title: evt.WorkItem.Name,
		Body:  body,
	}
	if len(forgeLabelIDs) > 0 {
		req.Labels = forgeLabelIDs
	}
	// v2 identity resolution (plane→forge direction). Prefer the event
	// Actor's member UUID (who took the action); fall back to the work
	// item's CreatedBy when the actor is absent (some plane payloads
	// omit it on system-generated events). An empty result leaves the
	// forge issue unassigned — the bridge bot is still the API caller
	// but doesn't impose itself as the assignee.
	planeMember := evt.Actor.ID
	if planeMember == "" {
		planeMember = evt.WorkItem.CreatedBy
	}
	if planeMember != "" {
		if login, _ := e.resolveForgeUser(ctx, planeMember); login != "" {
			req.Assignees = []string{login}
		}
	}

	issue, err := writer.CreateIssue(ctx, owner, repo, req)
	if err != nil {
		return nil, fmt.Errorf("sync: create forge issue: %w", err)
	}
	e.Log.Info("created forge issue from plane work item",
		"owner", owner, "repo", repo, "issue", issue.Number,
		"plane_project", evt.WorkItem.Project, "work_item", evt.WorkItem.ID,
		"delivery", evt.DeliveryID)
	return &Outcome{
		Action:     ActionCreated,
		WorkItemID: strconv.FormatInt(issue.Number, 10),
		Link:       link,
	}, nil
}

// linkForPlaneProject returns the first configured link whose
// PlaneProjectID matches projectID, or nil if none. v1 accepts only the
// first match: operators with multiple forge repos targeting a single
// plane project see only one mirror, by design — the alternative would
// be N forge issues per plane work item, which the LRU can't dedup
// because each side has a different delivery ID.
func (e *Engine) linkForPlaneProject(projectID string) *mapping.Link {
	for i := range e.Links {
		if e.Links[i].PlaneProjectID == projectID {
			return &e.Links[i]
		}
	}
	return nil
}

// splitForgeRepo parses an "owner/repo" string into its parts. Returns
// ok=false when the input has zero or more than one "/", which the
// config loader already rejects but we guard against here so a bad
// runtime config surfaces as a clear engine error instead of a
// confusing CreateIssue URL.
func splitForgeRepo(fullName string) (owner, repo string, ok bool) {
	o, r, found := strings.Cut(fullName, "/")
	if !found || o == "" || r == "" || strings.Contains(r, "/") {
		return "", "", false
	}
	return o, r, true
}

// RenderPlaneWorkItemDescription builds the forge issue body for a
// create or update mirrored from a plane work item. Format mirrors
// RenderDescription with TWO trailing markers:
//
//	[optional unmapped-author preface]
//
//	<original plane description>
//
//	<!-- pfb:plane-workitem=<ws>/<proj>/<wi> -->
//
//	<!-- pfb:src=plane,evt=<delivery-id> -->
//
// The PlaneRefMarker comes BEFORE the loop-break marker so the
// loop-break marker remains the trailing token — idemp.Extract anchors
// on the end of the body, and any reordering would silently break the
// inbound dedup. Both markers are required: the loop-break marker is
// idempotency per delivery, the PlaneRefMarker is reverse lookup per
// work item (durable across redeliveries).
//
// The plane → forge direction does not (in v1) try to attach an
// unmapped-author preface — the inverse identity map (plane member UUID
// → forge username) is not part of the v1 config schema. The mapped
// flag is therefore always treated as true for plane → forge; the
// originating actor's display name is logged but not stamped on the
// body. This matches the conservative HandlePlaneComment posture.
func RenderPlaneWorkItemDescription(planeBody, _ /* actor */ string, deliveryID string, ref PlaneRefMarker) string {
	var b strings.Builder
	b.WriteString(planeBody)
	// Make sure the plane-ref marker sits on its own visually-separated
	// line. The idemp.Wrap call below will handle the separator before
	// the loop-break marker; we only need to manage the gap between the
	// body and the plane-ref marker here.
	if planeBody != "" {
		if !strings.HasSuffix(planeBody, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(RenderPlaneRefMarker(ref))
	return idemp.Wrap(b.String(), idemp.SourcePlane, deliveryID)
}

// inverseStateMap builds a plane_state_name → forge_state ("open" or
// "closed") map from a link's forward forge → plane state map. Used by
// the (currently deferred) plane work_item.updated path to translate
// plane state changes back into the forge state vocabulary.
//
// Forward map shape:
//
//	link.StateMap["open"]   = "Todo"
//	link.StateMap["closed"] = "Done"
//
// Inverse:
//
//	"Todo" → "open"
//	"Done" → "closed"
//
// Any plane state name not present in the inverse map defaults to
// "open" in v1 — closure must be explicit on the plane side via the
// configured state, matching the "we do not invent transitions"
// posture. Returns nil for a nil/empty input so callers can skip the
// state-set step entirely.
func inverseStateMap(stateMap map[string]string) map[string]string {
	if len(stateMap) == 0 {
		return nil
	}
	out := make(map[string]string, len(stateMap))
	for forgeState, planeName := range stateMap {
		if planeName == "" {
			continue
		}
		// Last writer wins on collision (two forge states mapping to the
		// same plane name). The forward map is small (typically two
		// entries: open, closed) and the only realistic collision is an
		// operator typo, which we surface during config load.
		out[planeName] = forgeState
	}
	return out
}
