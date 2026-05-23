package sync

import (
	"context"
	"strings"
	"sync"
	"time"
)

// identityCacheTTL bounds how long an identity-resolution cache entry is
// kept before the engine refetches. Matches stateCacheTTL / labelCacheTTL
// so operators have one mental model for cache freshness.
const identityCacheTTL = 5 * time.Minute

// Member is the sync-local view of a Plane workspace member, used by the
// v2 identity resolver. The production *plane.Client is expected to
// provide a `ListWorkspaceMembers(ctx) ([]plane.Member, error)` method
// returning a sibling-package type with the same field set; the wiring
// layer in internal/server bridges between the two so this package can
// land independently of the sibling commit. See README "v2 identity
// resolution".
//
// Only Email and ID are load-bearing on the v2 path; the rest are kept
// so callers (and tests) can populate the struct from a real Plane
// payload without losing fields.
type Member struct {
	ID          string
	Email       string
	DisplayName string
	FirstName   string
	LastName    string
	Role        string
}

// identityCache is the per-engine cache backing v2 identity resolution.
// Two surfaces are cached independently:
//
//   - planeMembers: a single workspace-wide list (workspaces are small;
//     fetching all members once per TTL is cheaper than per-email
//     queries).
//   - forgeByEmail: per-email forge user lookups, since the upstream is
//     a per-query call. Negative results (no exact match) are cached
//     too as empty-string entries so the same miss doesn't re-trigger
//     a fan-out under load.
//
// Concurrency: each section has its own mutex. Refreshes coalesce — the
// second goroutine that asks for the same data while a refresh is in
// flight blocks on the section mutex, finds the cache fresh on retry,
// and returns without a second upstream call. This matches the
// thundering-herd guarantee of stateCache and labelCache.
type identityCache struct {
	planeMu            sync.Mutex
	planeMembers       []Member
	planeMembersExpiry time.Time

	forgeMu      sync.Mutex
	forgeByEmail map[string]forgeCacheEntry
}

// forgeCacheEntry holds the cached forge login for an email plus its
// expiry. An empty Login is a negative-result entry: "we looked, nobody
// matched", remembered for the TTL so the same miss doesn't fan out.
type forgeCacheEntry struct {
	Login  string
	Expiry time.Time
}

// resolvePlaneMember returns the Plane member UUID for a forge sender.
// Lookup order:
//
//  1. Engine.Users[forgeUsername] — static config map. Operator override
//     always wins.
//  2. Email match — list workspace members (cached), find one whose Email
//     matches forgeEmail (exact, case-insensitive).
//  3. "" — caller falls back to the bridge bot's PlaneMemberID.
//
// Identity resolution errors NEVER fail the calling handler: a network
// blip on ListWorkspaceMembers logs a warning and returns ""; the bridge
// then attributes the work item to the bot rather than dropping the
// event. Identity is best-effort; a wrong assignee is much better than
// a dropped event.
func (e *Engine) resolvePlaneMember(ctx context.Context, forgeUsername, forgeEmail string) (string, error) {
	// 1. Static config wins.
	if id, ok := e.Users[forgeUsername]; ok && id != "" {
		return id, nil
	}
	if forgeEmail == "" {
		return "", nil
	}
	// 2. Email match against the cached workspace member list.
	members, err := e.listPlaneMembers(ctx)
	if err != nil {
		e.Log.Warn("identity: plane member list failed, falling back to bridge bot",
			"forge_username", forgeUsername, "err", err)
		return "", nil
	}
	want := strings.ToLower(forgeEmail)
	for _, m := range members {
		if m.Email == "" {
			continue
		}
		if strings.ToLower(m.Email) == want {
			return m.ID, nil
		}
	}
	// 3. No match.
	return "", nil
}

