package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// testLogger discards every record so tests stay quiet by default. Use
// slog.NewTextHandler(os.Stderr, …) locally if you need to see what the
// engine logged.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	testRepo      = "acme/widgets"
	testProjectID = "proj-1111"
	testForgeUser = "alice"
	testPlaneUser = "plane-uuid-alice"
)

// newTestEngine returns an Engine wired to a fresh fake client and a small
// fixed config. Tests that need to tweak the config (e.g. unmapped user)
// drop fields off the returned Engine directly.
func newTestEngine(t *testing.T) (*Engine, *fakeClient) {
	t.Helper()
	fc := &fakeClient{}
	cfg := &mapping.Resolved{
		Links: []mapping.Link{{
			ForgeRepo:      testRepo,
			PlaneProjectID: testProjectID,
			StateMap: map[string]string{
				"open":   "Todo",
				"closed": "Done",
			},
		}},
		Users: map[string]string{
			testForgeUser: testPlaneUser,
		},
	}
	cfg.BridgeBot.ForgeUsername = "pfb-bot"
	cfg.BridgeBot.PlaneMemberID = "plane-uuid-bot"
	return NewEngine(fc, nil, cfg, testLogger()), fc
}

// mkIssueEvent builds a forge.Event for an issue.* delivery. Callers
// override fields on the returned event for case-specific tweaks.
func mkIssueEvent(kind forge.EventKind, sender string) *forge.Event {
	return &forge.Event{
		Kind:       kind,
		DeliveryID: "delivery-abc",
		Repo: forge.Repository{
			ID:       100,
			FullName: testRepo,
			HTMLURL:  "https://forge.example.com/acme/widgets",
		},
		Sender: forge.User{
			Login:   sender,
			HTMLURL: "https://forge.example.com/" + sender,
		},
		Issue: &forge.Issue{
			ID:     42,
			Number: 7,
			Title:  "Bug: things explode",
			Body:   "When I do X, Y happens.",
			User: forge.User{
				Login:   sender,
				HTMLURL: "https://forge.example.com/" + sender,
			},
		},
	}
}

func TestHandle_IssueOpened_Creates(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("got action %v, want ActionCreated", out.Action)
	}
	if out.WorkItemID == "" {
		t.Fatal("WorkItemID empty on create")
	}
	if len(fc.Creates) != 1 {
		t.Fatalf("want 1 create call, got %d", len(fc.Creates))
	}
	req := fc.Creates[0].Req
	if req.Name != evt.Issue.Title {
		t.Errorf("Name=%q, want %q", req.Name, evt.Issue.Title)
	}
	if req.ExternalSource != "forge:"+testRepo {
		t.Errorf("ExternalSource=%q", req.ExternalSource)
	}
	// external_id is the forge issue NUMBER (per-repo monotonic), not the
	// internal database ID — see externalRef and README "External reference
	// convention". Step 6 used ID; step 7 switched to Number so the plane→
	// forge inbound path can resolve the forge issue with GetIssue(owner,
	// repo, number).
	if req.ExternalID != "7" {
		t.Errorf("ExternalID=%q, want 7 (issue.Number)", req.ExternalID)
	}
	if !strings.Contains(req.DescriptionHTML, "<!-- pfb:src=forge,evt=delivery-abc -->") {
		t.Errorf("description missing marker: %q", req.DescriptionHTML)
	}
	if !strings.HasSuffix(strings.TrimRight(req.DescriptionHTML, " \t\r\n"), "-->") {
		t.Errorf("marker not last in description: %q", req.DescriptionHTML)
	}
	if req.StateID != "state-todo" {
		t.Errorf("StateID=%q, want state-todo", req.StateID)
	}
}

func TestHandle_IssueOpened_AlreadyExists_Updates(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("got action %v, want ActionUpdated", out.Action)
	}
	if len(fc.Creates) != 0 {
		t.Errorf("unexpected CreateIssue calls: %d", len(fc.Creates))
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 UpdateIssue call, got %d", len(fc.Updates))
	}
	upd := fc.Updates[0]
	if upd.IssueID != "wi-existing" {
		t.Errorf("update against %q, want wi-existing", upd.IssueID)
	}
	if upd.Req.Name == nil || *upd.Req.Name != evt.Issue.Title {
		t.Errorf("Name not reconciled: %+v", upd.Req.Name)
	}
}

func TestHandle_IssueOpened_UnmappedAuthor_PrefacesDescription(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, "stranger")

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.Creates) != 1 {
		t.Fatalf("want 1 create, got %d", len(fc.Creates))
	}
	desc := fc.Creates[0].Req.DescriptionHTML
	if !strings.HasPrefix(desc, "> Originally posted by `stranger`") {
		t.Errorf("missing unmapped-author preface; got: %q", desc)
	}
	if !strings.Contains(desc, "("+evt.Sender.HTMLURL+")") {
		t.Errorf("preface missing sender HTML URL; got: %q", desc)
	}
}

