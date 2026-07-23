package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------
//
// These mocks mirror the system_password_reset_test pattern: embed the
// production interface, then override only the methods the handler
// actually invokes. Embedding (rather than satisfying manually) means
// any future method added to the interface will nil-deref here, surfacing
// silent contract drift loudly in CI instead of breaking under load.

// stubUserService stands in for interfaces.UserService for the user-
// management endpoints. It records writes so tests can assert what the
// handler persisted without spinning up a real DB.
type stubUserService struct {
	interfaces.UserService

	usersByID    map[string]*types.User
	usersByEmail map[string]*types.User
	usersByName  map[string]*types.User
	listOffset   int
	listLimit    int
	listUsers    []*types.User
	searchCalls  int
	searchLimit  int
	searchQuery  string
	searchUsers  []*types.User

	updateCalls int
	updated     *types.User
	updateErr   error
}

func (s *stubUserService) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if u, ok := s.usersByID[id]; ok {
		return u, nil
	}
	return nil, nil
}

func (s *stubUserService) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if u, ok := s.usersByEmail[email]; ok {
		return u, nil
	}
	return nil, nil
}

func (s *stubUserService) GetUserByUsername(_ context.Context, username string) (*types.User, error) {
	if u, ok := s.usersByName[username]; ok {
		return u, nil
	}
	return nil, nil
}

func (s *stubUserService) ListUsers(_ context.Context, offset, limit int) ([]*types.User, error) {
	s.listOffset = offset
	s.listLimit = limit
	return s.listUsers, nil
}

func (s *stubUserService) SearchUsers(_ context.Context, query string, limit int) ([]*types.User, error) {
	s.searchCalls++
	s.searchQuery = query
	s.searchLimit = limit
	return s.searchUsers, nil
}

func (s *stubUserService) UpdateUser(_ context.Context, u *types.User) error {
	s.updateCalls++
	s.updated = u
	return s.updateErr
}

// stubTenantMemberService stands in for interfaces.TenantMemberService. It
// returns a fixed slice per user; tenant lookup is left to the real
// tenantSvc (or nil for the degraded path).
type stubTenantMemberService struct {
	interfaces.TenantMemberService

	membersByUser map[string][]*types.TenantMember
	err           error
}

func (s *stubTenantMemberService) ListByUser(_ context.Context, userID string) ([]*types.TenantMember, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.membersByUser[userID], nil
}

// stubTenantService stands in for interfaces.TenantService for the
// membership-join step. Returns a fixed map so loadMembershipsForUser
// can attach tenant names without hitting the DB.
type stubTenantService struct {
	interfaces.TenantService

	tenants map[uint64]*types.Tenant
	err     error
}

