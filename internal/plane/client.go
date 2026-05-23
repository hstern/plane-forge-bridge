package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultUserAgent is sent on every outbound request when Client.UserAgent
// is empty. Plane shows the value in its access logs, so a recognisable
// agent helps with troubleshooting at the source.
const defaultUserAgent = "plane-forge-bridge"

// maxResponseBytes caps the response body we will read from Plane. Plane's
// responses are tiny but a misconfigured reverse proxy could feed us
// anything; the limit keeps a single bad response from exhausting memory.
const maxResponseBytes = 4 << 20 // 4 MiB

// maxErrorBodyBytes is the cap on the body slice we include in *APIError.
// 4 KiB is enough to fit Plane's typical JSON error envelope while keeping
// logs grep-able.
const maxErrorBodyBytes = 4 << 10 // 4 KiB

// Client is the Plane REST API client. It is safe for concurrent use across
// goroutines as long as HTTPClient is not mutated after construction.
//
// BaseURL should be the root of the Plane installation (e.g.
// "https://plane.example.com"); WorkspaceSlug is the workspace path
// component; APIKey is a personal/workspace API token. HTTPClient may be
// nil, in which case a client with a 30 s timeout is used at call time.
// UserAgent, if empty, defaults to "plane-forge-bridge".
type Client struct {
	BaseURL       string
	WorkspaceSlug string
	APIKey        string
	UserAgent     string
	HTTPClient    *http.Client
}

// NewClient constructs a Client. It does no I/O; callers should validate
// the arguments themselves if needed. If httpClient is nil the Client uses
// a default http.Client with a 30 s timeout on its first call.
func NewClient(baseURL, workspaceSlug, apiKey string, httpClient *http.Client) *Client {
	return &Client{
		BaseURL:       baseURL,
		WorkspaceSlug: workspaceSlug,
		APIKey:        apiKey,
		HTTPClient:    httpClient,
	}
}

// httpClient returns the HTTPClient field if set, otherwise a default
// http.Client with a 30 s timeout. We do not mutate c.HTTPClient because
// Client is shared across goroutines.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// userAgent returns the configured User-Agent or the default.
func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return defaultUserAgent
}

