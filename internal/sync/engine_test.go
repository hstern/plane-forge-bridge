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
	return NewEngine(fc, cfg, testLogger()), fc
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
	if req.ExternalID != "42" {
		t.Errorf("ExternalID=%q, want 42", req.ExternalID)
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

func TestHandle_PropagatesPlaneError(t *testing.T) {
	t.Parallel()
	e, fc := newTestEngine(t)
	apiErr := &plane.APIError{StatusCode: 500, Method: "POST", URL: "/api/issues", Body: "boom"}
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
