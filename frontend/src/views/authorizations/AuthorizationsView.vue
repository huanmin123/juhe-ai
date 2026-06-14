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
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { rememberAccountSelection } from '@/shared/accountLabelCache'
import { mergeSelectedGroupOptions } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import type { ResourceAuthorizationSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import AuthorizationCreateModal from './AuthorizationCreateModal.vue'
import AuthorizationExpireModal from './AuthorizationExpireModal.vue'
import AuthorizationFilterToolbar from './AuthorizationFilterToolbar.vue'
import AuthorizationHelpModal from './AuthorizationHelpModal.vue'
import AuthorizationList from './AuthorizationList.vue'
import { hasManualSource } from './authorizationFormatters'
import {
  createAuthorizationCreateFormModel,
  createAuthorizationExpireFormModel,
  resetAuthorizationCreateForm,
  type AuthorizationCreateFormModel,
  type AuthorizationExpireFormModel
} from './authorizationFormModel'
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
import { useAuthorizationActions } from './useAuthorizationActions'
import { useAuthorizationOptionState } from './useAuthorizationOptionState'

const pageSize = 50
const createModalOpen = ref(false)
const helpOpen = ref(false)
const expireModalOpen = ref(false)
const route = useRoute()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const expireAuthorization = ref<ResourceAuthorizationSummary>()

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
const createExcludedGranteeIds = computed(() => createForm.ownerSystemAccountId ? [createForm.ownerSystemAccountId] : [])
const {
  accounts,
  groups,
  createTargetGroups,
  teams,
  users,
  createOwnerUsers,
  createUsers,
  createTeams,
  createOwnerUsersLoading,
  createResourceOptionsLoading,
  createGranteeOptionsLoading,
  createTargetGroupOptionsLoading,
  filterResourceOptionsLoading,
  filterTeamOptionsLoading,
  filterUserOptionsLoading,
  createResourceSearchKeyword,
  createGranteeSearchKeyword,
  filterResourceSearchKeyword,
  filterResourceDisabled,
  createOwnedAccounts,
  createOwnedGroups,
  selectedCreateAccount,
  createTargetGroupVisible,
  loadCreateOwnerOptions,
  loadCreateResourceOptions,
  loadCreateGranteeOptions,
  loadCreateTargetGroupOptions,
  loadFilterResourceOptions,
  loadFilterTeamOptions,
  loadFilterUserOptions,
  scheduleCreateOwnerSearch,
  scheduleCreateResourceSearch,
  scheduleCreateGranteeSearch,
  scheduleCreateTargetGroupSearch,
  scheduleFilterResourceSearch,
  scheduleFilterTeamSearch,
  scheduleFilterUserSearch,
  clearCreateResourceSearchTimer,
  clearCreateGranteeSearchTimer,
  clearFilterResourceSearchTimer,
  resetCreateOptionSearchState,
  resetCreateTargetGroupState,
  resetFilterResource,
  resetFilterResourceOptions,
  resetFilterOptionLists,
  resetFilterOptionSearchState: resetRemoteFilterOptionSearchState
} = useAuthorizationOptionState({
  createExcludedGranteeIds,
  createForm,
  filters,
  isManagementView,
  selectedFilterOwnerSystemAccountId
})

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

const hasCreateGranteeOptions = computed(() => createForm.granteeType === 'system_account'
  ? createUsers.value.some((user) => user.status === 'active' && !createExcludedGranteeIds.value.includes(user.id))
  : createTeams.value.some((team) => team.status === 'active'))
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
function canManageAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  return isManagementView.value || authorization.permissions?.canEdit === true
}

function canReturnAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  if (isManagementView.value || filters.direction !== 'inbound') return false
  if (authorization.granteeType !== 'system_account') return false
  if (!hasManualSource(authorization)) return false
  return authorization.status !== 'revoked' && authorization.status !== 'returned'
}

const hasReturnableInboundAuthorization = computed(() => {
  if (isManagementView.value || filters.direction !== 'inbound') return false
  return authorizations.value.some((authorization) => canReturnAuthorization(authorization))
})
const {
  authorizationCreating,
  confirmExpireChange,
  createAuthorization,
  handleActionMenuClick
} = useAuthorizationActions({
  createAuthorizationScopeParams,
  createExcludedGranteeIds,
  createForm,
  createModalOpen,
  createOwnedGroups,
  createTargetGroupVisible,
  createTeams,
  createUsers,
  expireAuthorization,
  expireForm,
  expireModalOpen,
  isManagementView,
  loadData,
  removeAuthorizationItems,
  selectedCreateAccount,
  updateAuthorizationItems
})
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
  resetFilterOptionLists()
}

function resetFilterOptionSearchState() {
  resetFilterOwnerSearch()
  resetRemoteFilterOptionSearchState()
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
  resetFilterResourceOptions()
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
  resetFilterResourceOptions()
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
