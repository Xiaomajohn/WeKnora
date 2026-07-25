package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// agentEditTestHarness builds a tiny gin engine with the
// RequireAgentEditAuthority middleware in front of a no-op handler, plus
// an optional AuditServiceProvider so tests can assert on the durable
// audit hook the reject path fires.
//
// `role` is the caller's tenant role (ignored by the middleware under
// test, but threaded in so the harness shape matches the real auth
// middleware's seeding). `userID` is the JWT subject. `user` — when
// non-nil — is attached as the *types.User in ctx so
// IsCrossTenantSuperuser sees CanAccessAllTenants correctly. `apiKey`
// is a non-zero TenantAPIKeyScope to drive the API-key short-circuit
// branch.
func agentEditTestHarness(
	t *testing.T,
	role types.TenantRole,
	userID string,
	user *types.User,
	apiKey *types.TenantAPIKeyScope,
	audit interfaces.AuditLogService,
	cfg *config.Config,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if audit != nil {
		r.Use(AuditServiceProvider(audit))
	}
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.UserIDContextKey, userID)
		if user != nil {
			ctx = context.WithValue(ctx, types.UserContextKey, user)
		}
		if apiKey != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *apiKey)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.PUT("/protected", RequireAgentEditAuthority(cfg), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.DELETE("/protected", RequireAgentEditAuthority(cfg), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/protected", nil)
	r.ServeHTTP(w, req)
	return w
}

// cfgAgentEdit returns a config that turns the cluster-wide
// EnableCrossTenantAccess flag on. The middleware does NOT consult
// EnableRBAC, so per-tenant RBAC is left at its zero value.
func cfgAgentEdit() *config.Config {
	return &config.Config{Tenant: &config.TenantConfig{EnableCrossTenantAccess: true}}
}

// TestRequireAgentEditAuthority_AllowsCrossTenantSuperuser is the
// happy path: a JWT user with CanAccessAllTenants=true sails through
// the PUT /agents/:id gate. The cluster-wide flag must also be on —
// the helper config enables it.
func TestRequireAgentEditAuthority_AllowsCrossTenantSuperuser(t *testing.T) {
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "su1",
		&types.User{ID: "su1", CanAccessAllTenants: true},
		nil, nil, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestRequireAgentEditAuthority_RejectsTenantOwnerEvenAsCreator
// pins the deliberate drop of the creator fallback: an Owner in the
// agent's home tenant who also created it (the previous
// OwnedAgentOrAdmin short-circuit) must now be 403'd. The product
// rule says only cross-workspace superusers can mutate agents.
func TestRequireAgentEditAuthority_RejectsTenantOwnerEvenAsCreator(t *testing.T) {
	audit := &stubDenyAudit{}
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "u-owner",
		&types.User{ID: "u-owner", CanAccessAllTenants: false},
		nil, audit, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusForbidden, w.Code,
		"Owner in home tenant must NOT clear the agent-edit gate (creator fallback is dropped)")
	if assert.Len(t, audit.calls, 1, "reject must fire audit exactly once") {
		assert.Equal(t, "cross_tenant_superuser", string(audit.calls[0].required))
	}
}

// TestRequireAgentEditAuthority_RejectsSystemAdminWithoutCrossTenant
// covers the IsSystemAdmin=true but CanAccessAllTenants=false case.
// The plan's "系统管理员视为普通用户" rule says is_system_admin alone
// does NOT grant agent-edit authority; only the cross-workspace
// attribute does.
func TestRequireAgentEditAuthority_RejectsSystemAdminWithoutCrossTenant(t *testing.T) {
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "sa1",
		&types.User{ID: "sa1", CanAccessAllTenants: false, IsSystemAdmin: true},
		nil, nil, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusForbidden, w.Code,
		"is_system_admin alone must NOT clear the agent-edit gate")
}

// TestRequireAgentEditAuthority_RejectsWhenCrossTenantFlagOff covers
// the "cluster-wide flag is off" branch of IsCrossTenantSuperuser.
// Even a user that has CanAccessAllTenants=true must NOT be allowed
// when EnableCrossTenantAccess is false — the BOTH-required rule is
// the same one tenant-admin routes rely on.
func TestRequireAgentEditAuthority_RejectsWhenCrossTenantFlagOff(t *testing.T) {
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableCrossTenantAccess: false}}
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "su1",
		&types.User{ID: "su1", CanAccessAllTenants: true},
		nil, nil, cfg,
	)
	assert.Equal(t, http.StatusForbidden, w.Code,
		"EnableCrossTenantAccess=false must reject even CanAccessAllTenants=true users")
}

// TestRequireAgentEditAuthority_ShortCircuitsAPIKey covers the
// "API-key principal short-circuits" branch: a JWT user without
// CanAccessAllTenants is normally rejected, but when the request is
// flagged as an API-key principal (TenantAPIKeyScope set), the
// middleware lets it through. Authority is decided upstream by the
// APIKeyRouteAuthorizer's manage_agents capability; this gate must
// not double-deny.
func TestRequireAgentEditAuthority_ShortCircuitsAPIKey(t *testing.T) {
	scope := types.TenantAPIKeyScope{KeyID: 1}
	w := agentEditTestHarness(t,
		types.TenantRoleViewer, "apikey-1",
		&types.User{ID: "apikey-1", CanAccessAllTenants: false},
		&scope, nil, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusOK, w.Code,
		"API-key principal must short-circuit regardless of CanAccessAllTenants")
}

// TestRequireAgentEditAuthority_NilAuditServiceDoesNotPanic mirrors
// the RequireRole nil-audit guard: the reject path must not panic
// when AuditServiceProvider was wired with nil (lite mode).
func TestRequireAgentEditAuthority_NilAuditServiceDoesNotPanic(t *testing.T) {
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "u1",
		&types.User{ID: "u1", CanAccessAllTenants: false},
		nil, nil, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestRequireAgentEditAuthority_NilUserInCtx covers the
// `ctx.Value(types.UserContextKey).(*types.User)` fail-closed branch
// in IsCrossTenantSuperuser. A request that somehow reaches this
// middleware without a *types.User attached (e.g. a JWT-only request
// where the auth middleware skipped the User stash) must be
// rejected, not silently allowed.
func TestRequireAgentEditAuthority_NilUserInCtx(t *testing.T) {
	w := agentEditTestHarness(t,
		types.TenantRoleOwner, "u1",
		nil, // no *types.User attached
		nil, nil, cfgAgentEdit(),
	)
	assert.Equal(t, http.StatusForbidden, w.Code,
		"missing *types.User in ctx must fail closed")
}

// Ensure stubDenyAudit is shared with rbac_audit_test.go. The type is
// already declared in that file; we only declare a local mu here to
// satisfy the linter when the test file is compiled in isolation
// (e.g. by `go test -run X ./internal/middleware`). The shared stub
// already has sync.Mutex embedded via its struct definition, so the
// _ = sync.Mutex{} reference below is just a compile-time guard.
var _ = sync.Mutex{}