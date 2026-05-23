package forge

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
		BaseURL:    srv.URL,
		Token:      "test-token",
		HTTPClient: srv.Client(),
	}
}

// assertCommonHeaders checks the headers every request must carry.
func assertCommonHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got, want := r.Header.Get("Authorization"), "token test-token"; got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept header = %q, want application/json", got)
	}
	if got := r.Header.Get("User-Agent"); got == "" {
		t.Error("User-Agent header is empty")
	}
}

func TestGetIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Issue{
			ID:     1234,
			Number: 7,
			Title:  "Hello",
			State:  "open",
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.GetIssue(context.Background(), "acme", "widgets", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/issues/7"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if got.ID != 1234 || got.Number != 7 || got.Title != "Hello" {
		t.Errorf("issue = %+v, want ID=1234 Number=7 Title=Hello", got)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"issue does not exist"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.GetIssue(context.Background(), "acme", "widgets", 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// And we should NOT surface an *APIError for the 404 — callers
	// distinguish "no such issue" (ErrNotFound) from "forge is broken".
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("404 was surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
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
		_ = json.NewEncoder(w).Encode(Comment{
			ID:   42,
			Body: gotBody.Body,
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateComment(context.Background(), "acme", "widgets", 7, CreateCommentRequest{Body: "Hi there"})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/issues/7/comments"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Body != "Hi there" {
		t.Errorf("body = %+v, want Body=Hi there", gotBody)
	}
	if got.ID != 42 || got.Body != "Hi there" {
		t.Errorf("returned comment = %+v, want ID=42 Body=Hi there", got)
	}
}

func TestCreateComment_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"body is required"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateComment(context.Background(), "acme", "widgets", 7, CreateCommentRequest{})
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
	if !strings.Contains(apiErr.Body, "body is required") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
	if apiErr.Method != http.MethodPost {
		t.Errorf("Method = %q, want POST", apiErr.Method)
	}
	if !strings.Contains(apiErr.Error(), "422") {
		t.Errorf("Error() = %q, want it to include 422", apiErr.Error())
	}
}

func TestUpdateComment_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   UpdateCommentRequest
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
		_ = json.NewEncoder(w).Encode(Comment{ID: 42, Body: gotBody.Body})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.UpdateComment(context.Background(), "acme", "widgets", 42, UpdateCommentRequest{Body: "edited"})
	if err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/issues/comments/42"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Body != "edited" {
		t.Errorf("body = %+v, want Body=edited", gotBody)
	}
	if got.Body != "edited" {
		t.Errorf("returned comment = %+v, want Body=edited", got)
	}
}

func TestDeleteComment_HappyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		wantErr  error
		wantAPI  bool
		wantCode int
	}{
		{name: "204 no content", status: http.StatusNoContent, wantErr: nil},
		{name: "404 → ErrNotFound", status: http.StatusNotFound, wantErr: ErrNotFound},
		{name: "500 → APIError", status: http.StatusInternalServerError, wantAPI: true, wantCode: 500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				gotMethod string
				gotPath   string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				assertCommonHeaders(t, r)
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)

			c := newTestClient(t, srv)
			err := c.DeleteComment(context.Background(), "acme", "widgets", 42)

			if gotMethod != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", gotMethod)
			}
			if want := "/api/v1/repos/acme/widgets/issues/comments/42"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}

			switch {
			case tc.wantAPI:
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("err = %v (type %T), want *APIError", err, err)
				}
				if apiErr.StatusCode != tc.wantCode {
					t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.wantCode)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("err = %v, want %v", err, tc.wantErr)
				}
			default:
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			}
		})
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
		_, gotErr = c.CreateComment(ctx, "acme", "widgets", 7, CreateCommentRequest{Body: "blocked"})
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

	// A proxy or WAF in front of the forge could return a text/html error
	// page. We must still surface an *APIError with the body preserved
	// and not panic trying to decode it as JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<html><body>upstream connect error</body></html>")
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateComment(context.Background(), "acme", "widgets", 7, CreateCommentRequest{Body: "x"})
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

