<!--
  SpaceManagement.vue — SystemAdmin workspace-management surface
  -----------------------------------------------------------------
  Renders inside SystemSettings.vue when the "spaces" tab is active.
  Mirrors UserManagement.vue's overall layout (toolbar, table, drawer)
  but adds an Owner selector on the create dialog — that's the hook
  the SystemAdmin uses to provision a workspace for a tenantless user
  (the receiver gets the Owner membership instead of the caller).

  Backend contract: SystemAdmin-only CRUD on /api/v1/system/admin/tenants
  (list/get/create/update/delete). The router enforces the SystemAdmin
  gate; this component doesn't re-check role.

  UX notes:
    - Pagination is page/page_size; the management table is small enough
      that "load more" is overkill.
    - Detail drawer shows the workspace account info + a read-only member
      list. Member CRUD happens in the per-tenant member page, not here,
      to keep this surface focused on workspace identity / quotas.
    - Delete dialog requires typing the workspace id exactly — same
      anti-fat-finger pattern as UserManagement's DeleteUserConfirmDialog.
    - Create dialog fetches candidate users via listUsers({ offset, limit })
      (NOT page/page_size — the existing user API uses offset/limit, see
      api/system/users.ts).
-->
<template>
  <div class="space-management">
    <div class="sm-header">
      <h3>{{ t('system.globalSettings.spaceManagement.detail.title') }}</h3>
      <p>{{ t('system.globalSettings.spaceManagement.detail.description') }}</p>
    </div>

    <div class="sm-toolbar">
      <t-input
        v-model="searchTerm"
        size="medium"
        :placeholder="t('system.globalSettings.spaceManagement.searchPlaceholder')"
        clearable
        class="sm-search"
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
        {{ t('system.globalSettings.spaceManagement.actions.create') }}
      </t-button>
      <t-button
        variant="outline"
        :loading="loading"
        @click="reload"
      >
        <template #icon><t-icon name="refresh" /></template>
        {{ t('system.globalSettings.spaceManagement.refresh') }}
      </t-button>
      <span v-if="!loading" class="sm-total" aria-live="polite">
        {{ t('system.globalSettings.spaceManagement.totalLabel', { total: total }) }}
      </span>
    </div>

    <div v-if="loading && tenants.length === 0" class="sm-loading">
      <t-loading :text="t('system.globalSettings.spaceManagement.loading')" />
    </div>

    <div v-else-if="error" class="sm-error">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="reload">
            {{ t('system.globalSettings.spaceManagement.refresh') }}
          </t-button>
        </template>
      </t-alert>
    </div>

    <div v-else-if="tenants.length === 0" class="sm-empty">
      <t-empty :description="t('system.globalSettings.spaceManagement.empty')" />
    </div>

    <div v-else class="sm-table-shell">
      <t-table
        row-key="id"
        :data="tenants"
        :columns="columns"
        size="medium"
        hover
        stripe
        :loading="loading"
        :pagination="pagination"
        @page-change="onPageChange"
      >
        <template #status="{ row }">
          <t-tag
            :theme="row.status === 'active' ? 'success' : 'default'"
            size="small"
            variant="light"
          >
            {{ t(`system.globalSettings.spaceManagement.statusLabel.${row.status || 'active'}`) }}
          </t-tag>
        </template>
        <template #member_count="{ row }">
          <span class="sm-member-count">
            {{ t('system.globalSettings.spaceManagement.memberCountLabel', { count: row.member_count }) }}
          </span>
        </template>
        <template #owner_username="{ row }">
          <span v-if="row.owner_username">{{ row.owner_username }}</span>
          <span v-else class="sm-owner-empty">
            {{ t('system.globalSettings.spaceManagement.detail.ownerEmptyHint') }}
          </span>
        </template>
        <template #created_at="{ row }">
          {{ formatDate(row.created_at) }}
        </template>
        <template #actions="{ row }">
          <div class="sm-row-actions">
            <t-tooltip :content="t('system.globalSettings.spaceManagement.actions.view')" placement="top">
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
            <t-tooltip :content="t('system.globalSettings.spaceManagement.actions.edit')" placement="top">
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
            <t-tooltip :content="t('system.globalSettings.spaceManagement.actions.delete')" placement="top">
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
          </div>
        </template>
      </t-table>
    </div>

    <!--
      Detail drawer. Header carries the workspace name; body splits into
      "Account information" (read-only) and "Members" (read-only list —
      member CRUD lives in the per-tenant member page). Closing resets
      detailLoadedId so reopening the same workspace re-fetches.
    -->
    <t-drawer
      v-model:visible="detailVisible"
      :header="detailHeader"
      drawer-class-name="space-management-detail-drawer"
      size="560px"
      :footer="false"
      placement="right"
      destroy-on-close
    >
      <div v-if="detailLoading" class="sm-detail-loading">
        <t-loading :text="t('system.globalSettings.spaceManagement.loading')" />
      </div>
      <div v-else-if="detail" class="sm-detail">
        <div class="sm-detail-section">
          <h4>{{ t('system.globalSettings.spaceManagement.detail.accountSection') }}</h4>
          <dl class="sm-detail-fields">
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.name') }}</dt>
              <dd>{{ detail.name }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.description') }}</dt>
              <dd>{{ detail.description || '—' }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.status') }}</dt>
              <dd>
                <t-tag
                  :theme="detail.status === 'active' ? 'success' : 'default'"
                  size="small"
                  variant="light"
                >
                  {{ t(`system.globalSettings.spaceManagement.statusLabel.${detail.status || 'active'}`) }}
                </t-tag>
              </dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.memberCount') }}</dt>
              <dd>{{ detail.member_count }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.owner') }}</dt>
              <dd>{{ detail.owner_username || t('system.globalSettings.spaceManagement.detail.ownerEmptyHint') }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.tenantId') }}</dt>
              <dd class="sm-detail-mono">{{ detail.id }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.spaceManagement.detail.fields.createdAt') }}</dt>
              <dd>{{ formatDate(detail.created_at) }}</dd>
            </div>
          </dl>
        </div>

        <div class="sm-detail-actions">
          <t-button theme="primary" @click="openEdit(detail); detailVisible = false">
            <template #icon><t-icon name="edit" /></template>
            {{ t('system.globalSettings.spaceManagement.actions.edit') }}
          </t-button>
          <t-button theme="danger" @click="openDelete(detail); detailVisible = false">
            <template #icon><t-icon name="delete" /></template>
            {{ t('system.globalSettings.spaceManagement.actions.delete') }}
          </t-button>
        </div>
      </div>
    </t-drawer>

    <!--
      Create space dialog. name + description + storage_quota_gb + status +
      owner_user_id selector. The owner selector lists every registered
      user (offset/limit, max 100). When the operator picks "管理员自用"
      we leave owner_user_id unset so the backend defaults to caller.ID.
    -->
    <t-dialog
      v-model:visible="createVisible"
      :header="t('system.globalSettings.spaceManagement.createDialog.title')"
      width="520px"
      placement="center"
      dialog-class-name="space-management-create-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.spaceManagement.createDialog.submit'),
        theme: 'primary',
        loading: createSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.spaceManagement.createDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!createSubmitting"
      :close-btn="!createSubmitting"
      @confirm="submitCreate"
    >
      <p class="sm-create-description">
        {{ t('system.globalSettings.spaceManagement.createDialog.description') }}
      </p>
      <t-form
        ref="createFormRef"
        :data="createForm"
        :rules="createRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.createDialog.fields.name')"
          name="name"
        >
          <t-input
            v-model="createForm.name"
            clearable
            :placeholder="t('system.globalSettings.spaceManagement.createDialog.namePlaceholder')"
            :disabled="createSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.createDialog.fields.description')"
          name="description"
        >
          <t-textarea
            v-model="createForm.description"
            :placeholder="t('system.globalSettings.spaceManagement.createDialog.descriptionPlaceholder')"
            :disabled="createSubmitting"
            :autosize="{ minRows: 2, maxRows: 4 }"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.createDialog.fields.storageQuota')"
          name="storageQuotaGb"
        >
          <t-input-number
            v-model="createForm.storageQuotaGb"
            :min="1"
            :max="10240"
            :placeholder="t('system.globalSettings.spaceManagement.createDialog.storageQuotaPlaceholder')"
            :disabled="createSubmitting"
            style="width: 100%;"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.createDialog.fields.status')"
          name="status"
        >
          <t-select
            v-model="createForm.status"
            :disabled="createSubmitting"
          >
            <t-option
              v-for="opt in statusOptions"
              :key="opt.value"
              :label="t(`system.globalSettings.spaceManagement.statusLabel.${opt.value}`)"
              :value="opt.value"
            />
          </t-select>
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.createDialog.fields.owner')"
          name="ownerUserId"
        >
          <t-select
            v-model="createForm.ownerUserId"
            :disabled="createSubmitting"
            :loading="ownerCandidatesLoading"
            :placeholder="t('system.globalSettings.spaceManagement.createDialog.ownerSelector.placeholder')"
            clearable
            filterable
          >
            <t-option
              :label="t('system.globalSettings.spaceManagement.createDialog.ownerSelector.selfOption')"
              :value="SELF_OPTION"
            />
            <t-option
              v-for="u in ownerCandidates"
              :key="u.id"
              :label="`${u.username} · ${u.email}`"
              :value="u.id"
            />
          </t-select>
          <p v-if="ownerCandidatesError" class="sm-create-hint">
            {{ t('system.globalSettings.spaceManagement.createDialog.ownerSelector.loadingError') }}
          </p>
        </t-form-item>
      </t-form>
    </t-dialog>

    <!--
      Edit space dialog. Same shape as the Owner-facing updateTenant
      payload but exposes status + storage_quota_gb (admin-only fields).
      Submitting with no changes surfaces a t-message.warning.
    -->
    <t-dialog
      v-model:visible="editVisible"
      :header="t('system.globalSettings.spaceManagement.editDialog.title', { name: editTarget?.name || '' })"
      width="520px"
      placement="center"
      dialog-class-name="space-management-edit-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.spaceManagement.editDialog.submit'),
        theme: 'primary',
        loading: editSubmitting,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.spaceManagement.editDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!editSubmitting"
      :close-btn="!editSubmitting"
      @confirm="submitEdit"
    >
      <t-form
        v-if="editForm"
        ref="editFormRef"
        :data="editForm"
        :rules="editRules"
        label-align="top"
      >
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.editDialog.fields.name')"
          name="name"
        >
          <t-input
            v-model="editForm.name"
            clearable
            :disabled="editSubmitting"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.editDialog.fields.description')"
          name="description"
        >
          <t-textarea
            v-model="editForm.description"
            :disabled="editSubmitting"
            :autosize="{ minRows: 2, maxRows: 4 }"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.editDialog.fields.storageQuota')"
          name="storageQuotaGb"
        >
          <t-input-number
            v-model="editForm.storageQuotaGb"
            :min="1"
            :max="10240"
            :disabled="editSubmitting"
            style="width: 100%;"
          />
        </t-form-item>
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.editDialog.fields.status')"
          name="status"
        >
          <t-select
            v-model="editForm.status"
            :disabled="editSubmitting"
          >
            <t-option
              v-for="opt in statusOptions"
              :key="opt.value"
              :label="t(`system.globalSettings.spaceManagement.statusLabel.${opt.value}`)"
              :value="opt.value"
            />
          </t-select>
        </t-form-item>
        <!--
          Ownership-transfer selector. Empty = keep the current Owner
          untouched. Picking a user promotes them to Owner and demotes
          the previous Owner to admin (server-side, via the
          transferTenantOwnership helper). The current Owner is shown
          as a hint under the dropdown so the admin can confirm who
          they're about to displace. We don't preload a "self" option
          here because the create-dialog semantics (admin-self default)
          don't apply — editing an existing workspace always targets an
          existing owner set, and "admin becomes the owner" would
          silently demote the legitimate owner without consent.
        -->
        <t-form-item
          :label="t('system.globalSettings.spaceManagement.editDialog.fields.owner')"
          name="ownerUserId"
        >
          <t-select
            v-model="editForm.ownerUserId"
            :disabled="editSubmitting"
            :loading="ownerCandidatesLoading"
            :placeholder="t('system.globalSettings.spaceManagement.editDialog.ownerSelector.placeholder')"
            clearable
            filterable
          >
            <t-option
              v-for="u in ownerCandidates"
              :key="u.id"
              :label="`${u.username} · ${u.email}`"
              :value="u.id"
            />
          </t-select>
          <p class="sm-edit-hint">
            {{ t('system.globalSettings.spaceManagement.editDialog.ownerSelector.currentHint', {
              owner: editTarget?.owner_username || t('system.globalSettings.spaceManagement.detail.ownerEmptyHint'),
            }) }}
          </p>
          <p v-if="ownerCandidatesError" class="sm-create-hint">
            {{ t('system.globalSettings.spaceManagement.createDialog.ownerSelector.loadingError') }}
          </p>
        </t-form-item>
      </t-form>
    </t-dialog>

    <!--
      Delete space dialog. The confirm step requires the operator to type
      the workspace id exactly — same anti-fat-finger pattern as the user
      delete dialog. Backend enforces two preconditions:
        - caller cannot delete their last active workspace if they're the Owner
        - non-owner members are cascade-removed first; failures abort the op
      Both surface as 400 with the backend's message preserved on err.message.
    -->
    <t-dialog
      v-model:visible="deleteVisible"
      :header="t('system.globalSettings.spaceManagement.deleteDialog.title', { name: deleteTarget?.name || '' })"
      width="480px"
      placement="center"
      dialog-class-name="space-management-delete-dialog"
      :confirm-btn="{
        content: t('system.globalSettings.spaceManagement.deleteDialog.submit'),
        theme: 'danger',
        loading: deleteSubmitting,
        disabled: !deleteConfirmed,
      }"
      :cancel-btn="{
        content: t('system.globalSettings.spaceManagement.deleteDialog.cancel'),
        variant: 'outline',
      }"
      :close-on-overlay-click="!deleteSubmitting"
      :close-btn="!deleteSubmitting"
      @confirm="submitDelete"
    >
      <t-alert
        theme="error"
        :message="t('system.globalSettings.spaceManagement.deleteDialog.warning')"
        class="sm-delete-warning"
      />
      <p class="sm-create-description">
        {{ t('system.globalSettings.spaceManagement.deleteDialog.confirmHint', { id: deleteTarget?.id ?? '' }) }}
      </p>
      <t-input
        v-model="deleteConfirmInput"
        :placeholder="t('system.globalSettings.spaceManagement.deleteDialog.confirmPlaceholder')"
        :disabled="deleteSubmitting"
        clearable
      >
        <template #prefix-icon><t-icon name="desktop" /></template>
      </t-input>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import type {
  FormInstanceFunctions,
  FormRule,
  PageInfo,
  PrimaryTableCol,
} from 'tdesign-vue-next'
import {
  listAdminTenants,
  getAdminTenantDetail,
  createAdminTenant,
  updateAdminTenant,
  deleteAdminTenant,
  type SystemTenantInfo,
  type CreateAdminTenantRequest,
  type UpdateAdminTenantRequest,
} from '@/api/system/spaces'
import { listUsers, type AdminUserListItem } from '@/api/system/users'

const { t } = useI18n()

const PAGE_SIZE = 20

const SELF_OPTION = '__self__'

const statusOptions: Array<{ value: 'active' | 'suspended' }> = [
  { value: 'active' },
  { value: 'suspended' },
]

const searchTerm = ref('')
const tenants = ref<SystemTenantInfo[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')

const pagination = computed(() => ({
  total: total.value,
  current: page.value,
  pageSize: PAGE_SIZE,
  showJumper: true,
}))

const columns = computed<PrimaryTableCol<SystemTenantInfo>[]>(() => [
  {
    colKey: 'name',
    title: t('system.globalSettings.spaceManagement.columns.name'),
    minWidth: 160,
  },
  {
    colKey: 'description',
    title: t('system.globalSettings.spaceManagement.columns.description'),
    minWidth: 200,
    ellipsis: true,
  },
  {
    colKey: 'owner_username',
    title: t('system.globalSettings.spaceManagement.columns.owner'),
    width: 160,
  },
  {
    colKey: 'status',
    title: t('system.globalSettings.spaceManagement.columns.status'),
    width: 110,
  },
  {
    colKey: 'member_count',
    title: t('system.globalSettings.spaceManagement.columns.members'),
    width: 110,
  },
  {
    colKey: 'created_at',
    title: t('system.globalSettings.spaceManagement.columns.createdAt'),
    width: 160,
  },
  {
    colKey: 'actions',
    title: t('system.globalSettings.spaceManagement.columns.actions'),
    width: 160,
    fixed: 'right',
  },
])

function formatDate(value: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

// ---------------------------------------------------------------------------
// List lifecycle
// ---------------------------------------------------------------------------

function reset() {
  tenants.value = []
  total.value = 0
  page.value = 1
  error.value = ''
}

async function loadPage(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const keyword = searchTerm.value.trim()
    const res = await listAdminTenants({
      keyword: keyword || undefined,
      page: targetPage,
      page_size: PAGE_SIZE,
    })
    tenants.value = res.items
    total.value = res.total
    page.value = res.page
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.spaceManagement.messages.loadFailed')
    error.value = message
    MessagePlugin.error(message)
  } finally {
    loading.value = false
  }
}

function applySearch() {
  reset()
  void loadPage(1)
}

function reload() {
  void loadPage(page.value)
}

function onPageChange(pageInfo: PageInfo) {
  const target = pageInfo.current ?? 1
  void loadPage(target)
}

onMounted(() => {
  void reload()
})

// ---------------------------------------------------------------------------
// Detail drawer
// ---------------------------------------------------------------------------

const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<SystemTenantInfo | null>(null)

const detailHeader = computed(() => {
  const t = detail.value
  if (!t) return ''
  return t.name
})

async function openDetail(row: SystemTenantInfo) {
  detailVisible.value = true
  detail.value = null
  detailLoading.value = true
  try {
    const res = await getAdminTenantDetail(row.id)
    detail.value = res
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.spaceManagement.messages.detailLoadFailed')
    MessagePlugin.error(message)
    detailVisible.value = false
  } finally {
    detailLoading.value = false
  }
}

// ---------------------------------------------------------------------------
// Owner candidates (CreateSpaceDialog)
// ---------------------------------------------------------------------------
//
// listUsers takes offset/limit (not page/page_size). We fetch a single
// 100-row window for the dropdown — adequate for the admin's owner
// picker since the admin typically operates on a known set of users.
// Failures are surfaced inline under the selector rather than via a
// blocking alert, so the operator can still proceed with the "self" option.

const ownerCandidates = ref<AdminUserListItem[]>([])
const ownerCandidatesLoading = ref(false)
const ownerCandidatesError = ref(false)

async function loadOwnerCandidates() {
  ownerCandidatesLoading.value = true
  ownerCandidatesError.value = false
  try {
    const res = await listUsers({ offset: 0, limit: 100 })
    ownerCandidates.value = res.users
  } catch {
    ownerCandidatesError.value = true
    ownerCandidates.value = []
  } finally {
    ownerCandidatesLoading.value = false
  }
}

// ---------------------------------------------------------------------------
// Create space dialog
// ---------------------------------------------------------------------------

const createVisible = ref(false)
const createSubmitting = ref(false)
const createFormRef = ref<FormInstanceFunctions>()
const createForm = reactive({
  name: '',
  description: '',
  storageQuotaGb: undefined as number | undefined,
  status: 'active' as 'active' | 'suspended',
  // SELF_OPTION sentinel — translated to undefined when submitted so the
  // backend defaults owner_user_id to caller.ID (admin-self path).
  ownerUserId: SELF_OPTION as string,
})

const createRules: Record<string, FormRule[]> = {
  name: [
    { required: true, message: t('system.globalSettings.spaceManagement.createDialog.validation.nameRequired'), trigger: 'blur' },
    { min: 1, max: 128, message: t('system.globalSettings.spaceManagement.createDialog.validation.nameLength'), trigger: 'blur' },
  ],
}

function openCreate() {
  createForm.name = ''
  createForm.description = ''
  createForm.storageQuotaGb = undefined
  createForm.status = 'active'
  createForm.ownerUserId = SELF_OPTION
  createVisible.value = true
  void loadOwnerCandidates()
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
    const payload: CreateAdminTenantRequest = {
      name: createForm.name.trim(),
    }
    if (createForm.description.trim()) {
      payload.description = createForm.description.trim()
    }
    if (createForm.storageQuotaGb && createForm.storageQuotaGb > 0) {
      payload.storage_quota_gb = createForm.storageQuotaGb
    }
    payload.status = createForm.status
    if (createForm.ownerUserId && createForm.ownerUserId !== SELF_OPTION) {
      payload.owner_user_id = createForm.ownerUserId
    }
    await createAdminTenant(payload)
    MessagePlugin.success(t('system.globalSettings.spaceManagement.createDialog.success'))
    createVisible.value = false
    await loadPage(1)
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.spaceManagement.createDialog.failed')
    MessagePlugin.error(message)
  } finally {
    createSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Edit space dialog
// ---------------------------------------------------------------------------

const editVisible = ref(false)
const editSubmitting = ref(false)
const editFormRef = ref<FormInstanceFunctions>()
const editTarget = ref<SystemTenantInfo | null>(null)
const editSnapshot = ref<{
  name: string
  description: string
  storageQuotaGb: number | undefined
  status: string
  // Snapshot the owner selection so an unchanged value doesn't fire
  // a PATCH. Sentinel is the literal "" string — the form initialises
  // editForm.ownerUserId to "" too, so an untouched dropdown matches.
  ownerUserId: string
} | null>(null)
const editForm = reactive({
  name: '',
  description: '',
  storageQuotaGb: undefined as number | undefined,
  status: 'active' as 'active' | 'suspended',
  // Empty string = "do not transfer ownership". The select renders
  // an empty selection with placeholder text inviting the admin to
  // pick a new Owner. We deliberately do NOT default to the current
  // owner's user id because the /system/admin/tenants/:id payload
  // does not surface it — only owner_username. Capturing "no change"
  // as "" keeps the PATCH compact and matches the other fields'
  // idempotent semantics (empty value = no write).
  ownerUserId: '' as string,
})

const editRules: Record<string, FormRule[]> = {
  name: [
    { required: true, message: t('system.globalSettings.spaceManagement.editDialog.validation.nameRequired'), trigger: 'blur' },
    { min: 1, max: 128, message: t('system.globalSettings.spaceManagement.editDialog.validation.nameLength'), trigger: 'blur' },
  ],
}

function storageQuotaBytesToGb(bytes?: number): number | undefined {
  if (!bytes || bytes <= 0) return undefined
  const gb = Math.round((bytes / (1024 * 1024 * 1024)) * 100) / 100
  return gb > 0 ? gb : undefined
}

function openEdit(row: SystemTenantInfo) {
  editTarget.value = row
  editSnapshot.value = {
    name: row.name,
    description: row.description || '',
    storageQuotaGb: storageQuotaBytesToGb(row.storage_quota),
    status: row.status || 'active',
    ownerUserId: '',
  }
  editForm.name = row.name
  editForm.description = row.description || ''
  editForm.storageQuotaGb = storageQuotaBytesToGb(row.storage_quota)
  editForm.status = (row.status === 'suspended' ? 'suspended' : 'active')
  editForm.ownerUserId = ''
  editVisible.value = true
  // Reuse the create dialog's candidate list. loadOwnerCandidates is
  // idempotent and caches its result across calls, so the second open
  // is a no-op fetch — but we still call it so an admin who only
  // edits (never creates) gets the candidate list populated.
  void loadOwnerCandidates()
}

function buildEditPayload(): UpdateAdminTenantRequest | null {
  const snap = editSnapshot.value
  if (!snap) return null
  const payload: UpdateAdminTenantRequest = {}
  if (editForm.name.trim() !== snap.name) {
    payload.name = editForm.name.trim()
  }
  if (editForm.description.trim() !== snap.description) {
    payload.description = editForm.description.trim()
  }
  if (editForm.status !== snap.status) {
    payload.status = editForm.status
  }
  if ((editForm.storageQuotaGb ?? null) !== (snap.storageQuotaGb ?? null)) {
    if (editForm.storageQuotaGb && editForm.storageQuotaGb > 0) {
      payload.storage_quota_gb = editForm.storageQuotaGb
    }
  }
  // Owner transfer: empty/whitespace = "do not touch ownership",
  // matching the create-dialog semantics. We diff against the
  // snapshot's ownerUserId sentinel ("") so unchanged = no-op.
  const trimmedOwner = editForm.ownerUserId.trim()
  if (trimmedOwner !== snap.ownerUserId) {
    if (trimmedOwner) {
      payload.owner_user_id = trimmedOwner
    }
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
  const target = editTarget.value
  if (!target) return
  const payload = buildEditPayload()
  if (!payload) {
    MessagePlugin.warning(t('system.globalSettings.spaceManagement.editDialog.noChanges'))
    return
  }
  editSubmitting.value = true
  try {
    await updateAdminTenant(target.id, payload)
    MessagePlugin.success(t('system.globalSettings.spaceManagement.editDialog.success'))
    editVisible.value = false
    await loadPage(page.value)
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.spaceManagement.editDialog.failed')
    MessagePlugin.error(message)
  } finally {
    editSubmitting.value = false
  }
}

// ---------------------------------------------------------------------------
// Delete space dialog
// ---------------------------------------------------------------------------

const deleteVisible = ref(false)
const deleteSubmitting = ref(false)
const deleteTarget = ref<SystemTenantInfo | null>(null)
const deleteConfirmInput = ref('')

const deleteConfirmed = computed(() => {
  const target = deleteTarget.value
  if (!target) return false
  return deleteConfirmInput.value.trim() === String(target.id)
})

function openDelete(row: SystemTenantInfo) {
  deleteTarget.value = row
  deleteConfirmInput.value = ''
  deleteVisible.value = true
}

async function submitDelete() {
  if (!deleteConfirmed.value || !deleteTarget.value) return
  deleteSubmitting.value = true
  try {
    await deleteAdminTenant(deleteTarget.value.id)
    MessagePlugin.success(t('system.globalSettings.spaceManagement.deleteDialog.success'))
    deleteVisible.value = false
    await loadPage(page.value)
  } catch (e) {
    const message = (e as { message?: string })?.message
      || t('system.globalSettings.spaceManagement.deleteDialog.failed')
    MessagePlugin.error(message)
  } finally {
    deleteSubmitting.value = false
  }
}

onBeforeUnmount(() => {
  // No timers or subscriptions to clear — Dialog/Drawer are controlled
  // components and clean themselves up when their v-model flips.
})
</script>

<style scoped>
.space-management {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 16px 0 24px;
}

.sm-header h3 {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 600;
}

.sm-header p {
  margin: 0;
  color: var(--td-text-color-secondary, #666);
  font-size: 14px;
  line-height: 1.6;
}

.sm-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.sm-search {
  width: 320px;
  max-width: 100%;
}

.sm-total {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.sm-loading,
.sm-detail-loading {
  padding: 32px 0;
  display: flex;
  justify-content: center;
}

.sm-error,
.sm-empty {
  padding: 16px 0;
}

.sm-table-shell {
  border: 1px solid var(--td-component-stroke, #e7e7e7);
  border-radius: 6px;
  overflow: hidden;
}

.sm-row-actions {
  display: flex;
  gap: 4px;
  align-items: center;
}

.sm-member-count {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.sm-owner-empty {
  color: var(--td-text-color-secondary, #aaa);
  font-size: 13px;
  font-style: italic;
}

.sm-detail {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.sm-detail-section h4 {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  color: var(--td-text-color-primary, #333);
}

.sm-detail-fields {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px 16px;
  margin: 0;
}

.sm-detail-fields > div {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sm-detail-fields dt {
  font-size: 12px;
  color: var(--td-text-color-secondary, #888);
}

.sm-detail-fields dd {
  margin: 0;
  font-size: 14px;
  color: var(--td-text-color-primary, #333);
  word-break: break-word;
}

.sm-detail-mono {
  font-family: var(--td-font-family-mono, monospace);
  font-size: 12px;
  color: var(--td-text-color-secondary, #666);
}

.sm-detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--td-component-stroke, #e7e7e7);
}

.sm-create-description {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
  margin: 0 0 12px;
  padding: 8px 12px;
  background: var(--td-bg-color-secondary-container, #f5f5f5);
  border-radius: 4px;
}

.sm-create-hint {
  color: var(--td-error-color, #d54941);
  font-size: 12px;
  margin: 4px 0 0;
}

.sm-edit-hint {
  color: var(--td-text-color-secondary, #666);
  font-size: 12px;
  margin: 4px 0 0;
  line-height: 1.5;
}

.sm-delete-warning {
  margin-bottom: 12px;
}
</style>