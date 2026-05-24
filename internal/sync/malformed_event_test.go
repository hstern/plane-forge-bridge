package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// These tests pin the contract that bad-payload errors wrap
// ErrMalformedEvent so the server can map them to HTTP 422. Without the
// wrapping the server defaults to 500 and the forge/Plane side retries
// the same malformed delivery forever.

func TestHandleForgeIssue_NilIssue_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueOpened, testForgeUser)
	evt.Issue = nil

	_, err := e.HandleForgeIssue(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestHandleForgeIssue_EditedNilIssue_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueEdited, testForgeUser)
	evt.Issue = nil

	_, err := e.HandleForgeIssue(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestHandleForgeIssue_ClosedNilIssue_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	evt := mkIssueEvent(forge.EventIssueClosed, testForgeUser)
	evt.Issue = nil

	_, err := e.HandleForgeIssue(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestHandleForgeComment_NilComment_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	evt := mkCommentEvent(forge.EventIssueCommentCreated, testForgeUser, "hello")
	evt.Comment = nil

	_, err := e.HandleForgeComment(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestHandleForgePullRequest_NilPR_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	enablePRAutomation(e)
	evt := mkPREvent(forge.EventPullRequestOpened, "[PFB-1] x", "", "pfb-1-x", false)
	evt.PullRequest = nil

	_, err := e.HandleForgePullRequest(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestHandlePlaneComment_NilComment_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t)
	evt := mkPlaneCommentEvent(plane.EventCommentCreated)
	evt.Comment = nil

	_, err := e.HandlePlaneComment(context.Background(), evt)
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestParseExternalRef_EmptySource_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseExternalRef("", "42")
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}

func TestParseExternalRef_EmptyID_WrapsMalformedEvent(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseExternalRef("forge:acme/widgets", "")
	if !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("err = %v, want ErrMalformedEvent wrap", err)
	}
}
