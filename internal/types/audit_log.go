package types

import (
	"time"
)

// AuditAction names a single audited action class. Action constants are
// dot-namespaced (`<area>.<event>`) so future PRs can plug in their own
// areas (e.g. `kb.shared`, `agent.copied`) without colliding with the
// RBAC events PR 6 ships.
type AuditAction string

const (
	// AuditActionMemberAdded fires when a tenant Owner / Admin adds a
	// new tenant_members row. The actor is the inviter; the target is
	// the invited user.
	AuditActionMemberAdded AuditAction = "rbac.member_added"
	// AuditActionMemberRemoved fires when an Owner / Admin removes a
	// tenant_members row. Distinct from MemberLeft so an audit reader
	// can tell "kicked out" from "left voluntarily".
	AuditActionMemberRemoved AuditAction = "rbac.member_removed"
	// AuditActionMemberRoleChanged fires for promote/demote operations.
	// The Details payload carries old_role and new_role.
	AuditActionMemberRoleChanged AuditAction = "rbac.member_role_changed"
	// AuditActionMemberLeft fires on POST /tenants/:id/leave — the
	// actor and target are the same user.
	AuditActionMemberLeft AuditAction = "rbac.member_left"
	// AuditActionAccessDenied fires when middleware/rbac.go's
	// RequireRole or RequireOwnershipOrRole rejects a request under
	// EnableRBAC=true. Subject to 1-minute sliding-window dedup so a
	// probing client cannot fill the table.
	AuditActionAccessDenied AuditAction = "rbac.access_denied"
	// AuditActionInvitationSent fires when an Owner issues a new
	// tenant invitation. Actor is the inviter, TargetUserID is the
	// invitee. Note: AuditActionMemberAdded only fires when the
	// invitee actually accepts and the tenant_members row is created.
	AuditActionInvitationSent AuditAction = "rbac.invitation_sent"
	// AuditActionInvitationAccepted fires when the invitee accepts a
	// pending invitation. Actor is the invitee (acting on their own
	// inbox); the matching rbac.member_added is emitted in the same
	// transaction.
	AuditActionInvitationAccepted AuditAction = "rbac.invitation_accepted"
	// AuditActionInvitationDeclined fires when the invitee rejects a
	// pending invitation. Actor and target are the same user.
	AuditActionInvitationDeclined AuditAction = "rbac.invitation_declined"
	// AuditActionInvitationRevoked fires when a tenant Owner cancels
	// a still-pending invitation before the invitee acts. Actor is
	// the Owner; target is the invitee.
	AuditActionInvitationRevoked AuditAction = "rbac.invitation_revoked"
	// AuditActionInvitationExpired fires when the lazy sweep transitions
	// an overdue pending row to expired. Actor is empty (system).
	AuditActionInvitationExpired AuditAction = "rbac.invitation_expired"

	// VectorStore lifecycle actions. Emitted by VectorStoreService.
	// Cover both env-store-derived (__env_*) and DB store create /
	// update / delete paths. Details payload identifies the store_id
	// and the changed key set; secret values (Password, APIKey,
	// connection_config encrypted blob) MUST NOT appear in details.

	// AuditActionVectorStoreCreated fires when a new VectorStore row
	// is committed to the DB (Phase 1 CRUD path). Actor is the tenant
	// user; resource is the VectorStore.
	AuditActionVectorStoreCreated AuditAction = "vector_store.created"
	// AuditActionVectorStoreUpdated fires on UPDATE of any VectorStore
	// mutable field. Details payload carries the changed key set
	// (never the secret values themselves — only the field names).
	AuditActionVectorStoreUpdated AuditAction = "vector_store.updated"
	// AuditActionVectorStoreDeleted fires when a VectorStore is
	// (soft-)deleted. Phase 2's delete guard already prevents deletion
	// of stores with bound KBs; the audit row records the actor and
	// the store_id for forensic traceability.
	AuditActionVectorStoreDeleted AuditAction = "vector_store.deleted"

	// OpenSearch-specific actions emitted by the driver shipped in
	// Phase 3. The OpenSearch index is a derived resource of the
	// VectorStore — these events capture cluster-side side effects
	// (PUT /<index>, DELETE /<index>, POST /_reindex) that operators
	// may need to correlate with VectorStore lifecycle events.

	// AuditActionOpenSearchIndexCreated fires when the OpenSearch
	// driver lazily creates a per-dimension index (the first time a
	// KB with a given embedding dim binds to the store). Details
	// payload: index name, alias name, dimension.
	AuditActionOpenSearchIndexCreated AuditAction = "opensearch.index_created"
	// AuditActionOpenSearchIndexDeleted fires when the OpenSearch
	// driver drops an index (e.g. cascade from VectorStore delete).
	AuditActionOpenSearchIndexDeleted AuditAction = "opensearch.index_deleted"
	// AuditActionOpenSearchReindexExecuted fires when CopyIndices
	// initiates a _reindex (sync or async). Details payload: source
	// KB id, target KB id, sync-or-async, doc count if known.
	AuditActionOpenSearchReindexExecuted AuditAction = "opensearch.reindex_executed"

	// AuditActionSystemSettingChanged fires when a SystemAdmin updates
	// a row in the platform-wide system_settings table via
	// PUT /api/v1/system/admin/settings/:key. Details payload carries
	// {key, value_type, old_value, new_value} — sensitive values are
	// redacted server-side before logging when is_secret=true (P3+;
	// for now no setting is marked secret). Audit rows always have
	// tenant_id=0 because the change is system-scope, not workspace-scoped.
	AuditActionSystemSettingChanged AuditAction = "system.setting_changed"

	// AuditActionSystemAdminPromoted fires when a SystemAdmin grants
	// system-administrator privileges to another user via
	// POST /api/v1/system/admin/promote. ActorUserID is the promoter,
	// TargetUserID is the user being promoted. Details payload carries
	// {target_email, target_username, idempotent} — `idempotent=true`
	// means the user was already a system admin and no row was written
	// (we still emit the row so probing the endpoint leaves a trail).
	// TenantID=0 because the change is system-scope.
	AuditActionSystemAdminPromoted AuditAction = "system.admin_promoted"
	// AuditActionSystemAdminRevoked fires when a SystemAdmin removes
	// system-administrator privileges from another user via
	// POST /api/v1/system/admin/revoke. Details payload carries
	// {target_email, target_username, changed} — `changed=false` covers
	// the idempotent path (target was already not an admin) so an audit
	// reader can distinguish a real revoke from a noop attempt.
	// TenantID=0 because the change is system-scope.
	AuditActionSystemAdminRevoked AuditAction = "system.admin_revoked"

	// AuditActionSystemUserPasswordReset fires when a SystemAdmin replaces
	// another user's local password. Details identify the target and record
	// session revocation, but never contain the old or new password.
	AuditActionSystemUserPasswordReset AuditAction = "system.user_password_reset"

	// AuditActionSystemUserUpdated fires when a SystemAdmin edits another
	// user's account through the user-management surface — username, email,
	// is_active. Toggling system-admin status is intentionally NOT covered
	// here; that path has its own dedicated actions (promoted / revoked).
	// Details payload carries {target_email, target_username, changes: {field:
	// {from, to}}} so an audit reader can see exactly which field changed
	// without having to diff two snapshots. Password resets do not flow
	// through this action — they emit AuditActionSystemUserPasswordReset
	// instead. TenantID=0 because the change is system-scope.
	AuditActionSystemUserUpdated AuditAction = "system.user_updated"

	// Runtime queue mutations are privileged SystemAdmin actions. Retrying an
	// archived task can repeat its original side effects; deleting one removes
	// the Redis failure record. Both must leave a platform audit trail.
	AuditActionSystemQueueTaskRetried   AuditAction = "system.queue_task_retried"
	AuditActionSystemQueueTaskDeleted   AuditAction = "system.queue_task_deleted"
	AuditActionSystemQueueTaskRunNow    AuditAction = "system.queue_task_run_now"
	AuditActionSystemQueueTaskCancelled AuditAction = "system.queue_task_cancelled"

	// AuditActionSystemUserCreated fires when a SystemAdmin creates a user
	// account directly through the user-management surface — it deliberately
	// bypasses Register (no invite/email flow) and writes TenantID=0 so the
	// freshly-created user lands in the tenantless state until an admin
	// assigns a workspace. Details payload carries
	// {target_email, target_username, password_set}. TenantID=0 because the
	// change is system-scope.
	AuditActionSystemUserCreated AuditAction = "system.user_created"
	// AuditActionSystemUserDeleted fires when a SystemAdmin removes a user
	// account. Self-delete and last-admin-delete are rejected at the handler
	// before the row reaches the service. Details payload carries
	// {target_email, target_username, deleted_memberships} so an audit
	// reader can see which workspaces lost access. TenantID=0 because the
	// change is system-scope.
	AuditActionSystemUserDeleted AuditAction = "system.user_deleted"

	// AuditActionSystemTenantCreated fires when a SystemAdmin provisions a new
	// workspace on behalf of a user (POST /system/admin/tenants). Distinct
	// from the owner-initiated POST /tenants path which does NOT emit this
	// row. Details payload carries {target_tenant_id, target_tenant_name,
	// target_status, owner_user_id, admin_self_owner} — admin_self_owner
	// distinguishes "admin created the workspace for themselves" from
	// "admin created it for someone else". TenantID=0 because the change is
	// system-scope; the row carries the target tenant id in Details.
	AuditActionSystemTenantCreated AuditAction = "system.tenant_created"
	// AuditActionSystemTenantUpdated fires when a SystemAdmin PATCHes a
	// workspace (name / description / status / storage_quota). Owner-initiated
	// PUT /tenants/:id does NOT emit this row. Details payload carries
	// {target_tenant_id, target_tenant_name, changes: {field: {from, to}}}
	// so an audit reader can diff the workspace's state without snapshot
	// diffs. TenantID=0 because the change is system-scope.
	AuditActionSystemTenantUpdated AuditAction = "system.tenant_updated"
	// AuditActionSystemTenantDeleted fires when a SystemAdmin removes a
	// workspace. Pre-conditions (no non-owner members, target is not the
	// admin's last home tenant) are enforced at the handler before the
	// delete reaches the service. Details payload carries
	// {target_tenant_id, target_tenant_name, cascade_removed_members}.
	// TenantID=0 because the change is system-scope.
	AuditActionSystemTenantDeleted AuditAction = "system.tenant_deleted"
)

