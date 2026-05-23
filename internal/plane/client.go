package plane

import "net/http"

// Client is a placeholder for the Plane REST API client. Methods will be
// added in later steps (issue create/update, comment create/update). It is
// declared here so the wider bridge can depend on the type as it grows.
//
// BaseURL should be the root of the Plane installation (e.g.
// "https://plane.example.com"); WorkspaceSlug is the workspace path
// component; APIKey is a personal/workspace API token sent via
// X-API-Key. HTTPClient may be nil, in which case http.DefaultClient is
// used at call time.
type Client struct {
	BaseURL       string
	WorkspaceSlug string
	APIKey        string
	HTTPClient    *http.Client
}

// NewClient constructs a Client. It does no I/O; callers should validate
// the arguments themselves if needed.
func NewClient(baseURL, workspaceSlug, apiKey string, httpClient *http.Client) *Client {
	return &Client{
		BaseURL:       baseURL,
		WorkspaceSlug: workspaceSlug,
		APIKey:        apiKey,
		HTTPClient:    httpClient,
	}
}