func TestHandle_IssueOpened_MappedAuthor_NoPreface(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	desc := fc.Creates[0].Req.DescriptionHTML
	if strings.Contains(desc, "Originally posted by") {
		t.Errorf("mapped author got a preface; desc: %q", desc)
	}
	if !strings.HasPrefix(desc, evt.Issue.Body) {
		t.Errorf("description doesn't start with forge body: %q", desc)
	}
}

func TestHandle_IssueOpened_NoLink_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)
	evt.Repo.FullName = "other/repo"

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != "no link configured for repo" {
		t.Errorf("Reason=%q", out.Reason)
	}
	if out.Link != nil {
		t.Errorf("expected nil Link on no-link skip, got %+v", out.Link)
	}
	gets, creates, updates, lists := fc.snapshot()
	if len(gets)+len(creates)+len(updates)+len(lists) != 0 {
		t.Errorf("no-link skip made API calls: gets=%d creates=%d updates=%d lists=%d",
			len(gets), len(creates), len(updates), len(lists))
	}
}

func TestHandle_IssueEdited_UpdatesWhenFound(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueEdited, testForgeUser)
	evt.Issue.Title = "Bug: things still explode"

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v, want ActionUpdated", out.Action)
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 update, got %d", len(fc.Updates))
	}
	upd := fc.Updates[0]
	if upd.Req.Name == nil || *upd.Req.Name != "Bug: things still explode" {
		t.Errorf("Name not propagated: %+v", upd.Req.Name)
	}
	if upd.Req.StateID != nil {
		t.Errorf("edit should not touch StateID; got %v", *upd.Req.StateID)
	}
}

func TestHandle_IssueEdited_SkipsWhenNotFound(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// Default GetIssueByExternalRef returns ErrNotFound.
	evt := mkIssueEvent(forge.EventIssueEdited, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "no prior open") {
		t.Errorf("Reason=%q, want one mentioning 'no prior open'", out.Reason)
	}
	if len(fc.Updates) != 0 || len(fc.Creates) != 0 {
		t.Errorf("edit-without-open should not call Update or Create; updates=%d creates=%d",
			len(fc.Updates), len(fc.Creates))
	}
}

func TestHandle_IssueClosed_TranslatesState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueClosed, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v, want ActionUpdated", out.Action)
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 update, got %d", len(fc.Updates))
	}
	upd := fc.Updates[0]
	if upd.Req.StateID == nil {
		t.Fatal("StateID nil; want pointer to state-done")
	}
	if *upd.Req.StateID != "state-done" {
		t.Errorf("StateID=%q, want state-done", *upd.Req.StateID)
	}
	if upd.Req.Name != nil || upd.Req.DescriptionHTML != nil {
		t.Errorf("close should only touch state; got Name=%v Desc=%v", upd.Req.Name, upd.Req.DescriptionHTML)
	}
}

func TestHandle_IssueClosed_NoStateMapping_LeavesStateAlone(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// Strip the closed mapping off the link.
	e.Links[0].StateMap = map[string]string{"open": "Todo"}
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueClosed, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v, want ActionUpdated", out.Action)
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 update, got %d", len(fc.Updates))
	}
	if fc.Updates[0].Req.StateID != nil {
		t.Errorf("StateID should be nil (don't touch state); got %v", *fc.Updates[0].Req.StateID)
	}
}

func TestHandle_IssueReopened_TranslatesState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueReopened, testForgeUser)

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v", out.Action)
	}
	upd := fc.Updates[0]
	if upd.Req.StateID == nil || *upd.Req.StateID != "state-todo" {
		t.Errorf("StateID=%v, want pointer to state-todo", upd.Req.StateID)
	}
}

func TestHandle_UnsupportedEvent_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := &forge.Event{
		Kind:       forge.EventPullRequestOpened,
		DeliveryID: "delivery-xyz",
		Repo:       forge.Repository{FullName: testRepo},
	}

	out, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != "unsupported event" {
		t.Errorf("Reason=%q", out.Reason)
	}
	// Unsupported events do match a link, so Link should be set.
	if out.Link == nil {
		t.Error("Link nil for repo-matched unsupported event")
	}
	if len(fc.Creates)+len(fc.Updates)+len(fc.Gets) != 0 {
		t.Error("unsupported event made API calls")
	}
}

func TestHandle_PreservesIdempotency(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// First call: not found, create succeeds with wi-default.
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)
	out1, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil || out1.Action != ActionCreated {
		t.Fatalf("first call: action=%v err=%v", out1.Action, err)
	}

	// Second call: fake now reports the existing work item.
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: out1.WorkItemID, Project: projectID}, nil
	}
	out2, err := e.HandleForgeIssue(context.Background(), evt)
	if err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if out2.Action != ActionUpdated {
		t.Errorf("second call action=%v, want ActionUpdated", out2.Action)
	}
	if out2.WorkItemID != out1.WorkItemID {
		t.Errorf("second call hit different work item %q vs %q", out2.WorkItemID, out1.WorkItemID)
	}
	if len(fc.Creates) != 1 {
		t.Errorf("want exactly 1 CreateIssue across the two calls, got %d", len(fc.Creates))
	}
}

