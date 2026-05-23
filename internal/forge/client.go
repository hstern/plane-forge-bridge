package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultUserAgent is sent on every outbound request when Client.UserAgent
// is empty. Forgejo/Gitea log the value in their access logs, so a
// recognisable agent helps with troubleshooting at the source.
const defaultUserAgent = "plane-forge-bridge"

// maxResponseBytes caps the response body we will read from the forge.
// Comment and issue responses are tiny but a misconfigured reverse proxy
// could feed us anything; the limit keeps a single bad response from
// exhausting memory.
const maxResponseBytes = 4 << 20 // 4 MiB

// maxErrorBodyBytes is the cap on the body slice we include in *APIError.
// 4 KiB is enough to fit Forgejo's typical JSON error envelope while
// keeping logs grep-able.
const maxErrorBodyBytes = 4 << 10 // 4 KiB

// Client is the Gitea/Forgejo REST v1 client. It is safe for concurrent
// use across goroutines as long as HTTPClient is not mutated after
// construction.
//
// BaseURL should be the root of the forge installation (e.g.
// "https://codeberg.org" or "https://gitea.example.com"); Token is a
// personal access token sent as "Authorization: token <T>" — Forgejo's
// and Gitea's docs explicitly use the "token" scheme, NOT "Bearer".
// HTTPClient may be nil, in which case a client with a 30 s timeout is
// used at call time. UserAgent, if empty, defaults to
// "plane-forge-bridge".
type Client struct {
	// BaseURL is the forge root, e.g. "https://codeberg.org" or
	// "https://gitea.example.com". Trailing slash is trimmed by NewClient.
	BaseURL string

	// Token is the personal access token used as "Authorization: token <T>".
	Token string

	// UserAgent overrides the default User-Agent header when set.
	UserAgent string

	// HTTPClient is the underlying transport. If nil, a client with a sane
	// 30-second timeout is used at call time.
	HTTPClient *http.Client
}

// NewClient constructs a Client with a default 30-second timeout when hc
// is nil. The BaseURL is normalized by stripping any trailing slash so
// callers can safely concatenate "/api/v1/..." paths.
func NewClient(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: hc,
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

// GetIssue returns a single issue by its (1-based) number in owner/repo.
// Returns ErrNotFound on 404 — callers distinguish "no such issue" from
// "forge is broken".
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int64) (*Issue, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d", owner, repo, number)
	var out Issue
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// CreateComment posts a new comment on issue number `issueNumber` in
// owner/repo. The Gitea/Forgejo API path is
// POST /repos/{owner}/{repo}/issues/{index}/comments.
func (c *Client) CreateComment(ctx context.Context, owner, repo string, issueNumber int64, req CreateCommentRequest) (*Comment, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", owner, repo, issueNumber)
	var out Comment
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateComment patches an existing comment by its forge ID.
// PATCH /repos/{owner}/{repo}/issues/comments/{id}.
func (c *Client) UpdateComment(ctx context.Context, owner, repo string, commentID int64, req UpdateCommentRequest) (*Comment, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/comments/%d", owner, repo, commentID)
	var out Comment
	if err := c.do(ctx, http.MethodPatch, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteComment removes a comment by its forge ID.
// DELETE /repos/{owner}/{repo}/issues/comments/{id}.
//
// A 204 response is treated as success; a 404 maps to ErrNotFound so
// callers can treat "already gone" as idempotent; any other non-2xx
// becomes *APIError.
func (c *Client) DeleteComment(ctx context.Context, owner, repo string, commentID int64) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/comments/%d", owner, repo, commentID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// do is the shared request/response plumbing. body may be nil; out may
// be nil.
//
// It sets Authorization, Accept, User-Agent on every request, and
// Content-Type when there is a body to send. Non-2xx responses become
// *APIError; 5xx and network errors propagate unchanged.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("forge: marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("forge: new request: %w", err)
	}
	// Forgejo and Gitea both document the "token" auth scheme for personal
	// access tokens (not "Bearer"); using "Bearer" silently fails on some
	// versions, so this is a regression test target.
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("forge: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the response body we will buffer so a misconfigured proxy cannot
	// feed us megabytes of garbage. Real forge responses are well under
	// the limit.
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("forge: read response: %w", err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return fmt.Errorf("forge: %s %s: response exceeds %d bytes", method, path, maxResponseBytes)
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
		return fmt.Errorf("forge: decode %s %s response: %w", method, path, err)
	}
	return nil
}
