package forge

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return b
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    string
		fixture  string
		wantKind EventKind
		check    func(t *testing.T, e *Event)
	}{
		{
			name:     "issues opened",
			event:    "issues",
			fixture:  "issues_opened.json",
			wantKind: EventIssueOpened,
			check: func(t *testing.T, e *Event) {
				if e.Issue == nil || e.Issue.Number != 42 {
					t.Fatalf("issue.number = %v, want 42", e.Issue)
				}
				if e.Issue.State != "open" {
					t.Errorf("issue.state = %q, want open", e.Issue.State)
				}
				if e.Repo.FullName != "acme/widget" {
					t.Errorf("repo.full_name = %q", e.Repo.FullName)
				}
				if len(e.Issue.Labels) != 1 || e.Issue.Labels[0].Name != "bug" {
					t.Errorf("labels = %+v", e.Issue.Labels)
				}
			},
		},
		{
			name:     "issues closed",
			event:    "issues",
			fixture:  "issues_closed.json",
			wantKind: EventIssueClosed,
			check: func(t *testing.T, e *Event) {
				if e.Issue.State != "closed" {
					t.Errorf("issue.state = %q, want closed", e.Issue.State)
				}
				if e.Issue.ClosedAt == nil {
					t.Errorf("issue.closed_at should be set on close")
				}
			},
		},
		{
			name:     "issues edited",
			event:    "issues",
			fixture:  "issues_edited.json",
			wantKind: EventIssueEdited,
			check: func(t *testing.T, e *Event) {
				if e.Issue.Title == "" {
					t.Error("expected edited title to be present")
				}
			},
		},
		{
			name:     "issue_comment created",
			event:    "issue_comment",
			fixture:  "issue_comment_created.json",
			wantKind: EventIssueCommentCreated,
			check: func(t *testing.T, e *Event) {
				if e.Comment == nil || e.Comment.ID != 5005 {
					t.Fatalf("comment = %+v", e.Comment)
				}
				if e.Issue == nil || e.Issue.Number != 42 {
					t.Fatalf("issue = %+v", e.Issue)
				}
			},
		},
		{
			name:     "pull_request opened",
			event:    "pull_request",
			fixture:  "pull_request_opened.json",
			wantKind: EventPullRequestOpened,
			check: func(t *testing.T, e *Event) {
				if e.PullRequest == nil || e.PullRequest.Number != 7 {
					t.Fatalf("pr = %+v", e.PullRequest)
				}
				if e.PullRequest.Merged {
					t.Errorf("new PR should not be merged")
				}
				if e.PullRequest.Head.Ref != "fix/login-flex" {
					t.Errorf("head.ref = %q", e.PullRequest.Head.Ref)
				}
			},
		},
		{
			name:     "pull_request closed merged",
			event:    "pull_request",
			fixture:  "pull_request_closed_merged.json",
			wantKind: EventPullRequestClosed,
			check: func(t *testing.T, e *Event) {
				if !e.PullRequest.Merged {
					t.Errorf("expected merged=true")
				}
				if e.PullRequest.MergedAt == nil {
					t.Errorf("expected merged_at to be set")
				}
			},
		},
		{
			name:     "pull_request closed unmerged",
			event:    "pull_request",
			fixture:  "pull_request_closed_unmerged.json",
			wantKind: EventPullRequestClosed,
			check: func(t *testing.T, e *Event) {
				if e.PullRequest.Merged {
					t.Errorf("expected merged=false on plain close")
				}
				if e.PullRequest.MergedAt != nil {
					t.Errorf("merged_at should be nil on plain close")
				}
			},
		},
		{
			name:     "pull_request_review submitted",
			event:    "pull_request_review",
			fixture:  "pull_request_review_submitted.json",
			wantKind: EventPullRequestReview,
			check: func(t *testing.T, e *Event) {
				if e.Review == nil || e.Review.Type != "pull_request_review_approved" {
					t.Fatalf("review = %+v", e.Review)
				}
				if e.PullRequest == nil || e.PullRequest.Number != 7 {
					t.Fatalf("pr = %+v", e.PullRequest)
				}
			},
		},
		{
			name:     "push",
			event:    "push",
			fixture:  "push.json",
			wantKind: EventPush,
			check: func(t *testing.T, e *Event) {
				if e.Push == nil {
					t.Fatal("push payload nil")
				}
				if e.Push.Ref != "refs/heads/main" {
					t.Errorf("ref = %q", e.Push.Ref)
				}
				if len(e.Push.Commits) != 1 {
					t.Errorf("commits = %d, want 1", len(e.Push.Commits))
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := loadFixture(t, tc.fixture)
			h := http.Header{}
			h.Set(HeaderEvent, tc.event)
			h.Set(HeaderDelivery, "delivery-"+tc.name)

			evt, err := Parse(h, body)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if evt.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", evt.Kind, tc.wantKind)
			}
			if evt.DeliveryID != "delivery-"+tc.name {
				t.Errorf("DeliveryID = %q", evt.DeliveryID)
			}
			if len(evt.Raw) != len(body) {
				t.Errorf("Raw not preserved: len=%d, want %d", len(evt.Raw), len(body))
			}
			tc.check(t, evt)
		})
	}
}

