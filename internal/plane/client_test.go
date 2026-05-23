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

// keys returns the sorted keys of m for use in error messages.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
