package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// rbacTestHarness builds a tiny gin engine with the RBAC middleware in
// front of a no-op handler. It seeds context just like the real auth
// middleware would, so RequireRole / RequireOwnershipOrRole see the
// expected TenantRole and UserID.
//
// Returning the recorder rather than asserting inline keeps each test
// case focused on the (input -> status) pair it cares about.
func rbacTestHarness(role types.TenantRole, userID string, mw gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		// Mirror what middleware/auth.go's JWT path sets.
		ctx := context.WithValue(c.Request.Context(), types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.UserIDContextKey, userID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/protected", mw, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(w, req)
	return w
}

func cfgRBAC(enabled bool) *config.Config {
	return &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}
}

// cfgRBACWithCrossTenant returns a config with both per-tenant RBAC
// enforcement AND the cluster-wide cross-tenant access flag enabled.
// IsCrossTenantSuperuser requires BOTH to honour the User attribute,
// so cross-tenant superuser tests need this rather than plain cfgRBAC.
func cfgRBACWithCrossTenant(enabled bool) *config.Config {
	return &config.Config{Tenant: &config.TenantConfig{
		EnableRBAC:              &enabled,
		EnableCrossTenantAccess: true,
	}}
}

// ---------- RequireRole ----------

func TestRequireRole_AllowsAtMin(t *testing.T) {
	w := rbacTestHarness(types.TenantRoleAdmin, "u1",
		RequireRole(types.TenantRoleAdmin, cfgRBAC(true)))
	if w.Code != http.StatusOK {
		t.Fatalf("Admin should clear Admin gate, got %d", w.Code)
	}
}

func TestRequireRole_AllowsAboveMin(t *testing.T) {
	w := rbacTestHarness(types.TenantRoleOwner, "u1",
		RequireRole(types.TenantRoleAdmin, cfgRBAC(true)))
	if w.Code != http.StatusOK {
		t.Fatalf("Owner should clear Admin gate, got %d", w.Code)
	}
}

func TestRequireRole_RejectsBelowMin(t *testing.T) {
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireRole(types.TenantRoleAdmin, cfgRBAC(true)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("Contributor must NOT clear Admin gate, got %d", w.Code)
	}
}

func TestRequireRoleOrSystemAdmin_AllowsSystemAdminBelowTenantRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireRoleOrSystemAdmin(types.TenantRoleAdmin, cfgRBAC(true)),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/protected", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequireRoleOrSystemAdmin_RejectsOrdinaryViewer(t *testing.T) {
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireRoleOrSystemAdmin(types.TenantRoleAdmin, cfgRBAC(true)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ordinary Viewer must not clear Admin-or-SystemAdmin gate, got %d", w.Code)
	}
}

func TestRequireRole_FailOpenWhenRBACDisabled(t *testing.T) {
	// EnableRBAC=false: the middleware should log but not block, so the
	// downstream handler still runs. This is the rollout-safety guarantee.
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireRole(types.TenantRoleOwner, cfgRBAC(false)))
	if w.Code != http.StatusOK {
		t.Fatalf("EnableRBAC=false must let Viewer through Owner gate, got %d", w.Code)
	}
}

func TestRequireRole_NilConfigFailsOpen(t *testing.T) {
	// Defensive: nil config must not panic and must fail open (no enforcement
	// configured = behave like the legacy path).
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireRole(types.TenantRoleAdmin, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("nil config must fail open, got %d", w.Code)
	}
}

func TestRequireRole_CrossTenantSuperuserBypass(t *testing.T) {
	// Org-level superusers (User.CanAccessAllTenants) bypass tenant role
	// gates — see auth.go's resolveTenantRole, which gives them a
	// transient Admin in foreign tenants. RequireRole has to honour the
	// same bypass for Owner-only gates, otherwise a superuser would be
	// locked out of DELETE /tenants/:id once enforcement turns on.
	//
	// Pinned as a regression test: if anyone reorders the fast paths so
	// the superuser check ends up after the enforcement branch, this
	// test fails before the change ships.
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "su1")
		ctx = context.WithValue(ctx, types.UserContextKey, &types.User{
			ID: "su1", CanAccessAllTenants: true,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireRole(types.TenantRoleOwner, cfgRBACWithCrossTenant(true)),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) },
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser must bypass Owner role gate, got %d", w.Code)
	}
}

