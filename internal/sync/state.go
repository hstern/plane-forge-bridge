package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// stateCacheTTL bounds how long a ListProjectStates response is cached
// before the engine refetches. Plane workflow states change rarely (a human
// edits them in the UI), so a 5-minute TTL gives operators a quick path to
// pick up renames without forcing a refetch on every event.
const stateCacheTTL = 5 * time.Minute

// stateCache caches ListProjectStates results by projectID. Entries are
// refreshed once stateCacheTTL elapses; concurrent calls for the same
// project coalesce on the per-project mutex so the engine never fans out N
// parallel ListProjectStates requests for the same project.
type stateCache struct {
	mu      sync.Mutex
	entries map[string]*stateCacheEntry
}

// stateCacheEntry holds the cached states and a per-project mutex used to
// serialise refresh attempts. We do not invalidate other readers while a
// refresh is in flight — they will see the previous values and then pick
// up the new ones on their next call, which is fine because the TTL is
// generous and the states are rarely contested.
type stateCacheEntry struct {
	mu        sync.Mutex
	states    []plane.State
	fetchedAt time.Time
}

// getOrCreate returns the per-project entry, allocating it under the cache
// mutex if needed.
func (c *stateCache) getOrCreate(projectID string) *stateCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*stateCacheEntry)
	}
	e, ok := c.entries[projectID]
	if !ok {
		e = &stateCacheEntry{}
		c.entries[projectID] = e
	}
	return e
}

// listStates returns the cached state slice for projectID, refreshing from
// the PlaneClient if the cache is empty or stale.
//
// Refresh is serialised per project on the entry's mutex: the second
// caller blocks on the first, and once the first completes the second
// finds the cache fresh and returns without a second API call. This makes
// the cache safe under concurrent ResolveStateID calls.
func (e *Engine) listStates(ctx context.Context, projectID string) ([]plane.State, error) {
	entry := e.stateCache.getOrCreate(projectID)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.states != nil && time.Since(entry.fetchedAt) < stateCacheTTL {
		return entry.states, nil
	}
	states, err := e.Client.ListProjectStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	entry.states = states
	entry.fetchedAt = time.Now()
	return states, nil
}

// ResolveStateID translates a forge state ("open" | "closed") to a Plane
// state UUID by:
//
//  1. looking up link.StateMap[forgeState] to get the Plane state NAME;
//  2. resolving the name to a UUID via the cached ListProjectStates result.
//
// Returns "" and a nil error if the link has no mapping for that forge
// state — callers treat that as "don't change state", matching the design
// doc's promise that the bridge does not invent transitions. Returns ""
// and a non-nil error if the mapped name is not present in the project's
// state set (an operator config mistake we surface rather than silently
// drop).
func (e *Engine) ResolveStateID(ctx context.Context, link *mapping.Link, forgeState string) (string, error) {
	if link == nil {
		return "", nil
	}
	name, ok := link.StateMap[forgeState]
	if !ok || name == "" {
		return "", nil
	}
	states, err := e.listStates(ctx, link.PlaneProjectID)
	if err != nil {
		return "", fmt.Errorf("sync: list states for project %s: %w", link.PlaneProjectID, err)
	}
	for _, s := range states {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("sync: state %q not found in project %s", name, link.PlaneProjectID)
}
