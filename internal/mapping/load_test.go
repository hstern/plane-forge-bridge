package mapping

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads a testdata YAML file. Fatal on failure — fixtures are
// part of the test contract.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fullEnv is the env lookup the happy-path fixtures expect. Every secret
// env var named in valid.yaml / minimal.yaml resolves here.
func fullEnv(extra map[string]string) func(string) string {
	base := map[string]string{
		"PFB_FORGE_TOKEN":          "forge-token-value",
		"PFB_FORGE_WEBHOOK_SECRET": "forge-webhook-secret",
		"PFB_PLANE_API_KEY":        "plane-api-key-value",
		"PFB_PLANE_WEBHOOK_SECRET": "plane-webhook-secret",
	}
	for k, v := range extra {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadFromReader_HappyPath(t *testing.T) {
	r := bytes.NewReader(fixture(t, "valid.yaml"))
	got, err := LoadFromReader(r, fullEnv(nil))
	if err != nil {
		t.Fatalf("LoadFromReader: %v", err)
	}
	if got.Listen != ":8080" {
		t.Errorf("Listen = %q, want :8080", got.Listen)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", got.LogLevel)
	}
	if got.Forge.BaseURL != "https://git.example.org" {
		t.Errorf("Forge.BaseURL = %q", got.Forge.BaseURL)
	}
	if got.Forge.Token != "forge-token-value" {
		t.Errorf("Forge.Token = %q", got.Forge.Token)
	}
	if got.Forge.WebhookSecret != "forge-webhook-secret" {
		t.Errorf("Forge.WebhookSecret = %q", got.Forge.WebhookSecret)
	}
	if got.Plane.APIKey != "plane-api-key-value" {
		t.Errorf("Plane.APIKey = %q", got.Plane.APIKey)
	}
	if got.Plane.WebhookSecret != "plane-webhook-secret" {
		t.Errorf("Plane.WebhookSecret = %q", got.Plane.WebhookSecret)
	}
	if got.Plane.WorkspaceSlug != "my-workspace" {
		t.Errorf("Plane.WorkspaceSlug = %q", got.Plane.WorkspaceSlug)
	}
	if got.BridgeBot.ForgeUsername != "plane-bridge" {
		t.Errorf("BridgeBot.ForgeUsername = %q", got.BridgeBot.ForgeUsername)
	}
	if got.BridgeBot.PlaneMemberID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("BridgeBot.PlaneMemberID = %q", got.BridgeBot.PlaneMemberID)
	}
	if len(got.Links) != 1 {
		t.Fatalf("len(Links) = %d, want 1", len(got.Links))
	}
	if got.Links[0].ForgeRepo != "my-org/my-repo" {
		t.Errorf("Links[0].ForgeRepo = %q", got.Links[0].ForgeRepo)
	}
	if got.Links[0].StateMap["open"] != "Todo" {
		t.Errorf("Links[0].StateMap[open] = %q", got.Links[0].StateMap["open"])
	}
	if got.Users["alice"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("Users[alice] = %q", got.Users["alice"])
	}
	if got.Idemp.LRUCapacity != 1024 {
		t.Errorf("Idemp.LRUCapacity = %d, want 1024", got.Idemp.LRUCapacity)
	}
}

func TestLoadFromReader_DefaultLRUCapacity(t *testing.T) {
	r := bytes.NewReader(fixture(t, "minimal.yaml"))
	got, err := LoadFromReader(r, fullEnv(nil))
	if err != nil {
		t.Fatalf("LoadFromReader: %v", err)
	}
	if got.Idemp.LRUCapacity != defaultLRUCapacity {
		t.Errorf("Idemp.LRUCapacity = %d, want %d", got.Idemp.LRUCapacity, defaultLRUCapacity)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want info", got.LogLevel)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !errors.Is(err, ErrConfigMissing) {
		t.Fatalf("err = %v, want ErrConfigMissing", err)
	}
}

func TestLoad_PathOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, fixture(t, "valid.yaml"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Load uses os.Getenv; populate the env it needs.
	t.Setenv("PFB_FORGE_TOKEN", "x1")
	t.Setenv("PFB_FORGE_WEBHOOK_SECRET", "x2")
	t.Setenv("PFB_PLANE_API_KEY", "x3")
	t.Setenv("PFB_PLANE_WEBHOOK_SECRET", "x4")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Forge.Token != "x1" {
		t.Errorf("Forge.Token = %q", got.Forge.Token)
	}
}

func TestLoadFromReader_Errors(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		env     func(string) string
		want    error
		errSub  string
	}{
		{
			name:    "malformed yaml",
			fixture: "malformed.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigMalformed,
		},
		{
			name:    "invalid listen",
			fixture: "invalid_listen.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "listen",
		},
		{
			name:    "invalid base url",
			fixture: "invalid_base_url.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "forge.base_url",
		},
		{
			name:    "missing token_env",
			fixture: "missing_token_env.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "forge.token_env",
		},
		{
			name:    "invalid log level",
			fixture: "invalid_log_level.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "log_level",
		},
		{
			name:    "invalid user uuid",
			fixture: "invalid_user_uuid.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "users",
		},
		{
			name:    "invalid link repo",
			fixture: "invalid_link_repo.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "forge_repo",
		},
		{
			name:    "invalid link uuid",
			fixture: "invalid_link_uuid.yaml",
			env:     fullEnv(nil),
			want:    ErrConfigInvalid,
			errSub:  "plane_project_id",
		},
		{
			name:    "secret env unset",
			fixture: "valid.yaml",
			env: func(k string) string {
				if k == "PFB_FORGE_TOKEN" {
					return ""
				}
				return fullEnv(nil)(k)
			},
			want:   ErrSecretMissing,
			errSub: "PFB_FORGE_TOKEN",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFromReader(bytes.NewReader(fixture(t, tc.fixture)), tc.env)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want wrap of %v", err, tc.want)
			}
			if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("err message %q missing substring %q", err.Error(), tc.errSub)
			}
		})
	}
}

