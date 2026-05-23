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
