package plane

import (
	"context"
	"errors"
)

// Stubs added by the sync/step-7 worktree so internal/sync compiles
// against the eventual PlaneClient interface (GetIssue / CreateComment /
// UpdateComment / DeleteComment). The sibling plane-Client worktree is
// authoring the real REST methods in parallel; when its commit lands these
// declarations will be replaced. Keep the field names stable — they're
// load-bearing in internal/sync.

// CreateCommentRequest is the body for POST .../issues/{iid}/comments/.
// Only the comment body and the access scope are needed for the bridge's
// outbound write; Plane fills in actor / created_by from the API token.
type CreateCommentRequest struct {
	CommentHTML string `json:"comment_html"`
	Access      string `json:"access,omitempty"` // "EXTERNAL" | "INTERNAL"; default EXTERNAL
}

// UpdateCommentRequest is the body for PATCH .../issues/{iid}/comments/{cid}/.
// Pointer so a caller can patch only what's set.
type UpdateCommentRequest struct {
	CommentHTML *string `json:"comment_html,omitempty"`
}

// errStubMethod is the placeholder error returned by the stub methods on
// plane.Client below. They exist so the rest of the codebase compiles
// against the step-7 PlaneClient interface; the sibling worktree replaces
// each method body with a real HTTP call.
var errStubMethod = errors.New("plane: method not yet implemented (stub from step-7 sync worktree)")

// GetIssue fetches a work item by its plane UUID. Stub.
func (c *Client) GetIssue(_ context.Context, _, _ string) (*WorkItem, error) {
	return nil, errStubMethod
}

// CreateComment posts a comment to an issue. Stub.
func (c *Client) CreateComment(_ context.Context, _, _ string, _ CreateCommentRequest) (*Comment, error) {
	return nil, errStubMethod
}

// UpdateComment patches an existing comment. Stub.
func (c *Client) UpdateComment(_ context.Context, _, _, _ string, _ UpdateCommentRequest) (*Comment, error) {
	return nil, errStubMethod
}

// DeleteComment deletes a comment. Stub.
func (c *Client) DeleteComment(_ context.Context, _, _, _ string) error {
	return errStubMethod
}
