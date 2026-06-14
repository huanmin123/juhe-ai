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
import {
  authorizationCreateExcludedGranteeIds,
  authorizationCreateHasGranteeOptions,
  authorizationCreateResourceOptions,
  authorizationCreateResourcePlaceholder,
  authorizationCreateResourceSelectDisabled,
  authorizationCreateTargetGroupDisabled,
  authorizationCreateTargetGroupPlaceholder,
  authorizationCreateTargetGroupTip
} from './authorizationCreateState'
import { hasManualSource } from './authorizationFormatters'
import {
  createAuthorizationCreateFormModel,
  createAuthorizationExpireFormModel,
  resetAuthorizationCreateForm,
  type AuthorizationCreateFormModel,
  type AuthorizationExpireFormModel
} from './authorizationFormModel'
import {
  activeAuthorizationFilterCount,
  advancedAuthorizationFilterCount,
  authorizationEmptyDescription as createAuthorizationEmptyDescription,
  authorizationListParams
} from './authorizationListFilters'
import {
  authorizationColumnStorageKey,
  authorizationListFilterContext as createAuthorizationListFilterContext,
  authorizationListFilterValues as createAuthorizationListFilterValues,
  authorizationListTotalText,
  authorizationVisibleColumns,
  authorizationsPageSize,
  disabledAuthorizationExpireDate
} from './authorizationPageConfig'
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
  authorizationDirectionOptions,
  authorizationResourceTypeOptions,
  authorizationSourceOptions,
  authorizationStatusOptions,
  createAuthorizationResourceTypeOptions
} from './authorizationTableColumns'
import { useAuthorizationActions } from './useAuthorizationActions'
import { useAuthorizationOptionState } from './useAuthorizationOptionState'

const createModalOpen = ref(false)
const helpOpen = ref(false)
const expireModalOpen = ref(false)
const route = useRoute()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const expireAuthorization = ref<ResourceAuthorizationSummary>()

const defaultAuthorizationsPageState = (): AuthorizationsPageState => createDefaultAuthorizationsPageState(authorizationsPageSize)
const pageStateCache = usePageStateCache<AuthorizationsPageState>(undefined, defaultAuthorizationsPageState, {
  version: 8,
  sanitize: (value, fallback) => sanitizeAuthorizationsPageState(value, fallback, authorizationsPageSize)
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
  pageSize: authorizationsPageSize,
  initialPagination: initialPageState.pagination,
  showTotal: authorizationListTotalText,
  fetchPage: async (_options, pageState) => {
    const params = createAuthorizationListParams(pageState)
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
const createExcludedGranteeIds = computed(() => authorizationCreateExcludedGranteeIds(createForm.ownerSystemAccountId))
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

const createResourceOptions = computed(() => authorizationCreateResourceOptions({
  form: createForm,
  accounts: createOwnedAccounts.value,
  groups: createOwnedGroups.value
}))
const createResourceSelectDisabled = computed(() => authorizationCreateResourceSelectDisabled({
  isManagementView: isManagementView.value,
  ownerSystemAccountId: createForm.ownerSystemAccountId
}))
const createResourcePlaceholder = computed(() => authorizationCreateResourcePlaceholder({
  isManagementView: isManagementView.value,
  ownerSystemAccountId: createForm.ownerSystemAccountId,
  resourceType: createForm.resourceType
}))

const hasCreateGranteeOptions = computed(() => authorizationCreateHasGranteeOptions({
  granteeType: createForm.granteeType,
  users: createUsers.value,
  teams: createTeams.value,
  excludedGranteeIds: createExcludedGranteeIds.value
}))
const createTargetGroupDisabled = computed(() => authorizationCreateTargetGroupDisabled({
  resourceId: createForm.resourceId,
  granteeId: createForm.granteeId,
  selectedAccountProviderCode: selectedCreateAccount.value?.providerCode
}))
const createTargetGroupPlaceholder = computed(() => authorizationCreateTargetGroupPlaceholder({
  resourceId: createForm.resourceId,
  granteeId: createForm.granteeId
}))
const createTargetGroupTip = computed(() => authorizationCreateTargetGroupTip(createTargetGroups.value.length))
const authorizationListFilterValues = () => createAuthorizationListFilterValues(filters)
const authorizationListFilterContext = () => createAuthorizationListFilterContext({
  filters,
  keyword: keywordFilter.value,
  isManagementView: isManagementView.value,
  filterResourceDisabled: filterResourceDisabled.value
})

const activeFilterCount = computed(() => {
  return activeAuthorizationFilterCount(authorizationListFilterContext())
})
const advancedFilterCount = computed(() => {
  return advancedAuthorizationFilterCount(authorizationListFilterContext())
})
const authorizationEmptyDescription = computed(() => {
  return createAuthorizationEmptyDescription({
    filters: authorizationListFilterValues(),
    isManagementView: isManagementView.value,
    activeFilterCount: activeFilterCount.value
  })
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
  return authorizationVisibleColumns({
    isManagementView: isManagementView.value,
    direction: filters.direction,
    hasReturnableInboundAuthorization: hasReturnableInboundAuthorization.value
  })
})
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(() => authorizationColumnStorageKey(isManagementView.value, filters.direction), rawColumns, {
  requiredKeys: ['resource'],
  minVisible: 1
})

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

function createAuthorizationListParams(pageState: { current: number; pageSize: number }) {
  return authorizationListParams({
    ...authorizationListFilterContext(),
    selectedResourceOwnerSystemAccountId: selectedFilterOwnerSystemAccountId.value,
    pageState
  })
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
  pagination.pageSize = state.pagination?.pageSize ?? fallback.pagination?.pageSize ?? authorizationsPageSize
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
