package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/idemp"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	pfbsync "github.com/hstern/plane-forge-bridge/internal/sync"
)

// fakeTranslator records every call and returns whatever the test set up.
type fakeTranslator struct {
	mu    sync.Mutex
	calls []*forge.Event

	// override; nil = return default outcome (Created with WorkItemID="wi-default").
	respond func(evt *forge.Event) (*pfbsync.Outcome, error)
}

func (f *fakeTranslator) HandleForgeIssue(_ context.Context, evt *forge.Event) (*pfbsync.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, evt)
	if f.respond != nil {
		return f.respond(evt)
	}
	return &pfbsync.Outcome{Action: pfbsync.ActionCreated, WorkItemID: "wi-default"}, nil
}

func (f *fakeTranslator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newTestServerWithTranslator(t *testing.T, tr Translator) *Server {
	t.Helper()
	cfg := &mapping.Resolved{Listen: ":0"}
	cfg.Forge.WebhookSecret = testForgeSecret
	cfg.Plane.WebhookSecret = testPlaneSecret
	cfg.Idemp.LRUCapacity = 64
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(cfg, logger, tr)
}

func TestForgeWebhook_DispatchesIssueOpenedToTranslator(t *testing.T) {
	ft := &fakeTranslator{}
	s := newTestServerWithTranslator(t, ft)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-dispatch-1")

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d (body=%s)", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	if ft.callCount() != 1 {
		t.Fatalf("translator calls: got %d want 1", ft.callCount())
	}
}

func TestForgeWebhook_RecordsLRUOnTranslateCreate(t *testing.T) {
	ft := &fakeTranslator{
		respond: func(_ *forge.Event) (*pfbsync.Outcome, error) {
			return &pfbsync.Outcome{Action: pfbsync.ActionCreated, WorkItemID: "wi-abc"}, nil
		},
	}
	s := newTestServerWithTranslator(t, ft)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-lru")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d", rr.Code)
	}
	if !s.dedupe.Seen(idemp.SourceForge, "delivery-lru", "wi-abc") {
		t.Error("LRU should have recorded (forge, delivery-lru, wi-abc)")
	}
}

func TestForgeWebhook_TranslateError_500(t *testing.T) {
	ft := &fakeTranslator{
		respond: func(_ *forge.Event) (*pfbsync.Outcome, error) {
			return nil, errors.New("translator boom")
		},
	}
	s := newTestServerWithTranslator(t, ft)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-boom")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestForgeWebhook_NonIssueEventBypassesTranslator(t *testing.T) {
	ft := &fakeTranslator{}
	s := newTestServerWithTranslator(t, ft)
	body := loadFixture(t, "../forge/testdata/push.json")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "push")
	req.Header.Set(forge.HeaderDelivery, "delivery-push")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusAccepted)
	}
	if ft.callCount() != 0 {
		t.Errorf("translator should not be called for push event; got %d calls", ft.callCount())
	}
}

func TestForgeWebhook_MarkerEvent_BypassesTranslator(t *testing.T) {
	ft := &fakeTranslator{}
	s := newTestServerWithTranslator(t, ft)
	body := loadFixture(t, "../forge/testdata/issues_opened.json")
	body = injectIssueBody(t, body, "issue text\n\n<!-- pfb:src=plane,evt=evt-1 -->\n")
	sig := sign(testForgeSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/forge/webhook", bytes.NewReader(body))
	req.Header.Set(forge.HeaderSignature, sig)
	req.Header.Set(forge.HeaderEvent, "issues")
	req.Header.Set(forge.HeaderDelivery, "delivery-marker")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d (marker should drop before translator)", rr.Code, http.StatusOK)
	}
	if ft.callCount() != 0 {
		t.Errorf("translator should not be called for marker-bearing event; got %d", ft.callCount())
	}
}
