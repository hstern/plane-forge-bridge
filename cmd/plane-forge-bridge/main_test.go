package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `
listen: "127.0.0.1:0"
log_level: info
forge:
  base_url: https://git.example.org
  token_env: TEST_FORGE_TOKEN
  webhook_secret_env: TEST_FORGE_WEBHOOK_SECRET
plane:
  base_url: https://app.example.org/api/v1
  workspace_slug: example
  api_key_env: TEST_PLANE_API_KEY
  webhook_secret_env: TEST_PLANE_WEBHOOK_SECRET
bridge_bot:
  forge_username: bridge-bot
  plane_member_id: 00000000-0000-0000-0000-000000000000
links: []
users: {}
`

func TestVersionFlag(t *testing.T) {
	stdout, stderr := pipePair(t)
	defer stdout.Close()
	defer stderr.Close()
	if err := run([]string{"--version"}, stdout, stderr); err != nil {
		t.Fatalf("run --version: %v", err)
	}
}

func TestRun_MissingConfig(t *testing.T) {
	stdout, stderr := pipePair(t)
	defer stdout.Close()
	defer stderr.Close()
	err := run([]string{"--config", "/nonexistent/path/to/config.yaml"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention 'load config', got: %v", err)
	}
}

func TestRun_BadFlag(t *testing.T) {
	stdout, stderr := pipePair(t)
	defer stdout.Close()
	defer stderr.Close()
	err := run([]string{"--no-such-flag"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("PFB_TEST_KEY", "from-env")
	if got := envOr("PFB_TEST_KEY", "fallback"); got != "from-env" {
		t.Errorf("envOr: got %q want %q", got, "from-env")
	}
	t.Setenv("PFB_TEST_EMPTY", "")
	if got := envOr("PFB_TEST_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("envOr empty: got %q want %q (empty should yield fallback)", got, "fallback")
	}
	if got := envOr("PFB_TEST_UNSET_KEY_XYZ", "fallback"); got != "fallback" {
		t.Errorf("envOr unset: got %q want %q", got, "fallback")
	}
}

func TestNewLogger_LevelParsing(t *testing.T) {
	cases := []string{"debug", "info", "warn", "error", "DEBUG", "garbage"}
	for _, c := range cases {
		stdout, _ := pipePair(t)
		_ = newLogger(c, stdout) // should not panic for any input
		stdout.Close()
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen: not-a-host-port\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	stdout, stderr := pipePair(t)
	defer stdout.Close()
	defer stderr.Close()
	if err := run([]string{"--config", cfgPath}, stdout, stderr); err == nil {
		t.Fatal("expected error for invalid config, got nil")
	}
}

// pipePair returns two os.File handles backed by os.Pipe so the run() function
// (which takes *os.File) can be exercised without touching the real stdout/stderr.
// The read sides are discarded — we only care that writes don't panic and that
// run returns the right error.
func pipePair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { rOut.Close(); rErr.Close() })
	// Drain the read sides in the background so writers don't block.
	go func() { _, _ = io.Copy(io.Discard, rOut) }()
	go func() { _, _ = io.Copy(io.Discard, rErr) }()
	return wOut, wErr
}
