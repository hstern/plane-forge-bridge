package sync

import (
	"context"
	"sync"

	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// fakeClient is the hand-written test double for PlaneClient. It records
// every call and offers per-method function overrides so each test can
// shape the response it needs without subclassing. Concurrency-safe — the
// mutex guards both the recorded slices and the override fields.
//
// Default behaviour when an override is nil:
//
//   - GetIssueByExternalRef → returns (nil, plane.ErrNotFound)
//   - CreateIssue           → returns a *plane.WorkItem with a generated ID
//   - UpdateIssue           → echoes the issueID back as the work item ID
//   - ListProjectStates     → returns a fixed three-state slice with the
//     names ("Backlog", "Todo", "Done") so the engine's resolve loop has
//     names to find. Tests that need a different state set set the
//     override.
type fakeClient struct {
	mu sync.Mutex

	GetIssueByExternalRefFunc func(ctx context.Context, projectID, source, externalID string) (*plane.WorkItem, error)
	CreateIssueFunc           func(ctx context.Context, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
	UpdateIssueFunc           func(ctx context.Context, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
	ListProjectStatesFunc     func(ctx context.Context, projectID string) ([]plane.State, error)

	Gets    []getCall
	Creates []createCall
	Updates []updateCall
	Lists   []string
}

type getCall struct {
	ProjectID  string
	Source     string
	ExternalID string
}

type createCall struct {
	ProjectID string
	Req       plane.CreateIssueRequest
}

type updateCall struct {
	ProjectID string
	IssueID   string
	Req       plane.UpdateIssueRequest
}

func (f *fakeClient) GetIssueByExternalRef(ctx context.Context, projectID, source, externalID string) (*plane.WorkItem, error) {
	f.mu.Lock()
	f.Gets = append(f.Gets, getCall{ProjectID: projectID, Source: source, ExternalID: externalID})
	fn := f.GetIssueByExternalRefFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, source, externalID)
	}
	return nil, plane.ErrNotFound
}

func (f *fakeClient) CreateIssue(ctx context.Context, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error) {
	f.mu.Lock()
	f.Creates = append(f.Creates, createCall{ProjectID: projectID, Req: req})
	fn := f.CreateIssueFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, req)
	}
	return &plane.WorkItem{
		ID:          "wi-default",
		Name:        req.Name,
		Description: req.DescriptionHTML,
		Project:     projectID,
	}, nil
}

func (f *fakeClient) UpdateIssue(ctx context.Context, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error) {
	f.mu.Lock()
	f.Updates = append(f.Updates, updateCall{ProjectID: projectID, IssueID: issueID, Req: req})
	fn := f.UpdateIssueFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, issueID, req)
	}
	wi := &plane.WorkItem{ID: issueID, Project: projectID}
	if req.Name != nil {
		wi.Name = *req.Name
	}
	if req.DescriptionHTML != nil {
		wi.Description = *req.DescriptionHTML
	}
	return wi, nil
}

func (f *fakeClient) ListProjectStates(ctx context.Context, projectID string) ([]plane.State, error) {
	f.mu.Lock()
	f.Lists = append(f.Lists, projectID)
	fn := f.ListProjectStatesFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID)
	}
	return []plane.State{
		{ID: "state-backlog", Name: "Backlog", Group: "backlog"},
		{ID: "state-todo", Name: "Todo", Group: "unstarted"},
		{ID: "state-done", Name: "Done", Group: "completed"},
	}, nil
}

// snapshot returns copies of the recorded call slices so assertions can read
// them without racing with concurrent calls in the test.
func (f *fakeClient) snapshot() (gets []getCall, creates []createCall, updates []updateCall, lists []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gets = append(gets, f.Gets...)
	creates = append(creates, f.Creates...)
	updates = append(updates, f.Updates...)
	lists = append(lists, f.Lists...)
	return
}
