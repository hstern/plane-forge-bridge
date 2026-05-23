package forge

// Stubs added by the sync/step-7 worktree so internal/sync compiles
// against the eventual ForgeClient interface (GetIssue / CreateComment /
// UpdateComment / DeleteComment). The sibling forge-Client worktree is
// authoring the real REST methods in parallel; when its commit lands these
// declarations will be replaced or supplemented. Keep the field names
// stable — they're load-bearing in internal/sync.

// CreateCommentRequest is the body for POST /repos/{owner}/{repo}/issues/{n}/comments.
// Gitea/Forgejo accept just {"body": "..."}; we keep the shape minimal so
// it's not a behavioural surface for the bridge.
type CreateCommentRequest struct {
	Body string `json:"body"`
}

// UpdateCommentRequest is the body for PATCH /repos/{owner}/{repo}/issues/comments/{cid}.
type UpdateCommentRequest struct {
	Body string `json:"body"`
}
