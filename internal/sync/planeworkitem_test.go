package sync

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/idemp"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// newPlaneWorkItemTestEngine wires an Engine for plane → forge tests.
// Both clients are fakes so assertions can read recorded calls; the
// fakeForgeClient implements ForgeIssueWriter via its CreateIssue /
// UpdateIssue methods, so the type assertion inside the handler
// succeeds by default.
func newPlaneWorkItemTestEngine(t *testing.T) (*Engine, *fakeClient, *fakeForgeClient) {
	t.Helper()
	pc := &fakeClient{}
	fc := &fakeForgeClient{}
	cfg := &mapping.Resolved{
		Links: []mapping.Link{{
			ForgeRepo:      testRepo,
			PlaneProjectID: testProjectID,
			StateMap: map[string]string{
				"open":   "Todo",
				"closed": "Done",
			},
		}},
		Users: map[string]string{},
	}
	cfg.BridgeBot.ForgeUsername = "pfb-bot"
	cfg.BridgeBot.PlaneMemberID = "plane-uuid-bot"
	return NewEngine(pc, fc, cfg, testLogger()), pc, fc
}

// mkPlaneWorkItemEvent builds a plane.Event for a work_item.* delivery.
func mkPlaneWorkItemEvent(kind plane.EventKind) *plane.Event {
	return &plane.Event{
		Kind:        kind,
		DeliveryID:  "plane-delivery-001",
		WorkspaceID: "ws-001",
		Actor: plane.Actor{
			ID:          "actor-1",
			DisplayName: "Bob Q. Reporter",
		},
		WorkItem: &plane.WorkItem{
			ID:          "wi-uuid-123",
			Name:        "Files vanish on Tuesdays",
			Description: "When the clock strikes 13:37, files vanish.",
			Project:     testProjectID,
			Workspace:   "ws-001",
			Labels: []plane.LabelRef{
				{ID: "label-uuid-bug", Name: "bug"},
				{ID: "label-uuid-help", Name: "help wanted"},
			},
		},
	}
}

func TestHandlePlaneWorkItem_CreatedCreatesForgeIssue(t *testing.T) {
	t.Parallel()
	e, pc, fc := newPlaneWorkItemTestEngine(t)

	// Wire the plane label cache so UUID → name resolves cleanly.
	pc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{
			{ID: "label-uuid-bug", Name: "bug"},
			{ID: "label-uuid-help", Name: "help wanted"},
		}, nil
	}
	// Forge side: one label exists, the other is auto-created.
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		return []forge.Label{{ID: 11, Name: "bug"}}, nil
	}
	fc.CreateRepoLabelFunc = func(_ context.Context, _, _ string, req forge.CreateLabelRequest) (*forge.Label, error) {
		return &forge.Label{ID: 22, Name: req.Name}, nil
	}

	evt := mkPlaneWorkItemEvent(plane.EventWorkItemCreated)
	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("Action=%v, want ActionCreated; reason=%q", out.Action, out.Reason)
	}
	if out.WorkItemID != "131313" {
		// 131313 is the fake's default forge.Issue.Number.
		t.Errorf("WorkItemID=%q, want %q (stringified forge issue number)", out.WorkItemID, "131313")
	}
	if out.CommentID != "" {
		t.Errorf("CommentID=%q on issue create outcome", out.CommentID)
	}
	if len(fc.IssueCreates) != 1 {
		t.Fatalf("IssueCreates=%d, want 1", len(fc.IssueCreates))
	}
	call := fc.IssueCreates[0]
	if call.Owner != "acme" || call.Repo != "widgets" {
		t.Errorf("owner/repo=%s/%s, want acme/widgets", call.Owner, call.Repo)
	}
	if call.Req.Title != evt.WorkItem.Name {
		t.Errorf("Title=%q, want %q", call.Req.Title, evt.WorkItem.Name)
	}
	// Body must contain BOTH markers.
	if !strings.Contains(call.Req.Body, "<!-- pfb:src=plane,evt=plane-delivery-001 -->") {
		t.Errorf("body missing loop-break marker: %q", call.Req.Body)
	}
	wantRef := "<!-- pfb:plane-workitem=ws-001/" + testProjectID + "/wi-uuid-123 -->"
	if !strings.Contains(call.Req.Body, wantRef) {
		t.Errorf("body missing plane-ref marker; want %q, got %q", wantRef, call.Req.Body)
	}
	// Labels: bug (11) existed, help wanted (22) auto-created.
	wantIDs := []int64{11, 22}
	if len(call.Req.Labels) != len(wantIDs) {
		t.Fatalf("Labels=%v, want %v", call.Req.Labels, wantIDs)
	}
	for i, want := range wantIDs {
		if call.Req.Labels[i] != want {
			t.Errorf("Labels[%d]=%d, want %d", i, call.Req.Labels[i], want)
		}
	}
}