// AuditOutcome distinguishes successful mutations from middleware-level
// rejections. The split lets the audit-log UI highlight denials in red
// without needing to enumerate every action class.
type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeDenied  AuditOutcome = "denied"
)

// AuditLog is a single immutable audit event. The schema is intentionally
// generic: PR 6 wires only RBAC events, but TargetType / TargetID /
// Details are set up to absorb KB / agent / datasource events in
// follow-up PRs without another migration.
//
// Rows are append-only — no UpdatedAt, no soft-delete column. The
// monotonic ID acts as both primary key and pagination cursor (newest-
// first is `WHERE id < AfterID ORDER BY id DESC`).
type AuditLog struct {
	ID            uint64       `json:"id"             gorm:"primaryKey;autoIncrement"`
	TenantID      uint64       `json:"tenant_id"      gorm:"not null;index:idx_audit_logs_tenant_id_desc,priority:1;index:idx_audit_logs_tenant_action,priority:1"`
	ActorUserID   string       `json:"actor_user_id"  gorm:"type:varchar(36);default:'';index:idx_audit_logs_actor"`
	ActorRole     string       `json:"actor_role"     gorm:"type:varchar(32);default:''"`
	Action        AuditAction  `json:"action"         gorm:"type:varchar(64);not null;index:idx_audit_logs_tenant_action,priority:2"`
	TargetType    string       `json:"target_type"    gorm:"type:varchar(32);default:''"`
	TargetID      string       `json:"target_id"      gorm:"type:varchar(64);default:''"`
	TargetUserID  string       `json:"target_user_id" gorm:"type:varchar(36);default:''"`
	RequestPath   string       `json:"request_path"   gorm:"type:varchar(512);default:''"`
	RequestMethod string       `json:"request_method" gorm:"type:varchar(16);default:''"`
	Outcome       AuditOutcome `json:"outcome"        gorm:"type:varchar(16);default:success"`
	Details       JSON         `json:"details"        gorm:"type:jsonb;default:'{}'"`
	CreatedAt     time.Time    `json:"created_at"     gorm:"index:idx_audit_logs_tenant_id_desc,priority:2,sort:desc"`
}

// TableName pins the table name even if a future GORM convention
// pluralisation refactor would otherwise rename it.
func (AuditLog) TableName() string { return "audit_logs" }
