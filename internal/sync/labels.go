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

// forgeLabelCache caches ListRepoLabels results by "owner/repo". Mirrors
// the labelCache structure on the plane side — per-repo mutex serialises
// refresh attempts so concurrent goroutines resolving labels for the
// same repo never fan out N parallel list requests under bursty load.
//
// Same rename-staleness limitation as labelCache: a label rename on the
// forge side while the cache is warm can produce a duplicate auto-create
// under the new name. Operators wait out the 5-minute TTL; the blast
// radius is a name collision, not data loss.
type forgeLabelCache struct {
	mu      sync.Mutex
	entries map[string]*forgeLabelCacheEntry
}

// forgeLabelCacheEntry holds the cached forge labels keyed by name for
// O(1) lookup plus the fetch timestamp for TTL checks. Per-entry mutex
// serialises refreshes and auto-create calls for a single repo, which is
// what guards the resolver against thundering-herd lists when N
// webhooks arrive for the same repo simultaneously.
type forgeLabelCacheEntry struct {
	mu        sync.Mutex
	byName    map[string]int64 // name → forge integer label ID
	fetchedAt time.Time
}

// getOrCreate returns the per-repo entry, allocating it under the cache
// mutex if needed. Same pattern as labelCache.getOrCreate.
func (c *forgeLabelCache) getOrCreate(key string) *forgeLabelCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*forgeLabelCacheEntry)
	}
	e, ok := c.entries[key]
	if !ok {
		e = &forgeLabelCacheEntry{}
		c.entries[key] = e
	}
	return e
}

// resolveForgeLabels translates forge label names into forge integer
// label IDs scoped to a single repository, in input order. Missing
// labels on the forge are auto-created with a neutral default color and
// the new ID is folded into the cache so the next lookup hits.
//
// This is the symmetric inverse of resolveLabels: same caching,
// concurrency, and auto-create story, just on the forge side and
// returning int64 IDs instead of UUID strings.
//
// Empty input → empty (nil) output, no API calls. Errors propagate from
// ListRepoLabels or CreateRepoLabel.
func (e *Engine) resolveForgeLabels(ctx context.Context, owner, repo string, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	key := owner + "/" + repo
	entry := e.forgeLabelCache.getOrCreate(key)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.byName == nil || time.Since(entry.fetchedAt) >= labelCacheTTL {
		labels, err := e.ForgeClient.ListRepoLabels(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("sync: list labels for repo %s: %w", key, err)
		}
		byName := make(map[string]int64, len(labels))
		for _, l := range labels {
			byName[l.Name] = l.ID
		}
		entry.byName = byName
		entry.fetchedAt = time.Now()
	}

	out := make([]int64, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		id, ok := entry.byName[name]
		if ok {
			out = append(out, id)
			continue
		}
		created, err := e.ForgeClient.CreateRepoLabel(ctx, owner, repo, forge.CreateLabelRequest{
			Name: name,
			// Forgejo/Gitea label create requires a color. "#cccccc" is a
			// neutral grey that's visually distinct from the small set of
			// stock label colors so operators can spot bridge-created
			// labels at a glance and recolor them.
			Color: defaultForgeLabelColor,
		})
		if err != nil {
			return nil, fmt.Errorf("sync: create label %q in repo %s: %w", name, key, err)
		}
		entry.byName[name] = created.ID
		out = append(out, created.ID)
	}
	return out, nil
}

// defaultForgeLabelColor is the hex color the bridge stamps on labels it
// auto-creates on the forge side. Neutral grey — distinct enough from
// Forgejo's stock palette that operators can tell which labels came
// from the bridge.
const defaultForgeLabelColor = "#cccccc"

// resolvePlaneLabelNames translates a slice of Plane label UUIDs into
// their display names by consulting the plane-side label cache. This
// powers the plane → forge translation: Plane work-item events carry
// label UUIDs, but forge labels are identified by name. Unknown UUIDs
// (a label that's been deleted on the plane side between the event and
// the cache fetch) are silently dropped — propagating those would force
// us to either fail the whole event or auto-create a uselessly-named
// forge label.
func (e *Engine) resolvePlaneLabelNames(ctx context.Context, projectID string, uuids []string) ([]string, error) {
	if len(uuids) == 0 {
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

	// Reverse-index the cached name→UUID map into UUID→name. We do this
	// inline rather than maintaining a second map field on the entry: the
	// reverse lookup is exclusive to the plane → forge path, the label
	// count per project is tiny, and avoiding a second map keeps the
	// labelCache invariants (one map, one timestamp) simple.
	byUUID := make(map[string]string, len(entry.byName))
	for name, id := range entry.byName {
		byUUID[id] = name
	}

	out := make([]string, 0, len(uuids))
	for _, id := range uuids {
		if name, ok := byUUID[id]; ok && name != "" {
			out = append(out, name)
		}
	}
	return out, nil
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
