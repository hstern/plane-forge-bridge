package plane

import "errors"

// EventKind is a normalized identifier for the events this package
// understands. It is composed from the Plane "event" + "action" fields in
// the webhook envelope.
type EventKind string

// EventKind constants enumerate the Plane webhook events this package
// surfaces. The string form is "<plane-event>.<plane-action>".
const (
	EventWorkItemCreated EventKind = "work_item.created"
	EventWorkItemUpdated EventKind = "work_item.updated"
	EventWorkItemDeleted EventKind = "work_item.deleted"
	EventCommentCreated  EventKind = "comment.created"
	EventCommentUpdated  EventKind = "comment.updated"
	EventCommentDeleted  EventKind = "comment.deleted"
)

// HTTP header names used by Plane.
const (
	HeaderSignature = "X-Plane-Signature"
	HeaderEvent     = "X-Plane-Event"
	HeaderDelivery  = "X-Plane-Delivery"
)

// Plane "event" header values we handle. Plane currently emits "project",
// "issue", "cycle", "module", "issue_comment", and a few relation events;
// this package handles the two that map to bridge concerns.
const (
	planeEventIssue        = "issue"
	planeEventIssueComment = "issue_comment"
)

// Plane "action" values in the body envelope.
const (
	actionCreate = "create"
	actionUpdate = "update"
	actionDelete = "delete"
)

// Actor is the (often partial) record of who triggered the event. Plane
// nests it under activity.actor in the envelope.
type Actor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// WorkItem is the minimal subset of Plane's IssueExpandSerializer output we
// need to translate to a forge issue. Plane calls these "work items" in its
// UI and "issues" in its database / API; we use the UI name in our public
// surface.
type WorkItem struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description_html"`
	State          string   `json:"state"`
	Priority       string   `json:"priority"`
	Assignees      []string `json:"assignees"`
	Labels         []string `json:"labels"`
	Project        string   `json:"project"`
	Workspace      string   `json:"workspace"`
	CreatedBy      string   `json:"created_by"`
	SequenceID     int      `json:"sequence_id"`
	ExternalSource string   `json:"external_source,omitempty"`
	ExternalID     string   `json:"external_id,omitempty"`
}

// Comment is the minimal subset of Plane's IssueCommentSerializer output
// the bridge needs.
type Comment struct {
	ID          string `json:"id"`
	IssueID     string `json:"issue"`
	Project     string `json:"project"`
	Workspace   string `json:"workspace"`
	CommentHTML string `json:"comment_html"`
	Actor       string `json:"actor"`
	CreatedBy   string `json:"created_by"`
	Access      string `json:"access"`
}

// Event is the parsed, normalized webhook delivery. Exactly one of WorkItem
// or Comment is populated, matching Kind.
type Event struct {
	Kind        EventKind
	DeliveryID  string
	Action      string
	WorkspaceID string
	WebhookID   string
	Actor       Actor
	WorkItem    *WorkItem
	Comment     *Comment
	Raw         []byte
}

// Sentinel errors. Mirrors internal/forge.
var (
	ErrEmptySecret        = errors.New("plane: empty secret")
	ErrMissingSignature   = errors.New("plane: missing signature header")
	ErrInvalidSignature   = errors.New("plane: invalid signature")
	ErrMissingEventHeader = errors.New("plane: missing event header")
	ErrUnsupportedEvent   = errors.New("plane: unsupported event type")
	ErrMalformedPayload   = errors.New("plane: malformed payload")
)
