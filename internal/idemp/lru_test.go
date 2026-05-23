package idemp

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLRU_BasicSeen(t *testing.T) {
	l := NewLRU(8)

	if l.Seen(SourceForge, "evt1", "obj1") {
		t.Fatalf("expected not seen before Record")
	}
	if !l.Record(SourceForge, "evt1", "obj1") {
		t.Fatalf("first Record should return true (newly inserted)")
	}
	if !l.Seen(SourceForge, "evt1", "obj1") {
		t.Fatalf("expected seen after Record")
	}
	// Different target object: independent key.
	if l.Seen(SourceForge, "evt1", "obj2") {
		t.Fatalf("different target should not be seen")
	}
	// Different source: independent key.
	if l.Seen(SourcePlane, "evt1", "obj1") {
		t.Fatalf("different source should not be seen")
	}
}

func TestLRU_EvictsOldest(t *testing.T) {
	l := NewLRU(3)
	l.Record(SourceForge, "evt1", "obj")
	l.Record(SourceForge, "evt2", "obj")
	l.Record(SourceForge, "evt3", "obj")
	// Fourth entry evicts the oldest (evt1).
	l.Record(SourceForge, "evt4", "obj")

	if l.Seen(SourceForge, "evt1", "obj") {
		t.Fatalf("evt1 should have been evicted")
	}
	for _, ev := range []string{"evt2", "evt3", "evt4"} {
		if !l.Seen(SourceForge, ev, "obj") {
			t.Errorf("%s should still be present", ev)
		}
	}
}

func TestLRU_RecordRefreshesRecency(t *testing.T) {
	l := NewLRU(3)
	l.Record(SourceForge, "evt1", "obj")
	l.Record(SourceForge, "evt2", "obj")
	l.Record(SourceForge, "evt3", "obj")
	// Re-record evt1 — it should refresh and now evt2 is the oldest.
	if l.Record(SourceForge, "evt1", "obj") {
		t.Fatalf("re-Record of existing entry should return false")
	}
	l.Record(SourceForge, "evt4", "obj")

	if l.Seen(SourceForge, "evt2", "obj") {
		t.Fatalf("evt2 should have been evicted after evt1 refresh")
	}
	if !l.Seen(SourceForge, "evt1", "obj") {
		t.Fatalf("evt1 should still be present after refresh")
	}
}

func TestLRU_RecordReturnsFalseOnDuplicate(t *testing.T) {
	l := NewLRU(4)
	if !l.Record(SourceForge, "evt1", "obj") {
		t.Fatalf("first record should be true")
	}
	if l.Record(SourceForge, "evt1", "obj") {
		t.Fatalf("duplicate record should be false")
	}
	if l.Record(SourceForge, "evt1", "obj") {
		t.Fatalf("second duplicate record should still be false")
	}
}

func TestLRU_ZeroOrNegativeCapClamped(t *testing.T) {
	for _, cap := range []int{0, -1, -100} {
		l := NewLRU(cap)
		l.Record(SourceForge, "evt1", "obj")
		l.Record(SourceForge, "evt2", "obj")
		if l.Seen(SourceForge, "evt1", "obj") {
			t.Errorf("cap=%d: evt1 should have been evicted", cap)
		}
		if !l.Seen(SourceForge, "evt2", "obj") {
			t.Errorf("cap=%d: evt2 should be present", cap)
		}
	}
}

func TestLRU_Concurrent(t *testing.T) {
	const (
		workers = 16
		perW    = 500
	)
	l := NewLRU(64)

	var wg sync.WaitGroup
	var inserts int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perW; i++ {
				// Use overlapping keys across workers so we exercise both
				// the duplicate path and the insert path under contention.
				ev := fmt.Sprintf("evt-%d", i%(perW/2))
				obj := fmt.Sprintf("obj-%d", w%4)
				if l.Record(SourceForge, ev, obj) {
					atomic.AddInt64(&inserts, 1)
				}
				_ = l.Seen(SourceForge, ev, obj)
			}
		}(w)
	}
	wg.Wait()

	// Sanity: at least one insert and no more than worker*perW.
	if inserts == 0 {
		t.Fatalf("expected some inserts")
	}
	if inserts > workers*perW {
		t.Fatalf("impossible insert count %d", inserts)
	}
}
