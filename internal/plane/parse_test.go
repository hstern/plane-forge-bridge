package plane

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
			// Verbatim capture from Plane CE v1.3.1 (plane.stern.ca,
			// 2026-05-24). The fixture exercises: object-form state
			// (PFB-24), past-tense action verb (PFB-22), extra
			// description_*/description_json fields the bridge ignores,
			// and the full activity.actor object Plane ships (the bridge
			// only reads .id + .display_name today).
			name:        "work item created",
			fixture:     "work_item_created.json",
			eventHeader: planeEventIssue,
			wantKind:    EventWorkItemCreated,
			check: func(t *testing.T, ev *Event) {
				if ev.WorkItem == nil {
					t.Fatal("WorkItem nil")
				}
				if got := ev.WorkItem.Name; got != "pfb-25 capture sample" {
					t.Errorf("Name = %q", got)
				}
				if got := ev.WorkItem.SequenceID; got != 35 {
					t.Errorf("SequenceID = %d", got)
				}
				if got := ev.WorkItem.State.ID; got != "e931d389-7080-4612-9f6a-05b535ac3afa" {
					t.Errorf("State.ID = %q", got)
				}
				if got := ev.WorkItem.State.Name; got != "Backlog" {
					t.Errorf("State.Name = %q", got)
				}
				if got := ev.WorkItem.Priority; got != "none" {
					t.Errorf("Priority = %q", got)
				}
				if ev.Comment != nil {
					t.Errorf("Comment should be nil")
				}
				if got := ev.Actor.DisplayName; got != "henry" {
					t.Errorf("Actor.DisplayName = %q", got)
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

// TestParse_RealPlaneActionVerbsAccepted pins the past-tense action
// verbs that real Plane (v1.3.1+) actually sends. The bridge originally
// shipped recognising only present-tense create/update/delete; every
// real Plane delivery hit ErrUnsupportedEvent and silently 204'd. See
// GH#10 (PFB-22) for the full reproduction.
//
// This test fails if anyone tightens the parser back to present-tense
// only.
func TestParse_RealPlaneActionVerbsAccepted(t *testing.T) {
	t.Parallel()

	const realPlanePayload = `{
		"event": "issue",
		"action": "updated",
		"webhook_id": "3eda452e-0000-0000-0000-000000000001",
		"workspace_id": "00000000-0000-0000-0000-000000000aaa",
		"data": {
			"id": "33333333-3333-3333-3333-333333333333",
			"name": "real plane payload",
			"project": "77777777-7777-7777-7777-777777777777",
			"workspace": "00000000-0000-0000-0000-000000000aaa",
			"sequence_id": 1
		}
	}`

	cases := []struct {
		event    string
		action   string
		wantKind EventKind
	}{
		{planeEventIssue, "created", EventWorkItemCreated},
		{planeEventIssue, "updated", EventWorkItemUpdated},
		{planeEventIssue, "deleted", EventWorkItemDeleted},
		// Present-tense aliases — kept so hand-rolled clients / older
		// Plane code paths that emit "create"/"update"/"delete" still
		// work. Removing these would be a silent behavior change.
		{planeEventIssue, "create", EventWorkItemCreated},
		{planeEventIssue, "update", EventWorkItemUpdated},
		{planeEventIssue, "delete", EventWorkItemDeleted},
		{planeEventIssueComment, "created", EventCommentCreated},
		{planeEventIssueComment, "updated", EventCommentUpdated},
		{planeEventIssueComment, "deleted", EventCommentDeleted},
		{planeEventIssueComment, "create", EventCommentCreated},
		{planeEventIssueComment, "update", EventCommentUpdated},
		{planeEventIssueComment, "delete", EventCommentDeleted},
	}

	for _, tc := range cases {
		t.Run(tc.event+"_"+tc.action, func(t *testing.T) {
			t.Parallel()
			body := []byte(strings.ReplaceAll(
				strings.ReplaceAll(realPlanePayload, `"event": "issue"`, `"event": "`+tc.event+`"`),
				`"action": "updated"`, `"action": "`+tc.action+`"`,
			))
			h := http.Header{}
			h.Set(HeaderEvent, tc.event)
			h.Set(HeaderDelivery, "plane-action-verb-test")
			ev, err := Parse(h, body)
			if err != nil {
				t.Fatalf("Parse failed for %s/%s: %v", tc.event, tc.action, err)
			}
			if ev.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", ev.Kind, tc.wantKind)
			}
		})
	}
}

// TestParse_RealPlaneWorkItemPayload_PFB24 pins decoding of a real
// Plane CE v1.3.1 work_item.created payload captured from
// postgres webhook_logs.request_body. It exercises the object-form
// state/labels (PFB-24) alongside the past-tense action (PFB-22) —
// either regressing alone produces a 400-malformed-payload response
// in the bridge handler.
func TestParse_RealPlaneWorkItemPayload_PFB24(t *testing.T) {
	t.Parallel()

	const realPlanePayload = `{
		"event": "issue",
		"action": "created",
		"webhook_id": "3eda452e-0000-0000-0000-000000000001",
		"workspace_id": "00000000-0000-0000-0000-000000000aaa",
		"data": {
			"id": "12345678-1234-1234-1234-123456789abc",
			"name": "Real Plane payload",
			"description_html": "<p>captured from postgres webhook_logs.request_body</p>",
			"state": {
				"id": "e931d389-7080-4612-9f6a-05b535ac3afa",
				"name": "Backlog",
				"color": "#60646C",
				"group": "backlog"
			},
			"priority": "none",
			"assignees": [],
			"labels": [
				{
					"id": "abcdef00-0000-0000-0000-000000000001",
					"name": "claude:DOCS-25",
					"color": "#FAB287"
				}
			],
			"project": "77777777-7777-7777-7777-777777777777",
			"workspace": "00000000-0000-0000-0000-000000000aaa",
			"created_by": "88888888-8888-8888-8888-888888888888",
			"sequence_id": 33
		}
	}`

	h := http.Header{}
	h.Set(HeaderEvent, planeEventIssue)
	h.Set(HeaderDelivery, "pfb24-regression")
	ev, err := Parse(h, []byte(realPlanePayload))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ev.Kind != EventWorkItemCreated {
		t.Errorf("Kind = %q, want %q", ev.Kind, EventWorkItemCreated)
	}
	if ev.WorkItem == nil {
		t.Fatal("WorkItem nil")
	}
	if got := ev.WorkItem.State.ID; got != "e931d389-7080-4612-9f6a-05b535ac3afa" {
		t.Errorf("State.ID = %q", got)
	}
	if got := ev.WorkItem.State.Name; got != "Backlog" {
		t.Errorf("State.Name = %q", got)
	}
	if got := len(ev.WorkItem.Labels); got != 1 {
		t.Fatalf("Labels len = %d, want 1", got)
	}
	if got := ev.WorkItem.Labels[0].Name; got != "claude:DOCS-25" {
		t.Errorf("Labels[0].Name = %q", got)
	}
	if got := len(ev.WorkItem.Assignees); got != 0 {
		t.Errorf("Assignees len = %d, want 0", got)
	}
}

// TestWorkItem_UnmarshalJSON_RESTShape_PFB25 pins decoding of a real
// Plane CE v1.3.1 REST POST /issues/ response captured against
// plane.stern.ca. The REST surface returns state as a bare UUID
// string and labels/assignees as arrays of bare UUID strings — the
// opposite shape from webhooks. v0.1.2 (the PFB-24 fix) hard-coded
// the object form on WorkItem, regressing every create call. See
// PFB-25.
//
// This test fails if anyone reverts the dual-shape UnmarshalJSON.
func TestWorkItem_UnmarshalJSON_RESTShape_PFB25(t *testing.T) {
	t.Parallel()

	// Verbatim shape Plane CE v1.3.1 returns from
	// POST /workspaces/<slug>/projects/<pid>/issues/. Captured against
	// plane.stern.ca on 2026-05-24 (probe in PFB-25 body).
	const restResponse = `{
		"id": "2d048fe5-c172-42f2-ab92-d5d26f4d7e96",
		"name": "pfb-25 capture sample",
		"description_html": "<p>sample</p>",
		"priority": "none",
		"sequence_id": 35,
		"state": "e931d389-7080-4612-9f6a-05b535ac3afa",
		"assignees": ["00a3fa45-f4f8-4a19-b7cf-f86a648f717d"],
		"labels": ["0798d982-9eef-4993-9d4b-b196d0d4ba3e"],
		"project": "ede7e196-e408-47b6-883b-d2188f101dd0",
		"workspace": "f0ebd07e-6540-4876-8b07-cdc15659e2b1",
		"created_by": "00a3fa45-f4f8-4a19-b7cf-f86a648f717d"
	}`

	var wi WorkItem
	if err := json.Unmarshal([]byte(restResponse), &wi); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if wi.State.ID != "e931d389-7080-4612-9f6a-05b535ac3afa" {
		t.Errorf("State.ID = %q, want e931d389-...", wi.State.ID)
	}
	if wi.State.Name != "" {
		t.Errorf("State.Name = %q, want empty (REST omits the name)", wi.State.Name)
	}
	if len(wi.Labels) != 1 || wi.Labels[0].ID != "0798d982-9eef-4993-9d4b-b196d0d4ba3e" {
		t.Errorf("Labels = %+v", wi.Labels)
	}
	if wi.Labels[0].Name != "" {
		t.Errorf("Labels[0].Name = %q, want empty (REST omits the name)", wi.Labels[0].Name)
	}
	if len(wi.Assignees) != 1 || wi.Assignees[0].ID != "00a3fa45-f4f8-4a19-b7cf-f86a648f717d" {
		t.Errorf("Assignees = %+v", wi.Assignees)
	}
	if wi.SequenceID != 35 {
		t.Errorf("SequenceID = %d", wi.SequenceID)
	}
}

// TestWorkItem_UnmarshalJSON_MixedShapesNullAndEmpty covers the
// corner cases: null values, empty arrays, and unknown keys in the
// object form. These are all observed in real Plane responses (REST
// returns null for unset state on certain endpoints; webhooks ship
// extra description_* fields).
func TestWorkItem_UnmarshalJSON_MixedShapesNullAndEmpty(t *testing.T) {
	t.Parallel()

	const payload = `{
		"id": "abc",
		"name": "n",
		"state": null,
		"labels": [],
		"assignees": null
	}`
	var wi WorkItem
	if err := json.Unmarshal([]byte(payload), &wi); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if wi.State.ID != "" {
		t.Errorf("State.ID = %q, want empty for null state", wi.State.ID)
	}
	if len(wi.Labels) != 0 {
		t.Errorf("Labels = %+v, want empty", wi.Labels)
	}
	if len(wi.Assignees) != 0 {
		t.Errorf("Assignees = %+v, want empty", wi.Assignees)
	}
}
