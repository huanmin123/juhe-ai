<template>
  <a-card class="page-card authorizations-page-card responsive-page-card">
    <AuthorizationFilterToolbar
      v-model:keyword="keywordFilter"
      :filters="filters"
      :is-management-view="isManagementView"
      :direction-options="directionOptions"
      :source-options="sourceOptions"
      :status-options="statusOptions"
      :resource-type-options="resourceTypeOptions"
      :resource-options="resourceOptions"
      :resource-disabled="filterResourceDisabled"
      :resource-loading="filterResourceOptionsLoading"
      :resource-placeholder="filterResourcePlaceholder"
      :owner-users="filterOwnerUsers"
      :owner-loading="filterOwnerUsersLoading"
      :teams="teams"
      :team-loading="filterTeamOptionsLoading"
      :users="users"
      :user-loading="filterUserOptionsLoading"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :loading="loading"
      @reset="resetFilters"
      @refresh="refreshData"
      @help="helpOpen = true"
      @create="openCreateModal"
      @owner-change="handleFilterOwnerChange"
      @owner-dropdown="handleFilterOwnerDropdown"
      @owner-search="handleFilterOwnerSearch"
      @resource-type-change="handleResourceTypeChange"
      @resource-dropdown="handleFilterResourceDropdown"
      @resource-search="handleFilterResourceSearch"
      @team-dropdown="handleFilterTeamDropdown"
      @team-search="handleFilterTeamSearch"
      @user-dropdown="handleFilterUserDropdown"
      @user-search="handleFilterUserSearch"
    >
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['resource']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
    </AuthorizationFilterToolbar>

    <AuthorizationList
      :authorizations="authorizations"
      :columns="managedColumns"
      :current-system-account-id="currentSystemAccountId"
      :direction="filters.direction"
      :empty-description="authorizationEmptyDescription"
      :is-management-view="isManagementView"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileAuthorizations"
      @refresh="refreshData"
      @menu-click="handleActionMenuClick"
    />

    <AuthorizationHelpModal v-model:open="helpOpen" />

    <AuthorizationCreateModal
      v-model:open="createModalOpen"
      :form="createForm"
      :excluded-grantee-ids="createExcludedGranteeIds"
      :has-grantee-options="hasCreateGranteeOptions"
      :is-management-view="isManagementView"
      :owner-users="createOwnerUsers"
      :owner-users-loading="createOwnerUsersLoading"
      :resource-loading="createResourceOptionsLoading"
      :resource-options="createResourceOptions"
      :resource-placeholder="createResourcePlaceholder"
      :resource-select-disabled="createResourceSelectDisabled"
      :resource-type-options="createResourceTypeOptions"
      :saving="authorizationCreating"
      :target-group-disabled="createTargetGroupDisabled"
      :target-group-loading="createTargetGroupOptionsLoading"
      :target-group-placeholder="createTargetGroupPlaceholder"
      :target-group-tip="createTargetGroupTip"
      :target-group-visible="createTargetGroupVisible"
      :target-groups="createTargetGroups"
      :disabled-date="disabledAuthorizationExpireDate"
      :teams="createTeams"
      :grantee-loading="createGranteeOptionsLoading"
      :users="createUsers"
      @grantee-dropdown="handleCreateGranteeDropdown"
      @grantee-search="handleCreateGranteeSearch"
      @owner-change="handleCreateOwnerChange"
      @owner-dropdown="handleCreateOwnerDropdown"
      @owner-search="handleCreateOwnerSearch"
      @resource-dropdown="handleCreateResourceDropdown"
      @resource-search="handleCreateResourceSearch"
      @target-group-dropdown="handleCreateTargetGroupDropdown"
      @target-group-search="handleCreateTargetGroupSearch"
      @ok="createAuthorization"
    />

    <AuthorizationExpireModal
      v-model:open="expireModalOpen"
      :form="expireForm"
      :disabled-date="disabledAuthorizationExpireDate"
      @ok="confirmExpireChange"
    />

  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { rememberAccountLabels, rememberAccountSelection } from '@/shared/accountLabelCache'
import { mergeSelectedGroupOptions, rememberGroupLabels } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import { serverDateTimeTimestamp } from '@/shared/formatters'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountOptionSummary, GroupOptionSummary, ResourceAuthorizationSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import AuthorizationCreateModal from './AuthorizationCreateModal.vue'
import AuthorizationExpireModal from './AuthorizationExpireModal.vue'
import AuthorizationFilterToolbar from './AuthorizationFilterToolbar.vue'
import AuthorizationHelpModal from './AuthorizationHelpModal.vue'
import AuthorizationList from './AuthorizationList.vue'
import {
  extractApiErrorMessage,
  formatDateTime,
  hasManualSource,
  parseStrictDatePickerValue
} from './authorizationFormatters'
import {
  authorizationCreatePayload,
  authorizationExpireFormFromSummary,
  authorizationExpirePayload,
  createAuthorizationCreateFormModel,
  createAuthorizationExpireFormModel,
  resetAuthorizationCreateForm,
  type AuthorizationCreateFormModel,
  type AuthorizationExpireFormModel
} from './authorizationFormModel'
import {
  mergeOptionsById,
  normalizeSearchKeyword,
  selectedAccountFromOptions,
  selectedGroupFromOptions,
  selectedTeamFromOptions,
  selectedUserFromOptions
} from './authorizationOptionHelpers'
import {
  authorizationFiltersFromRouteQuery,
  authorizationRouteFilterValues as routeAuthorizationFilterValues,
  createDefaultAuthorizationsPageState,
  hasAuthorizationRouteFilters as hasRouteAuthorizationFilters,
  sanitizeAuthorizationsPageState,
  type AuthorizationFilters,
  type AuthorizationsPageState
} from './authorizationPageState'
import {
  authorizationColumns,
  authorizationDirectionOptions,
  authorizationResourceTypeOptions,
  authorizationSourceOptions,
  authorizationStatusOptions,
  createAuthorizationResourceTypeOptions
} from './authorizationTableColumns'

const pageSize = 50
const { submitAction, submittingRef } = useSubmitAction('authorizations')
const authorizationCreating = submittingRef('authorizations.create')
const createModalOpen = ref(false)
const helpOpen = ref(false)
const expireModalOpen = ref(false)
const route = useRoute()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const accounts = ref<AccountOptionSummary[]>([])
const groups = ref<GroupOptionSummary[]>([])
const createAccounts = ref<AccountOptionSummary[]>([])
const createGroups = ref<GroupOptionSummary[]>([])
const createTargetGroups = ref<GroupOptionSummary[]>([])
const teams = ref<SystemTeamPrincipalSummary[]>([])
const users = ref<SystemAccountPrincipalSummary[]>([])
const createOwnerUsers = ref<SystemAccountPrincipalSummary[]>([])
const createUsers = ref<SystemAccountPrincipalSummary[]>([])
const createTeams = ref<SystemTeamPrincipalSummary[]>([])
const createOwnerUsersLoading = ref(false)
const createResourceOptionsLoading = ref(false)
const createGranteeOptionsLoading = ref(false)
const createTargetGroupOptionsLoading = ref(false)
const filterResourceOptionsLoading = ref(false)
const filterTeamOptionsLoading = ref(false)
const filterUserOptionsLoading = ref(false)
const createOwnerSearchKeyword = ref('')
const createResourceSearchKeyword = ref('')
const createGranteeSearchKeyword = ref('')
const createTargetGroupSearchKeyword = ref('')
const filterResourceSearchKeyword = ref('')
const filterTeamSearchKeyword = ref('')
const filterUserSearchKeyword = ref('')

