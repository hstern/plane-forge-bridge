package plane

import (
	"errors"
	"fmt"
)

// This file declares the Plane REST API request/response types and sentinel
// errors that the bridge needs to translate forge events into Plane writes.
// The concrete Client method implementations (HTTP transport, request
// encoding, response decoding) live in client.go and are being built out
// alongside this file. The types here are the surface that internal/sync
// programs against; they are intentionally minimal and field-additive over
// time.

// ErrNotFound is returned by lookup operations (e.g. GetIssueByExternalRef)
// when the requested resource does not exist. Callers compare with errors.Is.
var ErrNotFound = errors.New("plane: not found")

// APIError represents a non-2xx response from the Plane REST API. The wire
// shape is not yet stable — Plane's error responses are JSON in some
// endpoints and HTML in others — so we carry the raw status and body and
// expose Error() with the status code so log lines are useful.
type APIError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("plane: %s %s returned %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// CreateIssueRequest is the body sent to Plane's create-issue endpoint. Only
// the fields the bridge needs at this point are present; new ones can be
// added without breaking existing call sites.
//
// ExternalSource and ExternalID together form the durable external reference
// that lets the bridge look the work item back up on subsequent forge events
// (see GetIssueByExternalRef). The bridge uses "forge:<owner>/<repo>" as
// ExternalSource and the forge issue ID (as a decimal string) as ExternalID.
type CreateIssueRequest struct {
	Name            string   `json:"name"`
	DescriptionHTML string   `json:"description_html,omitempty"`
	StateID         string   `json:"state,omitempty"`
	AssigneeIDs     []string `json:"assignees,omitempty"`
	LabelIDs        []string `json:"labels,omitempty"`
	ExternalSource  string   `json:"external_source,omitempty"`
	ExternalID      string   `json:"external_id,omitempty"`
	CreatedBy       string   `json:"created_by,omitempty"`
}

// UpdateIssueRequest is the body sent to Plane's update-issue endpoint. All
// fields are pointers so callers can express the difference between
// "absent — don't touch" and "present with zero value". StateID in
// particular: a nil pointer means "leave the state alone", whereas a pointer
// to "" would erase it.
type UpdateIssueRequest struct {
	Name            *string   `json:"name,omitempty"`
	DescriptionHTML *string   `json:"description_html,omitempty"`
	StateID         *string   `json:"state,omitempty"`
	AssigneeIDs     *[]string `json:"assignees,omitempty"`
	LabelIDs        *[]string `json:"labels,omitempty"`
}

// State is a Plane project workflow state. The Group field carries Plane's
// fixed state group (backlog/unstarted/started/completed/cancelled) and the
// Name is the human-visible label that operators configure in StateMap.
type State struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
	Color string `json:"color"`
}