func TestParse_MissingEventHeader(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "issues_opened.json")
	_, err := Parse(http.Header{}, body)
	if !errors.Is(err, ErrMissingEventHeader) {
		t.Fatalf("err = %v, want ErrMissingEventHeader", err)
	}
}

func TestParse_UnsupportedEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		body   string
	}{
		{
			name:   "unknown top-level event",
			header: "release",
			body:   `{}`,
		},
		{
			name:   "issues with unknown action",
			header: "issues",
			body:   `{"action":"milestoned","issue":{},"repository":{},"sender":{}}`,
		},
		{
			name:   "issue_comment with unknown action",
			header: "issue_comment",
			body:   `{"action":"reacted","issue":{},"comment":{},"repository":{},"sender":{}}`,
		},
		{
			name:   "pull_request with unknown action",
			header: "pull_request",
			body:   `{"action":"synchronized","pull_request":{},"repository":{},"sender":{}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			h.Set(HeaderEvent, tc.header)
			_, err := Parse(h, []byte(tc.body))
			if !errors.Is(err, ErrUnsupportedEvent) {
				t.Fatalf("err = %v, want ErrUnsupportedEvent", err)
			}
		})
	}
}

func TestParse_Malformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		body   string
	}{
		{"invalid JSON for issues", "issues", `{not json`},
		{"invalid JSON for issue_comment", "issue_comment", `{not json`},
		{"invalid JSON for pull_request", "pull_request", `{not json`},
		{"invalid JSON for pull_request_review", "pull_request_review", `{not json`},
		{"invalid JSON for push", "push", `{not json`},
		{
			name:   "issues missing issue object",
			header: "issues",
			body:   `{"action":"opened","repository":{},"sender":{}}`,
		},
		{
			name:   "issue_comment missing comment object",
			header: "issue_comment",
			body:   `{"action":"created","issue":{},"repository":{},"sender":{}}`,
		},
		{
			name:   "pull_request_review missing review object",
			header: "pull_request_review",
			body:   `{"action":"submitted","pull_request":{},"repository":{},"sender":{}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			h.Set(HeaderEvent, tc.header)
			_, err := Parse(h, []byte(tc.body))
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("err = %v, want ErrMalformedPayload", err)
			}
		})
	}
}

func TestVerifyAndParse_HappyPath(t *testing.T) {
	t.Parallel()

	const secret = "topsecret"
	body := loadFixture(t, "issues_opened.json")
	sig := computeSignature(secret, body)

	h := http.Header{}
	h.Set(HeaderSignature, sig)
	h.Set(HeaderEvent, "issues")
	h.Set(HeaderDelivery, "9d3b1f6e-1111-2222-3333-444455556666")

	evt, err := VerifyAndParse(secret, h, body)
	if err != nil {
		t.Fatalf("VerifyAndParse error: %v", err)
	}
	if evt.Kind != EventIssueOpened {
		t.Errorf("Kind = %q, want %q", evt.Kind, EventIssueOpened)
	}
	if evt.DeliveryID != "9d3b1f6e-1111-2222-3333-444455556666" {
		t.Errorf("DeliveryID = %q", evt.DeliveryID)
	}
	if evt.Issue == nil || evt.Issue.Number != 42 {
		t.Errorf("issue not populated: %+v", evt.Issue)
	}
}

func TestVerifyAndParse_BadSignature(t *testing.T) {
	t.Parallel()

	body := loadFixture(t, "issues_opened.json")
	h := http.Header{}
	h.Set(HeaderSignature, "00")
	h.Set(HeaderEvent, "issues")

	_, err := VerifyAndParse("topsecret", h, body)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestNewClient_DefaultsAndTrim(t *testing.T) {
	t.Parallel()

	c := NewClient("https://forge.example.test/", "tok", nil)
	if c.BaseURL != "https://forge.example.test" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.HTTPClient == nil {
		t.Error("HTTPClient should default to non-nil")
	}
	if c.Token != "tok" {
		t.Errorf("Token = %q", c.Token)
	}
}
