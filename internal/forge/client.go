package forge

import (
	"net/http"
	"strings"
	"time"
)

// Client is a thin stub for the outbound Gitea/Forgejo REST v1 client. The
// real methods (create issue, update issue, create comment, set labels, ...)
// land in a later step; this struct exists so the rest of the codebase can
// already depend on the type.
type Client struct {
	// BaseURL is the forge root, e.g. "https://codeberg.org" or
	// "https://gitea.example.com". Trailing slash is trimmed by New.
	BaseURL string

	// Token is the personal access token used as "Authorization: token <T>".
	Token string

	// HTTPClient is the underlying transport. If nil, a client with a sane
	// 30-second timeout is used.
	HTTPClient *http.Client
}

// NewClient constructs a Client with a default 30-second timeout when hc is
// nil. The BaseURL is normalized by stripping any trailing slash so callers
// can safely concatenate "/api/v1/..." paths.
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
