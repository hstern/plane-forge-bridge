package sync

import (
	"strings"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/idemp"
)

func TestRenderDescription_Mapped(t *testing.T) {
	t.Parallel()
	got := RenderDescription(
		"forge body here",
		"alice", "https://forge.example.com/alice", "acme/widgets",
		"delivery-001", true,
	)
	if strings.Contains(got, "Originally posted by") {
		t.Errorf("mapped author should have no preface; got %q", got)
	}
	if !strings.HasPrefix(got, "forge body here") {
		t.Errorf("mapped description should start with forge body; got %q", got)
	}
	if _, ok := idemp.Extract(got); !ok {
		t.Errorf("description missing marker; got %q", got)
	}
}

func TestRenderDescription_Unmapped(t *testing.T) {
	t.Parallel()
	got := RenderDescription(
		"forge body here",
		"stranger", "https://forge.example.com/stranger", "acme/widgets",
		"delivery-002", false,
	)
	if !strings.HasPrefix(got, "> Originally posted by `stranger` on [acme/widgets](https://forge.example.com/stranger)") {
		t.Errorf("preface mismatch; got %q", got)
	}
	if !strings.Contains(got, "forge body here") {
		t.Errorf("body missing; got %q", got)
	}
	if _, ok := idemp.Extract(got); !ok {
		t.Errorf("marker missing; got %q", got)
	}
}

func TestRenderDescription_EmptyBody(t *testing.T) {
	t.Parallel()
	got := RenderDescription("", "alice", "https://forge.example.com/alice", "acme/widgets", "delivery-003", true)
	m, ok := idemp.Extract(got)
	if !ok {
		t.Fatalf("marker missing on empty body; got %q", got)
	}
	if m.EventID != "delivery-003" || m.Source != idemp.SourceForge {
		t.Errorf("marker mismatch: %+v", m)
	}
}

// TestRenderDescription_IdempotentMarker confirms that rendering with the
// same delivery ID twice does not produce two markers — the integration
// with idemp.Wrap covers idempotency by exact-match on the trailing marker.
func TestRenderDescription_IdempotentMarker(t *testing.T) {
	t.Parallel()
	first := RenderDescription(
		"forge body", "alice", "https://forge.example.com/alice", "acme/widgets",
		"delivery-004", true,
	)
	// Feeding the rendered output back through Wrap with the same (src, evt)
	// must be a no-op; this is the property RenderDescription relies on.
	second := idemp.Wrap(first, idemp.SourceForge, "delivery-004")
	if first != second {
		t.Errorf("Wrap on already-wrapped body changed it:\n  first  = %q\n  second = %q", first, second)
	}
	// And there is exactly one marker.
	if n := strings.Count(first, "<!-- pfb:src="); n != 1 {
		t.Errorf("expected exactly 1 marker, got %d in %q", n, first)
	}
}
