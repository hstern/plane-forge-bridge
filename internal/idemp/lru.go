package idemp

import (
	"container/list"
	"sync"
)

// LRU is a fixed-capacity, concurrency-safe cache of recently-seen event
// keys. It is the belt-and-braces complement to the loop-break marker: if a
// downstream client strips the HTML comment marker out of a body, the LRU
// still catches the duplicate as long as we see it within the cache window.
//
// Zero value is not usable — use NewLRU.
type LRU struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	idx map[string]*list.Element
}

// lruKey is the structured key used for indexing the LRU. We keep the raw
// fields on the list element so eviction can clean the map without re-parsing
// a composite string key.
type lruKey struct {
	src         Source
	eventID     string
	targetObjID string
}

func (k lruKey) string() string {
	return string(k.src) + "|" + k.eventID + "|" + k.targetObjID
}

// NewLRU returns an LRU with at most cap entries. Older entries are evicted
// on insert. A non-positive cap is clamped to 1.
func NewLRU(cap int) *LRU {
	if cap < 1 {
		cap = 1
	}
	return &LRU{
		cap: cap,
		ll:  list.New(),
		idx: make(map[string]*list.Element),
	}
}

// Seen returns true if (src, eventID, targetObjID) has been recorded
// recently. It does NOT update recency — it is a read-only check.
func (l *LRU) Seen(src Source, eventID, targetObjID string) bool {
	k := lruKey{src: src, eventID: eventID, targetObjID: targetObjID}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.idx[k.string()]
	return ok
}

// Record adds (src, eventID, targetObjID) to the cache. Returns false if the
// entry was already present (so the caller can treat that as "duplicate").
// A true return means the entry is newly inserted.
func (l *LRU) Record(src Source, eventID, targetObjID string) bool {
	k := lruKey{src: src, eventID: eventID, targetObjID: targetObjID}
	ks := k.string()

	l.mu.Lock()
	defer l.mu.Unlock()

	if el, ok := l.idx[ks]; ok {
		// Refresh recency on re-record so an existing entry isn't the next
		// one evicted, but report it as a duplicate.
		l.ll.MoveToFront(el)
		return false
	}

	el := l.ll.PushFront(k)
	l.idx[ks] = el

	for l.ll.Len() > l.cap {
		oldest := l.ll.Back()
		if oldest == nil {
			break
		}
		l.ll.Remove(oldest)
		delete(l.idx, oldest.Value.(lruKey).string())
	}
	return true
}