// TestHandlePlaneWorkItem_AssigneeResolvedFromForgeEmail is the v2
// identity-resolver coverage on the plane → forge path. The actor's
// plane member UUID is matched to a workspace member with email, that
// email is searched on the forge, and the matching forge login lands
// on the CreateIssueRequest.Assignees.
func TestHandlePlaneWorkItem_AssigneeResolvedFromForgeEmail(t *testing.T) {
	t.Parallel()
	e, pc, fc := newPlaneWorkItemTestEngine(t)

	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]plane.Member, error) {
		return []plane.Member{
			{ID: "actor-1", Email: "bob@example.com"},
		}, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, query string) ([]forge.User, error) {
		if query != "bob@example.com" {
			t.Errorf("SearchUsers query=%q, want bob@example.com", query)
		}
		return []forge.User{
			{Login: "bob", Email: "bob@example.com"},
		}, nil
	}

	evt := mkPlaneWorkItemEvent(plane.EventWorkItemCreated)
	// The fixture's evt.Actor.ID is "actor-1"; the workspace member list
	// above maps that UUID to bob@example.com.
	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("Action=%v, want ActionCreated; reason=%q", out.Action, out.Reason)
	}
	if len(fc.IssueCreates) != 1 {
		t.Fatalf("IssueCreates=%d, want 1", len(fc.IssueCreates))
	}
	got := fc.IssueCreates[0].Req.Assignees
	if len(got) != 1 || got[0] != "bob" {
		t.Errorf("Assignees=%v, want [bob]", got)
	}
}

// TestHandlePlaneWorkItem_CreatedEchoSkipsByExternalSource is the
// regression test for PFB-27. The bridge stamps external_source on
// every forge → plane create; Plane echoes that back on its own
// work_item.created webhook. The HTML-comment marker the bridge also
// stamps would normally catch this, but Plane CE v1.3.1 strips HTML
// comments out of description_html during sanitization (preserves
// them in comment_html — verified against plane.stern.ca). The
// external_source check is the durable defence.
//
// This test fails if anyone removes the external_source gate.
func TestHandlePlaneWorkItem_CreatedEchoSkipsByExternalSource(t *testing.T) {
	t.Parallel()
	e, _, fc := newPlaneWorkItemTestEngine(t)

	evt := mkPlaneWorkItemEvent(plane.EventWorkItemCreated)
	// The work item carries the bridge's external_source — this is our own
	// echo. The marker on the description is gone (Plane stripped it during
	// sanitization), so the external_source check is what saves us.
	evt.WorkItem.ExternalSource = "forge:acme/widgets"
	evt.WorkItem.ExternalID = "42"

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v reason=%q, want ActionSkipped", out.Action, out.Reason)
	}
	if out.Reason != reasonPlaneCreatedEchoExternalSource {
		t.Errorf("Reason=%q, want %q", out.Reason, reasonPlaneCreatedEchoExternalSource)
	}
	if out.Link == nil {
		t.Errorf("Link should be set on the echo skip — the project link was matched")
	}
	if len(fc.IssueCreates) != 0 {
		t.Errorf("IssueCreates=%d on echo path — must be 0 (duplicate avoidance)",
			len(fc.IssueCreates))
	}
}

// TestHandlePlaneWorkItem_CreatedNonBridgeExternalSourceProceeds
// pins that external_source pointing at a non-bridge system (e.g.
// "github:foo/bar" if an operator imports issues from GitHub) does
// NOT trigger the echo skip — only the "forge:" prefix is the bridge
// signal. Without this guard the bridge would silently drop legitimate
// non-bridge work items.
func TestHandlePlaneWorkItem_CreatedNonBridgeExternalSourceProceeds(t *testing.T) {
	t.Parallel()
	e, pc, fc := newPlaneWorkItemTestEngine(t)
	pc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{
			{ID: "label-uuid-bug", Name: "bug"},
			{ID: "label-uuid-help", Name: "help wanted"},
		}, nil
	}
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		return []forge.Label{{ID: 11, Name: "bug"}, {ID: 22, Name: "help wanted"}}, nil
	}

	evt := mkPlaneWorkItemEvent(plane.EventWorkItemCreated)
	evt.WorkItem.ExternalSource = "github:acme/widgets"
	evt.WorkItem.ExternalID = "99"

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionCreated {
		t.Fatalf("Action=%v reason=%q, want ActionCreated (non-bridge source should mirror)",
			out.Action, out.Reason)
	}
	if len(fc.IssueCreates) != 1 {
		t.Errorf("IssueCreates=%d, want 1", len(fc.IssueCreates))
	}
}

