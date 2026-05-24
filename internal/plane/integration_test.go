//go:build integration

// Package plane integration tests exercise the bridge's REST client
// against a real Plane CE backend (test/e2e-docker/plane-ce/). The
// build tag keeps them out of `go test ./...` — they only run when
// explicitly opted in:
//
//	go test -tags=integration ./internal/plane -run TestIntegration -v
//
// Wire-up: source the JSON the seed emits into env vars, then run.
//
//	eval "$(bash test/e2e-docker/plane-ce/run.sh up | tail -n1 | \
//	  jq -r 'to_entries[] | "export PFB_PLANE_TEST_\(.key|ascii_upcase)=\(.value)"')"
//	export PFB_PLANE_TEST_BASE_URL=http://localhost:8765
//	go test -tags=integration ./internal/plane -run TestIntegration -v
//
// CI does the same wiring inline in .github/workflows/ci.yaml.
//
// Goal: round-trip the plane.Client methods the bridge actually uses
// against a real Plane backend, so wire-shape drift (PFB-22/24/25)
// fails in CI instead of in production. The assertions intentionally
// pin only the fields the bridge consumes — decode is what we care
// about, not full payload parity.
package plane

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func newIntegrationClient(t *testing.T) (*Client, string) {
	t.Helper()
	base := os.Getenv("PFB_PLANE_TEST_BASE_URL")
	slug := os.Getenv("PFB_PLANE_TEST_WORKSPACE_SLUG")
	token := os.Getenv("PFB_PLANE_TEST_API_TOKEN")
	project := os.Getenv("PFB_PLANE_TEST_PROJECT_ID")
	if base == "" || slug == "" || token == "" || project == "" {
		t.Skip("PFB_PLANE_TEST_{BASE_URL,WORKSPACE_SLUG,API_TOKEN,PROJECT_ID} not set; " +
			"run test/e2e-docker/plane-ce/run.sh up first")
	}
	c := NewClient(base, slug, token, &http.Client{Timeout: 30 * time.Second})
	return c, project
}