// resolveForgeUser returns the forge login for a Plane member UUID.
// Lookup order:
//
//  1. Inverse scan of Engine.Users — find a forge_username whose mapped
//     value equals planeMemberID. Operator override always wins.
//  2. Email match — read the plane member's email from the cached
//     workspace list, search the forge for that email, and return the
//     login of the exact-match result (forge.SearchUsers is substring
//     on upstream so we filter exact-email here).
//  3. "" — caller falls back to the bridge bot's ForgeUsername.
//
// Like resolvePlaneMember, this NEVER fails the calling handler:
// upstream errors log a warning and return "".
func (e *Engine) resolveForgeUser(ctx context.Context, planeMemberID string) (string, error) {
	if planeMemberID == "" {
		return "", nil
	}
	// 1. Inverse static-config match.
	for forgeUsername, mappedID := range e.Users {
		if mappedID == planeMemberID && forgeUsername != "" {
			return forgeUsername, nil
		}
	}
	// 2. Email match: read the plane member's email, then search forge.
	members, err := e.listPlaneMembers(ctx)
	if err != nil {
		e.Log.Warn("identity: plane member list failed, falling back to bridge bot",
			"plane_member_id", planeMemberID, "err", err)
		return "", nil
	}
	var email string
	for _, m := range members {
		if m.ID == planeMemberID {
			email = m.Email
			break
		}
	}
	if email == "" {
		return "", nil
	}
	login, err := e.lookupForgeByEmail(ctx, email)
	if err != nil {
		e.Log.Warn("identity: forge user search failed, falling back to bridge bot",
			"plane_member_id", planeMemberID, "email", email, "err", err)
		return "", nil
	}
	return login, nil
}

// listPlaneMembers returns the cached workspace member list, refreshing
// from the PlaneClient if the cache is empty or stale. Refresh is
// serialised on the section mutex so concurrent resolver calls coalesce
// onto a single upstream call.
func (e *Engine) listPlaneMembers(ctx context.Context) ([]Member, error) {
	e.identityCache.planeMu.Lock()
	defer e.identityCache.planeMu.Unlock()

	if e.identityCache.planeMembers != nil && time.Now().Before(e.identityCache.planeMembersExpiry) {
		return e.identityCache.planeMembers, nil
	}
	members, err := e.Client.ListWorkspaceMembers(ctx)
	if err != nil {
		return nil, err
	}
	e.identityCache.planeMembers = members
	e.identityCache.planeMembersExpiry = time.Now().Add(identityCacheTTL)
	return members, nil
}

// lookupForgeByEmail returns the forge login for an email, going through
// the per-email cache. Both positive (login string) and negative (empty
// string) results are cached for the TTL to bound upstream calls under
// load: a flood of plane events from a member whose email isn't on the
// forge produces exactly one SearchUsers call per TTL window.
//
// SearchUsers is substring on upstream (a `?q=` search), so we filter
// the response for an exact case-insensitive email match before
// committing the answer.
func (e *Engine) lookupForgeByEmail(ctx context.Context, email string) (string, error) {
	e.identityCache.forgeMu.Lock()
	if e.identityCache.forgeByEmail == nil {
		e.identityCache.forgeByEmail = make(map[string]forgeCacheEntry)
	}
	key := strings.ToLower(email)
	if entry, ok := e.identityCache.forgeByEmail[key]; ok && time.Now().Before(entry.Expiry) {
		login := entry.Login
		e.identityCache.forgeMu.Unlock()
		return login, nil
	}
	// Hold the mutex across the upstream call so concurrent callers for
	// the same email coalesce — the second caller finds the cache fresh
	// after the first returns and skips the upstream call. This is the
	// same "stupid but correct" approach used by stateCache and
	// labelCache: one cache section, one mutex.
	defer e.identityCache.forgeMu.Unlock()

	users, err := e.ForgeClient.SearchUsers(ctx, email)
	if err != nil {
		return "", err
	}
	var login string
	for _, u := range users {
		if u.Email == "" {
			continue
		}
		if strings.EqualFold(u.Email, email) {
			login = u.Login
			break
		}
	}
	e.identityCache.forgeByEmail[key] = forgeCacheEntry{
		Login:  login,
		Expiry: time.Now().Add(identityCacheTTL),
	}
	return login, nil
}