func TestHandlePlaneWorkItem_NoLinkSkips(t *testing.T) {
	t.Parallel()
	e, _, fc := newPlaneWorkItemTestEngine(t)
	evt := mkPlaneWorkItemEvent(plane.EventWorkItemCreated)
	evt.WorkItem.Project = "some-other-project"

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v, want ActionSkipped", out.Action)
	}
	if !strings.Contains(out.Reason, "no link configured") {
		t.Errorf("Reason=%q", out.Reason)
	}
	if len(fc.IssueCreates) != 0 {
		t.Errorf("IssueCreates=%d on skip path", len(fc.IssueCreates))
	}
}

func TestHandlePlaneWorkItem_UpdatedSkipsWithDeferralReason(t *testing.T) {
	t.Parallel()
	e, _, fc := newPlaneWorkItemTestEngine(t)
	evt := mkPlaneWorkItemEvent(plane.EventWorkItemUpdated)

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != reasonPlaneUpdateDeferred {
		t.Errorf("Reason=%q, want %q", out.Reason, reasonPlaneUpdateDeferred)
	}
	if !strings.Contains(out.Reason, "SearchIssues") {
		t.Errorf("Reason should mention SearchIssues deferral: %q", out.Reason)
	}
	if out.Link == nil {
		t.Errorf("Link should be set on the skip — link was matched")
	}
	if len(fc.IssueCreates)+len(fc.IssueUpdates) != 0 {
		t.Errorf("expected zero forge writes, got creates=%d updates=%d",
			len(fc.IssueCreates), len(fc.IssueUpdates))
	}
}

func TestHandlePlaneWorkItem_DeletedSkipsByDesign(t *testing.T) {
	t.Parallel()
	e, _, fc := newPlaneWorkItemTestEngine(t)
	evt := mkPlaneWorkItemEvent(plane.EventWorkItemDeleted)

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != reasonPlaneDeleteNotMirrored {
		t.Errorf("Reason=%q, want %q", out.Reason, reasonPlaneDeleteNotMirrored)
	}
	if len(fc.IssueCreates)+len(fc.IssueUpdates) != 0 {
		t.Errorf("expected zero forge writes, got creates=%d updates=%d",
			len(fc.IssueCreates), len(fc.IssueUpdates))
	}
}

// TestHandlePlaneWorkItem_DeletedNilWorkItem covers the parser's
// "delete envelope without data" case where evt.WorkItem is nil — the
// handler must still return the by-design skip, not crash.
func TestHandlePlaneWorkItem_DeletedNilWorkItem(t *testing.T) {
	t.Parallel()
	e, _, _ := newPlaneWorkItemTestEngine(t)
	evt := &plane.Event{
		Kind:       plane.EventWorkItemDeleted,
		DeliveryID: "delete-nil",
	}
	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped || out.Reason != reasonPlaneDeleteNotMirrored {
		t.Errorf("got %+v", out)
	}
}

func TestHandlePlaneWorkItem_PropagatesForgeError(t *testing.T) {
	t.Parallel()
	e, _, fc := newPlaneWorkItemTestEngine(t)
	apiErr := &forge.APIError{
		StatusCode: http.StatusUnprocessableEntity,
		Method:     http.MethodPost,
		Path:       "/api/v1/repos/acme/widgets/issues",
		Body:       `{"message":"already exists"}`,
	}
	fc.CreateIssueFunc = func(_ context.Context, _, _ string, _ forge.CreateIssueRequest) (*forge.Issue, error) {
		return nil, apiErr
	}

	_, err := e.HandlePlaneWorkItem(context.Background(), mkPlaneWorkItemEvent(plane.EventWorkItemCreated))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var got *forge.APIError
	if !errors.As(err, &got) {
		t.Fatalf("error chain missing *forge.APIError; err=%v", err)
	}
	if got.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode=%d", got.StatusCode)
	}
}

