<!--
  UserManagement.vue — SystemAdmin user-management surface
  -----------------------------------------------------------------
  Renders inside SystemSettings.vue when the "users & permissions"
  tab is active. Mounted as a sibling of the settings section (not
  inside it) so the running tab's `.settings-group` / `.setting-row`
  CSS hooks don't bleed into the user list — see the comment in
  SystemSettings.vue's template. The component fetches its own data
  lazily and is fully self-contained.

  Backend contract: SystemAdmin-only GET/PATCH /api/v1/system/admin/users*
  + POST /api/v1/system/admin/users/reset-password. The router enforces
  the SystemAdmin gate; this component doesn't re-check role.

  UX notes:
    - Search (q) hits SearchUsers; empty q hits ListUsers. Both paths
      use the same AdminUserListResponse shape, so a single list state
      covers both modes.
    - Pagination is offset/limit (max 200). "Load more" advances
      offset by limit and appends — simpler than true cursor paging
      for the SystemAdmin use case.
    - Self-edit guard: the current user's row disables Edit / Reset
      Password / Activate|Deactivate (the backend rejects with 400
      anyway, but preempting the round-trip is friendlier).
    - Last-admin guard: the backend rejects disabling an active system
      admin — surfaced as an inline error message on the dialog
      (t-message.error from the err.message).