// TestExternalRef_UsesIssueNumber is a regression guard for the step-6 →
// step-7 contract change. external_id must be the forge issue Number, not
// the internal database ID, so the plane→forge inbound path can resolve
// the forge issue with forge.GetIssue(owner, repo, number).
func TestExternalRef_UsesIssueNumber(t *testing.T) {
	t.Parallel()
	repo := forge.Repository{FullName: "acme/widgets"}
	issue := forge.Issue{ID: 999, Number: 7}
	source, id := externalRef(repo, issue)
	if source != "forge:acme/widgets" {
		t.Errorf("source=%q, want forge:acme/widgets", source)
	}
	if id != "7" {
		t.Errorf("id=%q, want 7 (Number, not ID)", id)
	}
}

func TestParseExternalRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		source     string
		externalID string
		wantOwner  string
		wantRepo   string
		wantNumber int64
		wantErr    bool
	}{
		{
			name: "happy path", source: "forge:acme/widgets", externalID: "42",
			wantOwner: "acme", wantRepo: "widgets", wantNumber: 42,
		},
		{
			name: "missing prefix", source: "acme/widgets", externalID: "1",
			wantErr: true,
		},
		{
			name: "missing slash", source: "forge:acmewidgets", externalID: "1",
			wantErr: true,
		},
		{
			name: "too many slashes", source: "forge:acme/widgets/extra", externalID: "1",
			wantErr: true,
		},
		{
			name: "non-numeric external id", source: "forge:acme/widgets", externalID: "seven",
			wantErr: true,
		},
		{
			name: "negative external id", source: "forge:acme/widgets", externalID: "-1",
			wantErr: true,
		},
		{
			name: "zero external id", source: "forge:acme/widgets", externalID: "0",
			wantErr: true,
		},
		{
			name: "empty source", source: "", externalID: "1",
			wantErr: true,
		},
		{
			name: "empty external id", source: "forge:acme/widgets", externalID: "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owner, repo, n, err := parseExternalRef(tc.source, tc.externalID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got owner=%q repo=%q n=%d", owner, repo, n)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo || n != tc.wantNumber {
				t.Errorf("got (%q,%q,%d), want (%q,%q,%d)",
					owner, repo, n, tc.wantOwner, tc.wantRepo, tc.wantNumber)
			}
		})
	}
}

// mkCommentEvent builds a forge.Event for an issue_comment.* delivery.
func mkCommentEvent(kind forge.EventKind, sender, body string) *forge.Event {
	e := mkIssueEvent(forge.EventIssueCommentCreated, sender)
	e.Kind = kind
	e.DeliveryID = "delivery-cmt"
	e.Comment = &forge.Comment{
		ID:   555,
		Body: body,
		User: forge.User{
			Login:   sender,
			HTMLURL: "https://forge.example.com/" + sender,
		},
	}
	return e
}

func TestHandle_ForgeCommentCreated_PostsToPlane(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkCommentEvent(forge.EventIssueCommentCreated, testForgeUser, "hello from forge")

	out, err := e.HandleForgeComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("action=%v, want ActionCreated", out.Action)
	}
	if out.WorkItemID != "wi-existing" {
		t.Errorf("WorkItemID=%q, want wi-existing", out.WorkItemID)
	}
	if out.CommentID == "" {
		t.Error("CommentID empty on Created comment outcome")
	}
	if len(fc.CommentCreates) != 1 {
		t.Fatalf("want 1 CreateComment call, got %d", len(fc.CommentCreates))
	}
	cc := fc.CommentCreates[0]
	if cc.ProjectID != testProjectID {
		t.Errorf("ProjectID=%q", cc.ProjectID)
	}
	if cc.IssueID != "wi-existing" {
		t.Errorf("IssueID=%q, want wi-existing", cc.IssueID)
	}
	if !strings.Contains(cc.Req.CommentHTML, "<!-- pfb:src=forge,evt=delivery-cmt -->") {
		t.Errorf("CommentHTML missing marker: %q", cc.Req.CommentHTML)
	}
	if !strings.Contains(cc.Req.CommentHTML, "hello from forge") {
		t.Errorf("CommentHTML missing body: %q", cc.Req.CommentHTML)
	}
}

func TestHandle_ForgeCommentCreated_NoLinkSkips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkCommentEvent(forge.EventIssueCommentCreated, testForgeUser, "x")
	evt.Repo.FullName = "other/repo"

	out, err := e.HandleForgeComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if out.Reason != "no link configured for repo" {
		t.Errorf("Reason=%q", out.Reason)
	}
	if len(fc.CommentCreates)+len(fc.Gets)+len(fc.GetsByID) != 0 {
		t.Errorf("no-link skip touched API: creates=%d gets=%d getsByID=%d",
			len(fc.CommentCreates), len(fc.Gets), len(fc.GetsByID))
	}
}