const expireAuthorization = ref<ResourceAuthorizationSummary>()
const remoteOptionLimit = 50
const remoteSearchDelayMs = 250
const authorizationAccountOptionCache = createShortLivedQueryCache<AccountOptionSummary[]>({ ttlMs: 10_000 })
const authorizationGroupOptionCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
const authorizationUserOptionCache = createShortLivedQueryCache<SystemAccountPrincipalSummary[]>({ ttlMs: 10_000 })
const authorizationTeamOptionCache = createShortLivedQueryCache<SystemTeamPrincipalSummary[]>({ ttlMs: 10_000 })
let createOwnerUserRequestId = 0
let createOwnerUserSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let createOwnerResourceRequestId = 0
let createResourceSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let createGranteeRequestId = 0
let createGranteeSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let createTargetGroupRequestId = 0
let createTargetGroupSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let filterResourceRequestId = 0
let filterResourceSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let filterTeamRequestId = 0
let filterTeamSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let filterUserRequestId = 0
let filterUserSearchTimer: ReturnType<typeof window.setTimeout> | undefined

const defaultAuthorizationsPageState = (): AuthorizationsPageState => createDefaultAuthorizationsPageState(pageSize)
const pageStateCache = usePageStateCache<AuthorizationsPageState>(undefined, defaultAuthorizationsPageState, {
  version: 8,
  sanitize: (value, fallback) => sanitizeAuthorizationsPageState(value, fallback, pageSize)
})
const initialPageState = pageStateCache.read()

