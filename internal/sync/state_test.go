package sync

import (
	"context"
	"errors"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

func newStateLink(stateMap map[string]string) *mapping.Link {
	return &mapping.Link{
		ForgeRepo:      testRepo,
		PlaneProjectID: testProjectID,
		StateMap:       stateMap,
	}
}

func TestResolveStateID_HappyPath(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"closed": "Done"})

	id, err := e.ResolveStateID(context.Background(), link, "closed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "state-done" {
		t.Errorf("got %q, want state-done", id)
	}
}

func TestResolveStateID_NoMappingReturnsEmpty(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"open": "Todo"})

	id, err := e.ResolveStateID(context.Background(), link, "closed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("got %q, want empty string", id)
	}
	// No mapping → no API call needed.
	if len(fc.Lists) != 0 {
		t.Errorf("unexpected ListProjectStates calls: %d", len(fc.Lists))
	}
}

func TestResolveStateID_NameNotInProject(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"closed": "Bogus"})

	id, err := e.ResolveStateID(context.Background(), link, "closed")
	if err == nil {
		t.Fatalf("expected error, got id=%q", id)
	}
	if !strings.Contains(err.Error(), "Bogus") {
		t.Errorf("error should mention missing name, got %v", err)
	}
}

func TestResolveStateID_PropagatesListError(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{
		ListProjectStatesFunc: func(_ context.Context, _ string) ([]plane.State, error) {
			return nil, errors.New("boom")
		},
	}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"closed": "Done"})

	if _, err := e.ResolveStateID(context.Background(), link, "closed"); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveStateID_CachesAcrossCalls(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	fc := &fakeClient{
		ListProjectStatesFunc: func(_ context.Context, _ string) ([]plane.State, error) {
			calls.Add(1)
			return []plane.State{
				{ID: "state-done", Name: "Done", Group: "completed"},
			}, nil
		},
	}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"closed": "Done"})

	for i := 0; i < 5; i++ {
		if _, err := e.ResolveStateID(context.Background(), link, "closed"); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("ListProjectStates called %d times, want 1 (cache miss only)", got)
	}
}

// TestResolveStateID_ConcurrentCachesCoalesce verifies that N goroutines
// racing on the first call to a cold cache for the same project do not
// each trigger a ListProjectStates call. The per-entry mutex serialises
// the cold fill, so only the first caller hits the client.
func TestResolveStateID_ConcurrentCachesCoalesce(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	start := make(chan struct{})
	fc := &fakeClient{
		ListProjectStatesFunc: func(_ context.Context, _ string) ([]plane.State, error) {
			calls.Add(1)
			// Sleep a beat so all goroutines pile up on the per-project lock.
			time.Sleep(20 * time.Millisecond)
			return []plane.State{
				{ID: "state-done", Name: "Done", Group: "completed"},
			}, nil
		},
	}
	e := NewEngine(fc, &mapping.Resolved{}, testLogger())
	link := newStateLink(map[string]string{"closed": "Done"})

	var wg stdsync.WaitGroup
	const N = 8
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			if _, err := e.ResolveStateID(context.Background(), link, "closed"); err != nil {
				t.Errorf("resolve: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("ListProjectStates called %d times under contention, want 1", got)
	}
}