func TestHandle_ForgeCommentCreated_IssueNotMirroredSkips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// Default GetIssueByExternalRef returns ErrNotFound — comment fired
	// before the issue mirror caught up.
	evt := mkCommentEvent(forge.EventIssueCommentCreated, testForgeUser, "hi")

	out, err := e.HandleForgeComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "before plane issue was mirrored") {
		t.Errorf("Reason=%q does not mention mirror lag", out.Reason)
	}
	if len(fc.CommentCreates) != 0 {
		t.Errorf("CreateComment should not be called; got %d", len(fc.CommentCreates))
	}
	if len(fc.Gets) != 1 {
		t.Errorf("expected exactly the external-ref lookup, got %d gets", len(fc.Gets))
	}
}

func TestHandle_ForgeCommentEdited_Skipped_DocumentedDeferral(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkCommentEvent(forge.EventIssueCommentEdited, testForgeUser, "edited body")

	out, err := e.HandleForgeComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "identity mapping") {
		t.Errorf("Reason=%q does not mention identity mapping", out.Reason)
	}
	if len(fc.CommentCreates)+len(fc.CommentUpdates)+len(fc.CommentDeletes) != 0 {
		t.Errorf("edited path made unexpected comment writes")
	}
}

func TestHandle_ForgeCommentDeleted_Skipped_DocumentedDeferral(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkCommentEvent(forge.EventIssueCommentDeleted, testForgeUser, "")

	out, err := e.HandleForgeComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "identity mapping") {
		t.Errorf("Reason=%q does not mention identity mapping", out.Reason)
	}
	if len(fc.CommentDeletes) != 0 {
		t.Errorf("deleted path made unexpected DeleteComment calls")
	}
}

// mkPlaneCommentEvent builds a plane.Event for a comment.* delivery.
func mkPlaneCommentEvent(kind plane.EventKind) *plane.Event {
	return &plane.Event{
		Kind:       kind,
		DeliveryID: "plane-delivery-cmt",
		Action:     "create",
		Actor:      plane.Actor{ID: "actor-id", DisplayName: "Alice"},
		Comment: &plane.Comment{
			ID:          "cmt-from-plane",
			IssueID:     "wi-1234",
			Project:     testProjectID,
			CommentHTML: "hello from plane",
		},
	}
}

func TestHandle_PlaneCommentCreated_PostsToForge(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	ff := &fakeForgeClient{}
	e.ForgeClient = ff
	fc.GetIssueFunc = func(_ context.Context, projectID, issueID string) (*plane.WorkItem, error) {
		return &plane.WorkItem{
			ID:             issueID,
			Project:        projectID,
			ExternalSource: "forge:acme/widgets",
			ExternalID:     "7",
		}, nil
	}
	evt := mkPlaneCommentEvent(plane.EventCommentCreated)

	out, err := e.HandlePlaneComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("action=%v, want ActionCreated", out.Action)
	}
	if out.WorkItemID != "wi-1234" {
		t.Errorf("WorkItemID=%q", out.WorkItemID)
	}
	if out.CommentID == "" {
		t.Error("CommentID empty on plane→forge Created outcome")
	}
	if len(ff.CommentCreates) != 1 {
		t.Fatalf("want 1 forge CreateComment call, got %d", len(ff.CommentCreates))
	}
	cc := ff.CommentCreates[0]
	if cc.Owner != "acme" || cc.Repo != "widgets" || cc.IssueNumber != 7 {
		t.Errorf("forge target = (%q,%q,%d), want (acme,widgets,7)",
			cc.Owner, cc.Repo, cc.IssueNumber)
	}
	if !strings.Contains(cc.Req.Body, "<!-- pfb:src=plane,evt=plane-delivery-cmt -->") {
		t.Errorf("forge comment body missing marker: %q", cc.Req.Body)
	}
	if !strings.Contains(cc.Req.Body, "hello from plane") {
		t.Errorf("forge comment body missing source body: %q", cc.Req.Body)
	}
}

func TestHandle_PlaneCommentCreated_MalformedExternalRefSkips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	ff := &fakeForgeClient{}
	e.ForgeClient = ff
	fc.GetIssueFunc = func(_ context.Context, projectID, issueID string) (*plane.WorkItem, error) {
		// external_source missing the "forge:" prefix → parseExternalRef fails.
		return &plane.WorkItem{
			ID:             issueID,
			Project:        projectID,
			ExternalSource: "github:acme/widgets",
			ExternalID:     "1",
		}, nil
	}
	evt := mkPlaneCommentEvent(plane.EventCommentCreated)

	out, err := e.HandlePlaneComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "no forge mirror") {
		t.Errorf("Reason=%q does not mention missing forge mirror", out.Reason)
	}
	if len(ff.CommentCreates) != 0 {
		t.Errorf("malformed ref path called forge CreateComment")
	}
}

