package plane

import (
	"bytes"
	"encoding/json"
	"errors"
)

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

// Plane "action" values in the body envelope. Real Plane (verified
// against v1.3.1's webhook_task.py) sends past-tense verbs
// ("created"/"updated"/"deleted"), matching the GitHub/GitLab webhook
// convention. The present-tense form ("create"/"update"/"delete") shows
// up in older / non-bgtask code paths and in some hand-rolled clients,
// so the parser accepts both — the past-tense form is canonical.
const (
	actionCreate  = "create"
	actionUpdate  = "update"
	actionDelete  = "delete"
	actionCreated = "created"
	actionUpdated = "updated"
	actionDeleted = "deleted"
)

// Actor is the (often partial) record of who triggered the event. Plane
// nests it under activity.actor in the envelope.
type Actor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// StateRef is the nested state object Plane serializes inside a WorkItem.
//
// Plane CE v1.3.1 serializes the same WorkItem.state field in two different
// shapes depending on the path:
//   - webhook deliveries send the full object (id, name, color, group)
//   - REST POST/GET/PATCH /issues/ responses send a bare UUID string
//
// UnmarshalJSON accepts both so a single WorkItem type can decode either
// path. PFB-24 fixed the webhook (object) decode; PFB-25 added the REST
// (string) decode after a regression on the create-response path.
type StateRef struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
	Group string `json:"group,omitempty"`
}

// UnmarshalJSON accepts either a bare UUID string (REST) or the
// {id,name,color,group} object (webhook).
func (s *StateRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefBothShapes(data, &s.ID, (*stateRefAlias)(s))
}

// LabelRef is the nested label object Plane serializes inside a WorkItem.
// Same two-shape rationale as StateRef — REST returns a bare UUID string,
// webhook returns {id, name, color}. UnmarshalJSON handles both.
type LabelRef struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

// UnmarshalJSON accepts either a bare UUID string (REST) or the
// {id,name,color} object (webhook).
func (l *LabelRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefBothShapes(data, &l.ID, (*labelRefAlias)(l))
}

// AssigneeRef is the nested member object Plane serializes inside a
// WorkItem's assignees array. Same two-shape rationale as StateRef — REST
// returns a bare UUID string, webhook returns {id, display_name}.
// UnmarshalJSON handles both.
type AssigneeRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// UnmarshalJSON accepts either a bare UUID string (REST) or the
// {id,display_name} object (webhook).
func (a *AssigneeRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefBothShapes(data, &a.ID, (*assigneeRefAlias)(a))
}

// Aliases break the UnmarshalJSON recursion when we want the default
// struct decode for the object form.
type (
	stateRefAlias    StateRef
	labelRefAlias    LabelRef
	assigneeRefAlias AssigneeRef
)

// unmarshalRefBothShapes decodes a WorkItem ref field that may arrive as
// either a bare UUID string (REST) or an object (webhook). On a string the
// UUID is written into idOut; on an object the full struct is decoded via
// objOut (which must be an alias pointer so the default struct decoder
// runs, not the caller's custom UnmarshalJSON). null and empty payloads
// leave the target untouched.
func unmarshalRefBothShapes(data []byte, idOut *string, objOut any) error {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, idOut)
	}
	return json.Unmarshal(data, objOut)
}

// WorkItem is the minimal subset of Plane's IssueExpandSerializer output we
// need to translate to a forge issue. Plane calls these "work items" in its
// UI and "issues" in its database / API; we use the UI name in our public
// surface.
//
// State, Labels, and Assignees are serialized as nested objects by real
// Plane (not as the bare UUIDs the original modelling assumed). Callers
// that want just the UUID read .State.ID, iterate .Labels[i].ID, etc.
type WorkItem struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description_html"`
	State          StateRef      `json:"state"`
	Priority       string        `json:"priority"`
	Assignees      []AssigneeRef `json:"assignees"`
	Labels         []LabelRef    `json:"labels"`
	Project        string        `json:"project"`
	Workspace      string        `json:"workspace"`
	CreatedBy      string        `json:"created_by"`
	SequenceID     int           `json:"sequence_id"`
	ExternalSource string        `json:"external_source,omitempty"`
	ExternalID     string        `json:"external_id,omitempty"`
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
