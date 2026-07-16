package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// memberProvisioningMemberStub is a TenantMemberService whose ONLY useful
// method is ListByUser. The handler's A2 / A4 logic only reads this one
// method, so stubs keep the test focused without dragging in the full
// member surface. Named distinct from the existing stubMemberService in
// tenant_member_test.go because that fixture exposes a different method
// surface (AddMember / ListByTenant / ...) used by the membership handler.
type memberProvisioningMemberStub struct {
	interfaces.TenantMemberService
	listByUser func(ctx context.Context, userID string) ([]*types.TenantMember, error)
}

func (s *memberProvisioningMemberStub) ListByUser(ctx context.Context, userID string) ([]*types.TenantMember, error) {
	return s.listByUser(ctx, userID)
}

// memberProvisioningUserStub extends the UserService embedding with a
// configurable Register / GetUserByEmail / GetCurrentUser /
// BuildLoginMemberships surface so the A2 / A4 logic can be exercised
// without dragging the real UserService implementation in.
type memberProvisioningUserStub struct {
	interfaces.UserService
	register    func(ctx context.Context, req *types.RegisterRequest) (*types.User, error)
	getByEmail  func(ctx context.Context, email string) (*types.User, error)
	getCurrent  func(ctx context.Context) (*types.User, error)
	buildLogins func(ctx context.Context, user *types.User, tenant *types.Tenant) []types.Membership
}

func (s *memberProvisioningUserStub) Register(ctx context.Context, req *types.RegisterRequest) (*types.User, error) {
	return s.register(ctx, req)
}

func (s *memberProvisioningUserStub) GetUserByEmail(ctx context.Context, email string) (*types.User, error) {
	return s.getByEmail(ctx, email)
}

func (s *memberProvisioningUserStub) GetCurrentUser(ctx context.Context) (*types.User, error) {
	return s.getCurrent(ctx)
}

func (s *memberProvisioningUserStub) BuildLoginMemberships(ctx context.Context, user *types.User, tenant *types.Tenant) []types.Membership {
	return s.buildLogins(ctx, user, tenant)
}

// --- A2: Member-aware provisioning downgrade in Register ---

// TestRegister_MemberEmailDowngradesToTenantless verifies A2:
// when the registrant already has an active TenantRoleMember row in any
// tenant we drop create_personal -> tenantless so the system does NOT
// silently spin up a parallel personal workspace for them.
func TestRegister_MemberEmailDowngradesToTenantless(t *testing.T) {
	var captured *types.RegisterRequest
	us := &memberProvisioningUserStub{
		register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
			captured = req
			return &types.User{ID: "u-existing", Email: req.Email}, nil
		},
		getByEmail: func(_ context.Context, email string) (*types.User, error) {
			return &types.User{ID: "u-existing", Email: email}, nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "u-existing", TenantID: 42, Role: types.TenantRoleMember, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{
			RegistrationMode:  config.AuthRegistrationModeSelfServe,
			DefaultTenantMode: config.AuthDefaultTenantModeCreatePersonal,
		},
	}, us, nil, nil, nil, ms)

	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("member-aware downgrade should still create the user; got %d body=%s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatalf("Register was not invoked")
	}
	if captured.TenantProvisioning != types.TenantProvisioningTenantless {
		t.Fatalf("provisioning = %q, want tenantless (member-aware downgrade)", captured.TenantProvisioning)
	}
}

// TestRegister_NonMemberEmailKeepsCreatePersonal verifies A2 does NOT
// downgrade when the registrant is already in the user table but only
// holds owner/admin/contributor/viewer roles. TenantProvisioning must
// remain create_personal.
func TestRegister_NonMemberEmailKeepsCreatePersonal(t *testing.T) {
	var captured *types.RegisterRequest
	us := &memberProvisioningUserStub{
		register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
			captured = req
			return &types.User{ID: "u-existing", Email: req.Email}, nil
		},
		getByEmail: func(_ context.Context, email string) (*types.User, error) {
			return &types.User{ID: "u-existing", Email: email}, nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "u-existing", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{
			RegistrationMode:  config.AuthRegistrationModeSelfServe,
			DefaultTenantMode: config.AuthDefaultTenantModeCreatePersonal,
		},
	}, us, nil, nil, nil, ms)

	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("non-member email should keep create_personal; got %d body=%s", w.Code, w.Body.String())
	}
	if captured.TenantProvisioning != types.TenantProvisioningCreatePersonal {
		t.Fatalf("provisioning = %q, want create_personal", captured.TenantProvisioning)
	}
}