// TestHandlePlaneWorkItem_UnsupportedKindSkips covers comment-shaped
// events (or other future kinds) dispatched to the work-item handler
// by mistake — they must skip rather than panic on nil WorkItem.
func TestHandlePlaneWorkItem_UnsupportedKindSkips(t *testing.T) {
	t.Parallel()
	e, _, _ := newPlaneWorkItemTestEngine(t)
	evt := mkPlaneWorkItemEvent(plane.EventCommentCreated)

	out, err := e.HandlePlaneWorkItem(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != "unsupported event" {
		t.Errorf("Reason=%q", out.Reason)
	}
}

// TestHandlePlaneWorkItem_NilEventErrors guards the panic safety
// invariant — the dispatcher passes events; nil is a programmer error,
// not an operator-facing skip.
func TestHandlePlaneWorkItem_NilEventErrors(t *testing.T) {
	t.Parallel()
	e, _, _ := newPlaneWorkItemTestEngine(t)
	if _, err := e.HandlePlaneWorkItem(context.Background(), nil); err == nil {
		t.Fatal("want error on nil event")
	}
}

// TestHandlePlaneWorkItem_NoForgeClientErrors guards the wiring
// invariant — a deployment that wired NewEngine without a forge client
// can't translate plane events; surface that as an error rather than a
// silent skip.
func TestHandlePlaneWorkItem_NoForgeClientErrors(t *testing.T) {
	t.Parallel()
	pc := &fakeClient{}
	cfg := &mapping.Resolved{
		Links: []mapping.Link{{ForgeRepo: testRepo, PlaneProjectID: testProjectID}},
	}
	e := NewEngine(pc, nil, cfg, testLogger())
	if _, err := e.HandlePlaneWorkItem(context.Background(), mkPlaneWorkItemEvent(plane.EventWorkItemCreated)); err == nil {
		t.Fatal("want error when ForgeClient is unset")
	}
}

// TestHandlePlaneWorkItem_ForgeClientNotIssueWriter exercises the
// rollout path where the wired ForgeClient implements the v1 surface
// (issues read + comments + labels) but not the step-10 issue write
// methods. The handler must skip with a clear reason instead of
// crashing the type assertion.
func TestHandlePlaneWorkItem_ForgeClientNotIssueWriter(t *testing.T) {
	t.Parallel()
	pc := &fakeClient{}
	cfg := &mapping.Resolved{
		Links: []mapping.Link{{ForgeRepo: testRepo, PlaneProjectID: testProjectID}},
	}
	e := NewEngine(pc, &readOnlyForgeClient{}, cfg, testLogger())
	out, err := e.HandlePlaneWorkItem(context.Background(), mkPlaneWorkItemEvent(plane.EventWorkItemCreated))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionSkipped {
		t.Fatalf("Action=%v, want ActionSkipped", out.Action)
	}
	if out.Reason != reasonPlaneIssueWriterMissing {
		t.Errorf("Reason=%q, want %q", out.Reason, reasonPlaneIssueWriterMissing)
	}
}

// readOnlyForgeClient satisfies ForgeClient but NOT ForgeIssueWriter.
// Lets the rollout-stagger test exercise the type-assertion skip path.
type readOnlyForgeClient struct{}

func (readOnlyForgeClient) GetIssue(context.Context, string, string, int64) (*forge.Issue, error) {
	return nil, forge.ErrNotFound
}
func (readOnlyForgeClient) ListRepoLabels(context.Context, string, string) ([]forge.Label, error) {
	return nil, nil
}
func (readOnlyForgeClient) CreateRepoLabel(context.Context, string, string, forge.CreateLabelRequest) (*forge.Label, error) {
	return nil, nil
}
func (readOnlyForgeClient) CreateComment(context.Context, string, string, int64, forge.CreateCommentRequest) (*forge.Comment, error) {
	return nil, nil
}
func (readOnlyForgeClient) UpdateComment(context.Context, string, string, int64, forge.UpdateCommentRequest) (*forge.Comment, error) {
	return nil, nil
}
func (readOnlyForgeClient) DeleteComment(context.Context, string, string, int64) error {
	return nil
}
func (readOnlyForgeClient) SearchUsers(context.Context, string) ([]forge.User, error) {
	return nil, nil
}

// --- PlaneRefMarker round-trip ---

func TestRenderPlaneRefMarker_RoundTrips(t *testing.T) {
	t.Parallel()
	cases := []PlaneRefMarker{
		{WorkspaceID: "ws-1", ProjectID: "proj-1", WorkItemID: "wi-1"},
		{WorkspaceID: "01HXYZ", ProjectID: "abc-def-123", WorkItemID: "u_v_w"},
	}
	for _, m := range cases {
		s := RenderPlaneRefMarker(m)
		got, ok := ExtractPlaneRefMarker(s)
		if !ok {
			t.Fatalf("Extract failed on %q", s)
		}
		if got != m {
			t.Errorf("round trip mismatch: in=%+v, out=%+v, str=%q", m, got, s)
		}
	}
}

func TestExtractPlaneRefMarker_NoMarker_Empty(t *testing.T) {
	t.Parallel()
	_, ok := ExtractPlaneRefMarker("just a normal body, no markers in sight")
	if ok {
		t.Error("ok=true on bodyless input")
	}
}

func TestExtractPlaneRefMarker_Malformed_ReturnsFalse(t *testing.T) {
	t.Parallel()
	bad := []string{
		"<!-- pfb:plane-workitem=ws/proj -->",      // two segments, want three
		"<!-- pfb:plane-workitem= -->",             // empty
		"<!-- pfb:plane-workitem=ws/proj/wi/x -->", // four segments
		"<!-- pfb:plane-workitem=ws proj wi -->",   // spaces, not slashes
	}
	for _, b := range bad {
		if _, ok := ExtractPlaneRefMarker(b); ok {
			t.Errorf("ok=true on malformed %q", b)
		}
	}
}

// TestRenderPlaneWorkItemDescription_MarkersOrdered ensures the
// loop-break marker is the LAST token in the body, so idemp.Extract
// (which anchors on end-of-body) finds it on the round trip.
func TestRenderPlaneWorkItemDescription_MarkersOrdered(t *testing.T) {
	t.Parallel()
	body := RenderPlaneWorkItemDescription(
		"original body",
		"Some Actor",
		"delivery-xyz",
		PlaneRefMarker{WorkspaceID: "ws", ProjectID: "proj", WorkItemID: "wi"},
	)
	// idemp.Extract anchors on end-of-body — must still succeed.
	m, ok := idemp.Extract(body)
	if !ok {
		t.Fatalf("idemp.Extract failed on rendered body: %q", body)
	}
	if m.Source != idemp.SourcePlane || m.EventID != "delivery-xyz" {
		t.Errorf("marker mismatch: %+v", m)
	}
	// PlaneRefMarker comes before the loop-break marker.
	planeRefIdx := strings.Index(body, "pfb:plane-workitem=")
	loopBreakIdx := strings.Index(body, "pfb:src=plane")
	if planeRefIdx < 0 || loopBreakIdx < 0 {
		t.Fatalf("missing marker indices: planeRefIdx=%d loopBreakIdx=%d", planeRefIdx, loopBreakIdx)
	}
	if planeRefIdx >= loopBreakIdx {
		t.Errorf("plane-ref marker must precede loop-break marker; got planeRefIdx=%d loopBreakIdx=%d",
			planeRefIdx, loopBreakIdx)
	}
}

// --- resolveForgeLabels coverage ---

// newForgeLabelTestEngine wires an Engine with only the ForgeClient
// dependency the resolver touches.
func newForgeLabelTestEngine(t *testing.T) (*Engine, *fakeForgeClient) {
	t.Helper()
	fc := &fakeForgeClient{}
	return &Engine{ForgeClient: fc, Log: testLogger()}, fc
}

func TestResolveForgeLabels_AllExist(t *testing.T) {
	t.Parallel()
	e, fc := newForgeLabelTestEngine(t)
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		return []forge.Label{
			{ID: 1, Name: "bug"},
			{ID: 2, Name: "help wanted"},
		}, nil
	}
	got, err := e.resolveForgeLabels(context.Background(), "acme", "widgets", []string{"bug", "help wanted"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := []int64{1, 2}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d]=%d want %d", i, got[i], w)
		}
	}
	if len(fc.LabelCreates) != 0 {
		t.Errorf("CreateRepoLabel must not run; got %d", len(fc.LabelCreates))
	}
}

