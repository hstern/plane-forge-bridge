package sync

import (
	"strings"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/idemp"
)

func TestRenderComment_Mapped(t *testing.T) {
	t.Parallel()
	got := RenderComment(
		"a comment body",
		"alice", "https://forge.example.com/alice", "acme/widgets",
		"delivery-cmt-1", idemp.SourceForge, true,
	)
	if strings.Contains(got, "Originally posted by") {
		t.Errorf("mapped author should have no preface; got %q", got)
	}
	if !strings.HasPrefix(got, "a comment body") {
		t.Errorf("mapped comment should start with body; got %q", got)
	}
	m, ok := idemp.Extract(got)
	if !ok {
		t.Fatalf("comment missing marker; got %q", got)
	}
	if m.Source != idemp.SourceForge || m.EventID != "delivery-cmt-1" {
		t.Errorf("marker mismatch: %+v", m)
	}
}

func TestRenderComment_Unmapped(t *testing.T) {
	t.Parallel()
	got := RenderComment(
		"a comment body",
		"stranger", "https://forge.example.com/stranger", "acme/widgets",
		"delivery-cmt-2", idemp.SourceForge, false,
	)
	if !strings.HasPrefix(got, "> Originally posted by `stranger` on [acme/widgets](https://forge.example.com/stranger)") {
		t.Errorf("preface mismatch; got %q", got)
	}
	if !strings.Contains(got, "a comment body") {
		t.Errorf("body missing; got %q", got)
	}
	if _, ok := idemp.Extract(got); !ok {
		t.Errorf("marker missing; got %q", got)
	}
}

// TestRenderComment_EmptyBody_StillEmitsMarker covers the deletion-style
// case where the body is empty: we still want the marker so the loop-break
// check on the return path can drop our own write.
func TestRenderComment_EmptyBody_StillEmitsMarker(t *testing.T) {
	t.Parallel()
	got := RenderComment(
		"", "alice", "https://forge.example.com/alice", "acme/widgets",
		"delivery-cmt-3", idemp.SourcePlane, true,
	)
	m, ok := idemp.Extract(got)
	if !ok {
		t.Fatalf("marker missing on empty body; got %q", got)
	}
	if m.Source != idemp.SourcePlane || m.EventID != "delivery-cmt-3" {
		t.Errorf("marker mismatch: %+v", m)
	}
}

// TestRenderComment_PrefaceHelperSharedWithRenderDescription is a
// regression guard: the unmapped-author preface text is shared between
// RenderDescription and RenderComment. If someone changes one and not the
// other, the two outputs diverge — this test fails fast.
func TestRenderComment_PrefaceHelperSharedWithRenderDescription(t *testing.T) {
	t.Parallel()
	const (
		body     = "shared body"
		login    = "stranger"
		htmlURL  = "https://forge.example.com/stranger"
		repo     = "acme/widgets"
		delivery = "delivery-shared"
	)
	desc := RenderDescription(body, login, htmlURL, repo, delivery, false)
	cmt := RenderComment(body, login, htmlURL, repo, delivery, idemp.SourceForge, false)

	// Both should start with the same preface line.
	descPreface, _, _ := strings.Cut(desc, "\n")
	cmtPreface, _, _ := strings.Cut(cmt, "\n")
	if descPreface != cmtPreface {
		t.Errorf("preface drift:\n  description: %q\n  comment:     %q", descPreface, cmtPreface)
	}
}

func TestRenderComment_PlaneSource(t *testing.T) {
	t.Parallel()
	got := RenderComment(
		"from plane", "Alice", "", "acme/widgets",
		"plane-delivery-1", idemp.SourcePlane, true,
	)
	m, ok := idemp.Extract(got)
	if !ok {
		t.Fatalf("marker missing; got %q", got)
	}
	if m.Source != idemp.SourcePlane {
		t.Errorf("expected SourcePlane marker, got %v", m.Source)
	}
	if m.EventID != "plane-delivery-1" {
		t.Errorf("EventID=%q", m.EventID)
	}
}
