// Package server wires the HTTP routes (/forge/webhook, /plane/webhook,
// /healthz) onto the verified+parsed events from internal/forge and
// internal/plane, then hands forge issue events to a Translator
// (internal/sync) for outbound writes to Plane.
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/idemp"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
	pfbsync "github.com/hstern/plane-forge-bridge/internal/sync"
)

// Translator is the surface server uses to translate parsed webhook events
// into outbound calls on the other side. Production wires this to
// *sync.Engine; tests substitute a hand-written fake. Methods are scoped
// by direction (forge→plane vs plane→forge) and event family (issue vs
// comment) so a fake can stub only what a test exercises.
type Translator interface {
	HandleForgeIssue(ctx context.Context, evt *forge.Event) (*pfbsync.Outcome, error)
	HandleForgeComment(ctx context.Context, evt *forge.Event) (*pfbsync.Outcome, error)
	HandleForgePullRequest(ctx context.Context, evt *forge.Event) (*pfbsync.Outcome, error)
	HandlePlaneWorkItem(ctx context.Context, evt *plane.Event) (*pfbsync.Outcome, error)
	HandlePlaneComment(ctx context.Context, evt *plane.Event) (*pfbsync.Outcome, error)
}

// maxBodyBytes caps webhook bodies. Gitea/Forgejo and Plane payloads are well
// under this in practice — the cap is defence-in-depth against a misconfigured
// proxy fanning a giant body at the handler.
const maxBodyBytes = 1 << 20 // 1 MiB

// Server is the HTTP server fronting both webhook endpoints. Construct with
// New; the returned value implements http.Handler.
type Server struct {
	cfg        *mapping.Resolved
	log        *slog.Logger
	mux        *http.ServeMux
	dedupe     *idemp.LRU
	translator Translator
	clock      func() time.Time
}

// New returns a Server wired to cfg, logger, and a freshly-sized loop-break
// LRU. The translator may be nil — handlers will then verify+log+202 without
// outbound calls (useful in tests and during smoke deploys).
func New(cfg *mapping.Resolved, log *slog.Logger, translator Translator) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:        cfg,
		log:        log,
		mux:        http.NewServeMux(),
		dedupe:     idemp.NewLRU(cfg.Idemp.LRUCapacity),
		translator: translator,
		clock:      time.Now,
	}
	s.mux.HandleFunc("POST /forge/webhook", s.handleForge)
	s.mux.HandleFunc("POST /plane/webhook", s.handlePlane)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s
}