func TestResolveForgeLabels_AutoCreatesMissing(t *testing.T) {
	t.Parallel()
	e, fc := newForgeLabelTestEngine(t)
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		return []forge.Label{{ID: 1, Name: "bug"}}, nil
	}
	fc.CreateRepoLabelFunc = func(_ context.Context, _, _ string, req forge.CreateLabelRequest) (*forge.Label, error) {
		if req.Color == "" {
			t.Errorf("auto-create must supply a color; got empty for %q", req.Name)
		}
		return &forge.Label{ID: 100 + int64(len(req.Name)), Name: req.Name}, nil
	}

	got, err := e.resolveForgeLabels(context.Background(), "acme", "widgets",
		[]string{"bug", "feature", "docs"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 3 || got[0] != 1 {
		t.Errorf("first ID should be the existing bug=1; got %v", got)
	}
	if len(fc.LabelCreates) != 2 {
		t.Errorf("CreateRepoLabel calls=%d, want 2", len(fc.LabelCreates))
	}
}

func TestResolveForgeLabels_EmptyInput_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, fc := newForgeLabelTestEngine(t)

	got, err := e.resolveForgeLabels(context.Background(), "acme", "widgets", nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 0 {
		t.Errorf("got=%v, want empty", got)
	}
	if len(fc.LabelLists)+len(fc.LabelCreates) != 0 {
		t.Errorf("empty input triggered API calls: list=%d create=%d",
			len(fc.LabelLists), len(fc.LabelCreates))
	}
}

func TestResolveForgeLabels_CachedAcrossCalls(t *testing.T) {
	t.Parallel()
	e, fc := newForgeLabelTestEngine(t)
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		return []forge.Label{{ID: 1, Name: "bug"}}, nil
	}

	if _, err := e.resolveForgeLabels(context.Background(), "acme", "widgets", []string{"bug"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := e.resolveForgeLabels(context.Background(), "acme", "widgets", []string{"bug"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(fc.LabelLists) != 1 {
		t.Errorf("LabelLists=%d, want 1 (second call must hit cache)", len(fc.LabelLists))
	}
}

func TestResolveForgeLabels_ConcurrentSameRepo_OneList(t *testing.T) {
	t.Parallel()
	e, fc := newForgeLabelTestEngine(t)
	var calls atomic.Int64
	fc.ListRepoLabelsFunc = func(_ context.Context, _, _ string) ([]forge.Label, error) {
		calls.Add(1)
		return []forge.Label{{ID: 1, Name: "bug"}}, nil
	}

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, err := e.resolveForgeLabels(context.Background(), "acme", "widgets", []string{"bug"}); err != nil {
				t.Errorf("resolveForgeLabels: %v", err)
			}
		}()
	}
	wg.Wait()
	if c := calls.Load(); c != 1 {
		t.Errorf("ListRepoLabels ran %d times, want exactly 1", c)
	}
}

// --- inverseStateMap coverage ---

func TestInverseStateMap_RoundTrip(t *testing.T) {
	t.Parallel()
	forward := map[string]string{
		"open":   "Todo",
		"closed": "Done",
	}
	inverse := inverseStateMap(forward)
	if got := inverse["Todo"]; got != "open" {
		t.Errorf("inverse[Todo]=%q, want open", got)
	}
	if got := inverse["Done"]; got != "closed" {
		t.Errorf("inverse[Done]=%q, want closed", got)
	}
}

func TestInverseStateMap_DefaultsToOpen(t *testing.T) {
	t.Parallel()
	inverse := inverseStateMap(map[string]string{"closed": "Done"})
	if _, ok := inverse["In Progress"]; ok {
		t.Errorf("inverse must not contain unmapped names; got entry for In Progress")
	}
	// Empty input → nil map.
	if inverseStateMap(nil) != nil {
		t.Errorf("nil input should yield nil output")
	}
}

// TestInverseStateMap_SkipsEmptyValues guards the case where an
// operator config has a state-map entry pointing at "" — the inverse
// map must not gain an "" key (which would collide on the next
// "unmapped" lookup) and must not contain a stray reverse entry.
func TestInverseStateMap_SkipsEmptyValues(t *testing.T) {
	t.Parallel()
	inverse := inverseStateMap(map[string]string{
		"open":   "Todo",
		"closed": "",
	})
	if _, ok := inverse[""]; ok {
		t.Errorf("inverse should not contain an empty-string key")
	}
	if got := inverse["Todo"]; got != "open" {
		t.Errorf("inverse[Todo]=%q, want open", got)
	}
	if len(inverse) != 1 {
		t.Errorf("inverse size=%d, want 1 (the closed→empty entry must be dropped)", len(inverse))
	}
}
