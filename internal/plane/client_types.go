package plane

import (
	"errors"
	"strconv"
)

// CreateIssueRequest is the body for POST /workspaces/{slug}/projects/{pid}/issues/.
// Fields are omitempty so the JSON has no nulls — Plane treats nulls
// differently from absent for some fields.
type CreateIssueRequest struct {
	Name            string   `json:"name"`
	DescriptionHTML string   `json:"description_html,omitempty"`
	StateID         string   `json:"state,omitempty"`     // Plane state UUID
	Priority        string   `json:"priority,omitempty"`  // "none"|"low"|"medium"|"high"|"urgent"
	Assignees       []string `json:"assignees,omitempty"` // Plane member UUIDs
	Labels          []string `json:"labels,omitempty"`    // Plane label UUIDs
	ExternalSource  string   `json:"external_source,omitempty"`
	ExternalID      string   `json:"external_id,omitempty"`
}

// UpdateIssueRequest is the body for PATCH .../issues/{iid}/. Pointer fields
// let the caller patch only what's set without overwriting omitted fields
// with zero values.
type UpdateIssueRequest struct {
	Name            *string  `json:"name,omitempty"`
	DescriptionHTML *string  `json:"description_html,omitempty"`
	StateID         *string  `json:"state,omitempty"`
	Priority        *string  `json:"priority,omitempty"`
	Assignees       []string `json:"assignees,omitempty"`
	Labels          []string `json:"labels,omitempty"`
}

// CreateCommentRequest is the body for POST
// /workspaces/{slug}/projects/{pid}/issues/{iid}/comments/. Access is
// omitempty because Plane defaults to "EXTERNAL" when absent.
type CreateCommentRequest struct {
	CommentHTML string `json:"comment_html"`
	Access      string `json:"access,omitempty"` // "INTERNAL" or "EXTERNAL"; Plane defaults to EXTERNAL
}

// UpdateCommentRequest is the body for PATCH
// .../issues/{iid}/comments/{cid}/. The single pointer field lets the
// caller patch comment_html without sending it as an explicit null.
type UpdateCommentRequest struct {
	CommentHTML *string `json:"comment_html,omitempty"`
}

// CommentResponse is the shape Plane returns from the comment endpoints.
// It's an alias for the inbound webhook Comment type: Plane's
// IssueCommentSerializer is the same serializer used for both the webhook
// payload and the REST response, so the field layout matches. Keeping
// them as a single type avoids drift while still letting the public REST
// surface read CommentResponse.
type CommentResponse = Comment

// State is a workflow state defined on a Plane project.
type State struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"` // "backlog"|"unstarted"|"started"|"completed"|"cancelled"
	Color string `json:"color"`
}

// statesListResponse is the paginated response shape returned by Plane's
// state list endpoint. Plane's BasePaginator embeds the rows under "results"
// alongside cursor metadata; we only need the rows.
type statesListResponse struct {
	Results []State `json:"results"`
}

// Label is a Plane project label. Plane's LabelSerializer carries more
// fields (parent, sort_order, external_*); we keep the surface narrow to
// what the bridge actually uses on the issue create/update path.
type Label struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// CreateLabelRequest is the body for POST
// /workspaces/{slug}/projects/{pid}/labels/. Only Name is required; Color
// and Description are omitempty so the JSON has no nulls (Plane treats
// nulls differently from absent for some fields, mirroring our
// CreateIssueRequest stance).
type CreateLabelRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// labelsListResponse is the paginated response shape returned by Plane's
// label list endpoint. Like states it uses BasePaginator, so we read only
// the rows under "results".
type labelsListResponse struct {
	Results []Label `json:"results"`
}

// Member is a Plane workspace member as exposed by the public v1
// members endpoint. The endpoint deliberately omits OAuth/provider
// identity (no github_id, gitea_id, sso subject, etc.) — only the
// member's primary email is exposed. The bridge's identity resolver
// matches forge users to Plane members by that email.
type Member struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Role        int    `json:"role,omitempty"`
}

// ErrNotFound is returned by GetIssueByExternalRef when no work item matches
// the (source, externalID) pair. This is the normal "not yet mirrored" case
// and callers should treat it as a signal to create, not as a failure.
var ErrNotFound = errors.New("plane: not found")

// APIError captures non-2xx responses from Plane with the body for diagnosis.
// The body is truncated to 4 KiB so the error stays loggable.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string // truncated to 4 KiB
}

// Error implements the error interface. The body is included verbatim so
// it lands in logs alongside the status — most Plane errors are a short
// JSON object with an "error" key.
func (e *APIError) Error() string {
	return "plane: " + e.Method + " " + e.Path + ": unexpected status " +
		strconv.Itoa(e.StatusCode) + ": " + e.Body
}
