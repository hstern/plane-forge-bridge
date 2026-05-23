package plane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient wires a Client to a test server with sensible defaults.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		BaseURL:       srv.URL,
		WorkspaceSlug: "acme",
		APIKey:        "test-token",
		HTTPClient:    srv.Client(),
	}
}

// assertCommonHeaders checks the headers every request must carry.
func assertCommonHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got, want := r.Header.Get("X-Api-Key"), "test-token"; got != want {
		t.Errorf("X-Api-Key header = %q, want %q", got, want)
	}
	if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept header = %q, want application/json", got)
	}
	if got := r.Header.Get("User-Agent"); got == "" {
		t.Error("User-Agent header is empty")
	}
}

func TestCreateIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   CreateIssueRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode req body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(WorkItem{
			ID:       "issue-uuid",
			Name:     gotBody.Name,
			State:    gotBody.StateID,
			Priority: gotBody.Priority,
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateIssue(context.Background(), "proj-1", CreateIssueRequest{
		Name:           "Fix the thing",
		Priority:       "high",
		ExternalSource: "forgejo",
		ExternalID:     "42",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/issues/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Name != "Fix the thing" || gotBody.Priority != "high" {
		t.Errorf("body = %+v, want Name=Fix the thing Priority=high", gotBody)
	}
	if gotBody.ExternalSource != "forgejo" || gotBody.ExternalID != "42" {
		t.Errorf("external ref not posted: %+v", gotBody)
	}
	if got.ID != "issue-uuid" {
		t.Errorf("returned ID = %q, want issue-uuid", got.ID)
	}
}

func TestCreateIssue_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"name is required"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateIssue(context.Background(), "proj-1", CreateIssueRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "name is required") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
	if apiErr.Method != http.MethodPost {
		t.Errorf("Method = %q, want POST", apiErr.Method)
	}
	if !strings.Contains(apiErr.Error(), "422") {
		t.Errorf("Error() = %q, want it to include 422", apiErr.Error())
	}
}

func TestUpdateIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkItem{ID: "issue-uuid", Name: "Updated"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	name := "Updated"
	got, err := c.UpdateIssue(context.Background(), "proj-1", "issue-uuid", UpdateIssueRequest{Name: &name})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/issues/issue-uuid/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if got.Name != "Updated" {
		t.Errorf("returned Name = %q, want Updated", got.Name)
	}
}

func TestUpdateIssue_OnlyChangedFields(t *testing.T) {
	t.Parallel()

	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkItem{ID: "issue-uuid"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	stateID := "state-uuid"
	_, err := c.UpdateIssue(context.Background(), "proj-1", "issue-uuid", UpdateIssueRequest{StateID: &stateID})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	if len(raw) != 1 {
		t.Errorf("body keys = %v, want exactly 1 (state)", keys(raw))
	}
	if got, ok := raw["state"]; !ok || got != "state-uuid" {
		t.Errorf("state field = %v (present=%v), want state-uuid", got, ok)
	}
}

func TestGetIssueByExternalRef_Found(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkItem{ID: "issue-uuid", Name: "Mirrored"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.GetIssueByExternalRef(context.Background(), "proj-1", "forgejo", "42")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef: %v", err)
	}
	if got.ID != "issue-uuid" {
		t.Errorf("ID = %q, want issue-uuid", got.ID)
	}
	if !strings.Contains(gotQuery, "external_source=forgejo") || !strings.Contains(gotQuery, "external_id=42") {
		t.Errorf("query = %q, want it to include external_source=forgejo and external_id=42", gotQuery)
	}
}

func TestGetIssueByExternalRef_NotFound(t *testing.T) {
	t.Parallel()

	// Belt-and-braces case: if Plane ever changes the contract to return
	// HTTP 200 with an empty paginated result instead of 404, our client
	// must still surface ErrNotFound. We exercise that by decoding into
	// WorkItem from an empty object — ID will be the zero value.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.GetIssueByExternalRef(context.Background(), "proj-1", "forgejo", "no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetIssueByExternalRef_404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"Not found."}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.GetIssueByExternalRef(context.Background(), "proj-1", "forgejo", "no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// And we should NOT surface an *APIError for the 404 — callers
	// distinguish "not yet mirrored" (ErrNotFound) from "Plane is broken".
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("404 was surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
	}
}

func TestListProjectStates(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
            "results": [
                {"id":"s1","name":"Backlog","group":"backlog","color":"#cccccc"},
                {"id":"s2","name":"Done","group":"completed","color":"#22aa22"}
            ]
        }`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.ListProjectStates(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("ListProjectStates: %v", err)
	}
	if want := "/workspaces/acme/projects/proj-1/states/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(got) != 2 || got[0].ID != "s1" || got[1].Group != "completed" {
		t.Errorf("states = %+v, want [s1/backlog s2/completed]", got)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	t.Parallel()

	// The handler blocks until the test signals release (or 5 s elapse, as
	// a belt-and-braces fail-safe). The client context is cancelled while
	// the handler is blocked; the request must return promptly with
	// context.Canceled.
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	c := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	var gotErr error
	go func() {
		defer wg.Done()
		_, gotErr = c.CreateIssue(ctx, "proj-1", CreateIssueRequest{Name: "blocked"})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never received the request")
	}
	cancel()
	wg.Wait()

	if gotErr == nil {
		t.Fatal("expected an error from cancelled context, got nil")
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", gotErr)
	}
}

func TestClient_NonJSONErrorBody(t *testing.T) {
	t.Parallel()

	// A proxy or WAF in front of Plane could return a text/html error page.
	// We must still surface an *APIError with the body preserved and not
	// panic trying to decode it as JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<html><body>upstream connect error</body></html>")
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateIssue(context.Background(), "proj-1", CreateIssueRequest{Name: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "upstream connect error") {
		t.Errorf("Body = %q, want the HTML body preserved", apiErr.Body)
	}
}

func TestClient_UserAgentOverride(t *testing.T) {
	t.Parallel()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	c.UserAgent = "plane-forge-bridge/1.2.3"
	if _, err := c.ListProjectStates(context.Background(), "proj-1"); err != nil {
		t.Fatalf("ListProjectStates: %v", err)
	}
	if gotUA != "plane-forge-bridge/1.2.3" {
		t.Errorf("User-Agent = %q, want plane-forge-bridge/1.2.3", gotUA)
	}
}

func TestGetIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkItem{
			ID:   "issue-uuid",
			Name: "Mirrored",
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.GetIssue(context.Background(), "proj-1", "issue-uuid")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/issues/issue-uuid/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if got.ID != "issue-uuid" || got.Name != "Mirrored" {
		t.Errorf("WorkItem = %+v, want {ID:issue-uuid Name:Mirrored}", got)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"Not found."}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.GetIssue(context.Background(), "proj-1", "no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// As with GetIssueByExternalRef, callers must not need to dig into
	// *APIError for the 404 → not-found case.
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("404 surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
	}
}

func TestCreateComment_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   CreateCommentRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode req body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CommentResponse{
			ID:          "comment-uuid",
			IssueID:     "issue-uuid",
			Project:     "proj-1",
			CommentHTML: gotBody.CommentHTML,
			Access:      "EXTERNAL",
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateComment(context.Background(), "proj-1", "issue-uuid", CreateCommentRequest{
		CommentHTML: "<p>hi</p>",
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/issues/issue-uuid/comments/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.CommentHTML != "<p>hi</p>" {
		t.Errorf("posted body = %+v, want CommentHTML=<p>hi</p>", gotBody)
	}
	if got.ID != "comment-uuid" || got.CommentHTML != "<p>hi</p>" {
		t.Errorf("CommentResponse = %+v, want {ID:comment-uuid CommentHTML:<p>hi</p>}", got)
	}
}

func TestCreateComment_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"Invalid HTML passed"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateComment(context.Background(), "proj-1", "issue-uuid", CreateCommentRequest{
		CommentHTML: "<not-html",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "Invalid HTML passed") {
		t.Errorf("Body = %q, want upstream message", apiErr.Body)
	}
}

func TestUpdateComment_OnlyChangedFields(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		raw       map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CommentResponse{ID: "comment-uuid"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	html := "<p>edited</p>"
	got, err := c.UpdateComment(context.Background(), "proj-1", "issue-uuid", "comment-uuid", UpdateCommentRequest{
		CommentHTML: &html,
	})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/issues/issue-uuid/comments/comment-uuid/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(raw) != 1 {
		t.Errorf("body keys = %v, want exactly 1 (comment_html)", keys(raw))
	}
	if v, ok := raw["comment_html"]; !ok || v != "<p>edited</p>" {
		t.Errorf("comment_html = %v (present=%v), want <p>edited</p>", v, ok)
	}
	if got.ID != "comment-uuid" {
		t.Errorf("returned ID = %q, want comment-uuid", got.ID)
	}
}

func TestDeleteComment_HappyPath(t *testing.T) {
	t.Parallel()

	t.Run("204_no_content", func(t *testing.T) {
		t.Parallel()
		var gotMethod, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			assertCommonHeaders(t, r)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		c := newTestClient(t, srv)
		if err := c.DeleteComment(context.Background(), "proj-1", "issue-uuid", "comment-uuid"); err != nil {
			t.Fatalf("DeleteComment: %v", err)
		}
		if gotMethod != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", gotMethod)
		}
		if want := "/workspaces/acme/projects/proj-1/issues/issue-uuid/comments/comment-uuid/"; gotPath != want {
			t.Errorf("path = %q, want %q", gotPath, want)
		}
	})

	t.Run("404_returns_ErrNotFound", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"Not found."}`)
		}))
		t.Cleanup(srv.Close)

		c := newTestClient(t, srv)
		err := c.DeleteComment(context.Background(), "proj-1", "issue-uuid", "gone")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			t.Errorf("404 surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
		}
	})
}

