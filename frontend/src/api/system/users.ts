import { get, post, patch, del } from '@/utils/request'

// ---------------------------------------------------------------------------
// System Admin user-management surface
// ---------------------------------------------------------------------------
// P2 follow-up to the SystemAdmin role surface (promote/revoke/reset-password,
// all in api/system/index.ts). This module covers the *cross-cutting* view:
// every registered user, what tenants they belong to, and PATCH-style edits
// to username / email / is_active. Distinct from the role-management surface
// because the audience is "find and inspect arbitrary users", not "manage
// who is a SystemAdmin". Keeping the two surfaces in separate files lets the
// user-management view evolve independently (e.g. adding audit-log filters,
// bulk operations) without churning the SystemAdmin-only tab.
//
// All endpoints below are gated server-side by the SystemAdmin middleware
// (router.go:885) so the front-end doesn't need to re-check role.
// ---------------------------------------------------------------------------

/**
 * TenantRole mirrors types.TenantRole on the backend. We type it as the
 * string-literal union the handler actually emits rather than a free
 * `string`, so the user-management drawer can exhaustively switch on it
 * when rendering the role pill (owner/admin/contributor/viewer). Any new
 * role added in tenant_member.go without a corresponding update here will
 * be silently rendered as the unknown-fallback label until both sides
 * match — see the test in backend/internal/types/tenant_member_test.go.
 */
export type TenantRole = 'owner' | 'admin' | 'contributor' | 'viewer'

/**
 * TenantMemberStatus mirrors types.TenantMemberStatus. Only `active`
 * rows are surfaced by GET /users/:id (the drawer filter), but the type
 * stays exhaustive so consumers can render historical/suspended rows
 * elsewhere (e.g. audit detail) without a new field.
 */
export type TenantMemberStatus = 'active' | 'invited' | 'suspended'

/**
 * UserInfo mirrors types.UserInfo — the public-facing projection every
 * authenticated endpoint returns. Re-declared here instead of imported
 * from api/auth (LoginResponse) so this module doesn't reach into a
 * tenant-scoped file from a system-scoped context.
 */
export interface UserInfo {
  id: string
  username: string
  email: string
  avatar?: string
  is_active: boolean
  is_system_admin: boolean
  created_at: string
  updated_at: string
}

/**
 * One row of the GET /system/admin/users/:id response. Joins the
 * tenant_members row with its tenant's display name and a marker for
 * the user's home tenant. is_home_tenant is set when tenant_id equals
 * user.tenant_id (the column User.TenantID), so the drawer can show a
 * "Home" pill without a second lookup.
 */
export interface UserMembershipView {
  tenant_id: number
  tenant_name: string
  role: TenantRole
  status: TenantMemberStatus
  joined_at: string
  is_home_tenant: boolean
}

/**
 * One row of the paginated GET /system/admin/users response. Embeds
 * UserInfo so existing consumers (e.g. promote/revoke dialogs that
 * only need username + email) keep working with the same shape, and
 * adds MembershipCount for the table column "member of N spaces".
 * The full per-user list of spaces is fetched on demand via the
 * /:id endpoint — keeping list responses cheap on a large users table.
 */
export interface AdminUserListItem extends UserInfo {
  membership_count: number
}

/**
 * Paginated response for GET /system/admin/users. `total` is the
 * underlying COUNT(*) — when the query string `q` is empty, the
 * backend detects "more pages exist" by fetching limit+1 and reports
 * a conservative lower-bound total. When `q` is set, the total mirrors
 * the SearchUsers window (no COUNT roundtrip) so the UI should treat
 * it as a "current page size" rather than a hard pagination ceiling.
 */
export interface AdminUserListResponse {
  total: number
  users: AdminUserListItem[]
}

/**
 * Response for GET /system/admin/users/:id — public UserInfo plus
 * every active tenant membership the user holds. Memberships are
 * already filtered server-side to status=active; soft-deleted /
 * suspended rows stay in the audit log, not the drawer.
 */
export interface AdminUserDetailResponse extends UserInfo {
  memberships: UserMembershipView[]
}

/**
 * List parameters for GET /system/admin/users. offset/limit mirror the
 * defaults documented in handler/system.go (offset=0, limit=50, max=200).
 * q is optional: when empty, the backend returns the full list newest
 * first; when set, it returns a substring match against username/email
 * (case-insensitive). Both paths return AdminUserListResponse.
 */
export interface ListUsersParams {
  offset?: number
  limit?: number
  /** Case-insensitive substring against username or email. */
  q?: string
}

/**
 * Fetch the paginated, optionally filtered list of every registered user.
 *
 * Backend: GET /api/v1/system/admin/users (SystemAdmin only). Returns
 * AdminUserListResponse directly — no {data: ...} wrapping (see
 * utils/request.ts:97 — the axios interceptor unwraps response.data
 * project-wide).
 *
 * The query string is built manually because the shared `get` helper
 * doesn't accept a config object, matching the convention used by
 * listSystemAdmins and listSystemAuditLog.
 */
export async function listUsers(params?: ListUsersParams): Promise<AdminUserListResponse> {
  const qs = new URLSearchParams()
  if (params?.offset != null) qs.set('offset', String(params.offset))
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.q) qs.set('q', params.q)
  const suffix = qs.toString() ? `?${qs.toString()}` : ''
  const response = await get(`/api/v1/system/admin/users${suffix}`)
  return response as unknown as AdminUserListResponse
}