// ---------- RequireOwnershipOrRole ----------

func TestRequireOwnershipOrRole_AdminBypassesLookup(t *testing.T) {
	// Admin / Owner clear the role gate without touching the lookup,
	// so an erroring lookup still passes when the caller has the role.
	called := false
	lookup := func(c *gin.Context) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	w := rbacTestHarness(types.TenantRoleAdmin, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)))
	if w.Code != http.StatusOK {
		t.Fatalf("Admin should pass without lookup, got %d", w.Code)
	}
	if called {
		t.Fatalf("lookup must not run when role already meets min")
	}
}

func TestRequireOwnershipOrRole_CreatorAllowed(t *testing.T) {
	lookup := func(c *gin.Context) (string, error) { return "u1", nil }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)))
	if w.Code != http.StatusOK {
		t.Fatalf("creator must clear ownership gate, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_NonCreatorContributorRejected(t *testing.T) {
	// Contributor editing someone else's resource is the exact case the
	// matrix targets: only the original creator OR Admin+ may proceed.
	lookup := func(c *gin.Context) (string, error) { return "someone-else", nil }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-creator Contributor must hit 403, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_LegacyEmptyCreatorTreatedAsTenantOwned(t *testing.T) {
	// Pre-migration rows (or rows the backfill couldn't resolve) carry
	// creator_id = "". Per the contract those are tenant-owned: only the
	// role check decides.
	lookup := func(c *gin.Context) (string, error) { return "", nil }
	// Contributor on a tenant-owned row -> rejected, only Admin+ can mutate.
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("Contributor on legacy tenant-owned row should hit 403, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_LookupErrorReturns503(t *testing.T) {
	// A transient lookup error surfaces as 503 (not 403) so monitoring
	// and clients can tell "your permission was denied" from "the server
	// briefly couldn't verify ownership". Failing open here would mean
	// any DB hiccup on the creator query becomes a free pass.
	lookup := func(c *gin.Context) (string, error) { return "", errors.New("boom") }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("lookup error must surface as 503, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_NotFoundPassesThroughTo404(t *testing.T) {
	// When the lookup signals "no such resource visible to this tenant",
	// the middleware MUST NOT mask it as 403. The handler downstream
	// gets to decide the right status (usually 404), which keeps client
	// error handling honest and avoids hiding "wrong URL" behind a
	// permissions error.
	called := false
	lookup := func(c *gin.Context) (string, error) {
		return "", ErrResourceNotFound
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "u1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(true)),
		func(c *gin.Context) {
			called = true
			c.JSON(http.StatusNotFound, gin.H{"error": "kb not found"})
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !called {
		t.Fatalf("handler should have been invoked so it can produce 404")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected handler 404 to win, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_SkipsLookupWhenRBACDisabled(t *testing.T) {
	// H1 regression: when enforcement is off, the lookup must not run at
	// all. Hooking up RBAC pre-rollout used to add a hidden DB roundtrip
	// to every mutating request even though the result was thrown away.
	calls := 0
	lookup := func(c *gin.Context) (string, error) {
		calls++
		return "someone-else", nil
	}
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(false)))
	if w.Code != http.StatusOK {
		t.Fatalf("fail-open should let the request through, got %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("lookup must not run when EnableRBAC=false (got %d calls)", calls)
	}
}

func TestRequireOwnershipOrRole_CrossTenantSuperuserBypass(t *testing.T) {
	// Cross-tenant superusers resolve to Admin in foreign tenants (see
	// resolveTenantRole). For Owner-only gates we additionally let them
	// through to preserve the pre-RBAC ability to administer any tenant.
	calls := 0
	lookup := func(c *gin.Context) (string, error) {
		calls++
		return "", nil
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "su1")
		ctx = context.WithValue(ctx, types.UserContextKey, &types.User{
			ID: "su1", CanAccessAllTenants: true,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireOwnershipOrRole(types.TenantRoleOwner, lookup, cfgRBACWithCrossTenant(true)),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) },
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser must bypass Owner gate, got %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("superuser bypass must skip lookup, got %d", calls)
	}
}

func TestRequireOwnershipOrRole_FailOpenWhenRBACDisabled(t *testing.T) {
	// Enforcement off: even a failing lookup + non-creator + low role lets
	// the request through. This preserves today's "anyone in the tenant
	// can edit anything" behaviour while we ship the schema.
	lookup := func(c *gin.Context) (string, error) { return "someone-else", nil }
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(false)))
	if w.Code != http.StatusOK {
		t.Fatalf("EnableRBAC=false must let Viewer non-creator through, got %d", w.Code)
	}
}

func TestRequireOwnershipOrRole_FailOpenOnLookupErrorWhenRBACDisabled(t *testing.T) {
	// Lookup errors in fail-open mode also let the request through —
	// otherwise turning RBAC off wouldn't actually unblock anything that
	// needs the lookup.
	lookup := func(c *gin.Context) (string, error) { return "", errors.New("boom") }
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireOwnershipOrRole(types.TenantRoleAdmin, lookup, cfgRBAC(false)))
	if w.Code != http.StatusOK {
		t.Fatalf("EnableRBAC=false + lookup error must fail open, got %d", w.Code)
	}
}

// ---------- TenantRoleMember invariants ----------
//
// These tests pin two design contracts for the new TenantRoleMember
// (level 5) role:
//
//   1. The level values of the four legacy roles MUST stay in their
//      existing band (owner=40, admin=30, contributor=20, viewer=10).
//      Member MUST sit BELOW viewer (5) so every existing
//      'viewer'-thresholded check rejects Member automatically. This is
//      what preserves the “no impact on the original 4 roles” hard
//      constraint: any HasPermission(viewer) call must compare 5 < 10
//      and reject, regardless of code that doesn't yet know about the
//      new role.
//
//   2. IsValid / Level / AllTenantRoles must all agree. If any of them
//      drift the route-layer guards and the handler error message fall
//      out of sync; a member role can survive schema-wise but get
//      silently rejected by every RequireRole gate.

func TestTenantRoleLevels_LegacyBandsUnchanged(t *testing.T) {
	cases := map[types.TenantRole]int{
		types.TenantRoleOwner:       40,
		types.TenantRoleAdmin:       30,
		types.TenantRoleContributor: 20,
		types.TenantRoleViewer:      10,
	}
	for role, want := range cases {
		if got := role.Level(); got != want {
			t.Errorf("legacy role %s level drifted: got %d want %d", role, got, want)
		}
	}
}

func TestTenantRoleMember_SitsBelowViewer(t *testing.T) {
	if got := types.TenantRoleMember.Level(); got >= types.TenantRoleViewer.Level() {
		t.Fatalf("Member level must be strictly less than Viewer (member=%d viewer=%d) "+
			"so existing 'viewer'-threshold checks still reject Member", got, types.TenantRoleViewer.Level())
	}
}

func TestTenantRoleMember_HasPermissionRejectsForLegacyThresholds(t *testing.T) {
	// The whole point of Member=5: every existing role check that
	// requires >= viewer/ad/contributor/owner must transparently
	// reject Member without any code change. Without this, adding
	// Member at level 15 would accidentally widen every viewer-gate
	// to admit Member (the “hidden escalation” the design rejects).
	if types.TenantRoleMember.HasPermission(types.TenantRoleViewer) {
		t.Errorf("Member (level 5) must NOT have Viewer permission (the design rejects level >= 10)")
	}
	if types.TenantRoleMember.HasPermission(types.TenantRoleContributor) {
		t.Errorf("Member must NOT have Contributor permission")
	}
}

func TestTenantRole_LegacyHasPermissionStillAdmitsMember(t *testing.T) {
	// A legacy user with the Owner role can be a member of ANY group
	// that requires >= member (because Member is the lowest). This
	// keeps HasPermission monotonic so Member-floor routes do not
	// require an extra branch.
	for _, r := range []types.TenantRole{
		types.TenantRoleOwner, types.TenantRoleAdmin,
		types.TenantRoleContributor, types.TenantRoleViewer,
	} {
		if !r.HasPermission(types.TenantRoleMember) {
			t.Errorf("legacy role %s should clear Member floor, but does not", r)
		}
	}
}

func TestAllTenantRoles_IncludesMemberAtLowestLevel(t *testing.T) {
	got := types.AllTenantRoles()
	// Must end with Member (lowest level) — handlers rely on this order
	// when rendering role lists in invite dropdowns.
	wantTail := types.TenantRoleMember
	if len(got) == 0 || got[len(got)-1] != wantTail {
		t.Fatalf("AllTenantRoles must end with %s, got %v", wantTail, got)
	}
	for _, r := range got {
		if !r.IsValid() {
			t.Errorf("AllTenantRoles returned invalid role %s", r)
		}
	}
}

// ---------- RequireExactRoleOrOwnershipOrRole ----------
//
// RequireExactRoleOrOwnershipOrRole is the new guard that admits Member
// on KB content upload routes WITHOUT widening the matrix for the
// legacy 4 roles. Each legacy-role test below pins a specific cell of
// the parity table in the design doc; if any of these break, the guard
// has silently escalated a legacy role's rights.

func TestRequireExactRoleOrOwnershipOrRole_MemberShortCircuitsBeforeLookup(t *testing.T) {
	// The fast-path for Member must skip the creator lookup entirely
	// — that's how Member routes don't pay the per-request DB cost of
	// RequireOwnershipOrRole's KBCreatorLookup query.
	called := false
	lookup := func(c *gin.Context) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	w := rbacTestHarness(types.TenantRoleMember, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("Member must short-circuit to allow on Member gate, got %d", w.Code)
	}
	if called {
		t.Fatalf("Member fast-path must skip the creator lookup, got %d calls", 1)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_AdminParityBypassesLookup(t *testing.T) {
	// Admin must take the legacy fallback path with the SAME fast-path
	// semantics as OwnedKBOrAdmin — role >= min bypasses the lookup.
	called := false
	lookup := func(c *gin.Context) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	w := rbacTestHarness(types.TenantRoleAdmin, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("Admin must clear Member gate via fallback fast-path, got %d", w.Code)
	}
	if called {
		t.Fatalf("Admin fallback must not invoke lookup (parity with OwnedKBOrAdmin)")
	}
}

func TestRequireExactRoleOrOwnershipOrRole_OwnerParityBypassesLookup(t *testing.T) {
	called := false
	lookup := func(c *gin.Context) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	w := rbacTestHarness(types.TenantRoleOwner, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("Owner must clear Member gate via fallback, got %d", w.Code)
	}
	if called {
		t.Fatalf("Owner fallback must not invoke lookup")
	}
}

func TestRequireExactRoleOrOwnershipOrRole_ViewerNonCreatorRejected(t *testing.T) {
	// THE CRITICAL REGRESSION TEST: a non-creator Viewer who somehow
	// reaches a KB upload route must STILL get 403. This is what
	// guards against the rejected alternative “g.Member() on the KB
	// routes”, which would let Viewer(10) clear a Member(5) floor and
	// acquire KB Editor rights they never had.
	lookup := func(c *gin.Context) (string, error) { return "someone-else", nil }
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusForbidden {
		t.Fatalf("Viewer non-creator must hit 403 (parity with OwnedKBOrAdmin), got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_ViewerCreatorAllowed(t *testing.T) {
	// Identity-equality handoff: the legacy fallback treats a Viewer
	// who IS the KB creator exactly like OwnedKBOrAdmin does. We don't
	// keep an extra “creator but Viewer” case in production, but the
	// parity test pins it so a future “fast-path everyone” refactor
	// surfaces as a behaviour diff if it widens this.
	lookup := func(c *gin.Context) (string, error) { return "u1", nil }
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("Viewer-as-creator must pass fallback, got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_ContributorNonCreatorRejected(t *testing.T) {
	// Pin parity for contributor non-creator — they fail the
	// role >= admin branch and fall through to lookup; non-match ->
	// 403. Same matrix as OwnedKBOrAdmin.
	lookup := func(c *gin.Context) (string, error) { return "someone-else", nil }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusForbidden {
		t.Fatalf("Contributor non-creator must hit 403, got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_ContributorCreatorAllowed(t *testing.T) {
	// Contributors who own the KB pass via ownership equality — this
	// is the “Contributor in their OWN KB acts like Owner” invariant
	// the design relies on.
	lookup := func(c *gin.Context) (string, error) { return "u1", nil }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("Contributor creator must clear ownership gate, got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_LookupErrorReturns503(t *testing.T) {
	// Parity with RequireOwnershipOrRole: a transient lookup failure
	// surfaces as 503 for non-Member roles (Owner/Admin bypass lookup
	// via the fast-path so they never reach this branch). The contract
	// must remain consistent across both guards.
	lookup := func(c *gin.Context) (string, error) { return "", errors.New("boom") }
	w := rbacTestHarness(types.TenantRoleContributor, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("lookup error on non-Member fallback must be 503, got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_NotFoundPassesThroughTo404(t *testing.T) {
	// Parity with RequireOwnershipOrRole: ErrResourceNotFound from the
	// lookup must let the handler emit its own 404 instead of being
	// masked as 403.
	called := false
	lookup := func(c *gin.Context) (string, error) {
		return "", ErrResourceNotFound
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleContributor)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "u1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(true),
		),
		func(c *gin.Context) {
			called = true
			c.JSON(http.StatusNotFound, gin.H{"error": "kb not found"})
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !called {
		t.Fatalf("handler should run so it can emit 404")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected handler 404 to win, got %d", w.Code)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_FailOpenOnRBACDisabled(t *testing.T) {
	// Rollout safety: when EnableRBAC=false the middleware must let
	// every request through without consulting the lookup — same
	// contract as RequireOwnershipOrRole.
	calls := 0
	lookup := func(c *gin.Context) (string, error) {
		calls++
		return "someone-else", nil
	}
	w := rbacTestHarness(types.TenantRoleViewer, "u1",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleAdmin,
			lookup,
			cfgRBAC(false),
		))
	if w.Code != http.StatusOK {
		t.Fatalf("EnableRBAC=false must fail open, got %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("lookup must not run when EnableRBAC=false (got %d calls)", calls)
	}
}

func TestRequireExactRoleOrOwnershipOrRole_CrossTenantSuperuserBypass(t *testing.T) {
	// Cross-tenant superusers resolve to Admin in foreign tenants
	// (see resolveTenantRole) and must short-circuit the fallback
	// without invoking the lookup. Parity with the legacy
	// RequireOwnershipOrRole cross-tenant test.
	calls := 0
	lookup := func(c *gin.Context) (string, error) {
		calls++
		return "", nil
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "su1")
		ctx = context.WithValue(ctx, types.UserContextKey, &types.User{
			ID: "su1", CanAccessAllTenants: true,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/protected",
		RequireExactRoleOrOwnershipOrRole(
			types.TenantRoleMember,
			types.TenantRoleOwner,
			lookup,
			cfgRBACWithCrossTenant(true),
		),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) },
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser must bypass Owner-or-creator gate, got %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("superuser bypass must skip lookup, got %d calls", calls)
	}
}