func TestLoadFromReader_ReadError(t *testing.T) {
	_, err := LoadFromReader(errReader{}, fullEnv(nil))
	if !errors.Is(err, ErrConfigMalformed) {
		t.Fatalf("err = %v, want ErrConfigMalformed", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestLoadFromReader_NilEnvLookup(t *testing.T) {
	// nil envLookup is treated as "always empty". With valid YAML, we
	// should fail at the secret-resolution stage with ErrSecretMissing —
	// not panic.
	_, err := LoadFromReader(bytes.NewReader(fixture(t, "valid.yaml")), nil)
	if !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("err = %v, want ErrSecretMissing", err)
	}
}

// TestLoadFromReader_EnvOverrides covers every documented PFB_* override.
// Each row mutates one field via the override env var and asserts the
// resolved config saw the change.
func TestLoadFromReader_EnvOverrides(t *testing.T) {
	type assertion func(t *testing.T, r *Resolved)

	tests := []struct {
		name   string
		env    map[string]string
		extra  map[string]string // additional env values needed for secret resolution
		assert assertion
	}{
		{
			name: "PFB_LISTEN",
			env:  map[string]string{"PFB_LISTEN": ":9999"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Listen != ":9999" {
					t.Errorf("Listen = %q", r.Listen)
				}
			},
		},
		{
			name: "PFB_LOG_LEVEL",
			env:  map[string]string{"PFB_LOG_LEVEL": "debug"},
			assert: func(t *testing.T, r *Resolved) {
				if r.LogLevel != "debug" {
					t.Errorf("LogLevel = %q", r.LogLevel)
				}
			},
		},
		{
			name: "PFB_FORGE_BASE_URL",
			env:  map[string]string{"PFB_FORGE_BASE_URL": "https://override.example/forge"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Forge.BaseURL != "https://override.example/forge" {
					t.Errorf("Forge.BaseURL = %q", r.Forge.BaseURL)
				}
			},
		},
		{
			name:  "PFB_FORGE_TOKEN_ENV",
			env:   map[string]string{"PFB_FORGE_TOKEN_ENV": "ALT_FORGE_TOKEN"},
			extra: map[string]string{"ALT_FORGE_TOKEN": "alt-forge-token"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Forge.Token != "alt-forge-token" {
					t.Errorf("Forge.Token = %q (expected resolution via ALT_FORGE_TOKEN)", r.Forge.Token)
				}
			},
		},
		{
			name:  "PFB_FORGE_WEBHOOK_SECRET_ENV",
			env:   map[string]string{"PFB_FORGE_WEBHOOK_SECRET_ENV": "ALT_FORGE_WS"},
			extra: map[string]string{"ALT_FORGE_WS": "alt-forge-ws"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Forge.WebhookSecret != "alt-forge-ws" {
					t.Errorf("Forge.WebhookSecret = %q", r.Forge.WebhookSecret)
				}
			},
		},
		{
			name: "PFB_PLANE_BASE_URL",
			env:  map[string]string{"PFB_PLANE_BASE_URL": "https://override.example/plane"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Plane.BaseURL != "https://override.example/plane" {
					t.Errorf("Plane.BaseURL = %q", r.Plane.BaseURL)
				}
			},
		},
		{
			name: "PFB_PLANE_WORKSPACE_SLUG",
			env:  map[string]string{"PFB_PLANE_WORKSPACE_SLUG": "other-workspace"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Plane.WorkspaceSlug != "other-workspace" {
					t.Errorf("Plane.WorkspaceSlug = %q", r.Plane.WorkspaceSlug)
				}
			},
		},
		{
			name:  "PFB_PLANE_API_KEY_ENV",
			env:   map[string]string{"PFB_PLANE_API_KEY_ENV": "ALT_PLANE_KEY"},
			extra: map[string]string{"ALT_PLANE_KEY": "alt-plane-key"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Plane.APIKey != "alt-plane-key" {
					t.Errorf("Plane.APIKey = %q", r.Plane.APIKey)
				}
			},
		},
		{
			name:  "PFB_PLANE_WEBHOOK_SECRET_ENV",
			env:   map[string]string{"PFB_PLANE_WEBHOOK_SECRET_ENV": "ALT_PLANE_WS"},
			extra: map[string]string{"ALT_PLANE_WS": "alt-plane-ws"},
			assert: func(t *testing.T, r *Resolved) {
				if r.Plane.WebhookSecret != "alt-plane-ws" {
					t.Errorf("Plane.WebhookSecret = %q", r.Plane.WebhookSecret)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]string{}
			for k, v := range tc.env {
				extra[k] = v
			}
			for k, v := range tc.extra {
				extra[k] = v
			}
			env := fullEnv(extra)
			r, err := LoadFromReader(bytes.NewReader(fixture(t, "valid.yaml")), env)
			if err != nil {
				t.Fatalf("LoadFromReader: %v", err)
			}
			tc.assert(t, r)
		})
	}
}

// Sanity check: io.Reader contract — Resolved is independent of the
// reader instance after Load returns.
func TestLoadFromReader_ReaderIsDrained(t *testing.T) {
	r := bytes.NewReader(fixture(t, "valid.yaml"))
	if _, err := LoadFromReader(r, fullEnv(nil)); err != nil {
		t.Fatalf("LoadFromReader: %v", err)
	}
	// After the call, the reader should be at EOF (LoadFromReader uses
	// io.ReadAll under the hood).
	buf := make([]byte, 1)
	n, err := r.Read(buf)
	if n != 0 || err != io.EOF {
		t.Errorf("after Load: n=%d err=%v, want 0, EOF", n, err)
	}
}