func (s *stubTenantService) GetTenantsByIDs(_ context.Context, ids []uint64) (map[uint64]*types.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[uint64]*types.Tenant, len(ids))
	for _, id := range ids {
		if t, ok := s.tenants[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// quietLogger silences the package-level logrus instance so test output
// stays focused on assertion failures. Without this, every handler.Info
// hits stdout. We use logger.SetLogLevel rather than swapping the output
// so any sub-package that captures logs in tests still works.
func init() {
	logger.SetLogLevel(logger.LevelError)
}

func newUserMgmtRouter(h *SystemHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// All three endpoints are SystemAdmin-gated at the router layer; the
	// handler does its own self-edit checks. We bypass the middleware here
	// and rely on the per-handler guards (self-edit, last-admin).
	admin := r.Group("/system/admin")
	admin.GET("/users", h.ListUsers)
	admin.GET("/users/:id", h.GetUserDetail)
	admin.PATCH("/users/:id", h.UpdateUser)
	return r
}

func doRequest(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

// sampleUser returns a fresh types.User with stable fields for assertions.
func sampleUser(id, username, email string, isAdmin bool) *types.User {
	return &types.User{
		ID:              id,
		Username:        username,
		Email:           email,
		TenantID:        1,
		IsActive:        true,
		IsSystemAdmin:   isAdmin,
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// ListUsers
// ---------------------------------------------------------------------------

// TestListUsers_FullListUsesListUsersAndProbesOneExtra pins the contract
// for the empty-query path: handler asks the user service for limit+1
// rows to detect "more pages exist" without a separate COUNT(*), and
// the response surfaces a lower-bound total of offset+returned. The +1
// is stripped from the rendered slice.
func TestListUsers_FullListUsesListUsersAndProbesOneExtra(t *testing.T) {
	users := &stubUserService{
		// Return 3 rows even though caller asked for limit=2; handler
		// should trim the third one and report "there's more".
		listUsers: []*types.User{
			sampleUser("u1", "alice", "alice@x", false),
			sampleUser("u2", "bob", "bob@x", false),
			sampleUser("u3", "carol", "carol@x", true),
		},
	}
	tms := &stubTenantMemberService{
		membersByUser: map[string][]*types.TenantMember{
			"u1": {{TenantID: 1, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive}},
			"u2": nil,
			"u3": {{TenantID: 2, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive}},
		},
	}
	tenants := &stubTenantService{
		tenants: map[uint64]*types.Tenant{
			1: {ID: 1, Name: "Alpha"},
			2: {ID: 2, Name: "Beta"},
		},
	}
	h := &SystemHandler{
		userSvc:            users,
		tenantMemberService: tms,
		tenantSvc:          tenants,
	}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users?offset=0&limit=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AdminUserListResponse
	decodeJSON(t, w, &resp)
	if len(resp.Users) != 2 {
		t.Fatalf("expected 2 trimmed rows, got %d", len(resp.Users))
	}
	// handler computes total = offset + len(returned) - 1 when len > limit
	// (the "+1 probe" marker). With offset=0, returned=3, limit=2:
	// total = 0 + 3 - 1 = 2 — the rendered slice is also 2 rows, so this
	// is a precise lower bound equal to the slice length.
	if resp.Total != 2 {
		t.Fatalf("total should be offset+len-1 = 0+3-1 = 2 (the probe marker), got %d", resp.Total)
	}
	if users.listLimit != 3 {
		t.Fatalf("handler should probe with limit+1=3, got %d", users.listLimit)
	}
	if users.searchCalls != 0 {
		t.Fatalf("SearchUsers must not run on the full-list path; got %d calls", users.searchCalls)
	}
	// MembershipCount must reflect active rows joined with tenant names.
	if resp.Users[0].MembershipCount != 1 {
		t.Fatalf("alice should have 1 active membership, got %d", resp.Users[0].MembershipCount)
	}
	if resp.Users[1].MembershipCount != 0 {
		t.Fatalf("bob should have 0 active memberships, got %d", resp.Users[1].MembershipCount)
	}
}

// TestListUsers_SearchQueryDelegatesToSearchUsers confirms the q
// parameter routes through SearchUsers (no ListUsers call). The handler
// doesn't fabricate a total in this path; whatever the service returns
// is what the UI sees.
func TestListUsers_SearchQueryDelegatesToSearchUsers(t *testing.T) {
	users := &stubUserService{
		searchUsers: []*types.User{sampleUser("u1", "alice", "alice@x", false)},
	}
	h := &SystemHandler{userSvc: users}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users?q=ali", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if users.searchCalls != 1 || users.searchQuery != "ali" {
		t.Fatalf("expected SearchUsers(q=ali), got calls=%d q=%q", users.searchCalls, users.searchQuery)
	}
	if users.listLimit != 0 {
		t.Fatalf("ListUsers must not run on the search path; got limit=%d", users.listLimit)
	}
}

// TestListUsers_CapsLimitAt200 pins the upper bound. A client asking for
// 1000 must be served with 200, not the entire users table.
func TestListUsers_CapsLimitAt200(t *testing.T) {
	users := &stubUserService{}
	h := &SystemHandler{userSvc: users}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users?limit=1000", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if users.listLimit != 200 {
		t.Fatalf("limit should be capped to 200, got %d", users.listLimit)
	}
}

// TestListUsers_NilTenantMemberServiceDegradesGracefully documents the
// "best-effort, never fail the whole endpoint" contract. When the
// tenant-member dependency is missing, MembershipCount is 0 for every
// row instead of an HTTP 500.
func TestListUsers_NilTenantMemberServiceDegradesGracefully(t *testing.T) {
	users := &stubUserService{
		listUsers: []*types.User{sampleUser("u1", "alice", "alice@x", false)},
	}
	h := &SystemHandler{userSvc: users} // tenantMemberService deliberately nil
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AdminUserListResponse
	decodeJSON(t, w, &resp)
	if len(resp.Users) != 1 {
		t.Fatalf("expected 1 row, got %d", len(resp.Users))
	}
	if resp.Users[0].MembershipCount != 0 {
		t.Fatalf("MembershipCount should be 0 when tenantMemberService is nil, got %d", resp.Users[0].MembershipCount)
	}
}

// ---------------------------------------------------------------------------
// GetUserDetail
// ---------------------------------------------------------------------------

// TestGetUserDetail_HappyPathJoinsMemberships ensures the detail payload
// surfaces the full active-membership set joined with tenant names and
// the home-tenant marker. The home marker is the user's TenantID column,
// which can differ from any single membership (a user promoted from
// contributor in tenant A to owner of a new tenant B has TenantID=B and
// an active membership in A; only the B row is marked "home").
func TestGetUserDetail_HappyPathJoinsMemberships(t *testing.T) {
	users := &stubUserService{
		usersByID: map[string]*types.User{
			"u1": sampleUser("u1", "alice", "alice@x", false),
		},
	}
	users.usersByID["u1"].TenantID = 5
	tms := &stubTenantMemberService{
		membersByUser: map[string][]*types.TenantMember{
			"u1": {
				{TenantID: 5, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive, JoinedAt: time.Unix(1, 0)},
				{TenantID: 7, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive, JoinedAt: time.Unix(2, 0)},
				// Soft-deleted row must NOT appear in the active filter.
				{TenantID: 8, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended},
			},
		},
	}
	tenants := &stubTenantService{
		tenants: map[uint64]*types.Tenant{
			5: {ID: 5, Name: "Home"},
			7: {ID: 7, Name: "Side"},
		},
	}
	h := &SystemHandler{userSvc: users, tenantMemberService: tms, tenantSvc: tenants}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users/u1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AdminUserDetailResponse
	decodeJSON(t, w, &resp)
	if resp.ID != "u1" {
		t.Fatalf("id mismatch: %q", resp.ID)
	}
	if len(resp.Memberships) != 2 {
		t.Fatalf("expected 2 active memberships, got %d", len(resp.Memberships))
	}
	// The home tenant row must be flagged.
	var home, side *UserMembershipView
	for i := range resp.Memberships {
		m := &resp.Memberships[i]
		switch m.TenantID {
		case 5:
			home = m
		case 7:
			side = m
		}
	}
	if home == nil || !home.IsHomeTenant {
		t.Fatalf("tenant 5 should be marked home, got %+v", home)
	}
	if side == nil || side.IsHomeTenant {
		t.Fatalf("tenant 7 must NOT be home, got %+v", side)
	}
	if home.TenantName != "Home" || side.TenantName != "Side" {
		t.Fatalf("tenant names missing: home=%q side=%q", home.TenantName, side.TenantName)
	}
}

// TestGetUserDetail_UnknownIDReturns404 pins the not-found path. The
// handler must NOT leak "user exists but lookup failed" — that's an
// information disclosure path even for SystemAdmin.
func TestGetUserDetail_UnknownIDReturns404(t *testing.T) {
	users := &stubUserService{usersByID: map[string]*types.User{}}
	h := &SystemHandler{userSvc: users}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodGet, "/system/admin/users/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// UpdateUser
// ---------------------------------------------------------------------------

// TestUpdateUser_PatchUsernameAppliesAndAudits is the happy-path PATCH
// test. The audit row must carry the {from, to} change so a forensic
// reader can see exactly what shifted, without diffing two snapshots.
func TestUpdateUser_PatchUsernameAppliesAndAudits(t *testing.T) {
	target := sampleUser("u1", "alice", "alice@x", false)
	users := &stubUserService{
		usersByID:   map[string]*types.User{"u1": target},
		usersByName: map[string]*types.User{},
	}
	audits := &capturingAuditService{}
	h := &SystemHandler{userSvc: users, auditSvc: audits}

	w := doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{Username: stringPtr("alice2")})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if target.Username != "alice2" {
		t.Fatalf("username not updated, got %q", target.Username)
	}
	if users.updateCalls != 1 {
		t.Fatalf("expected 1 UpdateUser call, got %d", users.updateCalls)
	}
	if len(audits.entries) != 1 || audits.entries[0].Action != types.AuditActionSystemUserUpdated {
		t.Fatalf("expected one AuditActionSystemUserUpdated row, got %+v", audits.entries)
	}
	if !strings.Contains(string(audits.entries[0].Details), `"username"`) {
		t.Fatalf("audit details must include the changed field, got %s", audits.entries[0].Details)
	}
}

// TestUpdateUser_RejectsSelfEdit pins the self-lockout guard. The caller
// must use the profile settings endpoint to edit their own account.
func TestUpdateUser_RejectsSelfEdit(t *testing.T) {
	// Embedding the actor id "u1" in the request context is what makes
	// the handler consider this a self-edit. Our router harness
	// doesn't inject a user id, so we have to fall back on the
	// stubUserService returning the same user by id (the handler calls
	// GetUserByID) and then types.UserIDFromContext returning "" — which
	// means "not self". To exercise the self branch we need the actor id
	// in ctx. We do this with a one-off router that injects "u1".
	target := sampleUser("u1", "alice", "alice@x", false)
	users := &stubUserService{
		usersByID:   map[string]*types.User{"u1": target},
		usersByName: map[string]*types.User{},
	}
	h := &SystemHandler{userSvc: users}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.UserIDContextKey, "u1")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.PATCH("/system/admin/users/:id", h.UpdateUser)

	w := doRequest(t, r, http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{Username: stringPtr("alice2")})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if users.updateCalls != 0 {
		t.Fatalf("self-edit must not reach UpdateUser; got %d calls", users.updateCalls)
	}
}

// TestUpdateUser_UsernameCollisionRejected pins the uniqueness guard.
// Without it, two admins could end up pointing at the same username
// and the GORM unique index would surface a 500 instead of a clean
// 400 with an actionable message.
func TestUpdateUser_UsernameCollisionRejected(t *testing.T) {
	target := sampleUser("u1", "alice", "alice@x", false)
	other := sampleUser("u2", "alice2", "other@x", false)
	users := &stubUserService{
		usersByID:   map[string]*types.User{"u1": target},
		usersByName: map[string]*types.User{"alice2": other},
	}
	h := &SystemHandler{userSvc: users}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{Username: stringPtr("alice2")})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("error should mention collision, got %s", w.Body.String())
	}
	if users.updateCalls != 0 {
		t.Fatalf("collision must short-circuit before UpdateUser; got %d calls", users.updateCalls)
	}
}