// TestRegister_NewEmailKeepsCreatePersonal verifies A2 does NOT downgrade
// when the registrant email is not yet in the user table. This is the
// happy path for the vast majority of fresh registrations.
func TestRegister_NewEmailKeepsCreatePersonal(t *testing.T) {
	var captured *types.RegisterRequest
	us := &memberProvisioningUserStub{
		register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
			captured = req
			return &types.User{ID: "u-new", Email: req.Email}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return nil, nil // email not found — brand new user
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			t.Fatalf("ListByUser must not be called when email is new")
			return nil, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{
			RegistrationMode:  config.AuthRegistrationModeSelfServe,
			DefaultTenantMode: config.AuthDefaultTenantModeCreatePersonal,
		},
	}, us, nil, nil, nil, ms)

	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("new email should keep create_personal; got %d body=%s", w.Code, w.Body.String())
	}
	if captured.TenantProvisioning != types.TenantProvisioningCreatePersonal {
		t.Fatalf("provisioning = %q, want create_personal", captured.TenantProvisioning)
	}
}

// TestRegister_MemberLookupFailureFailsOpen verifies A2 fail-open: a
// transient lookup error must NOT block the registration. We fall back
// to the configured DefaultTenantMode so legitimate new accounts are
// not dropped on a DB hiccup.
func TestRegister_MemberLookupFailureFailsOpen(t *testing.T) {
	var captured *types.RegisterRequest
	us := &memberProvisioningUserStub{
		register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
			captured = req
			return &types.User{ID: "u-new", Email: req.Email}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return &types.User{ID: "u-existing", Email: "alice@example.com"}, nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return nil, context.DeadlineExceeded // transient DB hiccup
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{
			RegistrationMode:  config.AuthRegistrationModeSelfServe,
			DefaultTenantMode: config.AuthDefaultTenantModeCreatePersonal,
		},
	}, us, nil, nil, nil, ms)

	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("transient member-lookup failure must not block registration; got %d body=%s", w.Code, w.Body.String())
	}
	if captured.TenantProvisioning != types.TenantProvisioningCreatePersonal {
		t.Fatalf("provisioning = %q, want create_personal (fail-open)", captured.TenantProvisioning)
	}
}

// TestRegister_AlreadyTenantlessFromConfigNotOverridden verifies A2 only
// downgrades when the configured DefaultTenantMode is create_personal.
// If the operator already configured tenantless at the deployment level,
// we keep tenantless — never re-promote to create_personal.
func TestRegister_AlreadyTenantlessFromConfigNotOverridden(t *testing.T) {
	var captured *types.RegisterRequest
	us := &memberProvisioningUserStub{
		register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
			captured = req
			return &types.User{ID: "u-existing", Email: req.Email}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return &types.User{ID: "u-existing", Email: "alice@example.com"}, nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			t.Fatalf("ListByUser must not be called when DefaultTenantMode is already tenantless")
			return nil, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{
			RegistrationMode:  config.AuthRegistrationModeSelfServe,
			DefaultTenantMode: config.AuthDefaultTenantModeTenantless,
		},
	}, us, nil, nil, nil, ms)

	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("tenantless default mode should keep tenantless; got %d body=%s", w.Code, w.Body.String())
	}
	if captured.TenantProvisioning != types.TenantProvisioningTenantless {
		t.Fatalf("provisioning = %q, want tenantless", captured.TenantProvisioning)
	}
}

// --- A4: can_create_tenant projection ---

// newAuthMeRouter mounts the /auth/me handler with the supplied AuthHandler
// so we can inspect the can_create_tenant projection under different
// member/non-member configurations.
func newAuthMeRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.GET("/auth/me", h.GetCurrentUser)
	return r
}

