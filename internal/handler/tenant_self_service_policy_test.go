package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func tenantPolicyErrorCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) == 0 {
			return
		}
		if appErr, ok := c.Errors.Last().Err.(*apperrors.AppError); ok {
			c.JSON(appErr.HTTPCode, gin.H{"error": appErr})
		}
	}
}

type tenantPolicySettingService struct {
	interfaces.SystemSettingService
	enabled bool
}

func (s *tenantPolicySettingService) GetBool(context.Context, string, string, bool) bool {
	return s.enabled
}

func (s *tenantPolicySettingService) GetInt(_ context.Context, _ string, _ string, def int64) int64 {
	return def
}

type tenantPolicyUserService struct {
	interfaces.UserService
	user *types.User
}

func (s *tenantPolicyUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return s.user, nil
}

func (s *tenantPolicyUserService) BuildLoginMemberships(context.Context, *types.User, *types.Tenant) []types.Membership {
	return []types.Membership{}
}

// UpdateUser is a no-op stub for the non-Member create success path:
// when the caller has TenantID=0 CreateTenant will promote the new
// tenant to their home tenant, which calls UpdateUser. We don't care
// about persistence here, just don't panic.
func (s *tenantPolicyUserService) UpdateUser(context.Context, *types.User) error {
	return nil
}

type tenantPolicyTenantService struct {
	interfaces.TenantService
	createCalls int
}

func (s *tenantPolicyTenantService) CreateTenant(_ context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	s.createCalls++
	tenant.ID = 99
	return tenant, nil
}

func TestCreateTenantRejectsRegularUserWhenSelfServiceDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "regular-user"}},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.Use(tenantPolicyErrorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"blocked"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	if !strings.Contains(w.Body.String(), `"code":2005`) {
		t.Fatalf("response missing typed disabled code: %s", w.Body.String())
	}
}

func TestCreateTenantAllowsCrossTenantSuperuserWhenSelfServiceDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service: tenants,
		userService: &tenantPolicyUserService{user: &types.User{
			ID:                  "super-user",
			TenantID:            1,
			CanAccessAllTenants: true,
		}},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"admin-created"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant called %d times, want 1", tenants.createCalls)
	}
}

func TestAuthMeProjectsTenantCreationCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{
		userService: &tenantPolicyUserService{user: &types.User{
			ID:       "tenantless-user",
			Username: "tenantless",
			Email:    "tenantless@example.com",
		}},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.GET("/auth/me", h.GetCurrentUser)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"can_create_tenant":false`) {
		t.Fatalf("response missing capability: %s", w.Body.String())
	}
}

// --- A3: handler-level Member-role create guard on POST /tenants ---

// tenantPolicyMemberService lets each test inject the ListByUser answer so
// we can exercise the "caller is a Member in any tenant" defence without
// standing up the real tenant member service. EnsureOwner / RemoveMember
// are stubbed as no-ops so the post-create membership bootstrap in
// TenantHandler.CreateTenant (which fires for the non-Member success path)
// doesn't nil-panic. Tests that need to assert the bootstrap did or did
// not happen can inspect those counters.
type tenantPolicyMemberService struct {
	interfaces.TenantMemberService
	list        func(ctx context.Context, userID string) ([]*types.TenantMember, error)
	ensureCalls int
	removeCalls int
}

func (s *tenantPolicyMemberService) ListByUser(ctx context.Context, userID string) ([]*types.TenantMember, error) {
	return s.list(ctx, userID)
}

func (s *tenantPolicyMemberService) EnsureOwner(_ context.Context, _ string, _ uint64) (*types.TenantMember, error) {
	s.ensureCalls++
	return &types.TenantMember{UserID: "x", TenantID: 99, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive}, nil
}

func (s *tenantPolicyMemberService) RemoveMember(_ context.Context, _ string, _ uint64) error {
	s.removeCalls++
	return nil
}

// TestCreateTenantRejectsMemberUser verifies A3: when the caller is not a
// cross-tenant superuser and ListByUser returns at least one active
// TenantRoleMember row, CreateTenant must respond 403 BEFORE delegating
// to TenantService. This is the handler-level defence that backs up the
// POST /tenants route's g.Viewer() guard when EnableRBAC=false logs and
// passes the route middleware.
func TestCreateTenantRejectsMemberUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantPolicyMemberService{
		list: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "member-user", TenantID: 7, Role: types.TenantRoleMember, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := &TenantHandler{
		service:          tenants,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "member-user"}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"member-attempt"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403 for Member user", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant must not be called for Member users; got %d calls", tenants.createCalls)
	}
}

// TestCreateTenantAllowsNonMemberUser verifies A3 does NOT regress the
// legacy self-service path: a user with zero Member rows (or zero rows
// entirely) must still be able to create a workspace when self-service
// is enabled.
func TestCreateTenantAllowsNonMemberUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantPolicyMemberService{
		list: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			return []*types.TenantMember{
				{UserID: "viewer-user", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
			}, nil
		},
	}
	h := &TenantHandler{
		service:          tenants,
		userService:      &tenantPolicyUserService{user: &types.User{ID: "viewer-user"}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"viewer-attempt"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201 for non-Member user", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant called %d times, want 1", tenants.createCalls)
	}
}

// TestCreateTenantAllowsCrossTenantSuperuserIgnoresMember verifies A3's
// cross-tenant superuser branch: CanAccessAllTenants=true skips the
// Member check entirely. Mirrors the A4 can_create_tenant projection.
func TestCreateTenantAllowsCrossTenantSuperuserIgnoresMember(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenants := &tenantPolicyTenantService{}
	members := &tenantPolicyMemberService{
		list: func(_ context.Context, _ string) ([]*types.TenantMember, error) {
			t.Fatalf("ListByUser must not be called for cross-tenant superusers")
			return nil, nil
		},
	}
	h := &TenantHandler{
		service: tenants,
		userService: &tenantPolicyUserService{user: &types.User{
			ID:                  "super-user",
			TenantID:            1,
			CanAccessAllTenants: true,
		}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: false},
	}
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/tenants", h.CreateTenant)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(`{"name":"super-attempt"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201 for cross-tenant superuser", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant called %d times, want 1", tenants.createCalls)
	}
}