func TestHandle_PlaneCommentCreated_NoExternalIDSkips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	ff := &fakeForgeClient{}
	e.ForgeClient = ff
	fc.GetIssueFunc = func(_ context.Context, projectID, issueID string) (*plane.WorkItem, error) {
		return &plane.WorkItem{
			ID:             issueID,
			Project:        projectID,
			ExternalSource: "forge:acme/widgets",
			ExternalID:     "",
		}, nil
	}
	evt := mkPlaneCommentEvent(plane.EventCommentCreated)

	out, err := e.HandlePlaneComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if len(ff.CommentCreates) != 0 {
		t.Errorf("no-external-id path called forge CreateComment")
	}
}

func TestHandle_PlaneCommentEdited_Skipped_DocumentedDeferral(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	ff := &fakeForgeClient{}
	e.ForgeClient = ff
	evt := mkPlaneCommentEvent(plane.EventCommentUpdated)

	out, err := e.HandlePlaneComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "identity mapping") {
		t.Errorf("Reason=%q", out.Reason)
	}
	if len(ff.CommentUpdates) != 0 {
		t.Errorf("plane edit path called forge UpdateComment")
	}
}

func TestHandle_PlaneCommentDeleted_Skipped_DocumentedDeferral(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	ff := &fakeForgeClient{}
	e.ForgeClient = ff
	evt := mkPlaneCommentEvent(plane.EventCommentDeleted)

	out, err := e.HandlePlaneComment(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "identity mapping") {
		t.Errorf("Reason=%q", out.Reason)
	}
	if len(ff.CommentDeletes) != 0 {
		t.Errorf("plane delete path called forge DeleteComment")
	}
}

func TestHandle_PropagatesPlaneError(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	apiErr := &plane.APIError{StatusCode: 500, Method: "POST", Path: "/api/issues", Body: "boom"}
	fc.CreateIssueFunc = func(_ context.Context, _ string, _ plane.CreateIssueRequest) (*plane.WorkItem, error) {
		return nil, apiErr
	}
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	_, err := e.HandleForgeIssue(context.Background(), evt)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var got *plane.APIError
	if !errors.As(err, &got) {
		t.Fatalf("error not a *plane.APIError chain: %v", err)
	}
	if got.StatusCode != 500 {
		t.Errorf("StatusCode=%d, want 500", got.StatusCode)
	}
}

// TestHandle_IssueOpened_PropagatesLabels exercises the step-8 label
// resolver on the create path: an opened forge issue with N labels triggers
// one ListProjectLabels call, a CreateProjectLabel per missing name, and
// the resulting Plane UUIDs land on the CreateIssueRequest in input order.
func TestHandle_IssueOpened_PropagatesLabels(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		// Only "bug" already exists; "good first issue" must be auto-created.
		return []plane.Label{{ID: "lbl-bug", Name: "bug"}}, nil
	}
	fc.CreateProjectLabelFunc = func(_ context.Context, _ string, req plane.CreateLabelRequest) (*plane.Label, error) {
		return &plane.Label{ID: "lbl-created-" + req.Name, Name: req.Name}, nil
	}
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)
	evt.Issue.Labels = []forge.Label{
		{ID: 1, Name: "bug"},
		{ID: 2, Name: "good first issue"},
	}

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.Creates) != 1 {
		t.Fatalf("want 1 create, got %d", len(fc.Creates))
	}
	got := fc.Creates[0].Req.Labels
	want := []string{"lbl-bug", "lbl-created-good first issue"}
	if len(got) != len(want) {
		t.Fatalf("Labels=%v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Labels[%d]=%q, want %q", i, got[i], w)
		}
	}
	if len(fc.ListProjectLabelsCalls) != 1 {
		t.Errorf("ListProjectLabels calls=%d, want 1", len(fc.ListProjectLabelsCalls))
	}
	if len(fc.CreateProjectLabelCalls) != 1 {
		t.Errorf("CreateProjectLabel calls=%d, want 1", len(fc.CreateProjectLabelCalls))
	} else if fc.CreateProjectLabelCalls[0].Req.Name != "good first issue" {
		t.Errorf("created label name=%q", fc.CreateProjectLabelCalls[0].Req.Name)
	}
}

// TestHandle_IssueOpened_LabelsAlreadyExist confirms the no-create fast
// path: every forge label name already exists on the Plane side, so the
// engine only calls ListProjectLabels and the UUIDs come straight from
// the cached list.
func TestHandle_IssueOpened_LabelsAlreadyExist(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{
			{ID: "lbl-bug", Name: "bug"},
			{ID: "lbl-help", Name: "help wanted"},
		}, nil
	}
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)
	evt.Issue.Labels = []forge.Label{
		{Name: "bug"},
		{Name: "help wanted"},
	}

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.CreateProjectLabelCalls) != 0 {
		t.Errorf("CreateProjectLabel should not be called; got %d", len(fc.CreateProjectLabelCalls))
	}
	got := fc.Creates[0].Req.Labels
	want := []string{"lbl-bug", "lbl-help"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Labels[%d]=%q, want %q", i, got[i], w)
		}
	}
}

