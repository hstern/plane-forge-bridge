package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

const (
	testForgeSecret = "forge-secret"
	testPlaneSecret = "plane-secret"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &mapping.Resolved{Listen: ":0"}
	cfg.Forge.WebhookSecret = testForgeSecret
	cfg.Plane.WebhookSecret = testPlaneSecret
	cfg.Idemp.LRUCapacity = 64
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(cfg, logger, nil)
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "ok" {
		t.Errorf("body: got %q want %q", got, "ok")
	}
}

// loadFixture returns the bytes of a sibling package's testdata fixture.
// Tests run from the package directory; rel is relative to internal/server/.
func loadFixture(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return data
}

func TestForgeWebhook_HappyPath(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-abc-123")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d (body=%s)", rr.Code, http.StatusAccepted, rr.Body.String())
	}
}

func TestForgeWebhook_BadSignature(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, "deadbeef")
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-abc-123")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestForgeWebhook_MissingSignature(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-abc-123")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestForgeWebhook_UnsupportedEvent(t *testing.T) {
	s := newTestServer(t)
	body := []byte(`{}`)
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "create") // not in our supported set
	req.Header.Set(forge.HeaderDelivery, "delivery-xyz")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusNoContent)
	}
}

func TestForgeWebhook_LoopBreakMarkerDropsEvent(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	// Inject our loop-break marker into the issue body.
	body = injectIssueBody(t, body, "issue text\n\n<!-- pfb:src=plane,evt=plane-evt-1 -->\n")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-loop")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d (dropped events should respond 200, not 202)", rr.Code, http.StatusOK)
	}
}

func TestForgeWebhook_BodyTooLarge(t *testing.T) {
	s := newTestServer(t)
	big := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	sig := sign(testForgeSecret, big)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(big))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-big")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestPlaneWebhook_HappyPath(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../plane/testdata/work_item_created.json")
	sig := sign(testPlaneSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/plane/webhook", bytes.NewReader(body))
	req.Header.Set(plane.HeaderSignature, sig)
	req.Header.Set(plane.HeaderEvent, "issue")
	req.Header.Set(plane.HeaderDelivery, "plane-delivery-1")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d (body=%s)", rr.Code, http.StatusAccepted, rr.Body.String())
	}
}

func TestPlaneWebhook_BadSignature(t *testing.T) {
	s := newTestServer(t)
	body := loadFixture(t, "../plane/testdata/work_item_created.json")
	req := httptest.NewRequest(http.MethodPost, "/plane/webhook", bytes.NewReader(body))
	req.Header.Set(plane.HeaderSignature, "deadbeef")
	req.Header.Set(plane.HeaderEvent, "issue")
	req.Header.Set(plane.HeaderDelivery, "plane-delivery-2")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/forge/webhook", nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// injectIssueBody rewrites the issue.body field of a fixture JSON payload.
// Returns the re-encoded bytes. Used to plant a loop-break marker without
// editing the fixture file.
func injectIssueBody(t *testing.T, raw []byte, newBody string) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	issue, ok := payload["issue"].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no .issue object")
	}
	issue["body"] = newBody
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return out
}
