import { get, post, patch, del } from '@/utils/request'
import type { TenantInfo } from '@/api/tenant'

// ---------------------------------------------------------------------------
// System Admin workspace-management surface
// ---------------------------------------------------------------------------
// P3 follow-up to the SystemAdmin user-management surface (api/system/users.ts).
// Every endpoint below is gated server-side by the SystemAdmin middleware
// (router.go:885) so the front-end doesn't need to re-check role.
//
// All routes live under /api/v1/system/admin/tenants — distinct from the
// per-tenant Owner-facing /api/v1/tenants tree. The admin tree carries
// additional fields (status, storage_quota_gb) and supports listing every
// workspace across the platform, not just the caller's own.
//
// Type derivation:
//   * SystemTenantInfo embeds TenantInfo so existing Owner-facing code
//     (e.g. updateTenant in api/tenant/index.ts) keeps working without
//     shape changes, and the management table can read id/name/status
//     directly off the row.
//   * The backend's *types.Tenant serialises more fields than TenantInfo
//     (storage_used, retriever_engines, ...). TypeScript can't see those
//     without an explicit cast; we mark them optional in case the backend
//     trims them later.
// ---------------------------------------------------------------------------

/**
 * One row of GET /system/admin/tenants. Carries the standard TenantInfo
 * payload plus two join-derived columns the table renders inline:
 *   - `member_count` — number of active tenant_members
 *   - `owner_username` — username of the first active Owner (empty string
 *     when none exists; defensive)
 */
export interface SystemTenantInfo extends TenantInfo {
  member_count: number
  owner_username?: string
}

/**
 * Paginated response for GET /system/admin/tenants. Mirrors the
 * AdminUserListResponse shape (page/page_size, total, items[]) so the
 * pagination control on SpaceManagement.vue can reuse the same logic
 * as UserManagement.vue.
 */
export interface AdminTenantListResponse {
  total: number
  items: SystemTenantInfo[]
  page: number
  page_size: number
}

/**
 * Query parameters for GET /system/admin/tenants.
 * `keyword` is a substring match against tenant.name; `page` is 1-based.
 */
export interface ListAdminTenantsParams {
  keyword?: string
  page?: number
  page_size?: number
}

/**
 * Payload for POST /api/v1/system/admin/tenants.
 *
 * The admin create surface accepts a richer shape than the Owner-facing
 * POST /tenants: status and storage_quota_gb are writable here so an
 * admin can provision a workspace for a tenantless user with quotas
 * pre-baked. status defaults to 'active' on the server when omitted;
 * storage_quota_gb is a GiB integer (server converts to bytes).
 *
 * `owner_user_id` is the SystemAdmin's "create for someone else" hook:
 * when present, the backend uses it as the new workspace's Owner
 * instead of the caller. When empty/undefined, the caller becomes
 * the Owner (legacy admin-self-create path).
 */
export interface CreateAdminTenantRequest {
  name: string
  description?: string
  /** Storage quota in GiB. Server converts to bytes. */
  storage_quota_gb?: number
  /** Defaults to 'active' server-side. */
  status?: 'active' | 'suspended'
  /** Optional. UUID of the future Owner. Empty/undefined → caller. */
  owner_user_id?: string
}

/**
 * PATCH payload for /api/v1/system/admin/tenants/:id. All fields are
 * optional; empty PATCHes are rejected by the server (HTTP 400).
 *
 * Distinct from the Owner-facing updateTenant payload in
 * api/tenant/index.ts: this surface exposes status and
 * storage_quota_gb, which the Owner-facing surface deliberately
 * hides (so an Owner can't self-suspend their workspace or balloon
 * their own quota).
 */
export interface UpdateAdminTenantRequest {
  name?: string
  description?: string
  status?: 'active' | 'suspended'
  storage_quota_gb?: number
}

/**
 * List every workspace in the platform, paginated.
 *
 * Backend: GET /api/v1/system/admin/tenants (SystemAdmin only). Returns
 * AdminTenantListResponse directly — no {data: ...} wrapping (see
 * utils/request.ts:97 — the axios interceptor unwraps response.data
 * project-wide). The query string is built manually because the shared
 * `get` helper doesn't accept a config object (matching listUsers).
 */
export async function listAdminTenants(
  params?: ListAdminTenantsParams,
): Promise<AdminTenantListResponse> {
  const qs = new URLSearchParams()
  if (params?.keyword) qs.set('keyword', params.keyword)
  if (params?.page != null) qs.set('page', String(params.page))
  if (params?.page_size != null) qs.set('page_size', String(params.page_size))
  const suffix = qs.toString() ? `?${qs.toString()}` : ''
  const response = await get(`/api/v1/system/admin/tenants${suffix}`)
  return response as unknown as AdminTenantListResponse
}

/**
 * Fetch a single workspace's detail with member_count + owner_username.
 *
 * Backend: GET /api/v1/system/admin/tenants/:id (SystemAdmin only).
 * Throws (via the axios interceptor) if the id is unknown.
 */
export async function getAdminTenantDetail(id: number): Promise<SystemTenantInfo> {
  const response = await get(`/api/v1/system/admin/tenants/${encodeURIComponent(String(id))}`)
  return response as unknown as SystemTenantInfo
}

/**
 * Create a workspace as SystemAdmin. The new workspace is owned by
 * `req.owner_user_id` when supplied, otherwise by the caller. Both
 * `WEKNORA_AUTH_DEFAULT_TENANT_MODE=tenantless` accounts and admin-created
 * accounts land at TenantID=0 until they receive an Owner membership
 * through this endpoint.
 *
 * Backend: POST /api/v1/system/admin/tenants (SystemAdmin only).
 * Returns the created TenantInfo directly. The handler writes a
 * `system.tenant_created` audit row recording the Owner + admin_self_owner
 * flag for traceability.
 */
export async function createAdminTenant(req: CreateAdminTenantRequest): Promise<TenantInfo> {
  const response = await post('/api/v1/system/admin/tenants', req)
  return response as unknown as TenantInfo
}

/**
 * Update a workspace from the SystemAdmin surface. Same PATCH semantics
 * as the user-management equivalent — only the keys set on the request
 * body are applied; empty PATCHes are rejected with HTTP 400 by the
 * backend.
 *
 * Backend: PATCH /api/v1/system/admin/tenants/:id (SystemAdmin only).
 * Applied changes are recorded in the `system.tenant_updated` audit
 * row with before/after values for each field.
 */
export async function updateAdminTenant(
  id: number,
  req: UpdateAdminTenantRequest,
): Promise<SystemTenantInfo> {
  const response = await patch(
    `/api/v1/system/admin/tenants/${encodeURIComponent(String(id))}`,
    req,
  )
  return response as unknown as SystemTenantInfo
}

/**
 * Delete a workspace as SystemAdmin. Backend enforces:
 *   - caller cannot delete their last active workspace if they are its
 *     Owner (would lock the admin out of the platform)
 *   - non-owner members are cascade-removed before DeleteTenant runs;
 *     if any removal fails the whole transaction is aborted
 *   - emits a `system.tenant_deleted` audit row carrying the cascade
 *     count
 *
 * Backend: DELETE /api/v1/system/admin/tenants/:id (SystemAdmin only).
 */
export async function deleteAdminTenant(id: number): Promise<{ message: string }> {
  const response = await del(`/api/v1/system/admin/tenants/${encodeURIComponent(String(id))}`)
  return response as unknown as { message: string }
}