<template>
  <a-card class="page-card system-account-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keyword" search-placeholder="搜索用户名或显示名称" :show-reset="Boolean(keyword.trim())" :refresh-loading="loading" @search="searchAccounts" @reset="resetSearch" @refresh="refreshAccounts">
      <template #actions>
        <a-button v-if="canManageSystemAccounts" type="primary" @click="openCreate">新增系统账户</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table" :columns="columns" :data-source="accounts" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="1190" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileAccounts" @mobile-refresh="refreshMobileAccounts">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'role'">
          <a-tag :color="systemAccountRoleColor(record.role)">{{ systemAccountRoleLabel(record.role) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'red'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'imageGenerationEnabled'">
          <a-tag :color="record.imageGenerationEnabled ? 'green' : 'default'">{{ record.imageGenerationEnabled ? '支持' : '不支持' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'mustChangePassword'">
          <a-tag :color="record.mustChangePassword ? 'warning' : 'success'">{{ record.mustChangePassword ? '提醒' : '不提醒' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'lastLoginAt'">
          <span class="muted-cell">{{ formatDateTime(record.lastLoginAt) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions v-if="canManageSystemAccounts" :actions="systemAccountActions" @action-click="handleSystemAccountAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.username }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="systemAccountRoleColor(record.role)">{{ systemAccountRoleLabel(record.role) }}</a-tag>
              <a-tag :color="record.status === 'active' ? 'green' : 'red'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
              <a-tag :color="record.imageGenerationEnabled ? 'green' : 'default'">{{ record.imageGenerationEnabled ? '支持图像' : '禁用图像' }}</a-tag>
              <a-tag :color="record.mustChangePassword ? 'warning' : 'success'">{{ record.mustChangePassword ? '提醒改密' : '不提醒改密' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>显示名称</span>
              <strong>{{ record.displayName }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最后登录</span>
              <strong>{{ formatDateTime(record.lastLoginAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div v-if="canManageSystemAccounts" class="mobile-list-card-actions">
            <RowActions variant="button" :actions="systemAccountActions" @action-click="handleSystemAccountAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :width="760" :title="editingId ? '编辑系统账户' : '新增系统账户'" :confirm-loading="systemAccountSaving" :ok-button-props="{ disabled: systemAccountSaving }" @ok="handleSave">
      <a-form layout="vertical">
        <a-form-item label="用户名" required>
          <a-input v-model:value="form.username" :disabled="Boolean(editingId)" placeholder="例如 user01" />
        </a-form-item>
        <a-form-item label="显示名称" required>
          <a-input v-model:value="form.displayName" placeholder="例如业务用户" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" placeholder="可选，填写账户用途或归属说明" />
        </a-form-item>
        <a-form-item v-if="!editingId" label="初始密码" required>
          <a-input-password v-model:value="form.password" placeholder="请输入初始密码" />
        </a-form-item>
        <a-form-item label="角色">
          <a-select v-model:value="form.role" :options="roleOptions" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="form.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="登录后提醒改密">
          <a-switch v-model:checked="form.mustChangePassword" :disabled="systemAccountMustChangePasswordDisabled" checked-children="是" un-checked-children="否" />
        </a-form-item>
        <a-form-item label="支持图像生成">
          <a-switch v-model:checked="form.imageGenerationEnabled" checked-children="支持" un-checked-children="不支持" />
        </a-form-item>

        <div class="request-limit-editor">
          <div class="request-limit-editor-head">
            <strong>用户请求限制</strong>
            <span>留空继承全局，填写 0 表示该用户无限；到期日当天仍有效，次日自动继承全局。</span>
          </div>
          <div class="request-limit-grid">
            <a-form-item label="每分钟请求数">
              <a-input-number v-model:value="form.requestLimitPerMinute" :min="0" :max="1000000000" :precision="0" :step="1" placeholder="继承全局" style="width: 100%" />
            </a-form-item>
            <a-form-item label="每日请求数">
              <a-input-number v-model:value="form.requestLimitPerDay" :min="0" :max="1000000000" :precision="0" :step="1" placeholder="继承全局" style="width: 100%" />
            </a-form-item>
            <a-form-item label="每周请求数">
              <a-input-number v-model:value="form.requestLimitPerWeek" :min="0" :max="1000000000" :precision="0" :step="1" placeholder="继承全局" style="width: 100%" />
            </a-form-item>
            <a-form-item label="每月请求数">
              <a-input-number v-model:value="form.requestLimitPerMonth" :min="0" :max="1000000000" :precision="0" :step="1" placeholder="继承全局" style="width: 100%" />
            </a-form-item>
          </div>
          <a-form-item label="覆盖到期日" tooltip="可选。所选日期当天仍生效，次日 00:00 起按系统统计时区自动继承全局。">
            <a-date-picker v-model:value="form.requestLimitExpiresOn" value-format="YYYY-MM-DD" placeholder="长期有效" allow-clear style="width: 100%" />
          </a-form-item>
        </div>
      </a-form>
    </a-modal>

    <a-modal v-model:open="passwordModalOpen" title="重置系统账户密码" :confirm-loading="resetPasswordSaving" :ok-button-props="{ disabled: resetPasswordSaving }" @ok="handleResetPassword">
      <a-form layout="vertical">
        <a-form-item label="新密码" required>
          <a-input-password v-model:value="resetPassword" placeholder="请输入不含空格的新密码" />
        </a-form-item>
        <a-alert type="info" show-icon :message="resetPasswordHint" />
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { authState } from '@/composables/useAuth'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import { isAdminRole, isSuperAdminRole, systemAccountRoleColor, systemAccountRoleLabel } from '@/shared/systemAccountRoles'
import type { SystemAccountListItem, SystemAccountRole, SystemAccountStatus, UserRequestLimits } from '@/types/domain'
import { buildSystemAccountEditablePatch, cloneSystemAccountEditableValues, hasSystemAccountEditableChanges, mergeSystemAccountPageItems, reconcileCreatedSystemAccount, reconcileSystemAccountMutationPage, type SystemAccountEditableValues } from './systemAccountEditForm'

interface SystemAccountsPageState {
  keyword: string
  pagination: PagePaginationState
}

const pageSize = 20
const pageStateCache = usePageStateCache<SystemAccountsPageState>(undefined, defaultSystemAccountsPageState, {
  sanitize: sanitizeSystemAccountsPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const { submitAction, submittingRef } = useSubmitAction('system-accounts')
const systemAccountSaving = submittingRef('system_accounts.save')
const resetPasswordSaving = submittingRef('system_accounts.reset_password')
const modalOpen = ref(false)
const passwordModalOpen = ref(false)
const editingId = ref<string>()
const editingVersion = ref<string>()
const editingBaseline = ref<SystemAccountEditableValues>()
const resettingId = ref<string>()
const resettingVersion = ref<string>()
const resettingAccountRole = ref<SystemAccountRole>()
const resetPassword = ref('')
const keyword = ref(initialPageState.keyword)
const canManageSystemAccounts = computed(() => isSuperAdminRole(authState.currentUser.value?.role))
const whitespacePattern = /\s/

const form = reactive({
  username: '',
  displayName: '',
  description: '',
  password: '',
  role: 'user' as SystemAccountRole,
  status: 'active' as SystemAccountStatus,
  mustChangePassword: true,
  imageGenerationEnabled: false,
  requestLimitPerMinute: null as number | null,
  requestLimitPerDay: null as number | null,
  requestLimitPerWeek: null as number | null,
  requestLimitPerMonth: null as number | null,
  requestLimitExpiresOn: null as string | null
})

const roleOptions = computed(() => {
  const options = [
    { label: '管理员', value: 'admin' },
    { label: '用户', value: 'user' }
  ]
  return form.role === 'super_admin'
    ? [{ label: '超级管理员', value: 'super_admin', disabled: true }, ...options]
    : options
})

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]
const systemAccountMustChangePasswordDisabled = computed(() => isAdminRole(form.role))
const resetPasswordHint = computed(() => isAdminRole(resettingAccountRole.value)
  ? '密码不能包含空格，管理员账户保存后可直接进入控制台。'
  : '密码不能包含空格，保存后该账户下次登录会收到修改密码提醒。')

const baseColumns = [
  { title: '用户名', dataIndex: 'username', key: 'username', width: 160 },
  { title: '显示名称', dataIndex: 'displayName', key: 'displayName', width: 180 },
  { title: '角色', key: 'role', width: 110 },
  { title: '状态', key: 'status', width: 100 },
  { title: '图像生成', key: 'imageGenerationEnabled', width: 110 },
  { title: '改密提醒', key: 'mustChangePassword', width: 110 },
  { title: '最后登录', key: 'lastLoginAt', width: 180 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 }
]

const columns = computed(() => canManageSystemAccounts.value
  ? [...baseColumns, { title: '操作', key: 'actions', fixed: 'right' }]
  : baseColumns)

const systemAccountActions: RowActionItem[] = [
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  { key: 'reset-password', label: '重置密码', icon: 'password', tone: 'warning' }
]

const {
  items: accounts,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  invalidatePendingLoads,
  loadData,
  loadMoreMobile: loadMoreMobileAccounts,
  refreshMobile: refreshMobileAccounts,
  resetPagination,
  applyResult: applySystemAccountPageResult
} = useResponsivePagedList<SystemAccountListItem>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? total - 1)} 个系统账户，还有更多`
    : `共 ${formatNumber(total)} 个系统账户`,
  fetchPage: async (_options, pageState) => api.systemAccounts.listPage({
    keyword: keyword.value.trim() || undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }),
  mergeItems: mergeSystemAccountPageItems,
  onError: (error) => {
    console.error(error)
    message.error('加载系统账户失败')
  }
})

function openCreate() {
  if (!canManageSystemAccounts.value) return
  editingId.value = undefined
  editingVersion.value = undefined
  editingBaseline.value = undefined
  Object.assign(form, {
    username: '', displayName: '', description: '', password: '', role: 'user', status: 'active', mustChangePassword: true,
    imageGenerationEnabled: false, requestLimitPerMinute: null, requestLimitPerDay: null, requestLimitPerWeek: null, requestLimitPerMonth: null,
    requestLimitExpiresOn: null
  })
  modalOpen.value = true
}

function openEdit(record: SystemAccountListItem) {
  if (!canManageSystemAccounts.value) return
  editingId.value = record.id
  editingVersion.value = record.editVersion
  Object.assign(form, {
    username: record.username,
    displayName: record.displayName,
    description: record.description ?? '',
    password: '',
    role: record.role,
    status: record.status,
    mustChangePassword: record.mustChangePassword,
    imageGenerationEnabled: record.imageGenerationEnabled,
    requestLimitPerMinute: record.requestLimits?.perMinute ?? null,
    requestLimitPerDay: record.requestLimits?.perDay ?? null,
    requestLimitPerWeek: record.requestLimits?.perWeek ?? null,
    requestLimitPerMonth: record.requestLimits?.perMonth ?? null,
    requestLimitExpiresOn: record.requestLimits?.expiresOn ?? null
  })
  editingBaseline.value = cloneSystemAccountEditableValues(systemAccountEditableValues(record.displayName))
  modalOpen.value = true
}

function openResetPassword(record: SystemAccountListItem) {
  if (!canManageSystemAccounts.value) return
  resettingId.value = record.id
  resettingVersion.value = record.editVersion
  resettingAccountRole.value = record.role
  resetPassword.value = ''
  passwordModalOpen.value = true
}

function handleSystemAccountAction(key: string, record: SystemAccountListItem) {
  if (key === 'edit') {
    openEdit(record)
    return
  }
  if (key === 'reset-password') {
    openResetPassword(record)
  }
}

const handleSave = submitAction('system_accounts.save', async () => {
  if (!canManageSystemAccounts.value) {
    message.warning('需要超级管理员权限')
    return
  }
  const username = form.username.trim()
  const displayName = form.displayName.trim()
  if (!username || !displayName) {
    message.warning('请填写用户名和显示名称')
    return
  }
  if (hasWhitespace(form.username) || hasWhitespace(form.displayName)) {
    message.warning('用户名和显示名称不能包含空格')
    return
  }
  if (!editingId.value && form.password.length < 4) {
    message.warning('初始密码至少 4 位')
    return
  }
  if (!editingId.value && hasWhitespace(form.password)) {
    message.warning('初始密码不能包含空格')
    return
  }
  try {
    const editableValues = systemAccountEditableValues(displayName)
    const basePayload: {
      displayName: string
      description: string
      role?: SystemAccountRole
      status: SystemAccountStatus
      mustChangePassword: boolean
      imageGenerationEnabled: boolean
      requestLimits: UserRequestLimits | null
    } = {
      ...editableValues
    }
    if (basePayload.role === 'super_admin') {
      delete basePayload.role
    }
    if (editingId.value) {
      if (!editingBaseline.value || !editingVersion.value) throw new Error('系统账户编辑基线缺失，请重新打开编辑窗口')
      const patch = buildSystemAccountEditablePatch(editingBaseline.value, editableValues)
      if (!hasSystemAccountEditableChanges(patch)) {
        modalOpen.value = false
        return
      }
      const updated = await api.systemAccounts.update(editingId.value, {
        expectedUpdatedAt: editingVersion.value,
        ...patch
      })
      await applySystemAccountMutation(updated)
      message.success('系统账户已更新')
    } else {
      const payload = { ...basePayload, username, password: form.password }
      const createScopeKey = systemAccountListScopeKey()
      const created = await api.systemAccounts.create(payload)
      if (createScopeKey === systemAccountListScopeKey()) {
        await applyCreatedSystemAccount(created)
      }
      message.success('系统账户已创建')
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存系统账户失败'))
  } finally {
  }
})

const handleResetPassword = submitAction('system_accounts.reset_password', async () => {
  if (!canManageSystemAccounts.value) {
    message.warning('需要超级管理员权限')
    return
  }
  if (!resettingId.value || resetPassword.value.length < 4) {
    message.warning('新密码至少 4 位')
    return
  }
  if (hasWhitespace(resetPassword.value)) {
    message.warning('新密码不能包含空格')
    return
  }
  try {
    if (!resettingVersion.value) throw new Error('系统账户编辑版本缺失，请重新打开重置密码窗口')
    const updated = await api.systemAccounts.update(resettingId.value, {
      expectedUpdatedAt: resettingVersion.value,
      password: resetPassword.value,
      mustChangePassword: !isAdminRole(resettingAccountRole.value)
    })
    await applySystemAccountMutation(updated)
    message.success('密码已重置')
    passwordModalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '重置密码失败'))
  } finally {
  }
})

function searchAccounts() {
  resetPagination()
  void loadData()
}

async function applyCreatedSystemAccount(created: SystemAccountListItem): Promise<void> {
  cancelPendingSystemAccountLoads()
  const accumulated = pagination.current > 1 && accounts.value.length > pagination.pageSize
  const reconciliation = reconcileCreatedSystemAccount(accounts.value, created, {
    accumulated,
    hasMore: mobileHasMore.value,
    keyword: keyword.value,
    page: pagination.current,
    pageSize: pagination.pageSize,
    total: pagination.total
  })
  if (reconciliation.requiresReload) {
    await loadData({ quiet: true })
    return
  }
  applySystemAccountPageResult({
    items: reconciliation.items,
    page: pagination.current,
    pageSize: pagination.pageSize,
    total: reconciliation.total,
    hasMore: reconciliation.hasMore,
    currentPageCount: reconciliation.currentPageCount
  })
}

async function applySystemAccountMutation(mutation: Parameters<typeof reconcileSystemAccountMutationPage>[1]): Promise<void> {
  const current = accounts.value.find((item) => item.id === mutation.id)
  const mutationApplies = current !== undefined && mutation.updatedAt >= current.editVersion
  const orderingChanged = current !== undefined && mutation.updatedAt > current.editVersion
  if (mutationApplies) {
    cancelPendingSystemAccountLoads()
  }
  const accumulated = pagination.current > 1 && accounts.value.length > pagination.pageSize
  const reconciliation = reconcileSystemAccountMutationPage(accounts.value, mutation, {
    accumulated,
    hasMore: mobileHasMore.value,
    keyword: keyword.value,
    page: pagination.current,
    pageSize: pagination.pageSize,
    total: pagination.total
  })
  if (!mutationApplies || !orderingChanged) {
    accounts.value = reconciliation.items
    return
  }
  if (reconciliation.requiresReload) {
    await loadData({ quiet: true })
    return
  }
  applySystemAccountPageResult({
    items: reconciliation.items,
    page: pagination.current,
    pageSize: pagination.pageSize,
    total: reconciliation.total,
    hasMore: reconciliation.hasMore,
    currentPageCount: reconciliation.currentPageCount
  })
  if (reconciliation.requiresBackfill) {
    await loadData({ append: true, quiet: true })
  }
}

function cancelPendingSystemAccountLoads(): void {
  const mobileLoadWasPending = mobileLoadingMore.value
  invalidatePendingLoads()
  if (mobileLoadWasPending) {
    pagination.current = Math.max(1, pagination.current - 1)
  }
}

function systemAccountListScopeKey(): string {
  return keyword.value.trim()
}

function resetSearch() {
  keyword.value = ''
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

function refreshAccounts() {
  void loadData()
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
}

function requestLimitsPayload(): UserRequestLimits | null {
  const values = {
    perMinute: normalizedOptionalRequestLimit(form.requestLimitPerMinute, '每分钟请求数'),
    perDay: normalizedOptionalRequestLimit(form.requestLimitPerDay, '每日请求数'),
    perWeek: normalizedOptionalRequestLimit(form.requestLimitPerWeek, '每周请求数'),
    perMonth: normalizedOptionalRequestLimit(form.requestLimitPerMonth, '每月请求数')
  }
  const entries = Object.entries(values).filter((entry): entry is [string, number] => entry[1] !== null)
  if (!entries.length) return null
  const output = Object.fromEntries(entries) as UserRequestLimits
  const expiresOn = normalizedOptionalRequestLimitDate(form.requestLimitExpiresOn)
  if (expiresOn) output.expiresOn = expiresOn
  return output
}

function systemAccountEditableValues(displayName: string): SystemAccountEditableValues {
  return {
    displayName,
    description: form.description.trim(),
    role: form.role,
    status: form.status,
    mustChangePassword: isAdminRole(form.role) ? false : form.mustChangePassword,
    imageGenerationEnabled: form.imageGenerationEnabled,
    requestLimits: requestLimitsPayload()
  }
}

function normalizedOptionalRequestLimit(value: number | null, label: string): number | null {
  if (value === null) return null
  if (!Number.isInteger(value) || value < 0 || value > 1_000_000_000) {
    throw new Error(`${label}必须是 0 到 1000000000 之间的整数`)
  }
  return value
}

function normalizedOptionalRequestLimitDate(value: string | null): string | null {
  if (value === null || value === '') return null
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    throw new Error('覆盖到期日必须是有效的年月日')
  }
  return value
}

function defaultSystemAccountsPageState(): SystemAccountsPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize }
  }
}

function sanitizeSystemAccountsPageState(value: unknown, fallback: SystemAccountsPageState): SystemAccountsPageState {
  const source = value && typeof value === 'object' ? value as Partial<SystemAccountsPageState> : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination)
  }
}

function snapshotPageState(): SystemAccountsPageState {
  return {
    keyword: keyword.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)

watch(() => form.role, (role) => {
  if (isAdminRole(role)) {
    form.mustChangePassword = false
  }
})
</script>

<style scoped>
.system-account-card {
  margin-top: 4px;
}

.request-limit-editor {
  margin-top: 8px;
  padding: 16px;
  background: #f8fafc;
  border: 1px solid #e5eaf1;
  border-radius: 8px;
}

.request-limit-editor-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.request-limit-editor-head span {
  color: #64748b;
  font-size: 12px;
}

.request-limit-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 14px;
}

@media (max-width: 640px) {
  .request-limit-editor-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .request-limit-grid {
    grid-template-columns: 1fr;
  }
}
</style>
