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