// CreateIssue posts a new work item under projectID. Returns the created
// item or an error. Authorization: Bearer token from Client.APIKey.
func (c *Client) CreateIssue(ctx context.Context, projectID string, req CreateIssueRequest) (*WorkItem, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/", c.WorkspaceSlug, projectID)
	var out WorkItem
	if err := c.do(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateIssue patches an existing work item. Pointer fields in
// UpdateIssueRequest let the caller patch only what's set.
func (c *Client) UpdateIssue(ctx context.Context, projectID, issueID string, req UpdateIssueRequest) (*WorkItem, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/%s/", c.WorkspaceSlug, projectID, issueID)
	var out WorkItem
	if err := c.do(ctx, http.MethodPatch, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetIssueByExternalRef looks up a work item by its external_source +
// external_id pair. Returns (nil, ErrNotFound) when no match exists, which
// is the normal "we haven't mirrored this forge issue yet" case.
//
// Plane's list endpoint is overloaded: when both external_id and
// external_source are present it short-circuits to a single Issue.objects.get
// and returns the bare serialized object (HTTP 200) or a 404 if no row
// matches. We do not need to inspect the paginated "results" branch here.
func (c *Client) GetIssueByExternalRef(ctx context.Context, projectID, source, externalID string) (*WorkItem, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/", c.WorkspaceSlug, projectID)
	q := url.Values{}
	q.Set("external_source", source)
	q.Set("external_id", externalID)
	var out WorkItem
	if err := c.do(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Defensive: if Plane ever changes the contract to return a paginated
	// list with results when a filter matches nothing, treat an empty ID as
	// not-found rather than silently returning a zero-value WorkItem.
	if out.ID == "" {
		return nil, ErrNotFound
	}
	return &out, nil
}

// GetIssue returns a single work item by its Plane UUID. Useful for the
// plane→forge direction: when a comment event arrives, the bridge looks
// up the work item to read its external_source / external_id and figure
// out which forge repo + issue to comment on. Returns ErrNotFound on 404.
func (c *Client) GetIssue(ctx context.Context, projectID, issueID string) (*WorkItem, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/%s/", c.WorkspaceSlug, projectID, issueID)
	var out WorkItem
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// CreateComment posts a new comment on a work item.
func (c *Client) CreateComment(ctx context.Context, projectID, issueID string, req CreateCommentRequest) (*CommentResponse, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/%s/comments/", c.WorkspaceSlug, projectID, issueID)
	var out CommentResponse
	if err := c.do(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateComment patches an existing comment. The pointer field in
// UpdateCommentRequest lets the caller patch only what's set.
func (c *Client) UpdateComment(ctx context.Context, projectID, issueID, commentID string, req UpdateCommentRequest) (*CommentResponse, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/%s/comments/%s/", c.WorkspaceSlug, projectID, issueID, commentID)
	var out CommentResponse
	if err := c.do(ctx, http.MethodPatch, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteComment removes a comment. Returns ErrNotFound on 404 so callers
// can treat "already gone" as a no-op in the plane→forge mirror path.
func (c *Client) DeleteComment(ctx context.Context, projectID, issueID, commentID string) error {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/issues/%s/comments/%s/", c.WorkspaceSlug, projectID, issueID, commentID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// ListProjectStates returns the workflow states defined on a project. Used
// by callers that need to translate a forge state ("open"/"closed") to a
// Plane state UUID via the configured state_map. The endpoint is paginated;
// we read the first page only because Plane projects rarely have more than
// a handful of states.
func (c *Client) ListProjectStates(ctx context.Context, projectID string) ([]State, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/states/", c.WorkspaceSlug, projectID)
	var out statesListResponse
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// ListProjectLabels returns the labels defined on a project. Used by
// callers that need to translate a forge label name to a Plane label UUID
// (or discover that no such label exists yet so they can create it). The
// endpoint is paginated through Plane's BasePaginator; we read the first
// page only, by the same reasoning as ListProjectStates — Plane projects
// rarely carry more than a handful of labels.
func (c *Client) ListProjectLabels(ctx context.Context, projectID string) ([]Label, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/labels/", c.WorkspaceSlug, projectID)
	var out labelsListResponse
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// CreateProjectLabel creates a new label scoped to projectID. Returns the
// created Label including its server-assigned UUID. Plane responds with
// HTTP 409 (not 422) when a label with the same name already exists in the
// project; callers that want "get-or-create" should list first or inspect
// the *APIError.StatusCode on the way out.
func (c *Client) CreateProjectLabel(ctx context.Context, projectID string, req CreateLabelRequest) (*Label, error) {
	path := fmt.Sprintf("/workspaces/%s/projects/%s/labels/", c.WorkspaceSlug, projectID)
	var out Label
	if err := c.do(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do is the shared request/response plumbing. body may be nil; out may be
// nil. Query params are added to the URL if q is non-nil.
//
// It sets Authorization, Accept, User-Agent on every request, and
// Content-Type when there is a body to send. Non-2xx responses become
// *APIError; 5xx and network errors propagate unchanged.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("plane: marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("plane: new request: %w", err)
	}
	// Plane's APIKeyAuthentication middleware reads X-Api-Key for
	// personal/workspace API tokens (see makeplane/plane
	// apps/api/plane/api/middleware/api_authentication.py). Authorization
	// Bearer is for OAuth access tokens only. Sending both is harmless and
	// makes the client work against either deployment.
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("plane: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the response body we will buffer so a misconfigured proxy cannot
	// feed us megabytes of garbage. Plane's real responses are well under
	// the limit.
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("plane: read response: %w", err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return fmt.Errorf("plane: %s %s: response exceeds %d bytes", method, path, maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyStr := string(respBody)
		if len(bodyStr) > maxErrorBodyBytes {
			bodyStr = bodyStr[:maxErrorBodyBytes]
		}
		return &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       bodyStr,
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("plane: decode %s %s response: %w", method, path, err)
	}
	return nil
}
