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
          <h4>{{ t('system.globalSettings.userManagement.detail.membershipsSection') }}</h4>
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
              </div>
              <t-tag
                :theme="roleTagTheme(m.role)"
                size="small"
              >
                {{ t(`system.globalSettings.userManagement.roleLabels.${m.role}`) }}
              </t-tag>
            </li>
          </ul>
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
  type AdminUserListItem,
  type AdminUserDetailResponse,
  type UpdateUserRequest,
} from '@/api/system/users'
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
</style>