-->
<template>
  <div class="user-management">
    <div class="um-header">
      <h3>{{ t('system.globalSettings.userManagement.detail.title') }}</h3>
      <p>{{ t('system.globalSettings.userManagement.detail.description') }}</p>
    </div>

    <div class="um-toolbar">
      <t-input
        v-model="searchTerm"
        size="medium"
        :placeholder="t('system.globalSettings.userManagement.searchPlaceholder')"
        clearable
        class="um-search"
        @enter="applySearch"
        @clear="applySearch"
      >
        <template #prefix-icon><t-icon name="search" /></template>
      </t-input>
      <t-button
        theme="primary"
        @click="openCreate"
      >
        <template #icon><t-icon name="add" /></template>
        {{ t('system.globalSettings.userManagement.actions.createUser') }}
      </t-button>
      <t-button
        variant="outline"
        :loading="loading"
        @click="reload"
      >
        <template #icon><t-icon name="refresh" /></template>
        {{ t('system.globalSettings.userManagement.refresh') }}
      </t-button>
      <span v-if="!loading" class="um-total" aria-live="polite">
        {{ t('system.globalSettings.userManagement.totalLabel', { total: total }) }}
      </span>
    </div>

    <div v-if="loading && users.length === 0" class="um-loading">
      <t-loading :text="t('system.globalSettings.userManagement.loading')" />
    </div>

    <div v-else-if="error" class="um-error">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="reload">
            {{ t('system.globalSettings.userManagement.refresh') }}
          </t-button>
        </template>
      </t-alert>
    </div>

    <div v-else-if="users.length === 0" class="um-empty">
      <t-empty :description="t('system.globalSettings.userManagement.empty')" />
    </div>

    <div v-else class="um-table-shell">
      <t-table
        row-key="id"
        :data="users"
        :columns="columns"
        size="medium"
        hover
        stripe
        :loading="loading"
      >
        <template #memberships="{ row }">
          <span class="um-membership-count">
            {{ t('system.globalSettings.userManagement.membershipCount', { count: row.membership_count }) }}
          </span>
        </template>
        <template #is_system_admin="{ row }">
          <t-tag
            :theme="row.is_system_admin ? 'danger' : 'default'"
            size="small"
            variant="light"
          >
            {{ t(`system.globalSettings.userManagement.role.${row.is_system_admin}`) }}
          </t-tag>
        </template>
        <template #is_active="{ row }">
          <t-tag
            :theme="row.is_active ? 'success' : 'default'"
            size="small"
            variant="light"
          >
            {{ t(`system.globalSettings.userManagement.status.${row.is_active ? 'active' : 'inactive'}`) }}
          </t-tag>
        </template>
        <template #created_at="{ row }">
          {{ formatDate(row.created_at) }}
        </template>
        <template #actions="{ row }">
          <div class="um-row-actions">
            <t-tooltip :content="t('system.globalSettings.userManagement.actions.view')" placement="top">
              <t-button
                theme="primary"
                variant="text"
                shape="square"
                size="small"
                @click="openDetail(row)"
              >
                <template #icon><t-icon name="browse" /></template>
              </t-button>
            </t-tooltip>
            <t-tooltip
              v-if="row.id !== currentUserId"
              :content="t('system.globalSettings.userManagement.actions.edit')"
              placement="top"
            >
              <t-button
                theme="primary"
                variant="text"
                shape="square"
                size="small"
                @click="openEdit(row)"
              >
                <template #icon><t-icon name="edit" /></template>
              </t-button>
            </t-tooltip>
            <t-tooltip
              v-if="row.id !== currentUserId"
              :content="t('system.globalSettings.userManagement.actions.resetPassword')"
              placement="top"
            >
              <t-button
                theme="warning"
                variant="text"
                shape="square"
                size="small"
                @click="openPassword(row)"
              >
                <template #icon><t-icon name="lock-on" /></template>
              </t-button>
            </t-tooltip>
            <t-tooltip
              v-if="row.id !== currentUserId"
              :content="t('system.globalSettings.userManagement.actions.delete')"
              placement="top"
            >
              <t-button
                theme="danger"
                variant="text"
                shape="square"
                size="small"
                @click.stop="openDelete(row)"
              >
                <template #icon><t-icon name="delete" /></template>
              </t-button>
            </t-tooltip>
            <t-popconfirm
              v-if="row.id !== currentUserId"
              :content="row.is_active
                ? t('system.globalSettings.userManagement.confirm.deactivate.body', { name: row.username || row.email })
                : t('system.globalSettings.userManagement.confirm.activate.body', { name: row.username || row.email })"
              :confirm-btn="row.is_active
                ? { content: t('system.globalSettings.userManagement.confirm.deactivate.confirmBtn'), theme: 'danger' }
                : { content: t('system.globalSettings.userManagement.confirm.activate.confirmBtn'), theme: 'primary' }"
              :cancel-btn="t('common.cancel')"
              placement="left"
              @confirm="toggleActive(row)"
            >
              <t-tooltip
                :content="row.is_active
                  ? t('system.globalSettings.userManagement.actions.deactivate')
                  : t('system.globalSettings.userManagement.actions.activate')"
                placement="top"
              >
                <t-button
                  :theme="row.is_active ? 'danger' : 'success'"
                  variant="text"
                  shape="square"
                  size="small"
                  @click.stop
                >
                  <template #icon>
                    <t-icon :name="row.is_active ? 'pause' : 'play'" />
                  </template>
                </t-button>
              </t-tooltip>
            </t-popconfirm>
          </div>
        </template>
      </t-table>

      <div v-if="hasMore" class="um-loadmore">
        <t-button
          variant="outline"
          :loading="loadingMore"
          @click="loadMore"
        >
          {{ t('system.globalSettings.userManagement.loadMore') }}
        </t-button>
      </div>
    </div>

    <!--
      Detail drawer. Lazy-fetches memberships for the selected user
      on every open. The header carries the username; the body splits
      into "Account information" (read-only) and "Workspaces & roles"
      (also read-only — role changes happen in the per-tenant member
      page, not here, to keep the user-management surface focused on
      identity). Closing resets detailLoadedId so reopening the same
      user re-fetches.
    -->
    <t-drawer
      v-model:visible="detailVisible"
      :header="detailHeader"
      drawer-class-name="user-management-detail-drawer"
      size="560px"
      :footer="false"
      placement="right"
      destroy-on-close
    >
      <div v-if="detailLoading" class="um-detail-loading">
        <t-loading :text="t('system.globalSettings.userManagement.loading')" />
      </div>
      <div v-else-if="detail" class="um-detail">
        <div class="um-detail-section">
          <h4>{{ t('system.globalSettings.userManagement.detail.accountSection') }}</h4>
          <dl class="um-detail-fields">
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.username') }}</dt>
              <dd>{{ detail.username }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.email') }}</dt>
              <dd>{{ detail.email }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.userId') }}</dt>
              <dd class="um-detail-mono">{{ detail.id }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.isActive') }}</dt>
              <dd>
                <t-tag
                  :theme="detail.is_active ? 'success' : 'default'"
                  size="small"
                  variant="light"
                >
                  {{ detail.is_active
                    ? t('system.globalSettings.userManagement.detail.isActiveOn')
                    : t('system.globalSettings.userManagement.detail.isActiveOff') }}
                </t-tag>
              </dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.isSystemAdmin') }}</dt>
              <dd>
                <t-tag
                  :theme="detail.is_system_admin ? 'danger' : 'default'"
                  size="small"
                  variant="light"
                >
                  {{ t(`system.globalSettings.userManagement.role.${detail.is_system_admin}`) }}
                </t-tag>
              </dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.createdAt') }}</dt>
              <dd>{{ formatDate(detail.created_at) }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.userManagement.detail.fields.updatedAt') }}</dt>
              <dd>{{ formatDate(detail.updated_at) }}</dd>
            </div>
          </dl>
        </div>

        <div class="um-detail-section">
          <div class="um-detail-section-header">
            <h4>{{ t('system.globalSettings.userManagement.detail.membershipsSection') }}</h4>
            <t-button
              v-if="detail.id !== currentUserId"
              size="small"
              variant="outline"
              @click="openAddMembership"
            >
              <template #icon><t-icon name="add" /></template>
              {{ t('system.globalSettings.userManagement.detail.addMembership') }}
            </t-button>
          </div>
          <div v-if="detail.memberships.length === 0" class="um-detail-empty">
            {{ t('system.globalSettings.userManagement.detail.membershipsEmpty') }}
          </div>
          <ul v-else class="um-membership-list">
            <li v-for="m in detail.memberships" :key="m.tenant_id" class="um-membership-row">
              <div class="um-membership-info">
                <span class="um-membership-name">{{ m.tenant_name }}</span>
                <t-tag
                  v-if="m.is_home_tenant"
                  theme="primary"
                  size="small"
                  variant="light"
                >
                  {{ t('system.globalSettings.userManagement.homeBadge') }}
                </t-tag>
                <!--
                  Per-row role-change select. SystemAdmin can edit any
                  non-self member's role in any tenant directly from
                  this drawer; backend enforces the "cannot demote
                  last active Owner" invariant and returns 409 if a
                  change would orphan the tenant. Disabled on the
                  caller's own row (defense in depth — the row action
                  is also disabled at the table level) and during an
                  in-flight role change to keep the UI honest.
                -->
                <t-select
                  v-if="detail.id !== currentUserId"
                  v-model="roleEdits[m.tenant_id]"
                  size="small"
                  class="um-membership-role-select"
                  :loading="roleUpdating[m.tenant_id] === true"
                  :disabled="roleUpdating[m.tenant_id] === true"
                  @change="() => changeMembershipRole(m, roleEdits[m.tenant_id])"
                >
                  <t-option
                    v-for="r in availableRoles"
                    :key="r"
                    :label="t(`system.globalSettings.userManagement.roleLabels.${r}`)"
                    :value="r"
                  />
                </t-select>
                <t-tag
                  v-else
                  :theme="roleTagTheme(m.role)"
                  size="small"
                >
                  {{ t(`system.globalSettings.userManagement.roleLabels.${m.role}`) }}
                </t-tag>
              </div>
              <!--
                Remove-from-tenant popconfirm. Match the
                deactivate-user popconfirm pattern so the operator
                sees the destructive verb in the same colour scheme.
                Disabled for the caller's own row to mirror the
                self-edit guard — you shouldn't be able to remove
                yourself from your own home tenant from here (use
                the per-tenant member page's leave flow instead).
              -->
              <t-popconfirm
                v-if="detail.id !== currentUserId"
                :content="t('system.globalSettings.userManagement.detail.removeMembershipConfirm', {
                  name: detail.username || detail.email,
                  tenant: m.tenant_name,
                })"
                :confirm-btn="{ content: t('system.globalSettings.userManagement.detail.removeMembership'), theme: 'danger' }"
                :cancel-btn="t('common.cancel')"
                placement="topRight"
                @confirm="removeMembership(m)"
              >
                <t-button
                  size="small"
                  variant="text"
                  theme="danger"
                  :loading="removing[m.tenant_id] === true"
                  @click.stop
                >
                  <template #icon><t-icon name="logout" /></template>
                  {{ t('system.globalSettings.userManagement.detail.removeMembership') }}
                </t-button>
              </t-popconfirm>
            </li>
          </ul>
          <p v-if="membershipActionError" class="um-membership-error">
            {{ membershipActionError }}
          </p>
        </div>

        <div v-if="detail.id !== currentUserId" class="um-detail-actions">
          <t-button theme="primary" @click="openEdit(detail); detailVisible = false">
            <template #icon><t-icon name="edit" /></template>
            {{ t('system.globalSettings.userManagement.actions.edit') }}
          </t-button>
          <t-button theme="warning" @click="openPassword(detail); detailVisible = false">
            <template #icon><t-icon name="lock-on" /></template>
            {{ t('system.globalSettings.userManagement.actions.resetPassword') }}
          </t-button>
          <t-button theme="danger" @click="openDelete(detail); detailVisible = false">
            <template #icon><t-icon name="delete" /></template>
            {{ t('system.globalSettings.userManagement.actions.delete') }}
          </t-button>
          <t-popconfirm
            :content="detail.is_active
              ? t('system.globalSettings.userManagement.confirm.deactivate.body', { name: detail.username || detail.email })
              : t('system.globalSettings.userManagement.confirm.activate.body', { name: detail.username || detail.email })"
            :confirm-btn="detail.is_active
              ? { content: t('system.globalSettings.userManagement.confirm.deactivate.confirmBtn'), theme: 'danger' }
              : { content: t('system.globalSettings.userManagement.confirm.activate.confirmBtn'), theme: 'primary' }"
            :cancel-btn="t('common.cancel')"
            placement="top"
            @confirm="toggleActive(detail); detailVisible = false"
          >
            <t-button :theme="detail.is_active ? 'danger' : 'success'">
              <template #icon>
                <t-icon :name="detail.is_active ? 'pause' : 'play'" />
              </template>
              {{ detail.is_active
                ? t('system.globalSettings.userManagement.actions.deactivate')
                : t('system.globalSettings.userManagement.actions.activate') }}
            </t-button>
          </t-popconfirm>
        </div>
      </div>
    </t-drawer>

    <!--
      Edit dialog. PATCH payload only includes fields the caller set,
      so we detect "changed" by comparing to the snapshot captured at
      open time. Submitting with no changes surfaces a t-message.warning
      and skips the round-trip.
    -->
    <t-dialog
      v-model:visible="editVisible"
      :header="t('system.globalSettings.userManagement.editDialog.title')"
      width="480px"
      placement="center"
      dialog-class-name="user-management-edit-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.userManagement.editDialog.submit'),
        theme: 'primary',
        loading: editSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.userManagement.editDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!editSubmitting"
      :close-btn="!editSubmitting"
      @confirm="submitEdit"
    >
      <p class="um-edit-description">
        {{ t('system.globalSettings.userManagement.editDialog.description') }}
      </p>
      <t-form
        v-if="editForm"
        ref="editFormRef"
        :data="editForm"
        :rules="editRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.userManagement.editDialog.fields.username')"
          name="username"
        >
          <t-input
            v-model="editForm.username"
            clearable
            :placeholder="t('system.globalSettings.userManagement.editDialog.usernamePlaceholder')"
            :disabled="editSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.editDialog.fields.email')"
          name="email"
        >
          <t-input
            v-model="editForm.email"
            type="email"
            clearable
            :placeholder="t('system.globalSettings.userManagement.editDialog.emailPlaceholder')"
            :disabled="editSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.editDialog.fields.isActive')"
          name="isActive"
        >
          <t-switch v-model="editForm.isActive" :disabled="editSubmitting" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <!--
      Password reset dialog. Email is pre-filled from the row's email
      and disabled — the backend identifies the target by email, so
      allowing the operator to change it here would let them target a
      different user than the row they clicked from. selfBlocked is
      preempted by disabling the row action; this guard is here only
      for defense in depth.
    -->
    <t-dialog
      v-model:visible="passwordVisible"
      :header="t('system.globalSettings.userManagement.passwordDialog.title', { name: passwordTarget?.username || passwordTarget?.email || '' })"
      width="440px"
      placement="center"
      dialog-class-name="user-management-password-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.userManagement.passwordDialog.submit'),
        theme: 'danger',
        loading: passwordSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.userManagement.passwordDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!passwordSubmitting"
      :close-btn="!passwordSubmitting"
      @confirm="submitPassword"
    >
      <t-alert
        theme="warning"
        :message="t('system.globalSettings.userManagement.passwordDialog.warning')"
        class="um-password-warning"
      />
      <t-form
        ref="passwordFormRef"
        :data="passwordForm"
        :rules="passwordRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.userManagement.passwordDialog.emailLabel')"
          name="email"
        >
          <t-input
            v-model="passwordForm.email"
            type="email"
            disabled
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.passwordDialog.newPasswordLabel')"
          name="newPassword"
        >
          <t-input
            v-model="passwordForm.newPassword"
            type="password"
            autocomplete="new-password"
            :placeholder="t('system.globalSettings.userManagement.passwordDialog.newPasswordPlaceholder')"
            :disabled="passwordSubmitting"
          >
            <template #prefix-icon><t-icon name="lock-on" /></template>
          </t-input>
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.passwordDialog.confirmPasswordLabel')"
          name="confirmPassword"
        >
          <t-input
            v-model="passwordForm.confirmPassword"
            type="password"
            autocomplete="new-password"
            :placeholder="t('system.globalSettings.userManagement.passwordDialog.confirmPasswordPlaceholder')"
            :disabled="passwordSubmitting"
            @enter="submitPassword"
          >
            <template #prefix-icon><t-icon name="lock-on" /></template>
          </t-input>
        </t-form-item>
      </t-form>
    </t-dialog>

    <!--
      Create user dialog. Username + email + password (+ confirm) +
      is_active switch. Submission lands the user in the tenantless state
      (TenantID=0). The detail dialog stays open afterwards and the list
      reloads so the operator can immediately follow up with a workspace
      assignment from the space-management tab — see the success hint.
    -->
    <t-dialog
      v-model:visible="createVisible"
      :header="t('system.globalSettings.userManagement.createDialog.title')"
      width="480px"
      placement="center"
      dialog-class-name="user-management-create-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.userManagement.createDialog.submit'),
        theme: 'primary',
        loading: createSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.userManagement.createDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!createSubmitting"
      :close-btn="!createSubmitting"
      @confirm="submitCreate"
    >
      <p class="um-edit-description">
        {{ t('system.globalSettings.userManagement.createDialog.description') }}
      </p>
      <t-form
        ref="createFormRef"
        :data="createForm"
        :rules="createRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.userManagement.createDialog.fields.username')"
          name="username"
        >
          <t-input
            v-model="createForm.username"
            clearable
            :placeholder="t('system.globalSettings.userManagement.createDialog.usernamePlaceholder')"
            :disabled="createSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.createDialog.fields.email')"
          name="email"
        >
          <t-input
            v-model="createForm.email"
            type="email"
            clearable
            :placeholder="t('system.globalSettings.userManagement.createDialog.emailPlaceholder')"
            :disabled="createSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.createDialog.fields.password')"
          name="password"
        >
          <t-input
            v-model="createForm.password"
            type="password"
            autocomplete="new-password"
            :placeholder="t('system.globalSettings.userManagement.createDialog.passwordPlaceholder')"
            :disabled="createSubmitting"
          >
            <template #prefix-icon><t-icon name="lock-on" /></template>
          </t-input>
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.createDialog.fields.confirmPassword')"
          name="confirmPassword"
        >
          <t-input
            v-model="createForm.confirmPassword"
            type="password"
            autocomplete="new-password"
            :placeholder="t('system.globalSettings.userManagement.createDialog.confirmPasswordPlaceholder')"
            :disabled="createSubmitting"
            @enter="submitCreate"
          >
            <template #prefix-icon><t-icon name="lock-on" /></template>
          </t-input>
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.createDialog.fields.isActive')"
          name="isActive"
        >
          <t-switch v-model="createForm.isActive" :disabled="createSubmitting" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <!--
      Add user-to-workspace dialog. Triggered from the "Workspaces &
      roles" section in the detail drawer. Lists every workspace the
      user is NOT already an active member of, plus a role picker
      (Viewer/Admin/Contributor — Owner is intentionally omitted
      because the create-membership path can't promote a tenantless
      recipient to Owner; ownership transfer lives behind the
      separate SpaceManagement edit dialog). Backend: POST
      /api/v1/tenants/:id/members (OwnerOrSystemAdmin — the
      SystemAdmin bypass is what makes this surface work without
      requiring the admin to also be an Owner of every tenant).
    -->
    <t-dialog
      v-model:visible="addMembershipVisible"
      :header="t('system.globalSettings.userManagement.addMembershipDialog.title', {
        name: detail?.username || detail?.email || '',
      })"
      width="480px"
      placement="center"
      dialog-class-name="user-management-add-membership-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.userManagement.addMembershipDialog.submit'),
        theme: 'primary',
        loading: addMembershipSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.userManagement.addMembershipDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!addMembershipSubmitting"
      :close-btn="!addMembershipSubmitting"
      @confirm="submitAddMembership"
    >
      <p class="um-edit-description">
        {{ t('system.globalSettings.userManagement.addMembershipDialog.description', {
          email: detail?.email || '',
        }) }}
      </p>
      <t-form
        ref="addMembershipFormRef"
        :data="addMembershipForm"
        :rules="addMembershipRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.userManagement.addMembershipDialog.fields.tenant')"
          name="tenantId"
        >
          <t-select
            v-model="addMembershipForm.tenantId"
            :loading="availableTenantsLoading"
            :placeholder="t('system.globalSettings.userManagement.addMembershipDialog.tenantPlaceholder')"
            filterable
            clearable
          >
            <t-option
              v-for="t in availableTenants"
              :key="t.id"
              :label="t.name"
              :value="t.id"
            />
          </t-select>
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.userManagement.addMembershipDialog.fields.role')"
          name="role"
        >
          <t-select
            v-model="addMembershipForm.role"
            :placeholder="t('system.globalSettings.userManagement.addMembershipDialog.rolePlaceholder')"
          >
            <t-option
              v-for="r in memberAdditionRoles"
              :key="r"
              :label="t(`system.globalSettings.userManagement.roleLabels.${r}`)"
              :value="r"
            />
          </t-select>
        </t-form-item>
      </t-form>
      <t-alert
        v-if="availableTenants.length === 0 && !availableTenantsLoading"
        theme="info"
        :message="t('system.globalSettings.userManagement.addMembershipDialog.noAvailableWorkspaces')"
      />
    </t-dialog>

    <!--
      Delete user dialog. The confirm step requires the operator to type
      the target user's email exactly — same anti-fat-finger pattern as
      SpaceManagement's DeleteSpaceConfirmDialog. Backend enforces two
      preconditions (not-self, not-last-admin); both surface as thrown
      exceptions via the shared axios interceptor.
    -->
    <t-dialog
      v-model:visible="deleteVisible"
      :header="t('system.globalSettings.userManagement.deleteDialog.title', { name: deleteTarget?.username || deleteTarget?.email || '' })"
      width="480px"
      placement="center"
      dialog-class-name="user-management-delete-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.userManagement.deleteDialog.submit'),
        theme: 'danger',
        loading: deleteSubmitting,
        disabled: !deleteConfirmed,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.userManagement.deleteDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!deleteSubmitting"
      :close-btn="!deleteSubmitting"
      @confirm="submitDelete"
    >
      <t-alert
        theme="error"
        :message="t('system.globalSettings.userManagement.deleteDialog.warning')"
        class="um-delete-warning"
      />
      <p class="um-edit-description">
        {{ t('system.globalSettings.userManagement.deleteDialog.confirmHint', { email: deleteTarget?.email || '' }) }}
      </p>
      <t-input
        v-model="deleteConfirmInput"
        :placeholder="t('system.globalSettings.userManagement.deleteDialog.confirmPlaceholder')"
        :disabled="deleteSubmitting"
        clearable
      >
        <template #prefix-icon><t-icon name="user" /></template>
      </t-input>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import type { FormInstanceFunctions, FormRule, PrimaryTableCol } from 'tdesign-vue-next'
