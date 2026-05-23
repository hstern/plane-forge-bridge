package forge

import (
	"errors"
	"strconv"
)

// CreateCommentRequest is the body for
// POST /repos/{owner}/{repo}/issues/{index}/comments.
//
// The forge comment API today takes only `body`; we keep this as a
// struct (rather than a map[string]string) so future optional fields can
// be added without churning every caller.
type CreateCommentRequest struct {
	Body string `json:"body"`
}

// UpdateCommentRequest is the body for
// PATCH /repos/{owner}/{repo}/issues/comments/{id}.
type UpdateCommentRequest struct {
	Body string `json:"body"`
}

// CreateLabelRequest is the body for
// POST /repos/{owner}/{repo}/labels.
//
// Gitea/Forgejo require a name; color is a hex string (e.g. "#00ff00").
// Description is optional. The upstream API also accepts an `exclusive`
// flag (forge-only, scoped-label semantics) and an `is_archived` flag —
// v1 of the bridge does not use either, so they are intentionally
// omitted from this struct.
type CreateLabelRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
}

// ErrNotFound is returned by GetIssue (and DeleteComment) when the forge
// answers 404. It is intentionally distinct from the webhook sentinels
// in errors.go: those are inbound concerns, this is the outbound REST
// client's "no such resource" signal.
var ErrNotFound = errors.New("forge: not found")

// APIError captures non-2xx responses from the forge with the body for
// diagnosis. The body is truncated to 4 KiB so the error stays loggable.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string // truncated to 4 KiB
}

// Error implements the error interface. The body is included verbatim so
// it lands in logs alongside the status — Forgejo and Gitea both return
// a short JSON object with a "message" key.
func (e *APIError) Error() string {
	return "forge: " + e.Method + " " + e.Path + ": unexpected status " +
		strconv.Itoa(e.StatusCode) + ": " + e.Body
}
