package forge

import "time"

// EventKind names a parsed webhook event we care about.
//
// The string form is "<x-gitea-event>.<action>" for events that carry an
// "action" field (issues, issue_comment, pull_request), and just the event
// name for events that don't (push, pull_request_review).
type EventKind string

// Recognized event kinds. Anything not in this list parses to
// ErrUnsupportedEvent so callers can ack-and-drop unknown payloads.
const (
	EventIssueOpened         EventKind = "issue.opened"
	EventIssueEdited         EventKind = "issue.edited"
	EventIssueClosed         EventKind = "issue.closed"
	EventIssueReopened       EventKind = "issue.reopened"
	EventIssueLabeled        EventKind = "issue.labeled"
	EventIssueUnlabeled      EventKind = "issue.unlabeled"
	EventIssueAssigned       EventKind = "issue.assigned"
	EventIssueCommentCreated EventKind = "issue_comment.created"
	EventIssueCommentEdited  EventKind = "issue_comment.edited"
	EventIssueCommentDeleted EventKind = "issue_comment.deleted"
	EventPullRequestOpened   EventKind = "pull_request.opened"
	EventPullRequestEdited   EventKind = "pull_request.edited"
	EventPullRequestClosed   EventKind = "pull_request.closed"
	EventPullRequestReopened EventKind = "pull_request.reopened"
	EventPullRequestReview   EventKind = "pull_request_review"
	EventPush                EventKind = "push"
)

// Event is the parsed webhook delivery. Only the fields relevant to the
// matching event family are populated; the rest are nil.
type Event struct {
	Kind        EventKind
	DeliveryID  string // X-Gitea-Delivery — used as the loop-break event ID.
	Repo        Repository
	Sender      User
	Issue       *Issue       // issue.* and issue_comment.*
	Comment     *Comment     // issue_comment.*
	PullRequest *PullRequest // pull_request.* and pull_request_review
	Review      *Review      // pull_request_review
	Push        *Push        // push
	Raw         []byte       // original body, retained for debugging / replay.
}

// User is the minimum we need to identify the actor on either side.
type User struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	HTMLURL  string `json:"html_url"`
}

// Repository is the minimum we need to address the repo for outbound API
// calls (FullName is "owner/name").
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    User   `json:"owner"`
	HTMLURL  string `json:"html_url"`
	Private  bool   `json:"private"`
}

// Label is a Gitea/Forgejo issue or PR label.
type Label struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Issue mirrors the subset of the Gitea/Forgejo issue payload we sync.
type Issue struct {
	ID        int64      `json:"id"`
	Number    int64      `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"` // "open" | "closed"
	User      User       `json:"user"`
	Assignees []User     `json:"assignees"`
	Labels    []Label    `json:"labels"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// Comment is a comment on an issue or pull request.
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PullRequest mirrors the subset of the Gitea/Forgejo PR payload we sync.
// Merged distinguishes "closed by merge" from "closed without merge" in the
// pull_request.closed event.
type PullRequest struct {
	ID        int64      `json:"id"`
	Number    int64      `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	User      User       `json:"user"`
	Assignees []User     `json:"assignees"`
	Labels    []Label    `json:"labels"`
	HTMLURL   string     `json:"html_url"`
	Merged    bool       `json:"merged"`
	MergedAt  *time.Time `json:"merged_at,omitempty"`
	Head      PRBranch   `json:"head"`
	Base      PRBranch   `json:"base"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// PRBranch is one side of a pull request (head or base).
type PRBranch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// Review is a pull_request_review payload's review object.
type Review struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"` // "pull_request_review_approved" | "..._rejected" | "..._comment"
	Content string `json:"content"`
	User    User   `json:"user"`
}

// Push is the parsed push event.
type Push struct {
	Ref        string       `json:"ref"`
	Before     string       `json:"before"`
	After      string       `json:"after"`
	CompareURL string       `json:"compare_url"`
	Commits    []PushCommit `json:"commits"`
	Pusher     User         `json:"pusher"`
}

// PushCommit is a single commit in a push event.
type PushCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
}