import {
  listUsers,
  getUserDetail,
  updateUser,
  resetUserPassword,
  createUser,
  deleteUser,
  type AdminUserListItem,
  type AdminUserDetailResponse,
  type UpdateUserRequest,
} from '@/api/system/users'
import { listAdminTenants, type SystemTenantInfo } from '@/api/system/spaces'
import {
  addMember,
  updateMemberRole,
  removeMember,
  type TenantRole,
} from '@/api/tenant/members'
import { useAuthStore } from '@/stores/auth'

const PAGE_SIZE = 50

const { t } = useI18n()
const authStore = useAuthStore()
const currentUserId = computed(() => authStore.currentUserId)

const searchTerm = ref('')
const users = ref<AdminUserListItem[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')

// Snapshot of the full user list response — we keep `total` here so the
// header can render "共 N 名用户" even on a search-empty result.
const hasMore = computed(() => users.value.length < total.value)

const columns = computed<PrimaryTableCol<AdminUserListItem>[]>(() => [
  {
    colKey: 'username',
    title: t('system.globalSettings.userManagement.columns.username'),
    minWidth: 140,
  },
  {
    colKey: 'email',
    title: t('system.globalSettings.userManagement.columns.email'),
    minWidth: 200,
    ellipsis: true,
  },
  {
    colKey: 'is_system_admin',
    title: t('system.globalSettings.userManagement.columns.role'),
    width: 110,
  },
  {
    colKey: 'is_active',
    title: t('system.globalSettings.userManagement.columns.status'),
    width: 110,
  },
  {
    colKey: 'memberships',
    title: t('system.globalSettings.userManagement.columns.memberships'),
    width: 110,
  },
  {
    colKey: 'created_at',
    title: t('system.globalSettings.userManagement.columns.createdAt'),
    width: 160,
  },
  {
    colKey: 'actions',
    title: t('system.globalSettings.userManagement.columns.actions'),
    width: 200,
    fixed: 'right',
  },
])

function formatDate(value: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  // Locale-aware but bounded — we don't ship a full date-fns; Y/M/D + HH:mm
  // covers what the SystemAdmin needs at a glance. Locale falls back to the
  // browser default; zh-CN renders 2026/07/22 14:30, en-US renders
  // 7/22/2026, 2:30 PM.
  return d.toLocaleString()
}

function roleTagTheme(role: string): 'primary' | 'success' | 'warning' | 'default' {
  switch (role) {
    case 'owner':
      return 'primary'
    case 'admin':
      return 'success'
    case 'contributor':
      return 'warning'
    default:
      return 'default'
  }
}

// Roles offered for direct-creation of a new membership via the
// "Add to workspace" dialog. Owner is intentionally excluded — the
// AddMember backend path always inserts a non-Owner row (EnsureOwner
// is what promotes to Owner, and that's a registration-time path
// the user-management surface deliberately doesn't surface).
// Ownership transfer for existing tenants lives in the
// SpaceManagement edit dialog.
const memberAdditionRoles: TenantRole[] = ['admin', 'contributor', 'viewer']

// Available role choices shown in the per-row role-edit select.
// Same set as memberAdditionRoles — Owner is reachable only via the
// dedicated ownership-transfer surface (SpaceManagement edit).
const availableRoles: TenantRole[] = ['admin', 'contributor', 'viewer']

// ---------------------------------------------------------------------------
// List lifecycle
// ---------------------------------------------------------------------------
//
// Reload replaces the list from scratch; applySearch does the same but
// with a fresh q. Both share `loadPage(0)` so pagination state stays
// consistent. loadMore advances offset by PAGE_SIZE without resetting
// the search state — the caller is still on the same q.

function reset() {
  users.value = []
  total.value = 0
  offset.value = 0
  error.value = ''
}

async function loadPage(targetOffset: number, append: boolean): Promise<void> {
  if (append) {
    loadingMore.value = true
  } else {
    loading.value = true
    error.value = ''
  }
  try {
    const q = searchTerm.value.trim()
    const res = await listUsers({
      offset: targetOffset,
      limit: PAGE_SIZE,
      q: q || undefined,
    })
    if (append) {
      users.value = users.value.concat(res.users)
    } else {
      users.value = res.users
    }
    total.value = res.total
    offset.value = targetOffset + res.users.length
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.messages.loadFailed')
    if (!append) error.value = message
    MessagePlugin.error(message)
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function applySearch() {
  reset()
  void loadPage(0, false)
}

function reload() {
  reset()
  void loadPage(0, false)
}

async function loadMore() {
  await loadPage(offset.value, true)
}

// Auto-load on mount; the parent <UserManagement v-if> only mounts the
// component when the users tab becomes active, so onMounted fires after
// the user clicks the tab. No need for explicit visibility-driven fetches.
onMounted(() => {
  void reload()
})

// ---------------------------------------------------------------------------
// Detail drawer
// ---------------------------------------------------------------------------

const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<AdminUserDetailResponse | null>(null)
const detailLoadedId = ref('')

const detailHeader = computed(() => {
  const u = detail.value
  if (!u) return t('system.globalSettings.userManagement.detail.title')
  return u.username || u.email
})

async function openDetail(row: AdminUserListItem) {
  detailVisible.value = true
  detail.value = null
  detailLoading.value = true
  try {
    const res = await getUserDetail(row.id)
    detail.value = res
    detailLoadedId.value = row.id
    // Seed the per-row role-edit selects from the freshly-fetched
    // memberships so the select shows the current role without an
    // extra round-trip when the operator opens one. Reset on every
    // open so a stale role edit from a prior user doesn't leak into
    // the new one's drawer.
    resetMembershipEdits(res)
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.messages.detailLoadFailed')
    MessagePlugin.error(message)
    detailVisible.value = false
  } finally {
    detailLoading.value = false
  }
}

// ---------------------------------------------------------------------------
// Per-user membership CRUD (centralised user-management surface)
// ---------------------------------------------------------------------------
//
// The membership API at /tenants/:id/members was historically only
// gated behind Owner role, so SystemAdmins couldn't drive it without
// also being Owners. The OwnerOrSystemAdmin guard added in the router
// lifts that restriction; from this drawer we now let a system
// admin add users to / remove users from / change roles in any
// tenant they own or merely administer. All three operations use the
// already-bound TenantMemberService; we just shell out from a
// centralised UI surface to keep the per-tenant member page from
// being duplicated in two places.
//
// Self-edit guard: actions below early-return when the loaded user
// is the operator themselves — this mirrors the existing row-action
// disable so an admin can't quietly demote or evict themselves by
// accident. The role-change <t-select> and the remove button are
// already hidden via `v-if` against currentUserId; these guards are
// defense in depth for the script-level calls.

const membershipActionError = ref('')

// roleEdits mirrors detail.memberships[].role keyed by tenant_id so
// the per-row <t-select> has a model. seeded in openDetail's success
// branch via resetMembershipEdits.
const roleEdits = reactive<Record<number, TenantRole>>({})

// roleUpdating[tenant_id] = true while a PUT is in flight so the
// select's :loading + :disabled render the spinner and block stale
// re-fires. Removed when the request settles.
const roleUpdating = reactive<Record<number, boolean>>({})

// removing[tenant_id] = true while a DELETE is in flight, used by
// the remove button's :loading flag.
const removing = reactive<Record<number, boolean>>({})

function resetMembershipEdits(res: AdminUserDetailResponse) {
  // Wipe any state left over from a previous user so tenant-id
  // keys don't bleed between two different drawers.
  Object.keys(roleEdits).forEach((k) => delete roleEdits[Number(k)])
  Object.keys(roleUpdating).forEach((k) => delete roleUpdating[Number(k)])
  Object.keys(removing).forEach((k) => delete removing[Number(k)])
  membershipActionError.value = ''
  for (const m of res.memberships) {
    if (m && m.status === 'active') {
      roleEdits[m.tenant_id] = m.role
    }
  }
}

async function changeMembershipRole(
  m: AdminUserDetailResponse['memberships'][number],
  newRole: TenantRole,
) {
  if (!detail.value) return
  if (detail.value.id === currentUserId.value) return
  if (m.status !== 'active') return
  if (newRole === m.role) return
  membershipActionError.value = ''
  roleUpdating[m.tenant_id] = true
  try {
    await updateMemberRole(m.tenant_id, detail.value.id, newRole)
    // Optimistic UI: patch the cached detail row so the role tag /
    // select reflect the new value without a full re-fetch. On
    // success the select's two-way binding already mirrors the new
    // value; we still update the row so the visual "primary Owner"
    // tag re-renders if a contributor just became admin.
    detail.value = {
      ...detail.value,
      memberships: detail.value.memberships.map((x) => (x.tenant_id === m.tenant_id
        ? { ...x, role: newRole }
        : x)),
    }
    // Bump the list-row membership_count too if helpful — actually
    // role changes don't affect count, so nothing to do there.
    MessagePlugin.success(t('system.globalSettings.userManagement.detail.membershipRoleChanged'))
  } catch (e) {
    // Roll back the optimistic select value so the row returns to
    // its pre-edit role; the operator can retry without us silently
    // leaving them on a half-updated state.
    roleEdits[m.tenant_id] = m.role
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.detail.membershipActionFailed')
    membershipActionError.value = message
    MessagePlugin.error(message)
  } finally {
    delete roleUpdating[m.tenant_id]
  }
}

async function removeMembership(m: AdminUserDetailResponse['memberships'][number]) {
  if (!detail.value) return
  if (detail.value.id === currentUserId.value) return
  membershipActionError.value = ''
  removing[m.tenant_id] = true
  try {
    await removeMember(m.tenant_id, detail.value.id)
    detail.value = {
      ...detail.value,
      memberships: detail.value.memberships.filter((x) => x.tenant_id !== m.tenant_id),
    }
    delete roleEdits[m.tenant_id]
    // Decrement the list-row membership_count without a full reload
    // — the membership_count column was loaded eagerly on the list
    // page and we don't want to round-trip the whole table just for
    // a single decrement.
    const rowIdx = users.value.findIndex((u) => u.id === detail.value!.id)
    if (rowIdx >= 0) {
      const current = users.value[rowIdx]
      users.value[rowIdx] = {
        ...current,
        membership_count: Math.max(0, current.membership_count - 1),
      }
    }
    MessagePlugin.success(t('system.globalSettings.userManagement.detail.membershipRemoved'))
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.detail.membershipActionFailed')
    membershipActionError.value = message
    MessagePlugin.error(message)
  } finally {
    delete removing[m.tenant_id]
  }
}

// ---------------------------------------------------------------------------
// Add membership dialog
// ---------------------------------------------------------------------------
//
// We list workspaces via the SystemAdmin listAdminTenants helper
// (the same endpoint SpaceManagement.vue uses) and filter out
// tenants the user is already a member of so the dropdown only
// shows eligible targets. Tenant creation is intentionally not
// part of this surface — admin want to drop a user into an
// existing tenant, not provision a new one. New workspaces live
// behind the SpaceManagement create dialog.

const addMembershipVisible = ref(false)
const addMembershipSubmitting = ref(false)
const addMembershipFormRef = ref<FormInstanceFunctions>()
const addMembershipForm = reactive<{
  tenantId: number | undefined
  role: TenantRole
}>({
  tenantId: undefined,
  role: 'viewer',
})
const allTenants = ref<SystemTenantInfo[]>([])
const availableTenantsLoading = ref(false)

const addMembershipRules: Record<string, FormRule[]> = {
  tenantId: [
    {
      required: true,
      message: t('system.globalSettings.userManagement.addMembershipDialog.validation.tenantRequired'),
      trigger: 'change',
    },
  ],
  role: [
    {
      required: true,
      message: t('system.globalSettings.userManagement.addMembershipDialog.validation.roleRequired'),
      trigger: 'change',
    },
  ],
}

const availableTenants = computed(() => {
  if (!detail.value) return [] as SystemTenantInfo[]
  const taken = new Set(detail.value.memberships.map((m) => m.tenant_id))
  return allTenants.value.filter((tn) => !taken.has(tn.id))
})

async function loadAllTenants() {
  availableTenantsLoading.value = true
  try {
    // Single-page window with a generous limit; tenant count rarely
    // exceeds this and admin users benefit from seeing the full
    // set rather than paging through tenants to find the target.
    const res = await listAdminTenants({ page: 1, page_size: 200 })
    allTenants.value = res.items
  } catch {
    // Best-effort; fall back to empty list, the dialog surfaces
    // the "no available workspaces" alert when nothing was loaded.
    allTenants.value = []
  } finally {
    availableTenantsLoading.value = false
  }
}

function openAddMembership() {
  if (!detail.value) return
  if (detail.value.id === currentUserId.value) return
  addMembershipForm.tenantId = undefined
  addMembershipForm.role = 'viewer'
  addMembershipVisible.value = true
  // Always refresh on open — workspaces can be created / deleted
  // since the last open, and a stale candidate set would silently
  // hide a valid option.
  void loadAllTenants()
}

async function submitAddMembership() {
  const validate = addMembershipFormRef.value?.validate
  if (typeof validate === 'function') {
    try {
      await validate()
    } catch {
      return
    }
  }
  if (!detail.value) return
  if (addMembershipForm.tenantId === undefined || addMembershipForm.tenantId === null) return
  addMembershipSubmitting.value = true
  try {
    // Use the user's email to identify the target, matching the
    // AddMember backend contract (POST /tenants/:id/members takes
    // {email, role}). The router-level OwnerOrSystemAdmin guard
    // lets us call this from any tenant without first joining.
    const targetTenantId = addMembershipForm.tenantId
    const resp = await addMember(targetTenantId, {
      email: detail.value.email,
      role: addMembershipForm.role,
    })
    // Optimistically inject the new membership into the drawer so
    // the row appears immediately. If the response carries the
    // canonical row, use it; otherwise synthesise from what we know.
    if (resp.success && resp.data) {
      const d = resp.data
      const newRow = {
        tenant_id: targetTenantId,
        tenant_name: allTenants.value.find((x) => x.id === targetTenantId)?.name ?? '',
        role: d.role,
        status: d.status,
        joined_at: d.joined_at,
        is_home_tenant: false,
      }
      detail.value = {
        ...detail.value,
        memberships: detail.value.memberships.concat(newRow),
      }
      roleEdits[targetTenantId] = d.role
    } else {
      // Fallback: re-fetch the detail so the list-of-spaces is
      // guaranteed to reflect the backend truth.
      const refreshed = await getUserDetail(detail.value.id)
      detail.value = refreshed
      resetMembershipEdits(refreshed)
    }
    // Bump the list-row counter.
    const rowIdx = users.value.findIndex((u) => u.id === detail.value!.id)
    if (rowIdx >= 0) {
      const current = users.value[rowIdx]
      users.value[rowIdx] = {
        ...current,
        membership_count: current.membership_count + 1,
      }
    }
    MessagePlugin.success(t('system.globalSettings.userManagement.addMembershipDialog.success'))
    addMembershipVisible.value = false
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.addMembershipDialog.failed')
    MessagePlugin.error(message)
  } finally {
    addMembershipSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Edit dialog
// ---------------------------------------------------------------------------

const editVisible = ref(false)
const editSubmitting = ref(false)
const editFormRef = ref<FormInstanceFunctions>()
const editSnapshot = ref<AdminUserListItem | null>(null)
const editForm = reactive({
  username: '',
  email: '',
  isActive: true,
})

const editRules: Record<string, FormRule[]> = {
  username: [
    { required: true, message: t('system.globalSettings.userManagement.editDialog.fields.username'), trigger: 'blur' },
    { min: 2, max: 32, message: t('system.globalSettings.userManagement.editDialog.usernamePlaceholder'), trigger: 'blur' },
  ],
  email: [
    { required: true, message: t('system.globalSettings.userManagement.editDialog.fields.email'), trigger: 'blur' },
    { email: true, message: t('system.globalSettings.userManagement.editDialog.emailPlaceholder'), trigger: 'blur' },
  ],
}

function openEdit(row: AdminUserListItem | AdminUserDetailResponse) {
  if (row.id === currentUserId.value) {
    MessagePlugin.warning(t('system.globalSettings.userManagement.editDialog.selfBlocked'))
    return
  }
  editSnapshot.value = 'membership_count' in row
    ? row
    : { ...row, membership_count: row.memberships.length }
  editForm.username = row.username
  editForm.email = row.email
  editForm.isActive = row.is_active
  editVisible.value = true
}

function buildEditPayload(): UpdateUserRequest | null {
  const snap = editSnapshot.value
  if (!snap) return null
  const payload: UpdateUserRequest = {}
  if (editForm.username.trim() !== snap.username) {
    payload.username = editForm.username.trim()
  }
  if (editForm.email.trim() !== snap.email) {
    payload.email = editForm.email.trim()
  }
  if (editForm.isActive !== snap.is_active) {
    payload.is_active = editForm.isActive
  }
  return Object.keys(payload).length > 0 ? payload : null
}

async function submitEdit() {
  const validate = editFormRef.value?.validate
  if (typeof validate === 'function') {
    try {
      await validate()
    } catch {
      return
    }
  }
  const snap = editSnapshot.value
  if (!snap) return
  const payload = buildEditPayload()
  if (!payload) {
    MessagePlugin.warning(t('system.globalSettings.userManagement.editDialog.noChanges'))
    return
  }
  editSubmitting.value = true
  try {
    const updated = await updateUser(snap.id, payload)
    // Patch the row in the list so the table reflects the change without
    // a full reload; deep-replace preserves the membership_count we don't
    // get back from the PATCH response.
    const idx = users.value.findIndex((u) => u.id === updated.id)
    if (idx >= 0) {
      users.value[idx] = {
        ...users.value[idx],
        id: updated.id,
        username: updated.username,
        email: updated.email,
        is_active: updated.is_active,
        is_system_admin: updated.is_system_admin,
        created_at: updated.created_at,
        updated_at: updated.updated_at,
      }
    }
    MessagePlugin.success(t('system.globalSettings.userManagement.editDialog.success'))
    editVisible.value = false
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.editDialog.failed')
    MessagePlugin.error(message)
  } finally {
    editSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Password dialog
// ---------------------------------------------------------------------------

const passwordVisible = ref(false)
const passwordSubmitting = ref(false)
const passwordFormRef = ref<FormInstanceFunctions>()
const passwordTarget = ref<AdminUserListItem | AdminUserDetailResponse | null>(null)
const passwordForm = reactive({
  email: '',
  newPassword: '',
  confirmPassword: '',
})

const passwordRules: Record<string, FormRule[]> = {
  newPassword: [
    { required: true, message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordRequired'), trigger: 'blur' },
    { min: 8, message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordLength'), trigger: 'blur' },
    { max: 32, message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordLength'), trigger: 'blur' },
    { pattern: /[a-zA-Z]/, message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordLetter'), trigger: 'blur' },
    { pattern: /\d/, message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordNumber'), trigger: 'blur' },
  ],
  confirmPassword: [
    { required: true, message: t('system.globalSettings.userManagement.passwordDialog.validation.confirmRequired'), trigger: 'blur' },
    {
      validator: (val: string) => val === passwordForm.newPassword,
      message: t('system.globalSettings.userManagement.passwordDialog.validation.passwordMismatch'),
      trigger: 'blur',
    },
  ],
}

function openPassword(row: AdminUserListItem | AdminUserDetailResponse) {
  if (row.id === currentUserId.value) {
    MessagePlugin.warning(t('system.globalSettings.userManagement.passwordDialog.selfBlocked'))
    return
  }
  passwordTarget.value = row
  passwordForm.email = row.email
  passwordForm.newPassword = ''
  passwordForm.confirmPassword = ''
  passwordVisible.value = true
}

async function submitPassword() {
  const validate = passwordFormRef.value?.validate
  if (typeof validate === 'function') {
    try {
      await validate()
    } catch {
      return
    }
  }
  if (!passwordTarget.value) return
  passwordSubmitting.value = true
  try {
    await resetUserPassword({
      email: passwordForm.email,
      new_password: passwordForm.newPassword,
    })
    MessagePlugin.success(t('system.globalSettings.userManagement.passwordDialog.success'))
    passwordVisible.value = false
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.passwordDialog.failed')
    MessagePlugin.error(message)
  } finally {
    passwordSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Activate / Deactivate
// ---------------------------------------------------------------------------
//
// Backend rejects disabling an active system admin; we surface the 400
// error verbatim so the operator knows to revoke the role first.

async function toggleActive(row: AdminUserListItem | AdminUserDetailResponse) {
  if (row.id === currentUserId.value) {
    MessagePlugin.warning(t('system.globalSettings.userManagement.editDialog.selfBlocked'))
    return
  }
  const next = !row.is_active
  try {
    const updated = await updateUser(row.id, { is_active: next })
    const idx = users.value.findIndex((u) => u.id === updated.id)
    if (idx >= 0) {
      users.value[idx] = { ...users.value[idx], is_active: updated.is_active }
    }
    if (detail.value && detail.value.id === updated.id) {
      detail.value = { ...detail.value, is_active: updated.is_active }
    }
    MessagePlugin.success(t('system.globalSettings.userManagement.editDialog.success'))
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.messages.updateFailed')
    MessagePlugin.error(message)
  }
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

onBeforeUnmount(() => {
  // No timers or subscriptions to clear — Dialog/Drawer/Popconfirm are
  // controlled components and clean themselves up when their v-model flips.
})

// ---------------------------------------------------------------------------
// Create user dialog
// ---------------------------------------------------------------------------
//
// SystemAdmin creates a user via POST /api/v1/system/admin/users. The new
// account lands with TenantID=0 (tenantless). After success we reload the
// list and surface a hint suggesting the operator follow up with a
// workspace assignment from the space-management tab.

const createVisible = ref(false)
const createSubmitting = ref(false)
const createFormRef = ref<FormInstanceFunctions>()
const createForm = reactive({
  username: '',
  email: '',
  password: '',
  confirmPassword: '',
  isActive: true,
})

const createRules: Record<string, FormRule[]> = {
  username: [
    { required: true, message: t('system.globalSettings.userManagement.createDialog.validation.usernameRequired'), trigger: 'blur' },
    { min: 2, max: 100, message: t('system.globalSettings.userManagement.createDialog.usernamePlaceholder'), trigger: 'blur' },
  ],
  email: [
    { required: true, message: t('system.globalSettings.userManagement.createDialog.validation.emailRequired'), trigger: 'blur' },
    { email: true, message: t('system.globalSettings.userManagement.createDialog.emailPlaceholder'), trigger: 'blur' },
  ],
  password: [
    { required: true, message: t('system.globalSettings.userManagement.createDialog.validation.passwordRequired'), trigger: 'blur' },
    { min: 8, message: t('system.globalSettings.userManagement.createDialog.validation.passwordLength'), trigger: 'blur' },
    { max: 32, message: t('system.globalSettings.userManagement.createDialog.validation.passwordLength'), trigger: 'blur' },
    { pattern: /[a-zA-Z]/, message: t('system.globalSettings.userManagement.createDialog.validation.passwordLetter'), trigger: 'blur' },
    { pattern: /\d/, message: t('system.globalSettings.userManagement.createDialog.validation.passwordNumber'), trigger: 'blur' },
  ],
  confirmPassword: [
    { required: true, message: t('system.globalSettings.userManagement.createDialog.validation.confirmRequired'), trigger: 'blur' },
    {
      validator: (val: string) => val === createForm.password,
      message: t('system.globalSettings.userManagement.createDialog.validation.passwordMismatch'),
      trigger: 'blur',
    },
  ],
}

function openCreate() {
  createForm.username = ''
  createForm.email = ''
  createForm.password = ''
  createForm.confirmPassword = ''
  createForm.isActive = true
  createVisible.value = true
}

async function submitCreate() {
  const validate = createFormRef.value?.validate
  if (typeof validate === 'function') {
    try {
      await validate()
    } catch {
      return
    }
  }
  createSubmitting.value = true
  try {
    await createUser({
      username: createForm.username.trim(),
      email: createForm.email.trim().toLowerCase(),
      password: createForm.password,
      is_active: createForm.isActive,
    })
    MessagePlugin.success(t('system.globalSettings.userManagement.createDialog.success'))
    createVisible.value = false
    await reload()
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.createDialog.failed')
    MessagePlugin.error(message)
  } finally {
    createSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Delete user dialog
// ---------------------------------------------------------------------------
//
// Confirms by typing the target user's email exactly. Same anti-fat-finger
// pattern as SpaceManagement's DeleteSpaceConfirmDialog. The backend
// enforces two preconditions:
//   - cannot delete the caller themselves
//   - cannot delete the last remaining active system admin
// Both surface as 400 with the backend's message preserved on err.message.

const deleteVisible = ref(false)
const deleteSubmitting = ref(false)
const deleteTarget = ref<AdminUserListItem | AdminUserDetailResponse | null>(null)
const deleteConfirmInput = ref('')

const deleteConfirmed = computed(() => {
  const target = deleteTarget.value
  if (!target) return false
  return deleteConfirmInput.value.trim() === (target.email || '').trim()
})

function openDelete(row: AdminUserListItem | AdminUserDetailResponse) {
  if (row.id === currentUserId.value) {
    MessagePlugin.warning(t('system.globalSettings.userManagement.deleteDialog.selfBlocked'))
    return
  }
  deleteTarget.value = row
  deleteConfirmInput.value = ''
  deleteVisible.value = true
}

async function submitDelete() {
  if (!deleteConfirmed.value || !deleteTarget.value) return
  deleteSubmitting.value = true
  try {
    await deleteUser(deleteTarget.value.id)
    MessagePlugin.success(t('system.globalSettings.userManagement.deleteDialog.success'))
    deleteVisible.value = false
    await reload()
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.userManagement.deleteDialog.failed')
    MessagePlugin.error(message)
  } finally {
    deleteSubmitting.value = false
  }
}
</script>

<style scoped>
.user-management {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 16px 0 24px;
}

.um-header h3 {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 600;
}

.um-header p {
  margin: 0;
  color: var(--td-text-color-secondary, #666);
  font-size: 14px;
  line-height: 1.6;
}

.um-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.um-search {
  width: 320px;
  max-width: 100%;
}

.um-total {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.um-loading,
.um-detail-loading {
  padding: 32px 0;
  display: flex;
  justify-content: center;
}

.um-error,
.um-empty {
  padding: 16px 0;
}

.um-table-shell {
  border: 1px solid var(--td-component-stroke, #e7e7e7);
  border-radius: 6px;
  overflow: hidden;
}

.um-loadmore {
  display: flex;
  justify-content: center;
  padding: 16px;
  border-top: 1px solid var(--td-component-stroke, #e7e7e7);
}

.um-row-actions {
  display: flex;
  gap: 4px;
  align-items: center;
}

.um-membership-count {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.um-detail {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.um-detail-section h4 {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  color: var(--td-text-color-primary, #333);
}

.um-detail-fields {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px 16px;
  margin: 0;
}

.um-detail-fields > div {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.um-detail-fields dt {
  font-size: 12px;
  color: var(--td-text-color-secondary, #888);
}

.um-detail-fields dd {
  margin: 0;
  font-size: 14px;
  color: var(--td-text-color-primary, #333);
  word-break: break-word;
}

.um-detail-mono {
  font-family: var(--td-font-family-mono, monospace);
  font-size: 12px;
  color: var(--td-text-color-secondary, #666);
}

.um-detail-empty {
  color: var(--td-text-color-secondary, #888);
  font-size: 13px;
  padding: 8px 0;
}

.um-detail-section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.um-detail-section-header h4 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--td-text-color-primary, #333);
}

.um-membership-role-select {
  min-width: 130px;
}

.um-membership-error {
  color: var(--td-error-color, #d54941);
  font-size: 13px;
  margin: 8px 0 0;
  line-height: 1.5;
}

.um-membership-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.um-membership-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 8px 12px;
  border: 1px solid var(--td-component-stroke, #e7e7e7);
  border-radius: 4px;
  background: var(--td-bg-color-secondary-container, #fafafa);
}

.um-membership-info {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex-wrap: wrap;
}

.um-membership-name {
  font-size: 14px;
  color: var(--td-text-color-primary, #333);
}

.um-detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--td-component-stroke, #e7e7e7);
}

.um-edit-description {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
  margin: 0 0 12px;
  padding: 8px 12px;
  background: var(--td-bg-color-secondary-container, #f5f5f5);
  border-radius: 4px;
}

.um-password-warning {
  margin-bottom: 12px;
}

.um-delete-warning {
  margin-bottom: 12px;
}
</style>