// ServeHTTP delegates to the internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleForge(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	evt, err := forge.VerifyAndParse(s.cfg.Forge.WebhookSecret, r.Header, body)
	if err != nil {
		s.respondVerifyParseErr(w, r, "forge", err)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "forge webhook accepted",
		slog.String("kind", string(evt.Kind)),
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("repo", evt.Repo.FullName),
		slog.String("sender", evt.Sender.Login),
	)
	if marker, present := s.markerFromForge(evt); present {
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "dropping forge webhook with loop-break marker",
			slog.String("delivery_id", evt.DeliveryID),
			slog.String("marker_src", string(marker.Source)),
			slog.String("marker_event_id", marker.EventID),
		)
		w.WriteHeader(http.StatusOK)
		return
	}
	if s.translator != nil {
		switch {
		case isForgeIssueEvent(evt.Kind):
			s.dispatchForgeIssue(w, r, evt)
			return
		case isForgeCommentEvent(evt.Kind):
			s.dispatchForgeComment(w, r, evt)
			return
		case isForgePullRequestEvent(evt.Kind):
			s.dispatchForgePullRequest(w, r, evt)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// respondTranslatorErr writes the right HTTP response for a translator
// error. ErrMalformedEvent → 422 (the webhook signature was valid but the
// payload semantics were broken — the forge/Plane side should NOT retry,
// since a retry of the same bytes will produce the same bad result).
// Everything else → 500 (transient or programmer error; retry is fine).
func (s *Server) respondTranslatorErr(w http.ResponseWriter, r *http.Request, side, deliveryID string, terr error) {
	code := http.StatusInternalServerError
	msg := "translator error"
	level := slog.LevelError
	if errors.Is(terr, pfbsync.ErrMalformedEvent) {
		code = http.StatusUnprocessableEntity
		msg = "malformed event"
		level = slog.LevelWarn
	}
	s.log.LogAttrs(r.Context(), level, "translator failed",
		slog.String("side", side),
		slog.String("delivery_id", deliveryID),
		slog.Int("status", code),
		slog.String("err", terr.Error()),
	)
	http.Error(w, msg, code)
}

func (s *Server) dispatchForgeIssue(w http.ResponseWriter, r *http.Request, evt *forge.Event) {
	outcome, terr := s.translator.HandleForgeIssue(r.Context(), evt)
	if terr != nil {
		s.respondTranslatorErr(w, r, "forge issue", evt.DeliveryID, terr)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "forge issue translated",
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("action", actionString(outcome.Action)),
		slog.String("work_item_id", outcome.WorkItemID),
		slog.String("reason", outcome.Reason),
	)
	if outcome.WorkItemID != "" {
		s.dedupe.Record(idemp.SourceForge, evt.DeliveryID, outcome.WorkItemID)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) dispatchForgeComment(w http.ResponseWriter, r *http.Request, evt *forge.Event) {
	outcome, terr := s.translator.HandleForgeComment(r.Context(), evt)
	if terr != nil {
		s.respondTranslatorErr(w, r, "forge comment", evt.DeliveryID, terr)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "forge comment translated",
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("action", actionString(outcome.Action)),
		slog.String("work_item_id", outcome.WorkItemID),
		slog.String("comment_id", outcome.CommentID),
		slog.String("reason", outcome.Reason),
	)
	if outcome.CommentID != "" {
		s.dedupe.Record(idemp.SourceForge, evt.DeliveryID, outcome.CommentID)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) dispatchForgePullRequest(w http.ResponseWriter, r *http.Request, evt *forge.Event) {
	outcome, terr := s.translator.HandleForgePullRequest(r.Context(), evt)
	if terr != nil {
		s.respondTranslatorErr(w, r, "forge pull_request", evt.DeliveryID, terr)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "forge pull_request translated",
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("action", actionString(outcome.Action)),
		slog.String("work_item_id", outcome.WorkItemID),
		slog.String("reason", outcome.Reason),
	)
	if outcome.WorkItemID != "" {
		s.dedupe.Record(idemp.SourceForge, evt.DeliveryID, outcome.WorkItemID)
	}
	w.WriteHeader(http.StatusAccepted)
}

// isForgePullRequestEvent reports whether a forge event is a PR-lifecycle
// event the PR translator handles. pull_request_review currently flows
// through too — sync skips it with a deferred-handling reason.
func isForgePullRequestEvent(k forge.EventKind) bool {
	switch k {
	case forge.EventPullRequestOpened, forge.EventPullRequestEdited,
		forge.EventPullRequestClosed, forge.EventPullRequestReopened,
		forge.EventPullRequestReview:
		return true
	default:
		return false
	}
}

// isForgeIssueEvent reports whether a forge event is an issue-lifecycle event.
func isForgeIssueEvent(k forge.EventKind) bool {
	switch k {
	case forge.EventIssueOpened, forge.EventIssueEdited, forge.EventIssueClosed,
		forge.EventIssueReopened:
		return true
	default:
		return false
	}
}

// isForgeCommentEvent reports whether a forge event is an issue-comment event.
func isForgeCommentEvent(k forge.EventKind) bool {
	switch k {
	case forge.EventIssueCommentCreated, forge.EventIssueCommentEdited,
		forge.EventIssueCommentDeleted:
		return true
	default:
		return false
	}
}

// isPlaneCommentEvent reports whether a plane event is a comment event.
func isPlaneCommentEvent(k plane.EventKind) bool {
	switch k {
	case plane.EventCommentCreated, plane.EventCommentUpdated, plane.EventCommentDeleted:
		return true
	default:
		return false
	}
}

func actionString(a pfbsync.Action) string {
	switch a {
	case pfbsync.ActionCreated:
		return "created"
	case pfbsync.ActionUpdated:
		return "updated"
	case pfbsync.ActionSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

func (s *Server) handlePlane(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	evt, err := plane.VerifyAndParse(s.cfg.Plane.WebhookSecret, r.Header, body)
	if err != nil {
		s.respondVerifyParseErr(w, r, "plane", err)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "plane webhook accepted",
		slog.String("kind", string(evt.Kind)),
		slog.String("delivery_id", evt.DeliveryID),
	)
	if marker, present := s.markerFromPlane(evt); present {
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "dropping plane webhook with loop-break marker",
			slog.String("delivery_id", evt.DeliveryID),
			slog.String("marker_src", string(marker.Source)),
			slog.String("marker_event_id", marker.EventID),
		)
		w.WriteHeader(http.StatusOK)
		return
	}
	if s.translator != nil {
		switch {
		case isPlaneWorkItemEvent(evt.Kind):
			s.dispatchPlaneWorkItem(w, r, evt)
			return
		case isPlaneCommentEvent(evt.Kind):
			s.dispatchPlaneComment(w, r, evt)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) dispatchPlaneWorkItem(w http.ResponseWriter, r *http.Request, evt *plane.Event) {
	outcome, terr := s.translator.HandlePlaneWorkItem(r.Context(), evt)
	if terr != nil {
		s.respondTranslatorErr(w, r, "plane work_item", evt.DeliveryID, terr)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "plane work_item translated",
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("action", actionString(outcome.Action)),
		slog.String("work_item_id", outcome.WorkItemID),
		slog.String("reason", outcome.Reason),
	)
	if outcome.WorkItemID != "" {
		s.dedupe.Record(idemp.SourcePlane, evt.DeliveryID, outcome.WorkItemID)
	}
	w.WriteHeader(http.StatusAccepted)
}

func isPlaneWorkItemEvent(k plane.EventKind) bool {
	switch k {
	case plane.EventWorkItemCreated, plane.EventWorkItemUpdated, plane.EventWorkItemDeleted:
		return true
	default:
		return false
	}
}

func (s *Server) dispatchPlaneComment(w http.ResponseWriter, r *http.Request, evt *plane.Event) {
	outcome, terr := s.translator.HandlePlaneComment(r.Context(), evt)
	if terr != nil {
		s.respondTranslatorErr(w, r, "plane comment", evt.DeliveryID, terr)
		return
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "plane comment translated",
		slog.String("delivery_id", evt.DeliveryID),
		slog.String("action", actionString(outcome.Action)),
		slog.String("comment_id", outcome.CommentID),
		slog.String("reason", outcome.Reason),
	)
	if outcome.CommentID != "" {
		s.dedupe.Record(idemp.SourcePlane, evt.DeliveryID, outcome.CommentID)
	}
	w.WriteHeader(http.StatusAccepted)
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func (s *Server) respondVerifyParseErr(w http.ResponseWriter, r *http.Request, side string, err error) {
	switch {
	case errors.Is(err, forge.ErrMissingSignature),
		errors.Is(err, forge.ErrInvalidSignature),
		errors.Is(err, plane.ErrMissingSignature),
		errors.Is(err, plane.ErrInvalidSignature):
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "webhook signature rejected",
			slog.String("side", side),
			slog.String("err", err.Error()),
		)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, forge.ErrMissingEventHeader),
		errors.Is(err, plane.ErrMissingEventHeader):
		http.Error(w, "missing event header", http.StatusBadRequest)
	case errors.Is(err, forge.ErrUnsupportedEvent):
		// Forges legitimately fan out events the operator didn't subscribe
		// to (e.g. push events from a repo whose webhook also covers
		// issues). Log at DEBUG so production isn't flooded.
		s.log.LogAttrs(r.Context(), slog.LevelDebug, "webhook event ignored",
			slog.String("side", side),
			slog.String("err", err.Error()),
		)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, plane.ErrUnsupportedEvent):
		// Plane only sends event types the operator subscribed to, so
		// any unsupported event here means a contract mismatch with
		// real Plane (e.g. an action-verb spelling we don't recognise).
		// Log at INFO so it surfaces at the default log level — silent
		// 204s on a wholly-broken integration are a brutal failure
		// mode. See GH#10 (PFB-22).
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "plane webhook event ignored",
			slog.String("side", side),
			slog.String("err", err.Error()),
		)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, forge.ErrMalformedPayload),
		errors.Is(err, plane.ErrMalformedPayload):
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "webhook payload malformed",
			slog.String("side", side),
			slog.String("err", err.Error()),
		)
		http.Error(w, "malformed payload", http.StatusBadRequest)
	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "webhook handler error",
			slog.String("side", side),
			slog.String("err", err.Error()),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// markerFromForge returns the loop-break marker carried by a forge event's
// body field (issue.Body, comment.Body, PR body). If we wrote that body,
// the marker is present and the event is our own echo — drop it.
func (s *Server) markerFromForge(evt *forge.Event) (idemp.Marker, bool) {
	if evt == nil {
		return idemp.Marker{}, false
	}
	if evt.Comment != nil {
		if m, ok := idemp.Extract(evt.Comment.Body); ok {
			return m, true
		}
	}
	if evt.Issue != nil {
		if m, ok := idemp.Extract(evt.Issue.Body); ok {
			return m, true
		}
	}
	if evt.PullRequest != nil {
		if m, ok := idemp.Extract(evt.PullRequest.Body); ok {
			return m, true
		}
	}
	return idemp.Marker{}, false
}

func (s *Server) markerFromPlane(evt *plane.Event) (idemp.Marker, bool) {
	if evt == nil {
		return idemp.Marker{}, false
	}
	if evt.Comment != nil {
		if m, ok := idemp.Extract(evt.Comment.CommentHTML); ok {
			return m, true
		}
	}
	if evt.WorkItem != nil {
		if m, ok := idemp.Extract(evt.WorkItem.Description); ok {
			return m, true
		}
	}
	return idemp.Marker{}, false
}

// ListenAndServe starts an http.Server on cfg.Listen and blocks until ctx is
// cancelled, then shuts down gracefully. Returns the first non-nil error from
// the server or the shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	hs := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.log.LogAttrs(ctx, slog.LevelInfo, "http server listening",
			slog.String("addr", s.cfg.Listen),
		)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := hs.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}