func doAuthMe(t *testing.T, r *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestGetCurrentUser_MemberUserCannotCreateTenant verifies A4: a user
// holding TenantRoleMember in any tenant gets can_create_tenant=false
// regardless of the deployment's self_service_creation_enabled flag.
func TestGetCurrentUser_MemberUserCannotCreateTenant(t *testing.T) {
	us := &memberProvisioningUserStub{
		getCurrent: func(_ context.Context) (*types.User, error) {
			return &types.User{ID: "u-member", Email: "alice@example.com", TenantID: 42}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return nil, nil
		},
		buildLogins: func(_ context.Context, _ *types.User, _ *types.Tenant) []types.Membership {
			return nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "u-member", TenantID: 42, Role: types.TenantRoleMember, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Tenant: &config.TenantConfig{EnableCrossTenantAccess: false},
		Auth:   &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, nil, nil, ms)

	w := doAuthMe(t, newAuthMeRouter(h))
	if w.Code != http.StatusOK {
		t.Fatalf("GetCurrentUser must succeed; got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":false`) {
		t.Fatalf("can_create_tenant must be false for Member users; body=%s", w.Body.String())
	}
}

// TestGetCurrentUser_NonMemberUserKeepsCreateTenant verifies A4 does
// NOT suppress can_create_tenant when the user has zero Member rows.
// The capability should reflect the deployment's self_service_creation_enabled
// flag (true here via no policy deny in the cfg).
func TestGetCurrentUser_NonMemberUserKeepsCreateTenant(t *testing.T) {
	us := &memberProvisioningUserStub{
		getCurrent: func(_ context.Context) (*types.User, error) {
			return &types.User{ID: "u-viewer", Email: "alice@example.com", TenantID: 42}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return nil, nil
		},
		buildLogins: func(_ context.Context, _ *types.User, _ *types.Tenant) []types.Membership {
			return nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "u-viewer", TenantID: 42, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Tenant: &config.TenantConfig{EnableCrossTenantAccess: false},
		Auth:   &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, nil, nil, ms)

	w := doAuthMe(t, newAuthMeRouter(h))
	if w.Code != http.StatusOK {
		t.Fatalf("GetCurrentUser must succeed; got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":true`) {
		t.Fatalf("can_create_tenant must be true for non-Member users; body=%s", w.Body.String())
	}
}

// TestGetCurrentUser_CrossTenantSuperuserBypassesMemberCheck verifies A4
// the cross-tenant superuser branch: CanAccessAllTenants=true short-circuits
// to can_create_tenant=true regardless of any Member row.
func TestGetCurrentUser_CrossTenantSuperuserBypassesMemberCheck(t *testing.T) {
	us := &memberProvisioningUserStub{
		getCurrent: func(_ context.Context) (*types.User, error) {
			return &types.User{
				ID: "u-super", Email: "super@example.com", TenantID: 1,
				CanAccessAllTenants: true,
			}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return nil, nil
		},
		buildLogins: func(_ context.Context, _ *types.User, _ *types.Tenant) []types.Membership {
			return nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			t.Fatalf("ListByUser must not be called for cross-tenant superusers")
			return nil, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Tenant: &config.TenantConfig{EnableCrossTenantAccess: true},
		Auth:   &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, nil, nil, ms)

	w := doAuthMe(t, newAuthMeRouter(h))
	if w.Code != http.StatusOK {
		t.Fatalf("GetCurrentUser must succeed; got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":true`) {
		t.Fatalf("can_create_tenant must be true for cross-tenant superuser; body=%s", w.Body.String())
	}
}

// TestGetCurrentUser_MemberLookupFailureFailsOpenToPolicy verifies A4
// fail-open: a transient member-lookup error must NOT blank the
// capability for the user; we fall back to the policy flag.
func TestGetCurrentUser_MemberLookupFailureFailsOpenToPolicy(t *testing.T) {
	us := &memberProvisioningUserStub{
		getCurrent: func(_ context.Context) (*types.User, error) {
			return &types.User{ID: "u1", Email: "alice@example.com", TenantID: 42}, nil
		},
		getByEmail: func(_ context.Context, _ string) (*types.User, error) {
			return nil, nil
		},
		buildLogins: func(_ context.Context, _ *types.User, _ *types.Tenant) []types.Membership {
			return nil
		},
	}
	ms := &memberProvisioningMemberStub{
		listByUser: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return nil, context.DeadlineExceeded
		},
	}
	h := NewAuthHandler(&config.Config{
		Tenant: &config.TenantConfig{EnableCrossTenantAccess: false},
		Auth:   &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, nil, nil, ms)

	w := doAuthMe(t, newAuthMeRouter(h))
	if w.Code != http.StatusOK {
		t.Fatalf("transient lookup failure must not block GetCurrentUser; got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":true`) {
		t.Fatalf("can_create_tenant must fall back to the policy flag (true); body=%s", w.Body.String())
	}
}