// TestHandle_IssueOpened_AppliesOpenStateMap is the step-8 state-coverage
// guard: a forge issue.opened event with link.StateMap["open"]="Todo" must
// land the Todo state UUID on the CreateIssueRequest. This was already
// implemented in step 6 for the create path; the test pins it so the
// behaviour can't silently regress.
func TestHandle_IssueOpened_AppliesOpenStateMap(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc.Creates[0].Req.StateID != "state-todo" {
		t.Errorf("StateID=%q, want state-todo", fc.Creates[0].Req.StateID)
	}
}

// TestHandle_IssueOpened_NoOpenStateMap_LeavesStateUnset confirms the
// "we don't invent transitions" rule on the create path: if the link has
// no "open" entry in StateMap, the engine leaves CreateIssueRequest.StateID
// empty so Plane uses its project default.
func TestHandle_IssueOpened_NoOpenStateMap_LeavesStateUnset(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	e.Links[0].StateMap = map[string]string{"closed": "Done"}
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc.Creates[0].Req.StateID != "" {
		t.Errorf("StateID=%q, want empty", fc.Creates[0].Req.StateID)
	}
	// We also assert ListProjectStates was NOT called: ResolveStateID
	// short-circuits on a missing map entry before hitting the cache.
	if len(fc.Lists) != 0 {
		t.Errorf("unexpected ListProjectStates calls: %d", len(fc.Lists))
	}
}

// TestHandle_IssueEdited_DoesNotChangeState pins the documented decision:
// forge fires issues.edited for any property change, and the payload
// doesn't reliably signal whether the state changed. We rely on
// IssueClosed / IssueReopened for explicit transitions and leave Plane's
// state alone on plain edits, even when link.StateMap has an "open" entry.
func TestHandle_IssueEdited_DoesNotChangeState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	evt := mkIssueEvent(forge.EventIssueEdited, testForgeUser)

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 update, got %d", len(fc.Updates))
	}
	if fc.Updates[0].Req.StateID != nil {
		t.Errorf("edit must not touch StateID; got %v", *fc.Updates[0].Req.StateID)
	}
}

// mkPREvent builds a forge.Event for a pull_request.* delivery. The
// default link in newTestEngine has no PR automation configured, so each
// PR test enables it explicitly (matches the opt-in design — operators
// without ProjectIdentifier + PRStateMap get plain comment/issue mirror
// only).
func mkPREvent(kind forge.EventKind, title, body, headRef string, merged bool) *forge.Event {
	return &forge.Event{
		Kind:       kind,
		DeliveryID: "delivery-pr",
		Repo: forge.Repository{
			ID:       100,
			FullName: testRepo,
			HTMLURL:  "https://forge.example.com/acme/widgets",
		},
		Sender: forge.User{
			Login:   testForgeUser,
			HTMLURL: "https://forge.example.com/" + testForgeUser,
		},
		PullRequest: &forge.PullRequest{
			ID:     900,
			Number: 12,
			Title:  title,
			Body:   body,
			State:  "open",
			Merged: merged,
			Head:   forge.PRBranch{Ref: headRef, SHA: "deadbeef"},
			Base:   forge.PRBranch{Ref: "main", SHA: "cafef00d"},
			User: forge.User{
				Login:   testForgeUser,
				HTMLURL: "https://forge.example.com/" + testForgeUser,
			},
		},
	}
}

// enablePRAutomation configures the test engine's link with an
// identifier + a default PRStateMap covering all four supported keys.
// Returns the configured states list the fake should serve so tests
// don't have to keep these in sync manually.
func enablePRAutomation(e *Engine) {
	e.Links[0].ProjectIdentifier = "PFB"
	e.Links[0].PRStateMap = map[string]string{
		"opened": "In Progress",
		"merged": "Done",
		"closed": "Cancelled",
	}
}

// prStates returns the project state list used by the PR tests: a
// superset covering the three names enablePRAutomation references.
func prStates() []plane.State {
	return []plane.State{
		{ID: "state-todo", Name: "Todo", Group: "unstarted"},
		{ID: "state-inprogress", Name: "In Progress", Group: "started"},
		{ID: "state-done", Name: "Done", Group: "completed"},
		{ID: "state-cancelled", Name: "Cancelled", Group: "cancelled"},
	}
}