const keywordFilter = ref(initialPageState.keywordFilter)
const filters = reactive<AuthorizationFilters>({ ...initialPageState.filters })
const selectedFilterOwnerSystemAccountId = computed(() => {
  return isManagementView.value ? scopedSystemAccountId(filters.resourceOwnerSystemAccountId) : undefined
})
const {
  handleDropdown: handleFilterOwnerDropdown,
  handleSearch: handleFilterOwnerSearch,
  loading: filterOwnerUsersLoading,
  resetSearch: resetFilterOwnerSearch,
  systemAccounts: filterOwnerUsers
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [filters.resourceOwnerSystemAccountId]
})
const {
  items: authorizations,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileAuthorizations,
  removeItems: removeAuthorizationItems,
  resetPagination,
  updateItems: updateAuthorizationItems
} = useResponsivePagedList<ResourceAuthorizationSummary>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条授权，还有更多`
    : `共 ${total} 条授权`,
  fetchPage: async (_options, pageState) => {
    const params = authorizationListParams(pageState)
    return isManagementView.value
      ? await api.authorizations.listPage(params)
      : await api.myAuthorizations.listPage(params)
  },
  onError: (error) => {
    console.error(error)
    message.error('加载授权列表失败')
  }
})

const createForm = reactive<AuthorizationCreateFormModel>(createAuthorizationCreateFormModel())

const expireForm = reactive<AuthorizationExpireFormModel>(createAuthorizationExpireFormModel())

const directionOptions = authorizationDirectionOptions
const sourceOptions = authorizationSourceOptions
const statusOptions = authorizationStatusOptions
const resourceTypeOptions = authorizationResourceTypeOptions
const createResourceTypeOptions = createAuthorizationResourceTypeOptions
const currentSystemAccountId = computed(() => authState.currentUser.value?.id)

const resourceOptions = computed(() => {
  if (filters.resourceType === 'all') {
    return []
  }
  if (filterResourceDisabled.value) {
    return []
  }
  if (filters.resourceType === 'account') {
    return accounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return mergeSelectedGroupOptions(groups.value.map((group) => ({ label: group.name, value: group.id })), [filters.resourceId], [filters.resourceGroup])
})
const filterResourceDisabled = computed(() => {
  return isManagementView.value && filters.resourceType === 'group' && !selectedFilterOwnerSystemAccountId.value
})
const filterResourcePlaceholder = computed(() => {
  if (filterResourceDisabled.value) return '请先选择资源归属用户'
  return filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'
})

const createResourceOptions = computed(() => {
  if (createForm.resourceType === 'account') {
    return createOwnedAccounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return mergeSelectedGroupOptions(createOwnedGroups.value.map((group) => ({ label: group.name, value: group.id })), [createForm.resourceId], [createForm.resourceGroup])
})
const createResourceSelectDisabled = computed(() => {
  if (isManagementView.value && !createForm.ownerSystemAccountId) return true
  return false
})
const createResourcePlaceholder = computed(() => {
  if (isManagementView.value && !createForm.ownerSystemAccountId) return '请先选择授权人'
  if (createForm.resourceType === 'account') return '输入 AI 账户名称搜索'
  return '输入分组名称搜索'
})

const createExcludedGranteeIds = computed(() => createForm.ownerSystemAccountId ? [createForm.ownerSystemAccountId] : [])
const hasCreateGranteeOptions = computed(() => createForm.granteeType === 'system_account'
  ? createUsers.value.some((user) => user.status === 'active' && !createExcludedGranteeIds.value.includes(user.id))
  : createTeams.value.some((team) => team.status === 'active'))
const selectedCreateAccount = computed(() => createForm.resourceType === 'account'
  ? createOwnedAccounts.value.find((account) => account.id === createForm.resourceId)
  : undefined)
const createTargetGroupVisible = computed(() => createForm.resourceType === 'account' && createForm.granteeType === 'system_account')
const createTargetGroupDisabled = computed(() => !createForm.resourceId || !createForm.granteeId || !selectedCreateAccount.value?.providerCode)
const createTargetGroupPlaceholder = computed(() => {
  if (!createForm.resourceId) return '请先选择 AI 账户'
  if (!createForm.granteeId) return '请先选择被授权用户'
  return '选择目标用户分组'
})
const createTargetGroupTip = computed(() => createTargetGroups.value.length
  ? '默认选择目标用户的默认分组；授权创建后会直接把账户加入该分组。'
  : '目标用户暂无可选兼容分组，请先为目标用户准备分组。')
const activeFilterCount = computed(() => {
  let count = 0
  if (keywordFilter.value.trim()) count += 1
  if (!isManagementView.value && filters.direction !== 'outbound') count += 1
  if (!isManagementView.value && filters.sourceType !== 'all') count += 1
  if (filters.status !== 'all') count += 1
  if (isManagementView.value && filters.resourceOwnerSystemAccountId !== allSystemAccountsValue) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (!filterResourceDisabled.value && filters.resourceId) count += 1
  if (isManagementView.value && filters.teamId) count += 1
  if (isManagementView.value && filters.granteeSystemAccountId) count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (!isManagementView.value && filters.sourceType !== 'all') count += 1
  if (filters.status !== 'all') count += 1
  if (isManagementView.value && filters.resourceType !== 'all') count += 1
  if (isManagementView.value && filters.resourceOwnerSystemAccountId !== allSystemAccountsValue) count += 1
  if (!filterResourceDisabled.value && filters.resourceId) count += 1
  if (isManagementView.value && filters.teamId) count += 1
  if (isManagementView.value && filters.granteeSystemAccountId) count += 1
  return count
})
const authorizationEmptyDescription = computed(() => {
  if (isManagementView.value) {
    return activeFilterCount.value > 0 ? '没有符合当前筛选条件的授权记录。' : '暂无授权记录。'
  }
  if (filters.direction === 'inbound') {
    return '暂无授权给我的记录；获得授权后的资源会在对应使用菜单中显示。'
  }
  return activeFilterCount.value > 0 ? '没有符合当前筛选条件的授权记录。' : '暂无我授权出去的记录，可新增授权给其他用户或团队。'
})
const createAuthorizationScopeParams = computed(() => {
  const systemAccountId = createForm.ownerSystemAccountId
  return systemAccountId ? { systemAccountId } : undefined
})
const createOwnedAccounts = computed(() => createAccounts.value.filter((account) => account.permissions?.canAuthorize !== false))
const createOwnedGroups = computed(() => createGroups.value.filter((group) => group.permissions?.canAuthorize !== false))
const rawColumns = computed(() => {
  const showActions = isManagementView.value || filters.direction === 'outbound' || hasReturnableInboundAuthorization.value
  return authorizationColumns.filter((column) => {
    if (isManagementView.value && column.key === 'direction') return false
    if (['usageTotal', 'lastUsedAt', 'limits'].includes(String(column.key))) return false
    if (!showActions && column.key === 'actions') return false
    return true
  })
})
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(() => `authorizations:${isManagementView.value ? 'management' : 'self'}:${filters.direction}`, rawColumns, {
  requiredKeys: ['resource'],
  minVisible: 1
})

function disabledAuthorizationExpireDate(date: Dayjs): boolean {
  return date.isBefore(dayjs().startOf('day'))
}

function validateAuthorizationExpiresAt(expiresAt: Dayjs | undefined, accountExpiresAt?: string): boolean {
  if (!expiresAt) return true
  if (expiresAt.isBefore(dayjs())) {
    message.warning('授权到期时间不能早于当前时间')
    return false
  }
  if (!accountExpiresAt) return true
  let maxExpiresAt: Dayjs
  try {
    const parsed = parseStrictDatePickerValue(accountExpiresAt, '账户到期时间')
    if (!parsed) return true
    maxExpiresAt = parsed
  } catch (error) {
    message.error(extractApiErrorMessage(error, '账户到期时间数据异常，请清理后再配置授权'))
    return false
  }
  if (expiresAt.isAfter(maxExpiresAt)) {
    message.warning(`授权到期时间不能晚于账户到期时间：${formatDateTime(accountExpiresAt)}`)
    return false
  }
  return true
}

watch(() => createForm.granteeType, () => {
  createForm.granteeId = ''
  resetCreateTargetGroupState()
  createGranteeSearchKeyword.value = ''
  clearCreateGranteeSearchTimer()
  void loadCreateGranteeOptions()
})

watch(() => createForm.resourceType, () => {
  createForm.resourceId = ''
  createForm.resourceAccount = undefined
  createForm.resourceGroup = undefined
  resetCreateTargetGroupState()
  createResourceSearchKeyword.value = ''
  clearCreateResourceSearchTimer()
  void loadCreateResourceOptions()
})

watch(
  () => [createForm.resourceType, createForm.granteeType, createForm.granteeId, selectedCreateAccount.value?.providerCode, selectedCreateAccount.value?.id] as const,
  () => {
    resetCreateTargetGroupState()
    void loadCreateTargetGroupOptions()
  }
)

async function loadMetaData() {
  accounts.value = []
  groups.value = []
  teams.value = []
  users.value = []
}

function canManageAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  return isManagementView.value || authorization.permissions?.canEdit === true
}

const hasReturnableInboundAuthorization = computed(() => {
  if (isManagementView.value || filters.direction !== 'inbound') return false
  return authorizations.value.some((authorization) => canReturnAuthorization(authorization))
})

function canReturnAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  if (isManagementView.value || filters.direction !== 'inbound') return false
  if (authorization.granteeType !== 'system_account') return false
  if (!hasManualSource(authorization)) return false
  return authorization.status !== 'revoked' && authorization.status !== 'returned'
}

function authorizationListParams(pageState: { current: number; pageSize: number }) {
  return {
    keyword: keywordFilter.value.trim() || undefined,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' || filterResourceDisabled.value ? undefined : filters.resourceId,
    resourceOwnerSystemAccountId: isManagementView.value ? selectedFilterOwnerSystemAccountId.value : undefined,
    teamId: isManagementView.value ? filters.teamId : undefined,
    granteeSystemAccountId: isManagementView.value ? filters.granteeSystemAccountId : undefined,
    status: filters.status === 'all' ? undefined : filters.status,
    direction: isManagementView.value ? undefined : filters.direction,
    sourceType: !isManagementView.value && filters.sourceType !== 'all' ? filters.sourceType : undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
}

function authorizationOperationScopeParams(item: ResourceAuthorizationSummary) {
  if (!isManagementView.value || !item.resourceOwnerSystemAccountId) return undefined
  return { systemAccountId: item.resourceOwnerSystemAccountId }
}

function refreshData() {
  resetPagination()
  void loadData()
}

function openCreateModal() {
  resetAuthorizationCreateForm(createForm, {
    ownerSystemAccountId: isManagementView.value ? selectedFilterOwnerSystemAccountId.value : currentSystemAccountId.value,
    resourceType: filters.resourceType === 'group' ? 'group' : 'account'
  })
  resetCreateOptionSearchState()
  createModalOpen.value = true
  void loadCreateOwnerOptions()
  void loadCreateResourceOptions()
  void loadCreateGranteeOptions()
  void loadCreateTargetGroupOptions()
}

function handleCreateOwnerChange() {
  createForm.resourceId = ''
  createForm.resourceAccount = undefined
  createForm.resourceGroup = undefined
  resetCreateTargetGroupState()
  createResourceSearchKeyword.value = ''
  createGranteeSearchKeyword.value = ''
  clearCreateResourceSearchTimer()
  clearCreateGranteeSearchTimer()
  if (createForm.granteeType === 'system_account' && createForm.granteeId === createForm.ownerSystemAccountId) {
    createForm.granteeId = ''
  }
  void loadCreateResourceOptions()
  void loadCreateGranteeOptions()
}

function handleCreateOwnerDropdown(open: boolean) {
  if (open) {
    void loadCreateOwnerOptions()
  }
}

function handleCreateOwnerSearch(value: string) {
  scheduleCreateOwnerSearch(value)
}

function handleCreateResourceDropdown(open: boolean) {
  if (open) {
    void loadCreateResourceOptions()
  }
}

function handleCreateResourceSearch(value: string) {
  scheduleCreateResourceSearch(value)
}

function handleCreateGranteeDropdown(open: boolean) {
  if (open) {
    void loadCreateGranteeOptions()
  }
}

function handleCreateGranteeSearch(value: string) {
  scheduleCreateGranteeSearch(value)
}

function handleCreateTargetGroupDropdown(open: boolean) {
  if (open) {
    void loadCreateTargetGroupOptions()
  }
}

function handleCreateTargetGroupSearch(value: string) {
  scheduleCreateTargetGroupSearch(value)
}

function handleFilterResourceSearch(value: string) {
  scheduleFilterResourceSearch(value)
}

function handleFilterResourceDropdown(open: boolean) {
  if (open) {
    void loadFilterResourceOptions()
  }
}

function handleFilterTeamSearch(value: string) {
  scheduleFilterTeamSearch(value)
}

function handleFilterTeamDropdown(open: boolean) {
  if (open) {
    void loadFilterTeamOptions()
  }
}

function handleFilterUserSearch(value: string) {
  scheduleFilterUserSearch(value)
}

function handleFilterUserDropdown(open: boolean) {
  if (open) {
    void loadFilterUserOptions()
  }
}

function resetFilterResource() {
  filters.resourceId = undefined
  filters.resourceAccount = undefined
  filters.resourceGroup = undefined
}

function handleFilterOwnerChange() {
  if (filters.resourceOwnerSystemAccountId === allSystemAccountsValue) {
    filters.resourceOwnerSystemAccount = undefined
  }
  resetFilterOwnerSearch()
  if (filters.resourceType === 'group') {
    resetFilterResource()
  }
  filterResourceSearchKeyword.value = ''
  clearFilterResourceSearchTimer()
  accounts.value = []
  groups.value = []
  void loadFilterResourceOptions()
  refreshData()
}

function handleResourceTypeChange() {
  resetFilterResource()
  filterResourceSearchKeyword.value = ''
  clearFilterResourceSearchTimer()
  void loadFilterResourceOptions()
  refreshData()
}

function resetFilters() {
  keywordFilter.value = ''
  Object.assign(filters, defaultAuthorizationsPageState().filters)
  resetPagination()
  pageStateCache.clear()
  resetFilterOptionSearchState()
  void loadData()
}

async function loadCreateOwnerOptions(keyword?: string): Promise<void> {
  if (!isManagementView.value) {
    createOwnerUsers.value = []
    return
  }
  const search = normalizeSearchKeyword(keyword)
  const requestKey = JSON.stringify(['create-owner', search ?? '', createForm.ownerSystemAccountId ?? ''])
  const cachedUsers = authorizationUserOptionCache.get(requestKey)
  if (cachedUsers) {
    createOwnerUserRequestId += 1
    createOwnerUsersLoading.value = false
    createOwnerUsers.value = cachedUsers
    return
  }
  const requestId = ++createOwnerUserRequestId
  createOwnerUsersLoading.value = true
  try {
    let nextOptions = await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
    nextOptions = await ensureSelectedSystemAccountPrincipal(nextOptions, createForm.ownerSystemAccountId)
    authorizationUserOptionCache.set(requestKey, nextOptions)
    if (requestId !== createOwnerUserRequestId) return
    createOwnerUsers.value = nextOptions
  } catch (error) {
    if (requestId !== createOwnerUserRequestId) return
    console.error(error)
    message.error('加载授权人列表失败')
  } finally {
    if (requestId === createOwnerUserRequestId) {
      createOwnerUsersLoading.value = false
    }
  }
}

async function loadCreateResourceOptions(keyword?: string): Promise<void> {
  const ownerSystemAccountId = createForm.ownerSystemAccountId
  if (isManagementView.value && !ownerSystemAccountId) {
    createAccounts.value = []
    createGroups.value = []
    return
  }
  const search = normalizeSearchKeyword(keyword)
  const requestKey = JSON.stringify(['create-resource', createForm.resourceType, isManagementView.value ? 'management' : 'self', ownerSystemAccountId ?? '', search ?? '', createForm.resourceId ?? ''])
  if (createForm.resourceType === 'account') {
    const cachedAccounts = authorizationAccountOptionCache.get(requestKey)
    if (cachedAccounts) {
      createOwnerResourceRequestId += 1
      createResourceOptionsLoading.value = false
      rememberAccountLabels(cachedAccounts)
      syncCreateResourceAccount(cachedAccounts)
      createAccounts.value = cachedAccounts
      createGroups.value = []
      return
    }
  } else {
    const cachedGroups = authorizationGroupOptionCache.get(requestKey)
    if (cachedGroups) {
      createOwnerResourceRequestId += 1
      createResourceOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncCreateResourceGroup(cachedGroups)
      createGroups.value = cachedGroups
      createAccounts.value = []
      return
    }
  }
  const requestId = ++createOwnerResourceRequestId
  createResourceOptionsLoading.value = true
  try {
    if (createForm.resourceType === 'account') {
      let nextAccounts = isManagementView.value
        ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
        : await api.myAccounts.options({ keyword: search, limit: remoteOptionLimit })
      nextAccounts = await ensureSelectedAccountOption(nextAccounts, createForm.resourceId, ownerSystemAccountId)
      rememberAccountLabels(nextAccounts)
      syncCreateResourceAccount(nextAccounts)
      authorizationAccountOptionCache.set(requestKey, nextAccounts)
      if (requestId !== createOwnerResourceRequestId) return
      createAccounts.value = nextAccounts
      createGroups.value = []
    } else {
      let nextGroups = isManagementView.value
        ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
        : await api.myGroups.options({ keyword: search, limit: remoteOptionLimit })
      nextGroups = await ensureSelectedGroupOption(nextGroups, createForm.resourceId, ownerSystemAccountId)
      rememberGroupLabels(nextGroups)
      syncCreateResourceGroup(nextGroups)
      authorizationGroupOptionCache.set(requestKey, nextGroups)
      if (requestId !== createOwnerResourceRequestId) return
      createGroups.value = nextGroups
      createAccounts.value = []
    }
  } catch (error) {
    if (requestId !== createOwnerResourceRequestId) return
    console.error(error)
    message.error('加载授权资源失败')
  } finally {
    if (requestId === createOwnerResourceRequestId) {
      createResourceOptionsLoading.value = false
    }
  }
}

async function loadCreateGranteeOptions(keyword?: string): Promise<void> {
  const search = normalizeSearchKeyword(keyword)
  const excludedGranteeIds = [...createExcludedGranteeIds.value].sort()
  const requestKey = JSON.stringify(['create-grantee', createForm.granteeType, isManagementView.value ? 'management' : 'self', search ?? '', createForm.granteeId ?? '', excludedGranteeIds])
  if (createForm.granteeType === 'system_account') {
    const cachedUsers = authorizationUserOptionCache.get(requestKey)
    if (cachedUsers) {
      createGranteeRequestId += 1
      createGranteeOptionsLoading.value = false
      createUsers.value = cachedUsers
      createTeams.value = []
      return
    }
  } else {
    const cachedTeams = authorizationTeamOptionCache.get(requestKey)
    if (cachedTeams) {
      createGranteeRequestId += 1
      createGranteeOptionsLoading.value = false
      createTeams.value = cachedTeams
      createUsers.value = []
      return
    }
  }
  const requestId = ++createGranteeRequestId
  createGranteeOptionsLoading.value = true
  try {
    if (createForm.granteeType === 'system_account') {
      let nextUsers = isManagementView.value
        ? await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
        : await api.myAuthorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
      nextUsers = nextUsers.filter((user) => !createExcludedGranteeIds.value.includes(user.id))
      nextUsers = await ensureSelectedSystemAccountPrincipal(nextUsers, createForm.granteeId)
      authorizationUserOptionCache.set(requestKey, nextUsers)
      if (requestId !== createGranteeRequestId) return
      createUsers.value = nextUsers
      createTeams.value = []
    } else {
      let nextTeams = isManagementView.value
        ? await api.authorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
        : await api.myAuthorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
      nextTeams = await ensureSelectedTeamOption(nextTeams, createForm.granteeId)
      authorizationTeamOptionCache.set(requestKey, nextTeams)
      if (requestId !== createGranteeRequestId) return
      createTeams.value = nextTeams
      createUsers.value = []
    }
  } catch (error) {
    if (requestId !== createGranteeRequestId) return
    console.error(error)
    message.error('加载授权对象失败')
  } finally {
    if (requestId === createGranteeRequestId) {
      createGranteeOptionsLoading.value = false
    }
  }
}

async function loadCreateTargetGroupOptions(keyword?: string): Promise<void> {
  const granteeSystemAccountId = createForm.granteeType === 'system_account' ? createForm.granteeId : ''
  const providerCode = selectedCreateAccount.value?.providerCode
  if (!createTargetGroupVisible.value || !granteeSystemAccountId || !providerCode) {
    createTargetGroupRequestId += 1
    createTargetGroupOptionsLoading.value = false
    createTargetGroups.value = []
    return
  }
  const search = normalizeSearchKeyword(keyword)
  const requestKey = JSON.stringify(['create-target-group', isManagementView.value ? 'management' : 'self', granteeSystemAccountId, providerCode, search ?? '', createForm.targetGroupId ?? ''])
  const cachedGroups = authorizationGroupOptionCache.get(requestKey)
  if (cachedGroups) {
    createTargetGroupRequestId += 1
    createTargetGroupOptionsLoading.value = false
    rememberGroupLabels(cachedGroups)
    syncCreateTargetGroup(cachedGroups)
    selectDefaultCreateTargetGroup(cachedGroups)
    createTargetGroups.value = cachedGroups
    return
  }
  const requestId = ++createTargetGroupRequestId
  createTargetGroupOptionsLoading.value = true
  try {
    let nextGroups = isManagementView.value
      ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
      : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
    nextGroups = await ensureSelectedAuthorizationGranteeGroupOption(nextGroups, createForm.targetGroupId, granteeSystemAccountId, providerCode)
    if (requestId !== createTargetGroupRequestId) return
    rememberGroupLabels(nextGroups)
    syncCreateTargetGroup(nextGroups)
    selectDefaultCreateTargetGroup(nextGroups)
    authorizationGroupOptionCache.set(requestKey, nextGroups)
    createTargetGroups.value = nextGroups
  } catch (error) {
    if (requestId !== createTargetGroupRequestId) return
    console.error(error)
    message.error('加载目标分组失败')
  } finally {
    if (requestId === createTargetGroupRequestId) {
      createTargetGroupOptionsLoading.value = false
    }
  }
}

async function loadFilterResourceOptions(keyword?: string): Promise<void> {
  if (!isManagementView.value) {
    accounts.value = []
    groups.value = []
    return
  }
  if (filters.resourceType === 'all') {
    accounts.value = []
    groups.value = []
    return
  }
  if (filterResourceDisabled.value) {
    filterResourceRequestId += 1
    filterResourceOptionsLoading.value = false
    accounts.value = []
    groups.value = []
    resetFilterResource()
    return
  }
  const requestKeyword = normalizeSearchKeyword(keyword)
  const systemAccountId = selectedFilterOwnerSystemAccountId.value
  const requestKey = JSON.stringify(['filter-resource', filters.resourceType, systemAccountId ?? '', requestKeyword ?? '', filters.resourceId ?? ''])
  if (filters.resourceType === 'account') {
    const cachedAccounts = authorizationAccountOptionCache.get(requestKey)
    if (cachedAccounts) {
      filterResourceRequestId += 1
      filterResourceOptionsLoading.value = false
      rememberAccountLabels(cachedAccounts)
      syncFilterResourceAccount(cachedAccounts)
      accounts.value = cachedAccounts
      groups.value = []
      return
    }
  } else {
    const cachedGroups = authorizationGroupOptionCache.get(requestKey)
    if (cachedGroups) {
      filterResourceRequestId += 1
      filterResourceOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncFilterResourceGroup(cachedGroups)
      groups.value = cachedGroups
      accounts.value = []
      return
    }
  }
  const requestId = ++filterResourceRequestId
  filterResourceOptionsLoading.value = true
  try {
    if (filters.resourceType === 'account') {
      const nextAccounts = await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
      const mergedAccounts = await ensureSelectedAccountOption(nextAccounts, filters.resourceId, systemAccountId)
      rememberAccountLabels(mergedAccounts)
      syncFilterResourceAccount(mergedAccounts)
      authorizationAccountOptionCache.set(requestKey, mergedAccounts)
      if (requestId !== filterResourceRequestId) return
      accounts.value = mergedAccounts
      groups.value = []
      return
    }
    const nextGroups = await api.groups.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
    const mergedGroups = await ensureSelectedGroupOption(nextGroups, filters.resourceId, systemAccountId)
    rememberGroupLabels(mergedGroups)
    syncFilterResourceGroup(mergedGroups)
    authorizationGroupOptionCache.set(requestKey, mergedGroups)
    if (requestId !== filterResourceRequestId) return
    groups.value = mergedGroups
    accounts.value = []
  } catch (error) {
    if (requestId !== filterResourceRequestId) return
    console.error(error)
    message.error(filters.resourceType === 'account' ? '加载可授权账户失败' : '加载可授权分组失败')
  } finally {
    if (requestId === filterResourceRequestId) {
      filterResourceOptionsLoading.value = false
    }
  }
}

async function loadFilterTeamOptions(keyword?: string): Promise<void> {
  if (!isManagementView.value) {
    teams.value = []
    return
  }
  const requestKeyword = normalizeSearchKeyword(keyword)
  const requestKey = JSON.stringify(['filter-team', requestKeyword ?? '', filters.teamId ?? ''])
  const cachedTeams = authorizationTeamOptionCache.get(requestKey)
  if (cachedTeams) {
    filterTeamRequestId += 1
    filterTeamOptionsLoading.value = false
    syncFilterTeamSelection(cachedTeams)
    teams.value = cachedTeams
    return
  }
  const requestId = ++filterTeamRequestId
  filterTeamOptionsLoading.value = true
  try {
    const nextTeams = await api.authorizationOptions.granteeTeams({ keyword: requestKeyword, limit: remoteOptionLimit })
    const mergedTeams = await ensureSelectedTeamOption(nextTeams, filters.teamId)
    authorizationTeamOptionCache.set(requestKey, mergedTeams)
    if (requestId !== filterTeamRequestId) return
    syncFilterTeamSelection(mergedTeams)
    teams.value = mergedTeams
  } catch (error) {
    if (requestId !== filterTeamRequestId) return
    console.error(error)
    message.error('加载团队列表失败')
  } finally {
    if (requestId === filterTeamRequestId) {
      filterTeamOptionsLoading.value = false
    }
  }
}

async function loadFilterUserOptions(keyword?: string): Promise<void> {
  if (!isManagementView.value) {
    users.value = []
    return
  }
  const requestKeyword = normalizeSearchKeyword(keyword)
  const requestKey = JSON.stringify(['filter-user', requestKeyword ?? '', filters.granteeSystemAccountId ?? ''])
  const cachedUsers = authorizationUserOptionCache.get(requestKey)
  if (cachedUsers) {
    filterUserRequestId += 1
    filterUserOptionsLoading.value = false
    syncFilterUserSelection(cachedUsers)
    users.value = cachedUsers
    return
  }
  const requestId = ++filterUserRequestId
  filterUserOptionsLoading.value = true
  try {
    const nextUsers = await api.authorizationOptions.granteeAccounts({ keyword: requestKeyword, limit: remoteOptionLimit })
    const mergedUsers = await ensureSelectedSystemAccountPrincipal(nextUsers, filters.granteeSystemAccountId)
    authorizationUserOptionCache.set(requestKey, mergedUsers)
    if (requestId !== filterUserRequestId) return
    syncFilterUserSelection(mergedUsers)
    users.value = mergedUsers
  } catch (error) {
    if (requestId !== filterUserRequestId) return
    console.error(error)
    message.error('加载系统账户列表失败')
  } finally {
    if (requestId === filterUserRequestId) {
      filterUserOptionsLoading.value = false
    }
  }
}

function scheduleCreateOwnerSearch(value: string) {
  createOwnerSearchKeyword.value = value
  clearCreateOwnerSearchTimer()
  createOwnerUserSearchTimer = window.setTimeout(() => {
    createOwnerUserSearchTimer = undefined
    void loadCreateOwnerOptions(createOwnerSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleCreateResourceSearch(value: string) {
  createResourceSearchKeyword.value = value
  clearCreateResourceSearchTimer()
  createResourceSearchTimer = window.setTimeout(() => {
    createResourceSearchTimer = undefined
    void loadCreateResourceOptions(createResourceSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleCreateGranteeSearch(value: string) {
  createGranteeSearchKeyword.value = value
  clearCreateGranteeSearchTimer()
  createGranteeSearchTimer = window.setTimeout(() => {
    createGranteeSearchTimer = undefined
    void loadCreateGranteeOptions(createGranteeSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleCreateTargetGroupSearch(value: string) {
  createTargetGroupSearchKeyword.value = value
  clearCreateTargetGroupSearchTimer()
  createTargetGroupSearchTimer = window.setTimeout(() => {
    createTargetGroupSearchTimer = undefined
    void loadCreateTargetGroupOptions(createTargetGroupSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleFilterResourceSearch(value: string) {
  filterResourceSearchKeyword.value = value
  clearFilterResourceSearchTimer()
  filterResourceSearchTimer = window.setTimeout(() => {
    filterResourceSearchTimer = undefined
    void loadFilterResourceOptions(filterResourceSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleFilterTeamSearch(value: string) {
  filterTeamSearchKeyword.value = value
  clearFilterTeamSearchTimer()
  filterTeamSearchTimer = window.setTimeout(() => {
    filterTeamSearchTimer = undefined
    void loadFilterTeamOptions(filterTeamSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function scheduleFilterUserSearch(value: string) {
  filterUserSearchKeyword.value = value
  clearFilterUserSearchTimer()
  filterUserSearchTimer = window.setTimeout(() => {
    filterUserSearchTimer = undefined
    void loadFilterUserOptions(filterUserSearchKeyword.value)
  }, remoteSearchDelayMs)
}

function clearCreateOwnerSearchTimer() {
  if (createOwnerUserSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(createOwnerUserSearchTimer)
    createOwnerUserSearchTimer = undefined
  }
}

function clearCreateResourceSearchTimer() {
  if (createResourceSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(createResourceSearchTimer)
    createResourceSearchTimer = undefined
  }
}

function clearCreateGranteeSearchTimer() {
  if (createGranteeSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(createGranteeSearchTimer)
    createGranteeSearchTimer = undefined
  }
}

function clearCreateTargetGroupSearchTimer() {
  if (createTargetGroupSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(createTargetGroupSearchTimer)
    createTargetGroupSearchTimer = undefined
  }
}

function clearFilterResourceSearchTimer() {
  if (filterResourceSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(filterResourceSearchTimer)
    filterResourceSearchTimer = undefined
  }
}

function clearFilterTeamSearchTimer() {
  if (filterTeamSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(filterTeamSearchTimer)
    filterTeamSearchTimer = undefined
  }
}

function clearFilterUserSearchTimer() {
  if (filterUserSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(filterUserSearchTimer)
    filterUserSearchTimer = undefined
  }
}

function resetCreateOptionSearchState() {
  createOwnerSearchKeyword.value = ''
  createResourceSearchKeyword.value = ''
  createGranteeSearchKeyword.value = ''
  createTargetGroupSearchKeyword.value = ''
  clearCreateOwnerSearchTimer()
  clearCreateResourceSearchTimer()
  clearCreateGranteeSearchTimer()
  clearCreateTargetGroupSearchTimer()
  resetCreateTargetGroupState()
}

function resetCreateTargetGroupState() {
  createForm.targetGroupId = ''
  createForm.targetGroup = undefined
  createTargetGroups.value = []
  createTargetGroupSearchKeyword.value = ''
  clearCreateTargetGroupSearchTimer()
}

function syncCreateResourceGroup(nextGroups = createGroups.value): void {
  if (createForm.resourceType !== 'group') {
    createForm.resourceGroup = undefined
    return
  }
  createForm.resourceAccount = undefined
  createForm.resourceGroup = selectedGroupFromOptions(createForm.resourceId, nextGroups, createForm.resourceGroup)
}

function syncFilterResourceGroup(nextGroups = groups.value): void {
  if (filters.resourceType !== 'group') {
    filters.resourceGroup = undefined
    return
  }
  filters.resourceAccount = undefined
  filters.resourceGroup = selectedGroupFromOptions(filters.resourceId, nextGroups, filters.resourceGroup)
}

function syncCreateResourceAccount(nextAccounts = createAccounts.value): void {
  if (createForm.resourceType !== 'account') {
    createForm.resourceAccount = undefined
    return
  }
  createForm.resourceGroup = undefined
  createForm.resourceAccount = selectedAccountFromOptions(createForm.resourceId, nextAccounts, createForm.resourceAccount)
  rememberAccountSelection(createForm.resourceAccount)
}

function syncCreateTargetGroup(nextGroups = createTargetGroups.value): void {
  if (!createTargetGroupVisible.value) {
    createForm.targetGroup = undefined
    return
  }
  createForm.targetGroup = selectedGroupFromOptions(createForm.targetGroupId, nextGroups, createForm.targetGroup)
}

function selectDefaultCreateTargetGroup(nextGroups = createTargetGroups.value): void {
  if (createForm.targetGroupId || createTargetGroupSearchKeyword.value.trim()) return
  const defaultGroup = nextGroups.find((group) => group.enabled && group.isDefault)
  if (!defaultGroup) return
  createForm.targetGroupId = defaultGroup.id
  createForm.targetGroup = { id: defaultGroup.id, name: defaultGroup.name }
}

function syncFilterResourceAccount(nextAccounts = accounts.value): void {
  if (filters.resourceType !== 'account') {
    filters.resourceAccount = undefined
    return
  }
  filters.resourceGroup = undefined
  filters.resourceAccount = selectedAccountFromOptions(filters.resourceId, nextAccounts, filters.resourceAccount)
  rememberAccountSelection(filters.resourceAccount)
}

function syncFilterTeamSelection(nextTeams = teams.value): void {
  filters.team = selectedTeamFromOptions(filters.teamId, nextTeams, filters.team)
}

function syncFilterUserSelection(nextUsers = users.value): void {
  filters.granteeSystemAccount = selectedUserFromOptions(filters.granteeSystemAccountId, nextUsers, filters.granteeSystemAccount)
}

function resetFilterOptionSearchState() {
  resetFilterOwnerSearch()
  filterResourceSearchKeyword.value = ''
  filterTeamSearchKeyword.value = ''
  filterUserSearchKeyword.value = ''
  clearFilterResourceSearchTimer()
  clearFilterTeamSearchTimer()
  clearFilterUserSearchTimer()
}

async function ensureSelectedAccountOption(options: AccountOptionSummary[], selectedId?: string, systemAccountId?: string): Promise<AccountOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView.value
      ? await api.accounts.options({ systemAccountId, ids: [id], limit: 1 })
      : await api.myAccounts.options({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

async function ensureSelectedGroupOption(options: GroupOptionSummary[], selectedId?: string, systemAccountId?: string): Promise<GroupOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView.value
      ? await api.groups.options({ systemAccountId, ids: [id], limit: 1 })
      : await api.myGroups.options({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

async function ensureSelectedAuthorizationGranteeGroupOption(options: GroupOptionSummary[], selectedId: string | undefined, granteeSystemAccountId: string, providerCode: string): Promise<GroupOptionSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView.value
      ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
      : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

async function ensureSelectedSystemAccountPrincipal(options: SystemAccountPrincipalSummary[], selectedId?: string): Promise<SystemAccountPrincipalSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView.value
      ? await api.authorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
      : await api.myAuthorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

async function ensureSelectedTeamOption(options: SystemTeamPrincipalSummary[], selectedId?: string): Promise<SystemTeamPrincipalSummary[]> {
  const id = selectedId?.trim()
  if (!id || options.some((item) => item.id === id)) return options
  try {
    const selected = isManagementView.value
      ? await api.authorizationOptions.granteeTeams({ ids: [id], limit: 1 })
      : await api.myAuthorizationOptions.granteeTeams({ ids: [id], limit: 1 })
    return mergeOptionsById(selected, options)
  } catch {
    return options
  }
}

const createAuthorization = submitAction('authorizations.create', async () => {
  if (isManagementView.value && !createForm.ownerSystemAccountId) {
    message.warning('请先选择授权人')
    return
  }
  if (!createForm.resourceId) {
    message.warning(createForm.resourceType === 'account' ? '请选择要授权的 AI 账户' : '请选择要授权的分组')
    return
  }
  if (!createForm.granteeId) {
    message.warning(createForm.granteeType === 'system_account' ? '请选择被授权用户' : '请选择团队')
    return
  }
  if (createForm.granteeType === 'system_account' && !createUsers.value.some((user) => user.id === createForm.granteeId && user.status === 'active')) {
    message.warning('请选择启用中的系统账户')
    return
  }
  if (createForm.granteeType === 'system_account' && createExcludedGranteeIds.value.includes(createForm.granteeId)) {
    message.warning('不能授权给资源所有者自己')
    return
  }
  if (createForm.granteeType === 'team' && !createTeams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
    message.warning('请选择启用中的团队')
    return
  }
  const selectedResource = createForm.resourceType === 'account'
    ? selectedCreateAccount.value
    : createOwnedGroups.value.find((group) => group.id === createForm.resourceId)
  if (!selectedResource) {
    message.warning('只能授权自己拥有的资源')
    return
  }
  if (!validateAuthorizationExpiresAt(createForm.expiresAt, selectedCreateAccount.value?.accountExpiresAt)) {
    return
  }
  if (createTargetGroupVisible.value && !createForm.targetGroupId) {
    message.warning('请选择目标分组')
    return
  }
  try {
    const payload = authorizationCreatePayload(createForm, createTargetGroupVisible.value)
    if (isManagementView.value) {
      await api.authorizations.create(payload, createAuthorizationScopeParams.value)
    } else {
      await api.myAuthorizations.create(payload)
    }
    createModalOpen.value = false
    message.success(createForm.granteeType === 'team' ? '团队授权已创建' : '授权已创建')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建授权失败'))
  }
})

async function revokeManualSource(item: ResourceAuthorizationSummary) {
  try {
    let updated: ResourceAuthorizationSummary
    if (isManagementView.value) {
      updated = await api.authorizations.revoke(item.id, authorizationOperationScopeParams(item))
    } else {
      updated = await api.myAuthorizations.revoke(item.id)
    }
    updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
    message.success('个人授权来源已回收')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '回收个人授权失败'))
  }
}

async function revokeTeamSource(item: ResourceAuthorizationSummary) {
  try {
    let updated: ResourceAuthorizationSummary
    if (isManagementView.value) {
      updated = await api.authorizations.revoke(item.id, authorizationOperationScopeParams(item))
    } else {
      updated = await api.myAuthorizations.revoke(item.id)
    }
    updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
    message.success('团队授权来源已回收')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '回收团队授权失败'))
  }
}

async function revokeAuthorization(item: ResourceAuthorizationSummary) {
  try {
    let updated: ResourceAuthorizationSummary
    if (isManagementView.value) {
      updated = await api.authorizations.revoke(item.id, authorizationOperationScopeParams(item))
    } else {
      updated = await api.myAuthorizations.revoke(item.id)
    }
    updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
    message.success(item.granteeType === 'team' ? '团队授权已回收' : '授权已回收')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, item.granteeType === 'team' ? '回收团队授权失败' : '回收授权失败'))
  }
}

async function returnAuthorization(item: ResourceAuthorizationSummary) {
  try {
    await api.myAuthorizations.returnAuthorization(item.id)
    removeAuthorizationItems((authorization) => authorization.id === item.id)
    message.success('授权已归还')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '归还授权失败'))
  }
}

function handleActionMenuClick(event: { key: string | number }, item: ResourceAuthorizationSummary) {
  const key = String(event.key)
  if (key === 'return') {
    void returnAuthorization(item)
    return
  }
  if (key === 'edit-expire') {
    openExpireModal(item)
    return
  }
  if (key === 'pause') {
    void updateAuthorizationStatus(item, 'paused')
    return
  }
  if (key === 'resume') {
    void updateAuthorizationStatus(item, 'active')
    return
  }
  if (key === 'revoke-authorization') {
    void revokeAuthorization(item)
    return
  }
  if (key === 'revoke-manual') {
    void revokeManualSource(item)
    return
  }
  if (key === 'revoke-team-grant') {
    void revokeAuthorization(item)
    return
  }
  if (key.startsWith('team:')) {
    const sourceTeamId = key.slice('team:'.length)
    if (sourceTeamId) {
      void revokeTeamSource(item)
    }
  }
}

async function updateAuthorizationStatus(item: ResourceAuthorizationSummary, status: 'active' | 'paused') {
  try {
    const payload: { status: 'active' | 'paused'; expiresAt?: string | null } = { status }
    if (status === 'active' && item.expiresAt) {
      const expiresAtTimestamp = serverDateTimeTimestamp(item.expiresAt)
      if (expiresAtTimestamp === undefined || expiresAtTimestamp <= Date.now()) {
        payload.expiresAt = null
      }
    }
    if (isManagementView.value) {
      const updated = await api.authorizations.update(item.id, payload, authorizationOperationScopeParams(item))
      updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
    } else {
      const updated = await api.myAuthorizations.update(item.id, payload)
      updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
    }
    message.success(status === 'active' ? '授权已恢复' : '授权已暂停')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, status === 'active' ? '恢复授权失败' : '暂停授权失败'))
  }
}

function openExpireModal(item: ResourceAuthorizationSummary) {
  let nextForm: AuthorizationExpireFormModel
  try {
    nextForm = authorizationExpireFormFromSummary(item)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '授权数据结构异常，请清理后再编辑'))
    return
  }
  expireAuthorization.value = item
  Object.assign(expireForm, nextForm)
  expireModalOpen.value = true
}

async function confirmExpireChange() {
  const authorization = expireAuthorization.value
  if (!authorization) {
    expireModalOpen.value = false
    return
  }
  if (!validateAuthorizationExpiresAt(expireForm.expiresAt, authorization.resourceAccountExpiresAt)) {
    return
  }
  try {
    const payload = authorizationExpirePayload(expireForm)
    if (isManagementView.value) {
      const updated = await api.authorizations.updateExpire(authorization.id, payload, authorizationOperationScopeParams(authorization))
      updateAuthorizationItems((item) => item.id === authorization.id, () => updated)
    } else {
      const updated = await api.myAuthorizations.updateExpire(authorization.id, payload)
      updateAuthorizationItems((item) => item.id === authorization.id, () => updated)
    }
    expireModalOpen.value = false
    expireAuthorization.value = undefined
    message.success('授权配置已更新')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改授权配置失败'))
  }
}

onMounted(async () => {
  applyRouteFilters()
  await Promise.all([loadMetaData(), loadData()])
})

onBeforeUnmount(() => {
  resetCreateOptionSearchState()
  resetFilterOptionSearchState()
})

function applyRouteFilters() {
  if (!hasAuthorizationRouteFilters()) return
  const routeFilters = authorizationFiltersFromRouteQuery(route.query)
  filters.resourceType = 'all'
  filters.status = 'all'
  filters.resourceOwnerSystemAccountId = allSystemAccountsValue
  filters.resourceOwnerSystemAccount = undefined
  filters.resourceId = undefined
  filters.resourceAccount = undefined
  filters.resourceGroup = undefined
  filters.teamId = undefined
  filters.team = undefined
  filters.granteeSystemAccountId = undefined
  filters.granteeSystemAccount = undefined
  if (routeFilters.resourceType) {
    filters.resourceType = routeFilters.resourceType
    createForm.resourceType = routeFilters.resourceType
  }
  if (routeFilters.resourceId) {
    filters.resourceId = routeFilters.resourceId
  }
  filters.status = routeFilters.status
  if (routeFilters.resourceOwnerSystemAccountId) {
    filters.resourceOwnerSystemAccountId = routeFilters.resourceOwnerSystemAccountId
  }
  if (routeFilters.teamId) {
    filters.teamId = routeFilters.teamId
  }
  if (routeFilters.granteeSystemAccountId) {
    filters.granteeSystemAccountId = routeFilters.granteeSystemAccountId
  }
}

function authorizationRouteFilterValues() {
  return routeAuthorizationFilterValues(route.query)
}

function hasAuthorizationRouteFilters(): boolean {
  return hasRouteAuthorizationFilters(route.query)
}

function applyAuthorizationsPageState(state: AuthorizationsPageState): void {
  const fallback = defaultAuthorizationsPageState()
  keywordFilter.value = state.keywordFilter ?? fallback.keywordFilter
  Object.assign(filters, {
    ...fallback.filters,
    ...state.filters
  })
  pagination.current = state.pagination?.current ?? fallback.pagination?.current ?? 1
  pagination.pageSize = state.pagination?.pageSize ?? fallback.pagination?.pageSize ?? pageSize
  resetFilterOptionSearchState()
}

function restorePageStateAfterRouteFiltersCleared(): void {
  applyAuthorizationsPageState(pageStateCache.read())
  void Promise.all([loadMetaData(), loadData()])
}

function snapshotPageState(): AuthorizationsPageState {
  return {
    filters: { ...filters },
    keywordFilter: keywordFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => {
  if (hasAuthorizationRouteFilters()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(authorizationRouteFilterValues, () => {
  if (!hasAuthorizationRouteFilters()) {
    restorePageStateAfterRouteFiltersCleared()
    return
  }
  applyRouteFilters()
  resetPagination()
  resetFilterOptionSearchState()
  void Promise.all([loadMetaData(), loadData()])
})
watch(filterResourceDisabled, (disabled) => {
  if (!disabled) return
  resetFilterResource()
  accounts.value = []
  groups.value = []
}, { immediate: true })
watch(() => filters.resourceAccount, (selection) => rememberAccountSelection(selection), { deep: true, immediate: true })
watch(() => filters.resourceOwnerSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(() => filters.team, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(() => filters.granteeSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

</script>

<style scoped>
.authorizations-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

</style>
