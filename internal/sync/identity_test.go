package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/forge"
	"github.com/hstern/plane-forge-bridge/internal/mapping"
)

// newIdentityEngine wires a minimal Engine with both fakes installed
// and a small static-config map. Identity tests don't exercise the
// link/state/label machinery, so the link list is empty by default
// and tests reach into Engine.Users / Engine.Bot directly when they
// need to.
func newIdentityEngine(t *testing.T) (*Engine, *fakeClient, *fakeForgeClient) {
	t.Helper()
	pc := &fakeClient{}
	fc := &fakeForgeClient{}
	cfg := &mapping.Resolved{
		Users: map[string]string{},
	}
	cfg.BridgeBot.ForgeUsername = "pfb-bot"
	cfg.BridgeBot.PlaneMemberID = "plane-uuid-bot"
	return NewEngine(pc, fc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))), pc, fc
}

func TestResolvePlaneMember_StaticConfigWins(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	e.Users = map[string]string{"alice": "plane-uuid-alice"}
	// If the resolver ever falls through to the email step, this fake
	// would record a list call; we assert it stays at zero.
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		t.Errorf("ListWorkspaceMembers should not be called when static config matches")
		return nil, nil
	}

	got, err := e.resolvePlaneMember(context.Background(), "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plane-uuid-alice" {
		t.Errorf("got %q, want plane-uuid-alice", got)
	}
	if pc.ListWorkspaceMembersCalls != 0 {
		t.Errorf("ListWorkspaceMembers calls=%d, want 0", pc.ListWorkspaceMembersCalls)
	}
}

func TestResolvePlaneMember_EmailMatch(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-1", Email: "bob@example.com"},
			{ID: "plane-uuid-2", Email: "carol@example.com"},
		}, nil
	}

	got, err := e.resolvePlaneMember(context.Background(), "carol-unmapped", "carol@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plane-uuid-2" {
		t.Errorf("got %q, want plane-uuid-2", got)
	}
}

func TestResolvePlaneMember_EmailMatchCaseInsensitive(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		// Mixed case on the plane side, all-lowercase on the forge side
		// — historical Gitea normalises emails to lowercase on signup.
		return []Member{
			{ID: "plane-uuid-mixed", Email: "Dave.Mixed@Example.COM"},
		}, nil
	}

	got, err := e.resolvePlaneMember(context.Background(), "dave", "dave.mixed@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plane-uuid-mixed" {
		t.Errorf("got %q, want plane-uuid-mixed (case-insensitive match)", got)
	}
}

func TestResolvePlaneMember_NoMatch_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{{ID: "x", Email: "elsewhere@example.com"}}, nil
	}

	got, err := e.resolvePlaneMember(context.Background(), "frank", "frank@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolvePlaneMember_EmptyEmail_FallsThrough(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		t.Errorf("ListWorkspaceMembers should not be called when forge email is empty")
		return nil, nil
	}

	got, err := e.resolvePlaneMember(context.Background(), "anon", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (no email → skip email step)", got)
	}
	if pc.ListWorkspaceMembersCalls != 0 {
		t.Errorf("ListWorkspaceMembers calls=%d, want 0", pc.ListWorkspaceMembersCalls)
	}
}

func TestResolvePlaneMember_PlaneAPIError_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return nil, errors.New("503 service unavailable")
	}

	got, err := e.resolvePlaneMember(context.Background(), "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("identity errors must not propagate; got %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty on upstream error", got)
	}
}