func TestHandle_PullRequestOpened_UpdatesState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, seq int) (*plane.WorkItem, error) {
		if seq != 42 {
			t.Errorf("seq=%d, want 42", seq)
		}
		return &plane.WorkItem{ID: "wi-pr-42", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-42] fix login", "", "fix-login", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v, want ActionUpdated", out.Action)
	}
	if out.WorkItemID != "wi-pr-42" {
		t.Errorf("WorkItemID=%q", out.WorkItemID)
	}
	if len(fc.Updates) != 1 {
		t.Fatalf("want 1 UpdateIssue call, got %d", len(fc.Updates))
	}
	upd := fc.Updates[0]
	if upd.IssueID != "wi-pr-42" {
		t.Errorf("update against %q", upd.IssueID)
	}
	if upd.Req.StateID == nil || *upd.Req.StateID != "state-inprogress" {
		t.Errorf("StateID=%v, want state-inprogress", upd.Req.StateID)
	}
	if upd.Req.Name != nil || upd.Req.DescriptionHTML != nil || upd.Req.Labels != nil {
		t.Errorf("PR update should only touch StateID; got Name=%v Desc=%v Labels=%v",
			upd.Req.Name, upd.Req.DescriptionHTML, upd.Req.Labels)
	}
}

func TestHandle_PullRequestMerged_UsesMergedState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, _ int) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-merged", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestClosed, "[PFB-7] merge me", "", "feature/x", true)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v", out.Action)
	}
	if fc.Updates[0].Req.StateID == nil || *fc.Updates[0].Req.StateID != "state-done" {
		t.Errorf("StateID=%v, want state-done (merged → Done)", fc.Updates[0].Req.StateID)
	}
}

func TestHandle_PullRequestClosedUnmerged_UsesClosedState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, _ int) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-closed", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestClosed, "[PFB-7] abandoned", "", "feature/x", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v", out.Action)
	}
	if fc.Updates[0].Req.StateID == nil || *fc.Updates[0].Req.StateID != "state-cancelled" {
		t.Errorf("StateID=%v, want state-cancelled (closed-unmerged → Cancelled)", fc.Updates[0].Req.StateID)
	}
}

func TestHandle_PullRequestReopened_UsesOpenedState(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, _ int) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-reopened", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestReopened, "[PFB-7] back from the dead", "", "feature/x", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUpdated {
		t.Fatalf("action=%v", out.Action)
	}
	if fc.Updates[0].Req.StateID == nil || *fc.Updates[0].Req.StateID != "state-inprogress" {
		t.Errorf("StateID=%v, want state-inprogress (reopened → In Progress)", fc.Updates[0].Req.StateID)
	}
}

func TestHandle_PullRequest_NoLink_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-1] x", "", "pfb-1-x", false)
	evt.Repo.FullName = "other/repo"

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if out.Reason != "no link configured for repo" {
		t.Errorf("Reason=%q", out.Reason)
	}
	if len(fc.Updates)+len(fc.GetsBySeq)+len(fc.Lists) != 0 {
		t.Errorf("no-link skip made API calls")
	}
}

func TestHandle_PullRequest_NoAutomationConfigured_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// Default link has no ProjectIdentifier or PRStateMap — PR
	// automation is opt-in.
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-1] x", "", "pfb-1-x", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if !strings.Contains(out.Reason, "no PR automation configured") {
		t.Errorf("Reason=%q does not name the missing automation", out.Reason)
	}
	if out.Link == nil {
		t.Error("Link nil — automation skip still matched a link")
	}
	if len(fc.Updates)+len(fc.GetsBySeq)+len(fc.Lists) != 0 {
		t.Errorf("no-automation skip made API calls")
	}
}

func TestHandle_PullRequest_NoRef_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	evt := mkPREvent(forge.EventPullRequestOpened, "unrelated title", "no body refs", "feature/no-ref", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if !strings.Contains(out.Reason, "no [PFB-N] ref found") {
		t.Errorf("Reason=%q does not point at the missing ref", out.Reason)
	}
	if len(fc.GetsBySeq) != 0 {
		t.Errorf("no-ref skip looked up by sequence id")
	}
}

func TestHandle_PullRequest_WorkItemNotFound_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	// Default GetIssueBySequenceID returns ErrNotFound.
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-404] missing", "", "pfb-404", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if !strings.Contains(out.Reason, "does not exist on the configured Plane project") {
		t.Errorf("Reason=%q does not name the missing work item", out.Reason)
	}
	if len(fc.GetsBySeq) != 1 {
		t.Errorf("want 1 GetIssueBySequenceID call, got %d", len(fc.GetsBySeq))
	}
	if len(fc.Updates) != 0 {
		t.Errorf("work-item-not-found path called UpdateIssue")
	}
}

func TestHandle_PullRequest_NoActionMapping_Skips(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	// Drop "merged" so a merge event has no mapping.
	delete(e.Links[0].PRStateMap, "merged")
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, _ int) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-x", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestClosed, "[PFB-9] x", "", "x", true)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if !strings.Contains(out.Reason, `action "merged"`) {
		t.Errorf("Reason=%q does not name the missing action", out.Reason)
	}
	if len(fc.Updates) != 0 {
		t.Errorf("no-mapping path called UpdateIssue")
	}
}

