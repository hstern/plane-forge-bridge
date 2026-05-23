package plane

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func headersFor(eventHeader, deliveryID string) http.Header {
	h := http.Header{}
	if eventHeader != "" {
		h.Set(HeaderEvent, eventHeader)
	}
	if deliveryID != "" {
		h.Set(HeaderDelivery, deliveryID)
	}
	return h
}

func TestParse_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     string
		eventHeader string
		wantKind    EventKind
		check       func(t *testing.T, ev *Event)
	}{
		{
			name:        "work item created",
			fixture:     "work_item_created.json",
			eventHeader: planeEventIssue,
			wantKind:    EventWorkItemCreated,
			check: func(t *testing.T, ev *Event) {
				if ev.WorkItem == nil {
					t.Fatal("WorkItem nil")
				}
				if got := ev.WorkItem.Name; got != "Investigate flaky CI runner" {
					t.Errorf("Name = %q", got)
				}
				if got := ev.WorkItem.SequenceID; got != 42 {
					t.Errorf("SequenceID = %d", got)
				}
				if got := len(ev.WorkItem.Assignees); got != 1 {
					t.Errorf("Assignees len = %d", got)
				}
				if ev.WorkItem.Priority != "high" {
					t.Errorf("Priority = %q", ev.WorkItem.Priority)
				}
				if ev.Comment != nil {
					t.Errorf("Comment should be nil")
				}
				if ev.Actor.DisplayName != "Henry" {
					t.Errorf("Actor.DisplayName = %q", ev.Actor.DisplayName)
				}
			},
		},
		{
			name:        "work item updated",
			fixture:     "work_item_updated.json",
			eventHeader: planeEventIssue,
			wantKind:    EventWorkItemUpdated,
			check: func(t *testing.T, ev *Event) {
				if ev.WorkItem == nil {
					t.Fatal("WorkItem nil")
				}
				if got := len(ev.WorkItem.Labels); got != 2 {
					t.Errorf("Labels len = %d", got)
				}
				if ev.WorkItem.Priority != "urgent" {
					t.Errorf("Priority = %q", ev.WorkItem.Priority)
				}
			},
		},
		{
			name:        "work item deleted",
			fixture:     "work_item_deleted.json",
			eventHeader: planeEventIssue,
			wantKind:    EventWorkItemDeleted,
			check: func(t *testing.T, ev *Event) {
				if ev.WorkItem == nil {
					t.Fatal("WorkItem nil")
				}
				if ev.WorkItem.ID != "33333333-3333-3333-3333-333333333333" {
					t.Errorf("ID = %q", ev.WorkItem.ID)
				}
				// delete fixture intentionally has only an id; other fields zero.
				if ev.WorkItem.Name != "" {
					t.Errorf("Name = %q, want empty", ev.WorkItem.Name)
				}
			},
		},
		{
			name:        "comment created",
			fixture:     "comment_created.json",
			eventHeader: planeEventIssueComment,
			wantKind:    EventCommentCreated,
			check: func(t *testing.T, ev *Event) {
				if ev.Comment == nil {
					t.Fatal("Comment nil")
				}
				if ev.Comment.IssueID != "33333333-3333-3333-3333-333333333333" {
					t.Errorf("IssueID = %q", ev.Comment.IssueID)
				}
				if ev.Comment.Access != "INTERNAL" {
					t.Errorf("Access = %q", ev.Comment.Access)
				}
				if ev.WorkItem != nil {
					t.Errorf("WorkItem should be nil")
				}
			},
		},
		{
			name:        "comment updated",
			fixture:     "comment_updated.json",
			eventHeader: planeEventIssueComment,
			wantKind:    EventCommentUpdated,
			check: func(t *testing.T, ev *Event) {
				if ev.Comment == nil {
					t.Fatal("Comment nil")
				}
				if ev.Comment.CommentHTML == "" {
					t.Errorf("CommentHTML empty")
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := loadFixture(t, tc.fixture)
			h := headersFor(tc.eventHeader, "delivery-id-1")
			ev, err := Parse(h, body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ev.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.DeliveryID != "delivery-id-1" {
				t.Errorf("DeliveryID = %q", ev.DeliveryID)
			}
			if ev.WorkspaceID == "" {
				t.Errorf("WorkspaceID empty")
			}
			if len(ev.Raw) != len(body) {
				t.Errorf("Raw len = %d, want %d", len(ev.Raw), len(body))
			}
			tc.check(t, ev)
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()

	valid := loadFixture(t, "work_item_created.json")

	tests := []struct {
		name    string
		headers http.Header
		body    []byte
		wantErr error
	}{
		{
			name:    "missing event header",
			headers: http.Header{},
			body:    valid,
			wantErr: ErrMissingEventHeader,
		},
		{
			name:    "unknown event type",
			headers: headersFor("project", ""),
			body:    valid,
			wantErr: ErrUnsupportedEvent,
		},
		{
			name:    "unknown action",
			headers: headersFor(planeEventIssue, ""),
			body:    []byte(`{"event":"issue","action":"frobnicated","data":{}}`),
			wantErr: ErrUnsupportedEvent,
		},
		{
			name:    "malformed json",
			headers: headersFor(planeEventIssue, ""),
			body:    []byte(`{not json`),
			wantErr: ErrMalformedPayload,
		},
		{
			name:    "bad work item data",
			headers: headersFor(planeEventIssue, ""),
			body:    []byte(`{"event":"issue","action":"create","data":"not-an-object"}`),
			wantErr: ErrMalformedPayload,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.headers, tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyAndParse(t *testing.T) {
	t.Parallel()

	body := loadFixture(t, "work_item_created.json")

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()
		h := headersFor(planeEventIssue, "delivery-abc")
		h.Set(HeaderSignature, sign(t, testSecret, body))
		ev, err := VerifyAndParse(testSecret, h, body)
		if err != nil {
			t.Fatalf("VerifyAndParse: %v", err)
		}
		if ev.Kind != EventWorkItemCreated {
			t.Errorf("Kind = %q", ev.Kind)
		}
	})

	t.Run("bad signature short-circuits parse", func(t *testing.T) {
		t.Parallel()
		h := headersFor(planeEventIssue, "delivery-abc")
		h.Set(HeaderSignature, sign(t, "wrong", body))
		_, err := VerifyAndParse(testSecret, h, body)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("err = %v, want ErrInvalidSignature", err)
		}
	})

	t.Run("missing signature", func(t *testing.T) {
		t.Parallel()
		h := headersFor(planeEventIssue, "delivery-abc")
		_, err := VerifyAndParse(testSecret, h, body)
		if !errors.Is(err, ErrMissingSignature) {
			t.Fatalf("err = %v, want ErrMissingSignature", err)
		}
	})
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	c := NewClient("https://plane.example.com", "ws", "key", nil)
	if c.BaseURL != "https://plane.example.com" || c.WorkspaceSlug != "ws" || c.APIKey != "key" {
		t.Errorf("client fields wrong: %+v", c)
	}
}