// TestUpdateUser_RejectsDisableSystemAdmin pins the last-admin /
// disable-admin guard. Flipping an active system admin to inactive
// would lock the platform out of administrative recovery.
func TestUpdateUser_RejectsDisableSystemAdmin(t *testing.T) {
	target := sampleUser("u1", "admin", "admin@x", true)
	users := &stubUserService{
		usersByID:   map[string]*types.User{"u1": target},
		usersByName: map[string]*types.User{},
	}
	h := &SystemHandler{userSvc: users}
	w := doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{IsActive: boolPtr(false)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "system admin") {
		t.Fatalf("error should mention system-admin role, got %s", w.Body.String())
	}
	if users.updateCalls != 0 {
		t.Fatalf("disable-admin must short-circuit; got %d calls", users.updateCalls)
	}
}

// TestUpdateUser_NoChangesDoesNotAudit confirms the "PATCH to current
// values" no-op. Writing an audit row for every probe pollutes the
// audit log without adding forensic value.
func TestUpdateUser_NoChangesDoesNotAudit(t *testing.T) {
	target := sampleUser("u1", "alice", "alice@x", false)
	users := &stubUserService{
		usersByID:   map[string]*types.User{"u1": target},
		usersByName: map[string]*types.User{},
	}
	audits := &capturingAuditService{}
	h := &SystemHandler{userSvc: users, auditSvc: audits}

	w := doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{
			Username: stringPtr("alice"),
			Email:    stringPtr("alice@x"),
			IsActive: boolPtr(true),
		})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if users.updateCalls != 0 {
		t.Fatalf("no-op must not call UpdateUser; got %d calls", users.updateCalls)
	}
	if len(audits.entries) != 0 {
		t.Fatalf("no-op must not write an audit row; got %d", len(audits.entries))
	}
}

// TestUpdateUser_RejectsEmptyPatchAndUnknownUser covers the two "easy"
// 400/404 paths in one test: an empty body (no fields to update) and
// a target id that doesn't resolve.
func TestUpdateUser_RejectsEmptyPatchAndUnknownUser(t *testing.T) {
	users := &stubUserService{usersByID: map[string]*types.User{}}
	h := &SystemHandler{userSvc: users}

	// Empty body → 400
	w := doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/u1",
		UpdateUserRequest{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: expected 400, got %d body=%s", w.Code, w.Body.String())
	}

	// Unknown id → 404
	w = doRequest(t, newUserMgmtRouter(h), http.MethodPatch, "/system/admin/users/missing",
		UpdateUserRequest{Username: stringPtr("foo")})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }