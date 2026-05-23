package sync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// newLabelTestEngine returns a minimally-wired Engine for label-resolver
// tests. The PlaneClient is the only dependency the resolver touches, so
// links/users/bot are left zero.
func newLabelTestEngine(t *testing.T) (*Engine, *fakeClient) {
	t.Helper()
	fc := &fakeClient{}
	return &Engine{Client: fc, Log: testLogger()}, fc
}

func TestResolveLabels_HappyPathAllExist(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{
			{ID: "u-bug", Name: "bug"},
			{ID: "u-help", Name: "help wanted"},
		}, nil
	}

	got, err := e.resolveLabels(context.Background(), "proj-X", []string{"bug", "help wanted"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"u-bug", "u-help"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d]=%q, want %q", i, got[i], w)
		}
	}
	if len(fc.CreateProjectLabelCalls) != 0 {
		t.Errorf("CreateProjectLabel must not run on the all-exist path; got %d", len(fc.CreateProjectLabelCalls))
	}
}

func TestResolveLabels_AutoCreatesMissing(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{{ID: "u-bug", Name: "bug"}}, nil
	}
	fc.CreateProjectLabelFunc = func(_ context.Context, _ string, req plane.CreateLabelRequest) (*plane.Label, error) {
		return &plane.Label{ID: "u-new-" + req.Name, Name: req.Name}, nil
	}

	got, err := e.resolveLabels(context.Background(), "proj-X", []string{"bug", "feature", "docs"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"u-bug", "u-new-feature", "u-new-docs"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d]=%q, want %q", i, got[i], w)
		}
	}
	if len(fc.CreateProjectLabelCalls) != 2 {
		t.Errorf("CreateProjectLabel calls=%d, want 2", len(fc.CreateProjectLabelCalls))
	}
}

func TestResolveLabels_EmptyNames_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)

	got, err := e.resolveLabels(context.Background(), "proj-X", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	// No list, no create — empty input must not trip any API call.
	if len(fc.ListProjectLabelsCalls)+len(fc.CreateProjectLabelCalls) != 0 {
		t.Errorf("empty input made API calls: list=%d create=%d",
			len(fc.ListProjectLabelsCalls), len(fc.CreateProjectLabelCalls))
	}
}

func TestResolveLabels_CacheHitOnSecondCall(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return []plane.Label{{ID: "u-bug", Name: "bug"}}, nil
	}

	if _, err := e.resolveLabels(context.Background(), "proj-X", []string{"bug"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := e.resolveLabels(context.Background(), "proj-X", []string{"bug"}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(fc.ListProjectLabelsCalls) != 1 {
		t.Errorf("ListProjectLabels calls=%d, want 1 (second call must hit cache)",
			len(fc.ListProjectLabelsCalls))
	}
}

func TestResolveLabels_NewlyCreatedLabelInCache(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		return nil, nil
	}
	fc.CreateProjectLabelFunc = func(_ context.Context, _ string, req plane.CreateLabelRequest) (*plane.Label, error) {
		return &plane.Label{ID: "u-new", Name: req.Name}, nil
	}

	// First call: list (empty) + create.
	if _, err := e.resolveLabels(context.Background(), "proj-X", []string{"feature"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(fc.CreateProjectLabelCalls) != 1 {
		t.Fatalf("first call CreateProjectLabel=%d", len(fc.CreateProjectLabelCalls))
	}

	// Second call: same name, served from cache, no new create.
	got, err := e.resolveLabels(context.Background(), "proj-X", []string{"feature"})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(got) != 1 || got[0] != "u-new" {
		t.Errorf("second call result=%v", got)
	}
	if len(fc.CreateProjectLabelCalls) != 1 {
		t.Errorf("CreateProjectLabel should not run again; got %d total", len(fc.CreateProjectLabelCalls))
	}
}

// TestResolveLabels_ConcurrentSameProject_OneList is the thundering-herd
// guard: N goroutines resolving labels on the same project must serialise
// on the per-project mutex, so ListProjectLabels runs exactly once.
func TestResolveLabels_ConcurrentSameProject_OneList(t *testing.T) {
	t.Parallel()
	e, fc := newLabelTestEngine(t)
	var calls atomic.Int64
	fc.ListProjectLabelsFunc = func(_ context.Context, _ string) ([]plane.Label, error) {
		calls.Add(1)
		return []plane.Label{{ID: "u-bug", Name: "bug"}}, nil
	}

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, err := e.resolveLabels(context.Background(), "proj-X", []string{"bug"}); err != nil {
				t.Errorf("resolveLabels: %v", err)
			}
		}()
	}
	wg.Wait()
	if c := calls.Load(); c != 1 {
		t.Errorf("ListProjectLabels ran %d times, want exactly 1", c)
	}
}
