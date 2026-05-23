package sync

import (
	"context"
	"sync"

	"github.com/hstern/plane-forge-bridge/internal/forge"
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

	GetIssueFunc              func(ctx context.Context, projectID, issueID string) (*plane.WorkItem, error)
	GetIssueByExternalRefFunc func(ctx context.Context, projectID, source, externalID string) (*plane.WorkItem, error)
	GetIssueBySequenceIDFunc  func(ctx context.Context, projectID string, sequenceID int) (*plane.WorkItem, error)
	CreateIssueFunc           func(ctx context.Context, projectID string, req plane.CreateIssueRequest) (*plane.WorkItem, error)
	UpdateIssueFunc           func(ctx context.Context, projectID, issueID string, req plane.UpdateIssueRequest) (*plane.WorkItem, error)
	ListProjectStatesFunc     func(ctx context.Context, projectID string) ([]plane.State, error)
	ListProjectLabelsFunc     func(ctx context.Context, projectID string) ([]plane.Label, error)
	CreateProjectLabelFunc    func(ctx context.Context, projectID string, req plane.CreateLabelRequest) (*plane.Label, error)
	CreateCommentFunc         func(ctx context.Context, projectID, issueID string, req plane.CreateCommentRequest) (*plane.Comment, error)
	UpdateCommentFunc         func(ctx context.Context, projectID, issueID, commentID string, req plane.UpdateCommentRequest) (*plane.Comment, error)
	DeleteCommentFunc         func(ctx context.Context, projectID, issueID, commentID string) error

	Gets                    []getCall
	GetsByID                []getByIDCall
	GetsBySeq               []seqLookupCall
	Creates                 []createCall
	Updates                 []updateCall
	Lists                   []string
	ListProjectLabelsCalls  []string
	CreateProjectLabelCalls []labelCreateCall
	CommentCreates          []commentCreateCall
	CommentUpdates          []commentUpdateCall
	CommentDeletes          []commentDeleteCall
}

type labelCreateCall struct {
	ProjectID string
	Req       plane.CreateLabelRequest
}

type getByIDCall struct {
	ProjectID string
	IssueID   string
}

type seqLookupCall struct {
	ProjectID  string
	SequenceID int
}

type commentCreateCall struct {
	ProjectID string
	IssueID   string
	Req       plane.CreateCommentRequest
}

type commentUpdateCall struct {
	ProjectID string
	IssueID   string
	CommentID string
	Req       plane.UpdateCommentRequest
}

type commentDeleteCall struct {
	ProjectID string
	IssueID   string
	CommentID string
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

func (f *fakeClient) GetIssue(ctx context.Context, projectID, issueID string) (*plane.WorkItem, error) {
	f.mu.Lock()
	f.GetsByID = append(f.GetsByID, getByIDCall{ProjectID: projectID, IssueID: issueID})
	fn := f.GetIssueFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, issueID)
	}
	return nil, plane.ErrNotFound
}

func (f *fakeClient) GetIssueBySequenceID(ctx context.Context, projectID string, sequenceID int) (*plane.WorkItem, error) {
	f.mu.Lock()
	f.GetsBySeq = append(f.GetsBySeq, seqLookupCall{ProjectID: projectID, SequenceID: sequenceID})
	fn := f.GetIssueBySequenceIDFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, sequenceID)
	}
	return nil, plane.ErrNotFound
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

func (f *fakeClient) ListProjectLabels(ctx context.Context, projectID string) ([]plane.Label, error) {
	f.mu.Lock()
	f.ListProjectLabelsCalls = append(f.ListProjectLabelsCalls, projectID)
	fn := f.ListProjectLabelsFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID)
	}
	return nil, nil
}

func (f *fakeClient) CreateProjectLabel(ctx context.Context, projectID string, req plane.CreateLabelRequest) (*plane.Label, error) {
	f.mu.Lock()
	f.CreateProjectLabelCalls = append(f.CreateProjectLabelCalls, labelCreateCall{ProjectID: projectID, Req: req})
	fn := f.CreateProjectLabelFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, req)
	}
	return &plane.Label{ID: "label-" + req.Name, Name: req.Name}, nil
}

func (f *fakeClient) CreateComment(ctx context.Context, projectID, issueID string, req plane.CreateCommentRequest) (*plane.Comment, error) {
	f.mu.Lock()
	f.CommentCreates = append(f.CommentCreates, commentCreateCall{ProjectID: projectID, IssueID: issueID, Req: req})
	fn := f.CreateCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, issueID, req)
	}
	return &plane.Comment{
		ID:          "cmt-default",
		IssueID:     issueID,
		Project:     projectID,
		CommentHTML: req.CommentHTML,
	}, nil
}

func (f *fakeClient) UpdateComment(ctx context.Context, projectID, issueID, commentID string, req plane.UpdateCommentRequest) (*plane.Comment, error) {
	f.mu.Lock()
	f.CommentUpdates = append(f.CommentUpdates, commentUpdateCall{ProjectID: projectID, IssueID: issueID, CommentID: commentID, Req: req})
	fn := f.UpdateCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, issueID, commentID, req)
	}
	c := &plane.Comment{ID: commentID, IssueID: issueID, Project: projectID}
	if req.CommentHTML != nil {
		c.CommentHTML = *req.CommentHTML
	}
	return c, nil
}

