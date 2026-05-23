package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/plane"
)

// labelCacheTTL bounds how long a ListProjectLabels response is cached
// before the engine refetches. Labels change rarely (a human edits them in
// the Plane UI) and a 5-minute TTL matches stateCacheTTL so the operator
// has one mental model for cache freshness.
const labelCacheTTL = 5 * time.Minute

// labelCache caches ListProjectLabels results by projectID. Mirrors the
// stateCache structure — per-project mutex serialises refresh attempts so
// concurrent goroutines resolving labels on the same project never fan out
// N parallel list requests under bursty webhook load.
//
// Known limit: we do not invalidate on label rename. If an operator renames
// a label in Plane while the cache is warm, the engine will keep returning
// the cached UUID for the OLD name and may auto-create a duplicate under
// the NEW name. The blast radius is a label-name collision, not data loss,
// and operators can wait out the 5-minute TTL. Tracked in the README's
// "Open questions" section.
type labelCache struct {
	mu      sync.Mutex
	entries map[string]*labelCacheEntry
}

// labelCacheEntry holds the cached labels keyed by name for O(1) lookup
// plus the fetch timestamp for TTL checks. The per-entry mutex serialises
// refreshes and auto-create calls for a single project, which is what
// guards the resolver against thundering-herd lists when N webhooks
// arrive for the same project simultaneously.
type labelCacheEntry struct {
	mu        sync.Mutex
	byName    map[string]string // name → plane UUID
	fetchedAt time.Time
}

// getOrCreate returns the per-project entry, allocating it under the cache
// mutex if needed. Same pattern as stateCache.getOrCreate.
func (c *labelCache) getOrCreate(projectID string) *labelCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*labelCacheEntry)
	}
	e, ok := c.entries[projectID]
	if !ok {
		e = &labelCacheEntry{}
		c.entries[projectID] = e
	}
	return e
}

// forgeLabelNames extracts the .Name slice from a forge.Label slice. Keeps
// the call sites in engine.go readable and isolates the field projection so
// a future Label-type change touches one place.
func forgeLabelNames(labels []forge.Label) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

// resolveLabels translates forge label names into plane label UUIDs scoped
// to a single Plane project, in input order. Missing labels on Plane are
// auto-created and the new UUID is folded into the cache so the next
// lookup hits.
//
// Caching:
//
//   - First call for a project (or after TTL expiry): ListProjectLabels,
//     cache the result.
//   - Subsequent calls: serve from cache.
//   - Auto-create result: added to the cache immediately.
//
// Concurrency: callers resolving labels on the same project serialise on
// the per-project mutex. The first caller does the list (and any creates);
// the second caller finds the cache fresh and skips the list. This bounds
// list traffic to one call per project per TTL window under bursty load.
//
// Empty input → empty (nil) output, no API calls. Errors propagate from
// ListProjectLabels or CreateProjectLabel.
func (e *Engine) resolveLabels(ctx context.Context, projectID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	entry := e.labelCache.getOrCreate(projectID)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.byName == nil || time.Since(entry.fetchedAt) >= labelCacheTTL {
		labels, err := e.Client.ListProjectLabels(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("sync: list labels for project %s: %w", projectID, err)
		}
		byName := make(map[string]string, len(labels))
		for _, l := range labels {
			byName[l.Name] = l.ID
		}
		entry.byName = byName
		entry.fetchedAt = time.Now()
	}

	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" {
			// Defensive: forge.Label.Name is required upstream, but a
			// caller-fabricated event could include an empty name. Skip it
			// rather than auto-creating an empty-named label on Plane.
			continue
		}
		id, ok := entry.byName[name]
		if ok {
			out = append(out, id)
			continue
		}
		created, err := e.Client.CreateProjectLabel(ctx, projectID, plane.CreateLabelRequest{Name: name})
		if err != nil {
			return nil, fmt.Errorf("sync: create label %q in project %s: %w", name, projectID, err)
		}
		entry.byName[name] = created.ID
		out = append(out, created.ID)
	}
	return out, nil
}