func TestDeleteComment_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	err := c.DeleteComment(context.Background(), "proj-1", "issue-uuid", "comment-uuid")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
}

func TestListProjectLabels_HappyPath(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
            "results": [
                {"id":"l1","name":"bug","color":"#ff0000","description":"defects"},
                {"id":"l2","name":"enhancement","color":"#00ff00"}
            ]
        }`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.ListProjectLabels(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("ListProjectLabels: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/labels/"; gotPath != want {
		t.Errorf("path = %q, want %q (trailing slash is mandatory)", gotPath, want)
	}
	if len(got) != 2 {
		t.Fatalf("labels = %+v, want 2 rows", got)
	}
	if got[0].ID != "l1" || got[0].Name != "bug" || got[0].Color != "#ff0000" || got[0].Description != "defects" {
		t.Errorf("labels[0] = %+v, want {ID:l1 Name:bug Color:#ff0000 Description:defects}", got[0])
	}
	if got[1].ID != "l2" || got[1].Name != "enhancement" || got[1].Description != "" {
		t.Errorf("labels[1] = %+v, want {ID:l2 Name:enhancement Description:\"\"}", got[1])
	}
}

func TestListProjectLabels_Empty(t *testing.T) {
	t.Parallel()

	// Plane returns the paginated envelope with an empty "results" array
	// when a project has no labels. We decode that into a nil slice — the
	// zero value of []Label — because Go's json package leaves a missing
	// or empty JSON array as a nil slice and we don't allocate one
	// ourselves. Callers should use len(got) rather than == nil.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.ListProjectLabels(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("ListProjectLabels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("labels = %+v, want empty", got)
	}
}

func TestListProjectLabels_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.ListProjectLabels(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if apiErr.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", apiErr.Method)
	}
}

func TestCreateProjectLabel_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   CreateLabelRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode req body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Label{
			ID:          "label-uuid",
			Name:        gotBody.Name,
			Color:       gotBody.Color,
			Description: gotBody.Description,
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateProjectLabel(context.Background(), "proj-1", CreateLabelRequest{
		Name:  "bug",
		Color: "#ff0000",
	})
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/workspaces/acme/projects/proj-1/labels/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Name != "bug" || gotBody.Color != "#ff0000" {
		t.Errorf("posted body = %+v, want Name=bug Color=#ff0000", gotBody)
	}
	if got.ID != "label-uuid" || got.Name != "bug" || got.Color != "#ff0000" {
		t.Errorf("Label = %+v, want {ID:label-uuid Name:bug Color:#ff0000}", got)
	}
}

func TestCreateProjectLabel_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"name is required"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateProjectLabel(context.Background(), "proj-1", CreateLabelRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "name is required") {
		t.Errorf("Body = %q, want upstream message preserved", apiErr.Body)
	}
}

func TestGetIssueBySequenceID_HappyPath(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkItem{
			ID:         "issue-uuid",
			Name:       "Fix the thing",
			SequenceID: 123,
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.GetIssueBySequenceID(context.Background(), "PFB", 123)
	if err != nil {
		t.Fatalf("GetIssueBySequenceID: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	// Public v1 has no "?sequence_id=" filter on the per-project list
	// endpoint; the lookup goes through the workspace-level work-items
	// endpoint, which encodes the (project_identifier, sequence_id) pair
	// in the URL path rather than as query params. Source:
	// apps/api/plane/api/urls/work_item.py (the work-item-by-identifier
	// route) and apps/api/plane/api/views/issue.py
	// (WorkspaceIssueAPIEndpoint.get).
	if want := "/workspaces/acme/work-items/PFB-123/"; gotPath != want {
		t.Errorf("path = %q, want %q (trailing slash is mandatory)", gotPath, want)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty (sequence_id is in the path, not query)", gotQuery)
	}
	if got.ID != "issue-uuid" || got.SequenceID != 123 {
		t.Errorf("WorkItem = %+v, want {ID:issue-uuid SequenceID:123}", got)
	}
}

func TestGetIssueBySequenceID_NotFound(t *testing.T) {
	t.Parallel()

	t.Run("404_returns_ErrNotFound", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"The requested resource does not exist."}`)
		}))
		t.Cleanup(srv.Close)

		c := newTestClient(t, srv)
		_, err := c.GetIssueBySequenceID(context.Background(), "PFB", 9999)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		// Callers must not need to dig into *APIError for the 404 case.
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			t.Errorf("404 surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
		}
	})

	t.Run("empty_body_returns_ErrNotFound", func(t *testing.T) {
		t.Parallel()
		// Belt-and-braces: if Plane ever changes the contract to return
		// HTTP 200 with an empty object instead of a 404, our client must
		// still surface ErrNotFound rather than a zero-value WorkItem.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		}))
		t.Cleanup(srv.Close)

		c := newTestClient(t, srv)
		_, err := c.GetIssueBySequenceID(context.Background(), "PFB", 9999)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestGetIssueBySequenceID_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.GetIssueBySequenceID(context.Background(), "PFB", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if apiErr.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", apiErr.Method)
	}
}

// keys returns the sorted keys of m for use in error messages.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