func (f *fakeClient) DeleteComment(ctx context.Context, projectID, issueID, commentID string) error {
	f.mu.Lock()
	f.CommentDeletes = append(f.CommentDeletes, commentDeleteCall{ProjectID: projectID, IssueID: issueID, CommentID: commentID})
	fn := f.DeleteCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, projectID, issueID, commentID)
	}
	return nil
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

// fakeForgeClient is the hand-written test double for ForgeClient.
// Same shape and conventions as fakeClient — per-method func overrides
// with default behaviour when nil, and a mutex protecting both the
// recorded slices and the override fields.
//
// Default behaviour when an override is nil:
//
//   - GetIssue       → returns (nil, forge.ErrUnsupportedEvent) so tests
//     that don't supply an override see a clear failure path.
//   - CreateComment  → returns a *forge.Comment with a generated ID.
//   - UpdateComment  → echoes the commentID back.
//   - DeleteComment  → returns nil.
type fakeForgeClient struct {
	mu sync.Mutex

	GetIssueFunc        func(ctx context.Context, owner, repo string, number int64) (*forge.Issue, error)
	ListRepoLabelsFunc  func(ctx context.Context, owner, repo string) ([]forge.Label, error)
	CreateRepoLabelFunc func(ctx context.Context, owner, repo string, req forge.CreateLabelRequest) (*forge.Label, error)
	CreateCommentFunc   func(ctx context.Context, owner, repo string, issueNumber int64, req forge.CreateCommentRequest) (*forge.Comment, error)
	UpdateCommentFunc   func(ctx context.Context, owner, repo string, commentID int64, req forge.UpdateCommentRequest) (*forge.Comment, error)
	DeleteCommentFunc   func(ctx context.Context, owner, repo string, commentID int64) error

	IssueGets      []forgeGetIssueCall
	LabelLists     []forgeLabelListCall
	LabelCreates   []forgeLabelCreateCall
	CommentCreates []forgeCommentCreateCall
	CommentUpdates []forgeCommentUpdateCall
	CommentDeletes []forgeCommentDeleteCall
}

type forgeLabelListCall struct {
	Owner string
	Repo  string
}

type forgeLabelCreateCall struct {
	Owner string
	Repo  string
	Req   forge.CreateLabelRequest
}

type forgeGetIssueCall struct {
	Owner  string
	Repo   string
	Number int64
}

type forgeCommentCreateCall struct {
	Owner       string
	Repo        string
	IssueNumber int64
	Req         forge.CreateCommentRequest
}

type forgeCommentUpdateCall struct {
	Owner     string
	Repo      string
	CommentID int64
	Req       forge.UpdateCommentRequest
}

type forgeCommentDeleteCall struct {
	Owner     string
	Repo      string
	CommentID int64
}

func (f *fakeForgeClient) GetIssue(ctx context.Context, owner, repo string, number int64) (*forge.Issue, error) {
	f.mu.Lock()
	f.IssueGets = append(f.IssueGets, forgeGetIssueCall{Owner: owner, Repo: repo, Number: number})
	fn := f.GetIssueFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo, number)
	}
	return nil, forge.ErrUnsupportedEvent
}

func (f *fakeForgeClient) ListRepoLabels(ctx context.Context, owner, repo string) ([]forge.Label, error) {
	f.mu.Lock()
	f.LabelLists = append(f.LabelLists, forgeLabelListCall{Owner: owner, Repo: repo})
	fn := f.ListRepoLabelsFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo)
	}
	return nil, nil
}

func (f *fakeForgeClient) CreateRepoLabel(ctx context.Context, owner, repo string, req forge.CreateLabelRequest) (*forge.Label, error) {
	f.mu.Lock()
	f.LabelCreates = append(f.LabelCreates, forgeLabelCreateCall{Owner: owner, Repo: repo, Req: req})
	fn := f.CreateRepoLabelFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo, req)
	}
	return &forge.Label{ID: 1, Name: req.Name}, nil
}

func (f *fakeForgeClient) CreateComment(ctx context.Context, owner, repo string, issueNumber int64, req forge.CreateCommentRequest) (*forge.Comment, error) {
	f.mu.Lock()
	f.CommentCreates = append(f.CommentCreates, forgeCommentCreateCall{Owner: owner, Repo: repo, IssueNumber: issueNumber, Req: req})
	fn := f.CreateCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo, issueNumber, req)
	}
	return &forge.Comment{
		ID:   424242,
		Body: req.Body,
	}, nil
}

func (f *fakeForgeClient) UpdateComment(ctx context.Context, owner, repo string, commentID int64, req forge.UpdateCommentRequest) (*forge.Comment, error) {
	f.mu.Lock()
	f.CommentUpdates = append(f.CommentUpdates, forgeCommentUpdateCall{Owner: owner, Repo: repo, CommentID: commentID, Req: req})
	fn := f.UpdateCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo, commentID, req)
	}
	return &forge.Comment{ID: commentID, Body: req.Body}, nil
}

func (f *fakeForgeClient) DeleteComment(ctx context.Context, owner, repo string, commentID int64) error {
	f.mu.Lock()
	f.CommentDeletes = append(f.CommentDeletes, forgeCommentDeleteCall{Owner: owner, Repo: repo, CommentID: commentID})
	fn := f.DeleteCommentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, repo, commentID)
	}
	return nil
}