func TestHandle_PullRequest_StateNameNotInProject_Errors(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	// PRStateMap names a state that doesn't exist on the project.
	e.Links[0].PRStateMap["opened"] = "Nonexistent State"
	fc.ListProjectStatesFunc = func(_ context.Context, _ string) ([]plane.State, error) {
		return prStates(), nil
	}
	fc.GetIssueBySequenceIDFunc = func(_ context.Context, projectID string, _ int) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-x", Project: projectID}, nil
	}
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-1] x", "", "pfb-1", false)

	_, err := e.HandleForgePullRequest(context.Background(), evt)
	if err == nil {
		t.Fatal("expected error for misconfigured state name")
	}
	if !strings.Contains(err.Error(), "Nonexistent State") {
		t.Errorf("error=%v does not name the offending state", err)
	}
	if len(fc.Updates) != 0 {
		t.Errorf("config-error path called UpdateIssue")
	}
}

func TestHandle_PullRequestReview_Skipped_Deferred(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	evt := mkPREvent(forge.EventPullRequestReview, "[PFB-1] x", "", "pfb-1", false)
	evt.Review = &forge.Review{ID: 1, Type: "pull_request_review_approved"}

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v", out.Action)
	}
	if !strings.Contains(out.Reason, "review event handling deferred") {
		t.Errorf("Reason=%q does not name the deferral", out.Reason)
	}
	if len(fc.Updates)+len(fc.GetsBySeq) != 0 {
		t.Errorf("review path made API calls")
	}
}

func TestHandle_PullRequestEdited_Skipped(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	enablePRAutomation(e)
	evt := mkPREvent(forge.EventPullRequestEdited, "[PFB-1] x", "", "pfb-1", false)

	out, err := e.HandleForgePullRequest(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action=%v, want ActionSkipped (edits shouldn't move state)", out.Action)
	}
	if !strings.Contains(out.Reason, "does not map to a state transition") {
		t.Errorf("Reason=%q does not name the missing action mapping", out.Reason)
	}
	if len(fc.Updates) != 0 {
		t.Errorf("edited path called UpdateIssue")
	}
}

// TestHandle_IssueOpened_AssigneeFromEmailMatch is the v2 identity-
// resolution coverage on the forge → plane path. The sender has no
// entry in the static config map but does have a matching Plane
// member by email; the resolver returns that member's UUID and the
// CreateIssueRequest.Assignees lands it.
func TestHandle_IssueOpened_AssigneeFromEmailMatch(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.ListWorkspaceMembersFunc = func(_ context.Context) ([]plane.Member, error) {
		return []plane.Member{
			{ID: "plane-uuid-stranger", Email: "stranger@example.com"},
		}, nil
	}
	evt := mkIssueEvent(forge.EventIssueOpened, "stranger")
	evt.Sender.Email = "stranger@example.com"

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.Creates) != 1 {
		t.Fatalf("want 1 create, got %d", len(fc.Creates))
	}
	got := fc.Creates[0].Req.Assignees
	if len(got) != 1 || got[0] != "plane-uuid-stranger" {
		t.Errorf("Assignees=%v, want [plane-uuid-stranger]", got)
	}
}

// TestHandle_IssueOpened_NoAssigneeWhenNoMatch covers the third arm
// of the v2 resolver: no static config entry, no email match. The
// request must omit Assignees so Plane leaves the work item
// unassigned — the bridge bot is the API caller but doesn't impose
// itself as assignee.
func TestHandle_IssueOpened_NoAssigneeWhenNoMatch(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	// Default ListWorkspaceMembers returns nil — no email match.
	evt := mkIssueEvent(forge.EventIssueOpened, "stranger")
	evt.Sender.Email = "stranger@example.com"

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.Creates) != 1 {
		t.Fatalf("want 1 create, got %d", len(fc.Creates))
	}
	if len(fc.Creates[0].Req.Assignees) != 0 {
		t.Errorf("Assignees=%v, want empty (no static + no email match)", fc.Creates[0].Req.Assignees)
	}
}

// TestHandle_IssueEdited_PropagatesLabelChanges confirms forge label
// changes on an existing issue propagate to Plane via the edit update.
// forge fires issues.edited for label add/remove events, so the edit
// branch is the only place these changes reach the engine.
func TestHandle_IssueEdited_PropagatesLabelChanges(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	fc.GetIssueByExternalRefFunc = func(_ context.Context, projectID, _, _ string) (*plane.WorkItem, error) {
		return &plane.WorkItem{ID: "wi-existing", Project: projectID}, nil
	}
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{{ID: "lbl-bug", Name: "bug"}}, nil
	}
	evt := mkIssueEvent(forge.EventIssueEdited, testForgeUser)
	evt.Issue.Labels = []forge.Label{{Name: "bug"}}

	if _, err := e.HandleForgeIssue(context.Background(), evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := fc.Updates[0].Req.Labels
	if len(got) != 1 || got[0] != "lbl-bug" {
		t.Errorf("Labels=%v, want [lbl-bug]", got)
	}
}