/**
 * Fetch a single user's full profile plus their active tenant memberships.
 *
 * Backend: GET /api/v1/system/admin/users/:id (SystemAdmin only). Returns
 * AdminUserDetailResponse directly — same unwrap contract as listUsers.
 * Throws (via the axios interceptor) if the id is unknown.
 */
export async function getUserDetail(userId: string): Promise<AdminUserDetailResponse> {
  const response = await get(`/api/v1/system/admin/users/${encodeURIComponent(userId)}`)
  return response as unknown as AdminUserDetailResponse
}

/**
 * PATCH payload for editing another user's account. Each field is
 * optional — only keys the caller sets are applied; everything else is
 * preserved. Field-level validation (uniqueness, self-lockout,
 * last-admin guard) is enforced server-side; this module intentionally
 * does not pre-flight those checks because the backend is the source
 * of truth and a duplicate email check would still race against another
 * admin's edit. Sending an empty object is rejected with HTTP 400.
 */
export interface UpdateUserRequest {
  username?: string
  email?: string
  is_active?: boolean
}

/**
 * Update a user's username, email, and/or active status. PATCH semantics
 * — only the keys set on the request body are touched. The backend
 * returns the updated UserInfo directly; no envelope.
 *
 * Backend enforces:
 *   - caller cannot edit themselves (use profile settings instead)
 *   - username / email uniqueness
 *   - cannot disable an active system admin (revoke first)
 * Each of these errors surfaces as a thrown exception with the backend's
 * message preserved on err.message, ready for t-message.error display.
 */
export async function updateUser(
  userId: string,
  req: UpdateUserRequest,
): Promise<UserInfo> {
  const response = await patch(
    `/api/v1/system/admin/users/${encodeURIComponent(userId)}`,
    req,
  )
  return response as unknown as UserInfo
}

/**
 * Reset another user's password and revoke all their active sessions.
 * Re-exported here so the user-management view can fire it from the
 * drawer without crossing back into api/system/index.ts (which holds
 * the SystemAdmin role surface). The backend route remains
 * POST /system/admin/users/reset-password and is unchanged.
 *
 * The backend rejects attempts to reset the caller's own password —
 * surfacing here as a thrown exception; the caller-side guard in
 * UserManagement.vue disables the button preemptively to avoid the
 * round-trip.
 */
export interface ResetUserPasswordRequest {
  email: string
  new_password: string
}

export async function resetUserPassword(req: ResetUserPasswordRequest): Promise<{ message: string }> {
  const response = await post('/api/v1/system/admin/users/reset-password', req)
  return response as unknown as { message: string }
}

/**
 * Payload for POST /api/v1/system/admin/users — SystemAdmin-managed
 * user creation. Deliberately bypasses /auth/register (no email/invite
 * flow) and lands the user in the tenantless state (User.TenantID=0)
 * until an admin assigns a workspace via the space-management surface.
 *
 * Field-level validation lives on the backend (UserService.CreateUser):
 *   - bcrypt hashing of `password`
 *   - ValidatePasswordPolicy on `password`
 *   - GetUserByEmail / GetUserByUsername uniqueness checks
 * The handler returns the new UserInfo directly — same unwrap contract
 * as updateUser. Sending a duplicate email/username surfaces as HTTP
 * 400 with the backend's message preserved on err.message.
 */
export interface CreateUserRequest {
  username: string
  email: string
  password: string
  /** Defaults to true on the server when omitted. */
  is_active?: boolean
  /**
   * Optional workspace to enroll the new user into. When supplied,
   * `tenant_role` must also be supplied; both are ignored otherwise.
   * Backend: POST /api/v1/system/admin/users (SystemAdmin only).
   */
  tenant_id?: number
  /**
   * Role to grant the new user inside `tenant_id`. Must pair with
   * `tenant_id`. Role=owner also pins the user's home tenant so they
   * land inside that workspace on first login; other roles leave
   * TenantID=0 and the user picks a home workspace via onboarding.
   */
  tenant_role?: TenantRole
}

/**
 * Create a new user as SystemAdmin. The freshly created user is in
 * the tenantless state and must be assigned to a workspace via
 * `createAdminTenant` (or by joining an existing one). The returned
 * UserInfo is the public-facing projection — `tenant_id` will be 0.
 *
 * Backend: POST /api/v1/system/admin/users (SystemAdmin only).
 */
export async function createUser(req: CreateUserRequest): Promise<UserInfo> {
  const response = await post('/api/v1/system/admin/users', req)
  return response as unknown as UserInfo
}

/**
 * Delete a user as SystemAdmin. Backend enforces two preconditions:
 *   - cannot delete self (return 400)
 *   - cannot delete the last remaining system admin (return 400)
 * Memberships owned by the user are cascade-removed server-side; the
 * response is a simple acknowledgement message. If the user id is
 * unknown the backend returns 404 — both surface as thrown exceptions
 * via the shared axios interceptor.
 *
 * Backend: DELETE /api/v1/system/admin/users/:id (SystemAdmin only).
 */
export async function deleteUser(userId: string): Promise<{ message: string }> {
  const response = await del(`/api/v1/system/admin/users/${encodeURIComponent(userId)}`)
  return response as unknown as { message: string }
}