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
