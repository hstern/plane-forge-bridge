// Command plane-stub is a recording HTTP service used in e2e-docker tests as
// a stand-in for a real Plane workspace. It records every incoming request
// to its in-memory log and serves minimal canned JSON responses for the
// subset of the Plane REST API that the bridge calls.
//
// It exposes two surfaces:
//
//   - The simulated Plane API under /api/v1/... — any path is accepted and
//     recorded; the response is a minimal JSON object with a fresh UUID.
//   - A control plane under /_/... — used by assert.sh to inspect, reset, or
//     health-check the stub.
//
// This is not a Plane mock for production use. It only records and echoes.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxBodyBytes is the per-request read limit enforced via http.MaxBytesReader.
const maxBodyBytes = 1 << 20 // 1 MiB

// noisyHeaders lists request headers that are dropped from the recording
// because they're protocol noise and would clutter assertions.
var noisyHeaders = map[string]struct{}{
	"Connection": {},
	"Host":       {},
	"User-Agent": {},
}

// recordedCall is one captured request. The JSON shape is part of the
// contract with assert.sh — keep field names stable.
type recordedCall struct {
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	Query      string      `json:"query"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
	ReceivedAt time.Time   `json:"received_at"`
}

// recorder is the goroutine-safe in-memory log of received requests.
type recorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (r *recorder) add(c recordedCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

// snapshot returns a copy of the recorded calls so the caller can serialize
// it without holding the lock.
func (r *recorder) snapshot() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

// newUUID returns a random RFC-4122 v4 UUID string using crypto/rand. We
// avoid a third-party dependency for this single use.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// version 4
	b[6] = (b[6] & 0x0f) | 0x40
	// variant 10
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// filterHeaders returns a copy of h with noisy headers removed.
func filterHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		if _, drop := noisyHeaders[k]; drop {
			continue
		}
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

// recordRequest reads and records the request, returning the body so the
// handler can also use it. On read error it records what it has and returns
// the (possibly truncated) bytes plus the error.
func recordRequest(r *http.Request, rec *recorder, logger *slog.Logger) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Warn("read body", "err", err.Error(), "path", r.URL.Path)
	}
	call := recordedCall{
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		Headers:    filterHeaders(r.Header),
		Body:       string(body),
		ReceivedAt: time.Now().UTC(),
	}
	rec.add(call)
	return body, err
}

// writeJSON writes v as JSON with the given status. Errors are logged but
// don't propagate — the response is already partially committed at that
// point.
func writeJSON(w http.ResponseWriter, status int, v any, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("write json", "err", err.Error())
	}
}

// projectIDFromPath extracts the {project_id} segment from a path shaped
// like /api/v1/workspaces/{slug}/projects/{project_id}/... It returns an
// empty string if the path doesn't match — we don't fail the request.
func projectIDFromPath(path string) string {
	const marker = "/projects/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	rest := path[i+len(marker):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return rest[:j]
	}
	return rest
}

// handleAPI handles every request under /api/v1/... It records the request,
// remembers POSTed work items by external_source+external_id so subsequent
// lookups find them, and returns a minimal JSON body with a fresh UUID.
// Unknown paths get a 404 but are still recorded.
func handleAPI(rec *recorder, store *workItemStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := recordRequest(r, rec, logger)

		if !isKnownAPIPath(r.URL.Path) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"detail": "not found",
				"path":   r.URL.Path,
			}, logger)
			return
		}

		// GETs are lookups. The only one the bridge uses today is
		// GetIssueByExternalRef, which is the issue-list endpoint with
		// ?external_source=X&external_id=Y query params. Serve any work
		// item we remember from a prior POST; otherwise 404.
		if r.Method == http.MethodGet {
			src := r.URL.Query().Get("external_source")
			ext := r.URL.Query().Get("external_id")
			if src != "" && ext != "" {
				if wi, ok := store.get(src, ext); ok {
					writeJSON(w, http.StatusOK, wi, logger)
					return
				}
			}
			writeJSON(w, http.StatusNotFound, map[string]string{
				"detail": "not found",
				"path":   r.URL.Path,
			}, logger)
			return
		}

		id, err := newUUID()
		if err != nil {
			logger.Error("uuid", "err", err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"detail": "uuid generation failed",
			}, logger)
			return
		}

		resp := map[string]any{
			"id":      id,
			"project": projectIDFromPath(r.URL.Path),
		}

		// On POST (create) and PATCH (update), echo title/state/external_*
		// from the request body if it's JSON. POST also stores the work
		// item under (external_source, external_id) so future lookups find
		// it — this mirrors how real Plane behaves on the same endpoint
		// shape used by GetIssueByExternalRef.
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			var parsed map[string]any
			if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
				if name, ok := parsed["name"].(string); ok {
					resp["name"] = name
				}
				if state, ok := parsed["state"].(string); ok {
					resp["state"] = state
				}
				if src, ok := parsed["external_source"].(string); ok && src != "" {
					resp["external_source"] = src
				}
				if ext, ok := parsed["external_id"].(string); ok && ext != "" {
					resp["external_id"] = ext
				}
			}
			if r.Method == http.MethodPost {
				if src, _ := resp["external_source"].(string); src != "" {
					if ext, _ := resp["external_id"].(string); ext != "" {
						store.put(src, ext, resp)
					}
				}
			}
		}

		writeJSON(w, http.StatusOK, resp, logger)
	}
}

// workItemStore is a tiny in-memory index of (external_source, external_id)
// → work item JSON. Populated on POST, queried on GET. Concurrency-safe.
type workItemStore struct {
	mu    sync.Mutex
	items map[string]map[string]any
}

func newWorkItemStore() *workItemStore {
	return &workItemStore{items: make(map[string]map[string]any)}
}

func (s *workItemStore) key(src, ext string) string { return src + "|" + ext }

func (s *workItemStore) put(src, ext string, item map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Defensive copy so subsequent map mutations elsewhere don't leak in.
	cp := make(map[string]any, len(item))
	for k, v := range item {
		cp[k] = v
	}
	s.items[s.key(src, ext)] = cp
}

func (s *workItemStore) get(src, ext string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wi, ok := s.items[s.key(src, ext)]
	if !ok {
		return nil, false
	}
	cp := make(map[string]any, len(wi))
	for k, v := range wi {
		cp[k] = v
	}
	return cp, true
}

func (s *workItemStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]map[string]any)
}

// isKnownAPIPath returns true if path matches one of the Plane endpoints
// the bridge is expected to call. The match is loose — we only care about
// the shape, not the exact UUIDs.
func isKnownAPIPath(path string) bool {
	// Strip trailing slash for matching, but the API conventionally uses
	// trailing slashes, so we accept both.
	p := strings.TrimSuffix(path, "/")
	parts := strings.Split(p, "/")
	// /api/v1/workspaces/{slug}/projects/{pid}/issues             (POST)
	// /api/v1/workspaces/{slug}/projects/{pid}/issues/{iid}       (PATCH)
	// /api/v1/workspaces/{slug}/projects/{pid}/issues/{iid}/comments        (POST)
	// /api/v1/workspaces/{slug}/projects/{pid}/issues/{iid}/comments/{cid}  (PATCH)
	//
	// After splitting on "/", a leading "" appears at index 0:
	// ["", "api", "v1", "workspaces", "{slug}", "projects", "{pid}", "issues", ...]
	if len(parts) < 8 {
		return false
	}
	if parts[1] != "api" || parts[2] != "v1" || parts[3] != "workspaces" || parts[5] != "projects" || parts[7] != "issues" {
		return false
	}
	switch len(parts) {
	case 8: // .../issues
		return true
	case 9: // .../issues/{iid}
		return true
	case 10: // .../issues/{iid}/comments
		return parts[9] == "comments"
	case 11: // .../issues/{iid}/comments/{cid}
		return parts[9] == "comments"
	}
	return false
}

// handleRecorded serves the recorded call log as JSON.
func handleRecorded(rec *recorder, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, rec.snapshot(), logger)
	}
}

// handleReset clears both the recorded call log and the in-memory work
// item store.
func handleReset(rec *recorder, store *workItemStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rec.reset()
		store.reset()
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleHealthz responds 200 OK with body "ok".
func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}
}

// newMux wires up the routes against a fresh recorder + work item store.
// Exposed for tests.
func newMux(rec *recorder, logger *slog.Logger) *http.ServeMux {
	store := newWorkItemStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", handleAPI(rec, store, logger))
	mux.HandleFunc("/_/recorded", handleRecorded(rec, logger))
	mux.HandleFunc("/_/reset", handleReset(rec, store))
	mux.HandleFunc("/_/healthz", handleHealthz())
	return mux
}

// defaultListen returns the listen address, honoring -listen flag first and
// the STUB_LISTEN env var as fallback.
func resolveListen(flagVal, envVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if envVal != "" {
		return envVal
	}
	return ":8080"
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	listenFlag := flag.String("listen", "", "address to listen on (default :8080, env STUB_LISTEN)")
	flag.Parse()

	addr := resolveListen(*listenFlag, os.Getenv("STUB_LISTEN"))

	rec := &recorder{}
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(rec, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