func TestClient_UserAgentDefaultAndOverride(t *testing.T) {
	t.Parallel()

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Issue{ID: 1, Number: 1})
	}))
	t.Cleanup(srv.Close)

	// Default user-agent.
	c := newTestClient(t, srv)
	if _, err := c.GetIssue(context.Background(), "acme", "widgets", 1); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if gotUA != defaultUserAgent {
		t.Errorf("default User-Agent = %q, want %q", gotUA, defaultUserAgent)
	}

	// Overridden user-agent.
	c.UserAgent = "plane-forge-bridge/1.2.3"
	if _, err := c.GetIssue(context.Background(), "acme", "widgets", 1); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if gotUA != "plane-forge-bridge/1.2.3" {
		t.Errorf("override User-Agent = %q, want plane-forge-bridge/1.2.3", gotUA)
	}
}

func TestListRepoLabels_HappyPath(t *testing.T) {
	t.Parallel()

	// Forge list endpoints return a bare JSON array, NOT a {results: [...]}
	// envelope. This test pins that contract — if the client ever grows an
	// envelope unwrap, this is the regression target.
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		assertCommonHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Label{
			{ID: 1, Name: "bug", Color: "#ee0701"},
			{ID: 2, Name: "enhancement", Color: "#84b6eb"},
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.ListRepoLabels(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("ListRepoLabels: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/labels"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "page=1&limit=50"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if len(got) != 2 {
		t.Fatalf("len(labels) = %d, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].Name != "bug" || got[0].Color != "#ee0701" {
		t.Errorf("labels[0] = %+v, want {ID:1 Name:bug Color:#ee0701}", got[0])
	}
	if got[1].ID != 2 || got[1].Name != "enhancement" {
		t.Errorf("labels[1] = %+v, want {ID:2 Name:enhancement ...}", got[1])
	}
}

func TestListRepoLabels_Empty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.ListRepoLabels(context.Background(), "acme", "widgets")
	if err != nil {
		t.Fatalf("ListRepoLabels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(labels) = %d, want 0 (got %+v)", len(got), got)
	}
}

func TestListRepoLabels_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"db is down"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.ListRepoLabels(context.Background(), "acme", "widgets")
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
	if !strings.Contains(apiErr.Body, "db is down") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
	if apiErr.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", apiErr.Method)
	}
}

func TestCreateRepoLabel_HappyPath(t *testing.T) {
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
			ID:    99,
			Name:  gotBody.Name,
			Color: gotBody.Color,
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateRepoLabel(context.Background(), "acme", "widgets", CreateLabelRequest{
		Name:        "needs-triage",
		Color:       "#00ff00",
		Description: "fresh from the bridge",
	})
	if err != nil {
		t.Fatalf("CreateRepoLabel: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/labels"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Name != "needs-triage" || gotBody.Color != "#00ff00" || gotBody.Description != "fresh from the bridge" {
		t.Errorf("body = %+v, want Name=needs-triage Color=#00ff00 Description=fresh from the bridge", gotBody)
	}
	if got.ID != 99 || got.Name != "needs-triage" || got.Color != "#00ff00" {
		t.Errorf("returned label = %+v, want ID=99 Name=needs-triage Color=#00ff00", got)
	}
}

func TestCreateRepoLabel_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"label name already exists"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateRepoLabel(context.Background(), "acme", "widgets", CreateLabelRequest{
		Name:  "bug",
		Color: "#ee0701",
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
	if !strings.Contains(apiErr.Body, "label name already exists") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
}

func TestCreateRepoLabel_RequiredFields(t *testing.T) {
	t.Parallel()

	// Pin the wire shape: Gitea/Forgejo require `name` and `color`; an
	// empty description must be omitted (json:"description,omitempty")
	// rather than sent as an empty string. This is what every upstream
	// example in the API docs shows and a few older Gitea versions
	// reject an explicit empty description.
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		rawBody = b
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Label{ID: 1, Name: "bug", Color: "#ee0701"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	if _, err := c.CreateRepoLabel(context.Background(), "acme", "widgets", CreateLabelRequest{
		Name:  "bug",
		Color: "#ee0701",
	}); err != nil {
		t.Fatalf("CreateRepoLabel: %v", err)
	}

	// Decode into a map so we can assert key presence/absence.
	var asMap map[string]any
	if err := json.Unmarshal(rawBody, &asMap); err != nil {
		t.Fatalf("unmarshal sent body %q: %v", rawBody, err)
	}
	if got := asMap["name"]; got != "bug" {
		t.Errorf(`body["name"] = %v, want "bug"`, got)
	}
	if got := asMap["color"]; got != "#ee0701" {
		t.Errorf(`body["color"] = %v, want "#ee0701"`, got)
	}
	if _, present := asMap["description"]; present {
		t.Errorf("body contains description key when value was empty; raw=%q", rawBody)
	}
}

func TestCreateIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		rawBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		rawBody = b
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Issue{
			ID:     555,
			Number: 12,
			Title:  "Bridge: investigate flaky e2e",
			Body:   "saw it twice this week",
			State:  "open",
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	got, err := c.CreateIssue(context.Background(), "acme", "widgets", CreateIssueRequest{
		Title:     "Bridge: investigate flaky e2e",
		Body:      "saw it twice this week",
		Labels:    []int64{1, 2},
		Assignees: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/issues"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}

	// Decode into a map to assert the wire shape: labels must be a JSON
	// array of *numbers* (forge IDs), NOT strings.
	var asMap map[string]any
	if err := json.Unmarshal(rawBody, &asMap); err != nil {
		t.Fatalf("unmarshal sent body %q: %v", rawBody, err)
	}
	if asMap["title"] != "Bridge: investigate flaky e2e" {
		t.Errorf(`body["title"] = %v, want the issue title`, asMap["title"])
	}
	if asMap["body"] != "saw it twice this week" {
		t.Errorf(`body["body"] = %v, want the issue body`, asMap["body"])
	}
	labels, ok := asMap["labels"].([]any)
	if !ok {
		t.Fatalf(`body["labels"] = %v (type %T), want a JSON array`, asMap["labels"], asMap["labels"])
	}
	if len(labels) != 2 {
		t.Fatalf("len(labels) = %d, want 2", len(labels))
	}
	for i, lbl := range labels {
		if _, isNum := lbl.(float64); !isNum {
			t.Errorf("labels[%d] = %v (type %T), want number (forge label ID)", i, lbl, lbl)
		}
	}
	assignees, ok := asMap["assignees"].([]any)
	if !ok || len(assignees) != 1 || assignees[0] != "alice" {
		t.Errorf(`body["assignees"] = %v, want ["alice"]`, asMap["assignees"])
	}

	if got.ID != 555 || got.Number != 12 || got.Title != "Bridge: investigate flaky e2e" {
		t.Errorf("issue = %+v, want ID=555 Number=12 Title=Bridge: investigate flaky e2e", got)
	}
}

func TestCreateIssue_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"title is required"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	_, err := c.CreateIssue(context.Background(), "acme", "widgets", CreateIssueRequest{})
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
	if !strings.Contains(apiErr.Body, "title is required") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
	if apiErr.Method != http.MethodPost {
		t.Errorf("Method = %q, want POST", apiErr.Method)
	}
}

func TestCreateIssue_RequiredFields(t *testing.T) {
	t.Parallel()

	// Pin the wire shape: only `title` is required upstream. Empty body,
	// nil/empty labels, and nil/empty assignees must be omitted from the
	// JSON (json:"...,omitempty") rather than sent as empty values.
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		rawBody = b
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Issue{ID: 1, Number: 1, Title: "only-title"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	if _, err := c.CreateIssue(context.Background(), "acme", "widgets", CreateIssueRequest{
		Title: "only-title",
	}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(rawBody, &asMap); err != nil {
		t.Fatalf("unmarshal sent body %q: %v", rawBody, err)
	}
	if got := asMap["title"]; got != "only-title" {
		t.Errorf(`body["title"] = %v, want "only-title"`, got)
	}
	for _, key := range []string{"body", "labels", "assignees"} {
		if _, present := asMap[key]; present {
			t.Errorf("body contains %q key when value was empty; raw=%q", key, rawBody)
		}
	}
}

func TestUpdateIssue_HappyPath(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		rawBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		assertCommonHeaders(t, r)
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		rawBody = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Issue{
			ID:     555,
			Number: 12,
			Title:  "edited title",
			State:  "open",
		})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	title := "edited title"
	body := "edited body"
	labels := []int64{3, 4}
	assignees := []string{"bob"}
	got, err := c.UpdateIssue(context.Background(), "acme", "widgets", 12, UpdateIssueRequest{
		Title:     &title,
		Body:      &body,
		Labels:    &labels,
		Assignees: &assignees,
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if want := "/api/v1/repos/acme/widgets/issues/12"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}

	var asMap map[string]any
	if err := json.Unmarshal(rawBody, &asMap); err != nil {
		t.Fatalf("unmarshal sent body %q: %v", rawBody, err)
	}
	if asMap["title"] != "edited title" {
		t.Errorf(`body["title"] = %v, want "edited title"`, asMap["title"])
	}
	if asMap["body"] != "edited body" {
		t.Errorf(`body["body"] = %v, want "edited body"`, asMap["body"])
	}
	gotLabels, ok := asMap["labels"].([]any)
	if !ok || len(gotLabels) != 2 {
		t.Fatalf(`body["labels"] = %v, want 2-element array`, asMap["labels"])
	}
	for i, lbl := range gotLabels {
		if _, isNum := lbl.(float64); !isNum {
			t.Errorf("labels[%d] = %v (type %T), want number (forge label ID)", i, lbl, lbl)
		}
	}

	if got.ID != 555 || got.Number != 12 || got.Title != "edited title" {
		t.Errorf("issue = %+v, want ID=555 Number=12 Title=\"edited title\"", got)
	}
}

func TestUpdateIssue_StateCloseOnly(t *testing.T) {
	t.Parallel()

	// An UpdateIssueRequest with only State set must produce a body
	// containing only "state" — every other field is a pointer with
	// json:"...,omitempty" so a nil value drops it from the wire. This
	// guarantees the bridge can flip state without inadvertently
	// blanking the title/body/labels/assignees.
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		rawBody = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Issue{ID: 555, Number: 12, State: "closed"})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	state := "closed"
	if _, err := c.UpdateIssue(context.Background(), "acme", "widgets", 12, UpdateIssueRequest{
		State: &state,
	}); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(rawBody, &asMap); err != nil {
		t.Fatalf("unmarshal sent body %q: %v", rawBody, err)
	}
	if got := asMap["state"]; got != "closed" {
		t.Errorf(`body["state"] = %v, want "closed"`, got)
	}
	if len(asMap) != 1 {
		t.Errorf("body has %d keys, want exactly 1 (state); raw=%q", len(asMap), rawBody)
	}
	for _, key := range []string{"title", "body", "labels", "assignees"} {
		if _, present := asMap[key]; present {
			t.Errorf("body contains %q key when pointer was nil; raw=%q", key, rawBody)
		}
	}
}

func TestUpdateIssue_NotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"issue does not exist"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	state := "closed"
	_, err := c.UpdateIssue(context.Background(), "acme", "widgets", 999, UpdateIssueRequest{State: &state})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// And we should NOT surface an *APIError for the 404 — callers
	// distinguish "no such issue" (ErrNotFound) from "forge is broken".
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("404 was surfaced as *APIError (%v); want sentinel ErrNotFound only", apiErr)
	}
}

func TestUpdateIssue_APIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"db is down"}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	state := "closed"
	_, err := c.UpdateIssue(context.Background(), "acme", "widgets", 12, UpdateIssueRequest{State: &state})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (type %T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "db is down") {
		t.Errorf("Body = %q, want it to include the upstream message", apiErr.Body)
	}
	if apiErr.Method != http.MethodPatch {
		t.Errorf("Method = %q, want PATCH", apiErr.Method)
	}
}

func TestClient_AuthorizationToken(t *testing.T) {
	t.Parallel()

	// Regression guard: Forgejo/Gitea document the "token" scheme, NOT
	// "Bearer". Sending "Bearer ..." silently fails on some versions, so
	// this test pins the exact header value the client must produce.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Issue{ID: 1, Number: 1})
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv)
	if _, err := c.GetIssue(context.Background(), "acme", "widgets", 1); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if gotAuth != "token test-token" {
		t.Errorf("Authorization = %q, want exactly %q (not Bearer)", gotAuth, "token test-token")
	}
}
