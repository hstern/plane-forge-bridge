package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// testServer returns a started httptest.Server wired to a fresh recorder so
// tests don't share state.
func testServer(t *testing.T) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(newMux(rec, logger))
	t.Cleanup(srv.Close)
	return srv, rec
}

// doJSON sends a JSON request and returns the response status and decoded
// body. It t.Fatals on transport errors.
func doJSON(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "test-key")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func TestCreateIssueReturnsIDAndRecords(t *testing.T) {
	srv, rec := testServer(t)
	const path = "/api/v1/workspaces/acme/projects/proj-1/issues/"
	status, body := doJSON(t, srv, http.MethodPost, path, map[string]string{
		"name":  "hello",
		"state": "backlog",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if id, _ := body["id"].(string); id == "" {
		t.Fatalf("response missing id: %+v", body)
	}
	if name, _ := body["name"].(string); name != "hello" {
		t.Errorf("echo name = %q, want %q", name, "hello")
	}
	if state, _ := body["state"].(string); state != "backlog" {
		t.Errorf("echo state = %q, want %q", state, "backlog")
	}
	if proj, _ := body["project"].(string); proj != "proj-1" {
		t.Errorf("project = %q, want proj-1", proj)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	got := calls[0]
	if got.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.Path != path {
		t.Errorf("path = %q, want %q", got.Path, path)
	}
	if !strings.Contains(got.Body, `"name":"hello"`) {
		t.Errorf("body missing name: %q", got.Body)
	}
	if h := got.Headers.Get("X-Api-Key"); h != "test-key" {
		t.Errorf("X-Api-Key = %q, want test-key", h)
	}
	if got.Headers.Get("User-Agent") != "" {
		t.Errorf("User-Agent should be filtered, got %q", got.Headers.Get("User-Agent"))
	}
}

func TestRecordedEndpointReturnsPriorCalls(t *testing.T) {
	srv, _ := testServer(t)
	const path = "/api/v1/workspaces/acme/projects/proj-1/issues/"
	if status, _ := doJSON(t, srv, http.MethodPost, path, map[string]string{"name": "first"}); status != http.StatusOK {
		t.Fatalf("post status = %d", status)
	}

	resp, err := srv.Client().Get(srv.URL + "/_/recorded")
	if err != nil {
		t.Fatalf("get recorded: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recorded status = %d, want 200", resp.StatusCode)
	}
	var calls []recordedCall
	if err := json.NewDecoder(resp.Body).Decode(&calls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if calls[0].Path != path {
		t.Errorf("path = %q, want %q", calls[0].Path, path)
	}
	if !strings.Contains(calls[0].Body, "first") {
		t.Errorf("body = %q, missing 'first'", calls[0].Body)
	}
	if calls[0].ReceivedAt.IsZero() {
		t.Error("received_at is zero")
	}
}

func TestResetClearsRecordedList(t *testing.T) {
	srv, rec := testServer(t)
	const path = "/api/v1/workspaces/acme/projects/proj-1/issues/"
	doJSON(t, srv, http.MethodPost, path, map[string]string{"name": "x"})
	if len(rec.snapshot()) != 1 {
		t.Fatalf("expected 1 recorded call before reset")
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/_/reset", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reset status = %d, want 204", resp.StatusCode)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("after reset len = %d, want 0", got)
	}
}

func TestUnknownAPIPathReturns404ButRecords(t *testing.T) {
	srv, rec := testServer(t)
	const path = "/api/v1/foo"
	status, body := doJSON(t, srv, http.MethodPost, path, map[string]string{"a": "b"})
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Errorf("missing detail in 404 body: %+v", body)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("recorded %d, want 1", len(calls))
	}
	if calls[0].Path != path {
		t.Errorf("path = %q, want %q", calls[0].Path, path)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := testServer(t)
	resp, err := srv.Client().Get(srv.URL + "/_/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", string(body))
	}
}

func TestConcurrentPOSTsAllRecorded(t *testing.T) {
	srv, rec := testServer(t)
	const path = "/api/v1/workspaces/acme/projects/proj-1/issues/"
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			status, body := doJSON(t, srv, http.MethodPost, path, map[string]any{
				"name": "issue", "i": i,
			})
			if status != http.StatusOK {
				t.Errorf("goroutine %d status = %d", i, status)
				return
			}
			if id, _ := body["id"].(string); id == "" {
				t.Errorf("goroutine %d missing id", i)
			}
		}(i)
	}
	wg.Wait()
	calls := rec.snapshot()
	if len(calls) != n {
		t.Fatalf("recorded %d calls, want %d", len(calls), n)
	}
	for _, c := range calls {
		if c.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", c.Method)
		}
	}
}

func TestIsKnownAPIPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/workspaces/acme/projects/p1/issues/", true},
		{"/api/v1/workspaces/acme/projects/p1/issues", true},
		{"/api/v1/workspaces/acme/projects/p1/issues/i1/", true},
		{"/api/v1/workspaces/acme/projects/p1/issues/i1/comments/", true},
		{"/api/v1/workspaces/acme/projects/p1/issues/i1/comments/c1/", true},
		{"/api/v1/foo", false},
		{"/api/v1/workspaces/acme/things", false},
		{"/api/v1/workspaces/acme/projects/p1/issues/i1/comments/c1/extra", false},
	}
	for _, tc := range cases {
		if got := isKnownAPIPath(tc.path); got != tc.want {
			t.Errorf("isKnownAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// GET on an issues endpoint with external_id/external_source returns 404
// when no matching work item has been POSTed, and 200 with the previously
// POSTed object when one has. This lets the bridge's GetIssueByExternalRef
// take the create path the first time and the update path on re-delivery,
// just like real Plane.
func TestGetExternalRef_NotFoundThenFound(t *testing.T) {
	srv := httptest.NewServer(newMux(&recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	url := srv.URL + "/api/v1/workspaces/ci/projects/abc/issues/?external_id=42&external_source=forge:o/r"

	// Before any POST: 404.
	resp, err := srv.Client().Get(url)
	if err != nil {
		t.Fatalf("get pre-post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("pre-post status = %d, want 404", resp.StatusCode)
	}

	// POST a work item with external_source / external_id.
	createBody := `{"name":"hello","external_source":"forge:o/r","external_id":"42"}`
	postResp, err := srv.Client().Post(srv.URL+"/api/v1/workspaces/ci/projects/abc/issues/", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("post status = %d, want 200", postResp.StatusCode)
	}

	// After POST: 200 with the work item.
	resp2, err := srv.Client().Get(url)
	if err != nil {
		t.Fatalf("get post-post: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("post-post status = %d, want 200", resp2.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["external_id"] != "42" || got["external_source"] != "forge:o/r" {
		t.Errorf("recalled item missing external fields: %+v", got)
	}
	if got["id"] == nil || got["id"] == "" {
		t.Error("recalled item has no id")
	}
}

// Reset clears the work item store so a subsequent lookup misses again.
func TestResetClearsWorkItemStore(t *testing.T) {
	srv := httptest.NewServer(newMux(&recorder{}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	url := srv.URL + "/api/v1/workspaces/ci/projects/abc/issues/?external_id=7&external_source=forge:o/r"
	postBody := `{"external_source":"forge:o/r","external_id":"7"}`
	r1, err := srv.Client().Post(srv.URL+"/api/v1/workspaces/ci/projects/abc/issues/", "application/json", strings.NewReader(postBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	r1.Body.Close()
	r2, err := srv.Client().Post(srv.URL+"/_/reset", "", nil)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	r2.Body.Close()
	r3, err := srv.Client().Get(url)
	if err != nil {
		t.Fatalf("get post-reset: %v", err)
	}
	r3.Body.Close()
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("post-reset status = %d, want 404", r3.StatusCode)
	}
}

func TestResolveListen(t *testing.T) {
	if got := resolveListen("", ""); got != ":8080" {
		t.Errorf("default = %q, want :8080", got)
	}
	if got := resolveListen("", ":9000"); got != ":9000" {
		t.Errorf("env = %q, want :9000", got)
	}
	if got := resolveListen(":7000", ":9000"); got != ":7000" {
		t.Errorf("flag wins = %q, want :7000", got)
	}
}

func TestNewUUIDFormat(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	// 8-4-4-4-12 = 36 chars
	if len(id) != 36 {
		t.Fatalf("len = %d, want 36 (id=%q)", len(id), id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("parts = %d, want 5", len(parts))
	}
	if id[14] != '4' {
		t.Errorf("version nibble = %c, want 4", id[14])
	}
}