func TestResolvePlaneMember_CacheHit_NoSecondCall(t *testing.T) {
	t.Parallel()
	e, pc, _ := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-1", Email: "a@example.com"},
			{ID: "plane-uuid-2", Email: "b@example.com"},
		}, nil
	}

	if _, err := e.resolvePlaneMember(context.Background(), "u1", "a@example.com"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := e.resolvePlaneMember(context.Background(), "u2", "b@example.com"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if pc.ListWorkspaceMembersCalls != 1 {
		t.Errorf("want exactly 1 ListWorkspaceMembers call across both resolves, got %d",
			pc.ListWorkspaceMembersCalls)
	}
}

func TestResolveForgeUser_InverseConfigWins(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	e.Users = map[string]string{"alice": "plane-uuid-alice"}
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		t.Errorf("ListWorkspaceMembers should not be called when inverse config matches")
		return nil, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		t.Errorf("SearchUsers should not be called when inverse config matches")
		return nil, nil
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
}

func TestResolveForgeUser_EmailMatch(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-bob", Email: "bob@example.com"},
		}, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, query string) ([]forge.User, error) {
		if query != "bob@example.com" {
			t.Errorf("SearchUsers query=%q, want bob@example.com", query)
		}
		return []forge.User{
			{Login: "bob", Email: "bob@example.com"},
		}, nil
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "bob" {
		t.Errorf("got %q, want bob", got)
	}
}

func TestResolveForgeUser_ExactEmailFilter(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-bob", Email: "bob@example.com"},
		}, nil
	}
	// SearchUsers is substring upstream — a query for "bob@example.com"
	// can return bob@example.com.au or robob@example.com too. The
	// resolver must filter for exact email match.
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		return []forge.User{
			{Login: "rob-bob", Email: "bob@example.com.au"},
			{Login: "bob-extra", Email: "bobextra@example.com"},
			{Login: "bob", Email: "bob@example.com"}, // the real one
			{Login: "bobby", Email: "bobby@example.com"},
		}, nil
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "bob" {
		t.Errorf("got %q, want bob (exact-email filter must reject substring matches)", got)
	}
}

func TestResolveForgeUser_NoMatch_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-ghost", Email: "ghost@example.com"},
		}, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		return nil, nil
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-ghost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveForgeUser_UnknownMember_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	// Resolver is asked about a UUID that isn't in the workspace list —
	// might be a member of a different workspace, a stale UUID, or a
	// system actor. Skip the forge search entirely.
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-other", Email: "other@example.com"},
		}, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		t.Errorf("SearchUsers should not be called when member is unknown")
		return nil, nil
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveForgeUser_ConcurrentSameEmail_OneSearch(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{
			{ID: "plane-uuid-shared", Email: "shared@example.com"},
		}, nil
	}
	var searchCalls int32
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		atomic.AddInt32(&searchCalls, 1)
		// Brief sleep to widen the window for the race detector and to
		// give concurrent goroutines a chance to overlap on the mutex.
		time.Sleep(2 * time.Millisecond)
		return []forge.User{{Login: "shared-user", Email: "shared@example.com"}}, nil
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			got, err := e.resolveForgeUser(context.Background(), "plane-uuid-shared")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != "shared-user" {
				t.Errorf("got %q, want shared-user", got)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&searchCalls); got != 1 {
		t.Errorf("SearchUsers calls=%d, want exactly 1 (concurrent resolution must coalesce)", got)
	}
}

// TestResolveForgeUser_ForgeAPIError_ReturnsEmpty mirrors the
// plane-side error policy: a SearchUsers failure logs and degrades to
// the bridge bot fallback rather than failing the calling handler.
func TestResolveForgeUser_ForgeAPIError_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	e, pc, fc := newIdentityEngine(t)
	pc.ListWorkspaceMembersFunc = func(_ context.Context) ([]Member, error) {
		return []Member{{ID: "plane-uuid-x", Email: "x@example.com"}}, nil
	}
	fc.SearchUsersFunc = func(_ context.Context, _ string) ([]forge.User, error) {
		return nil, errors.New("502 bad gateway")
	}

	got, err := e.resolveForgeUser(context.Background(), "plane-uuid-x")
	if err != nil {
		t.Fatalf("identity errors must not propagate; got %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty on upstream error", got)
	}
}