// TestIntegration_RoundTrip exercises every plane.Client method the
// bridge calls today. It's a single test, not a table, so cleanup
// happens in the right order: the issue → its comments → the labels.
// If Plane CE changes the wire shape of any response the bridge
// decodes, this test fails on the decode, exactly the failure mode
// PFB-25 exposed in production.
func TestIntegration_RoundTrip(t *testing.T) {
	c, projectID := newIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// --- ListProjectStates: locked-in shape, used by state mapping ---
	states, err := c.ListProjectStates(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectStates: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("ListProjectStates returned no states; seed.py should have created the defaults")
	}
	var backlogID, doneID string
	for _, s := range states {
		switch s.Name {
		case "Backlog":
			backlogID = s.ID
		case "Done":
			doneID = s.ID
		}
	}
	if backlogID == "" || doneID == "" {
		t.Fatalf("missing default states; got %d states", len(states))
	}

	// --- CreateProjectLabel + ListProjectLabels ---
	labelName := "pfb-integ-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	createdLabel, err := c.CreateProjectLabel(ctx, projectID, CreateLabelRequest{
		Name:        labelName,
		Color:       "#7c3aed",
		Description: "round-trip label",
	})
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	if createdLabel.ID == "" || createdLabel.Name != labelName {
		t.Errorf("CreateProjectLabel returned %+v, want non-empty ID + matching name", createdLabel)
	}
	labels, err := c.ListProjectLabels(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectLabels: %v", err)
	}
	var foundLabel bool
	for _, l := range labels {
		if l.ID == createdLabel.ID {
			foundLabel = true
			break
		}
	}
	if !foundLabel {
		t.Errorf("ListProjectLabels missing the just-created label %q", createdLabel.ID)
	}

	// --- ListWorkspaceMembers: bridge identity resolver uses this ---
	members, err := c.ListWorkspaceMembers(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaceMembers: %v", err)
	}
	if len(members) == 0 {
		t.Fatal("ListWorkspaceMembers returned no members; seed.py created an admin")
	}
	if members[0].Email == "" {
		t.Errorf("ListWorkspaceMembers returned member with empty Email; identity resolver depends on this")
	}

	// --- CreateIssue: this is the PFB-25 failure path ---
	// The bridge stamps external_source on every create; using a real
	// value here also covers the PFB-27 echo-detection invariant from
	// the *creation* side (the bridge's handler now skips inbound
	// echoes that carry this prefix).
	issueName := "pfb-integ-issue-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	created, err := c.CreateIssue(ctx, projectID, CreateIssueRequest{
		Name:            issueName,
		DescriptionHTML: "<p>round-trip body</p>",
		StateID:         backlogID,
		Priority:        "low",
		Labels:          []string{createdLabel.ID},
		ExternalSource:  "forge:acme/widgets",
		ExternalID:      "42",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if created.ID == "" || created.SequenceID == 0 {
		t.Errorf("CreateIssue returned %+v, want populated ID + SequenceID", created)
	}
	// PFB-25 specifically: state must decode from the bare UUID Plane
	// REST returns. PFB-24 specifically: webhooks come back as objects;
	// this test covers the REST path.
	if created.State.ID != backlogID {
		t.Errorf("State.ID = %q, want %q (PFB-25 regression if decode fails)",
			created.State.ID, backlogID)
	}
	if len(created.Labels) != 1 || created.Labels[0].ID != createdLabel.ID {
		t.Errorf("Labels = %+v, want [{ID:%s}]", created.Labels, createdLabel.ID)
	}

	// --- GetIssue: independent decode path ---
	got, err := c.GetIssue(ctx, projectID, created.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Name != issueName || got.State.ID != backlogID {
		t.Errorf("GetIssue mismatch: name=%q state=%q", got.Name, got.State.ID)
	}

	// --- UpdateIssue: PATCH response decode ---
	newName := issueName + " (updated)"
	updated, err := c.UpdateIssue(ctx, projectID, created.ID, UpdateIssueRequest{
		Name:    &newName,
		StateID: &doneID,
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if updated.Name != newName || updated.State.ID != doneID {
		t.Errorf("UpdateIssue mismatch: name=%q state=%q", updated.Name, updated.State.ID)
	}

	// --- GetIssueByExternalRef: bridge's reverse-lookup path ---
	found, err := c.GetIssueByExternalRef(ctx, projectID, "forge:acme/widgets", "42")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef: %v", err)
	}
	if found.ID != created.ID {
		t.Errorf("GetIssueByExternalRef returned %q, want %q", found.ID, created.ID)
	}

	// --- GetIssueByExternalRef on a non-existent pair returns ErrNotFound ---
	missing, err := c.GetIssueByExternalRef(ctx, projectID, "forge:nope/nope", "0")
	if err == nil || err != ErrNotFound {
		t.Errorf("GetIssueByExternalRef(missing) = (%v, %v), want (nil, ErrNotFound)", missing, err)
	}

	// --- CreateComment / UpdateComment / DeleteComment ---
	createdComment, err := c.CreateComment(ctx, projectID, created.ID, CreateCommentRequest{
		CommentHTML: "<p>round-trip comment <!-- pfb:src=forge,evt=integ -->",
		Access:      "EXTERNAL",
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if createdComment.ID == "" || createdComment.IssueID != created.ID {
		t.Errorf("CreateComment returned %+v", createdComment)
	}

	patched, err := c.UpdateComment(ctx, projectID, created.ID, createdComment.ID, UpdateCommentRequest{
		CommentHTML: pStr("<p>edited body</p>"),
	})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if patched.CommentHTML == "" {
		t.Errorf("UpdateComment dropped comment_html: %+v", patched)
	}

	if err := c.DeleteComment(ctx, projectID, created.ID, createdComment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	// Re-delete must be ErrNotFound, not an opaque error.
	if err := c.DeleteComment(ctx, projectID, created.ID, createdComment.ID); err != ErrNotFound {
		t.Errorf("DeleteComment(already deleted) = %v, want ErrNotFound", err)
	}

	// --- Cleanup: the issue was created with external_source so the
	// next test run finds it via GetIssueByExternalRef. We DON'T delete
	// it from REST — Plane CE deletes propagate to its webhook system,
	// and leaving a residual issue is harmless (next CI run re-uses
	// the same external_id and reconciles via UpdateIssue). The label
	// stays too. Tests run against an ephemeral DB in CI so nothing
	// accumulates across runs.
}

func pStr(s string) *string { return &s }
