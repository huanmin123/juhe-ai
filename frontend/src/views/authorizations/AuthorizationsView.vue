<template>
  <a-card class="page-card authorizations-page-card responsive-page-card">
    <AuthorizationFilterToolbar
      :filters="filters"
      :is-management-view="isManagementView"
      :direction-options="directionOptions"
      :source-options="sourceOptions"
      :resource-type-options="resourceTypeOptions"
      :resource-options="resourceOptions"
      :teams="teams"
      :users="users"
      :active-filter-count="activeFilterCount"
      :loading="loading"
      @reset="resetFilters"
      @refresh="refreshData"
      @help="helpOpen = true"
      @create="openCreateModal"
      @resource-type-change="handleResourceTypeChange"
    />

    <AuthorizationList
      :authorizations="authorizations"
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
      :owner-users="users"
      :resource-options="createResourceOptions"
      :resource-placeholder="createResourcePlaceholder"
      :resource-select-disabled="createResourceSelectDisabled"
      :resource-type-options="createResourceTypeOptions"
      :saving="authorizationCreating"
      :teams="teams"
      :users="users"
      @owner-change="handleCreateOwnerChange"
      @ok="createAuthorization"
    />

    <AuthorizationExpireModal
      v-model:open="expireModalOpen"
      :form="expireForm"
      @ok="confirmExpireChange"
    />

  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import type { Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import type { AccountOptionSummary, GroupOptionSummary, ResourceAuthorizationSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import AuthorizationCreateModal from './AuthorizationCreateModal.vue'
import AuthorizationExpireModal from './AuthorizationExpireModal.vue'
import AuthorizationFilterToolbar from './AuthorizationFilterToolbar.vue'
import AuthorizationHelpModal from './AuthorizationHelpModal.vue'
import AuthorizationList from './AuthorizationList.vue'
import type { AuthorizationCreateFormModel, AuthorizationExpireFormModel } from './authorizationFormTypes'
import { createQuotaLimitForm, quotaLimitsPayload } from '../shared/requestQuotaForm'
import {
  extractApiErrorMessage,
  formatServerDateTimeInput,
  parseDatePickerValue
} from './authorizationFormatters'
import {
  type AuthorizationDirectionFilter,
  type AuthorizationFilterResourceType,
  type AuthorizationSourceFilter,
  authorizationDirectionOptions,
  authorizationResourceTypeOptions,
  authorizationSourceOptions,
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
const teams = ref<SystemTeamPrincipalSummary[]>([])
const users = ref<SystemAccountPrincipalSummary[]>([])

const expireAuthorization = ref<ResourceAuthorizationSummary>()
let createOwnerResourceRequestId = 0

type AuthorizationFilters = {
  direction: AuthorizationDirectionFilter
  sourceType: AuthorizationSourceFilter
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
  teamId?: string
  granteeSystemAccountId?: string
}
type AuthorizationsPageState = {
  filters: AuthorizationFilters
  pagination?: { current: number; pageSize: number }
}
const defaultAuthorizationsPageState = (): AuthorizationsPageState => ({
  filters: {
    direction: 'outbound',
    sourceType: 'all',
    resourceType: 'all',
    resourceId: undefined,
    teamId: undefined,
    granteeSystemAccountId: undefined
  },
  pagination: { current: 1, pageSize }
})
const pageStateCache = usePageStateCache<AuthorizationsPageState>(undefined, defaultAuthorizationsPageState, {
  version: 4,
  sanitize: (value, fallback) => {
    const state = value as Partial<AuthorizationsPageState>
    const filters = state.filters && typeof state.filters === 'object'
      ? state.filters as Partial<AuthorizationFilters> & { direction?: unknown; sourceType?: unknown }
      : {}
    const pagination = state.pagination && typeof state.pagination === 'object'
      ? state.pagination as Partial<{ current: number; pageSize: number }>
      : {}
    return {
      filters: {
        ...fallback.filters,
        ...filters,
        direction: filters.direction === 'inbound' ? 'inbound' : 'outbound',
        sourceType: filters.sourceType === 'manual' || filters.sourceType === 'team' ? filters.sourceType : 'all'
      },
      pagination: {
        current: typeof pagination.current === 'number' && Number.isFinite(pagination.current) && pagination.current > 0 ? Math.trunc(pagination.current) : fallback.pagination?.current ?? 1,
        pageSize: typeof pagination.pageSize === 'number' && Number.isFinite(pagination.pageSize) && pagination.pageSize > 0 ? Math.trunc(pagination.pageSize) : fallback.pagination?.pageSize ?? pageSize
      }
    }
  }
})
const initialPageState = pageStateCache.read()

const filters = reactive<AuthorizationFilters>({ ...initialPageState.filters })
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
  resetPagination
} = useResponsivePagedList<ResourceAuthorizationSummary, Record<string, never>>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条授权，还有更多`
    : `共 ${total} 条授权`,
  fetchPage: async (_options, pageState) => {
    const systemAccountId = isManagementView.value ? authorizationScopeParams.value?.systemAccountId : undefined
    const params = authorizationListParams(systemAccountId, pageState)
    return isManagementView.value
      ? await api.authorizations.listPage(params)
      : await api.myAuthorizations.listPage(params)
  },
  onError: (error) => {
    console.error(error)
    message.error('加载授权列表失败')
  }
})

const createForm = reactive<AuthorizationCreateFormModel>({
  ownerSystemAccountId: undefined,
  resourceType: 'account' as 'account' | 'group',
  resourceId: '' as string,
  granteeType: 'system_account' as 'system_account' | 'team',
  granteeId: '' as string,
  remark: '',
  expiresAt: undefined as Dayjs | undefined,
  quotaLimits: createQuotaLimitForm()
})

const expireForm = reactive<AuthorizationExpireFormModel>({
  expiresAt: undefined as Dayjs | undefined,
  quotaLimits: createQuotaLimitForm()
})

const directionOptions = authorizationDirectionOptions
const sourceOptions = authorizationSourceOptions
const resourceTypeOptions = authorizationResourceTypeOptions
const createResourceTypeOptions = createAuthorizationResourceTypeOptions
const currentSystemAccountId = computed(() => authState.currentUser.value?.id)

const resourceOptions = computed(() => {
  if (filters.resourceType === 'all') {
    return []
  }
  if (filters.resourceType === 'account') {
    return accounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return groups.value.map((group) => ({ label: group.name, value: group.id }))
})

const createResourceOptions = computed(() => {
  if (createForm.resourceType === 'account') {
    return createOwnedAccounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return createOwnedGroups.value.map((group) => ({ label: group.name, value: group.id }))
})
const createResourceSelectDisabled = computed(() => {
  if (isManagementView.value && !createForm.ownerSystemAccountId) return true
  return createResourceOptions.value.length === 0
})
const createResourcePlaceholder = computed(() => {
  if (isManagementView.value && !createForm.ownerSystemAccountId) return '请先选择授权人'
  if (createForm.resourceType === 'account') return createResourceOptions.value.length ? '请选择单个 AI 账户' : '该授权人暂无可授权 AI 账户'
  return createResourceOptions.value.length ? '请选择整个分组账号池' : '该授权人暂无可授权分组'
})

const createExcludedGranteeIds = computed(() => createForm.ownerSystemAccountId ? [createForm.ownerSystemAccountId] : [])
const hasCreateGranteeOptions = computed(() => createForm.granteeType === 'system_account'
  ? users.value.some((user) => user.status === 'active' && !createExcludedGranteeIds.value.includes(user.id))
  : teams.value.some((team) => team.status === 'active'))
const activeFilterCount = computed(() => {
  let count = 0
  if (!isManagementView.value && filters.sourceType !== 'all') count += 1
  if (filters.resourceType !== 'all') count += 1
  if (filters.resourceId) count += 1
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
const authorizationScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId()
  return systemAccountId ? { systemAccountId } : undefined
})
const createOwnedAccounts = computed(() => createAccounts.value.filter((account) => account.permissions?.canAuthorize !== false))
const createOwnedGroups = computed(() => createGroups.value.filter((group) => group.permissions?.canAuthorize !== false))

watch(() => createForm.granteeType, () => {
  createForm.granteeId = ''
})

watch(() => createForm.resourceType, () => {
  createForm.resourceId = ''
})

async function loadMetaData() {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId() : undefined
  const [accountResult, groupResult, teamResult, userResult] = await Promise.allSettled([
    isManagementView.value ? api.accounts.options({ systemAccountId, limit: 200 }) : api.myAccounts.options({ limit: 200 }),
    isManagementView.value ? api.groups.options({ systemAccountId }) : api.myGroups.options(),
    isManagementView.value ? api.authorizationOptions.granteeTeams() : api.myAuthorizationOptions.granteeTeams(),
    isManagementView.value ? api.authorizationOptions.granteeAccounts() : api.myAuthorizationOptions.granteeAccounts()
  ])
  if (accountResult.status === 'fulfilled') {
    accounts.value = accountResult.value
  } else {
    console.error(accountResult.reason)
    message.error('加载可授权账户失败')
  }
  if (groupResult.status === 'fulfilled') {
    groups.value = groupResult.value
  } else {
    console.error(groupResult.reason)
    message.error('加载可授权分组失败')
  }
  if (teamResult.status === 'fulfilled') {
    teams.value = teamResult.value
  } else {
    console.error(teamResult.reason)
    message.error('加载团队列表失败')
  }
  if (userResult.status === 'fulfilled') {
    users.value = userResult.value
  } else {
    console.error(userResult.reason)
    message.error('加载系统账户列表失败')
  }
}

function authorizationListParams(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }) {
  return {
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
    teamId: isManagementView.value ? filters.teamId : undefined,
    granteeSystemAccountId: isManagementView.value ? filters.granteeSystemAccountId : undefined,
    direction: isManagementView.value ? undefined : filters.direction,
    sourceType: !isManagementView.value && filters.sourceType !== 'all' ? filters.sourceType : undefined,
    status: 'all' as const,
    systemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
}

function refreshData() {
  resetPagination()
  void loadData()
}

function openCreateModal() {
  createForm.ownerSystemAccountId = isManagementView.value ? authorizationScopeParams.value?.systemAccountId : currentSystemAccountId.value
  createForm.resourceType = filters.resourceType === 'group' ? 'group' : 'account'
  createForm.resourceId = ''
  createForm.granteeType = 'system_account'
  createForm.granteeId = ''
  createForm.remark = ''
  createForm.expiresAt = undefined
  createForm.quotaLimits = createQuotaLimitForm()
  createModalOpen.value = true
  void loadCreateOwnerResources()
}

function handleCreateOwnerChange() {
  createForm.resourceId = ''
  if (createForm.granteeType === 'system_account' && createForm.granteeId === createForm.ownerSystemAccountId) {
    createForm.granteeId = ''
  }
  void loadCreateOwnerResources()
}

async function loadCreateOwnerResources() {
  const requestId = createOwnerResourceRequestId + 1
  createOwnerResourceRequestId = requestId
  const ownerSystemAccountId = createForm.ownerSystemAccountId
  const resourceType = createForm.resourceType
  createAccounts.value = []
  createGroups.value = []
  const accountRequest = isManagementView.value
    ? ownerSystemAccountId
      ? api.accounts.options({ systemAccountId: ownerSystemAccountId, limit: 200 })
      : undefined
    : api.myAccounts.options({ limit: 200 })
  const groupRequest = isManagementView.value
    ? ownerSystemAccountId
      ? api.groups.options({ systemAccountId: ownerSystemAccountId })
      : undefined
    : api.myGroups.options()
  if (!accountRequest || !groupRequest) return
  const [accountResult, groupResult] = await Promise.allSettled([
    accountRequest,
    groupRequest
  ])
  if (
    requestId !== createOwnerResourceRequestId
    || createForm.ownerSystemAccountId !== ownerSystemAccountId
    || createForm.resourceType !== resourceType
  ) {
    return
  }
  if (accountResult.status === 'fulfilled') {
    createAccounts.value = accountResult.value
  } else {
    console.error(accountResult.reason)
    message.error('加载授权人的 AI 账户失败')
  }
  if (groupResult.status === 'fulfilled') {
    createGroups.value = groupResult.value
  } else {
    console.error(groupResult.reason)
    message.error('加载授权人的分组失败')
  }
}

function handleResourceTypeChange() {
  filters.resourceId = undefined
  refreshData()
}

function resetFilters() {
  Object.assign(filters, defaultAuthorizationsPageState().filters)
  resetPagination()
  pageStateCache.clear()
  void loadData()
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
  if (createForm.granteeType === 'system_account' && !users.value.some((user) => user.id === createForm.granteeId && user.status === 'active')) {
    message.warning('请选择启用中的系统账户')
    return
  }
  if (createForm.granteeType === 'system_account' && createExcludedGranteeIds.value.includes(createForm.granteeId)) {
    message.warning('不能授权给资源所有者自己')
    return
  }
  if (createForm.granteeType === 'team' && !teams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
    message.warning('请选择启用中的团队')
    return
  }
  const selectedResource = createForm.resourceType === 'account'
    ? createOwnedAccounts.value.find((account) => account.id === createForm.resourceId)
    : createOwnedGroups.value.find((group) => group.id === createForm.resourceId)
  if (!selectedResource) {
    message.warning('只能授权自己拥有的资源')
    return
  }
  try {
    const expiresAt = formatServerDateTimeInput(createForm.expiresAt) ?? undefined
    const payload = {
      resourceType: createForm.resourceType,
      resourceId: createForm.resourceId,
      granteeType: createForm.granteeType,
      granteeId: createForm.granteeId,
      remark: createForm.remark.trim() || undefined,
      expiresAt,
      limits: quotaLimitsPayload(createForm.quotaLimits)
    }
    if (isManagementView.value) {
      await api.authorizations.create(payload, createForm.ownerSystemAccountId ? { systemAccountId: createForm.ownerSystemAccountId } : undefined)
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
    if (isManagementView.value) {
      await api.authorizations.revoke(item.id, { sourceType: 'manual' }, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.revoke(item.id, { sourceType: 'manual' })
    }
    message.success('个人授权来源已收回')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '收回个人授权失败'))
  }
}

async function revokeTeamSource(item: ResourceAuthorizationSummary, sourceTeamId: string) {
  try {
    if (isManagementView.value) {
      await api.authorizations.revoke(item.id, { sourceType: 'team', sourceTeamId }, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.revoke(item.id, { sourceType: 'team', sourceTeamId })
    }
    message.success('团队授权来源已收回')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '收回团队授权失败'))
  }
}

async function revokeAuthorization(item: ResourceAuthorizationSummary) {
  try {
    if (isManagementView.value) {
      await api.authorizations.revoke(item.id, undefined, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.revoke(item.id)
    }
    message.success(item.granteeType === 'team' ? '团队授权已收回' : '授权已收回')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, item.granteeType === 'team' ? '收回团队授权失败' : '收回授权失败'))
  }
}

function handleActionMenuClick(event: { key: string | number }, item: ResourceAuthorizationSummary) {
  const key = String(event.key)
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
      void revokeTeamSource(item, sourceTeamId)
    }
  }
}

async function updateAuthorizationStatus(item: ResourceAuthorizationSummary, status: 'active' | 'paused') {
  try {
    if (isManagementView.value) {
      await api.authorizations.update(item.id, { status }, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.update(item.id, { status })
    }
    message.success(status === 'active' ? '授权已恢复' : '授权已暂停')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, status === 'active' ? '恢复授权失败' : '暂停授权失败'))
  }
}

function openExpireModal(item: ResourceAuthorizationSummary) {
  expireAuthorization.value = item
  expireForm.expiresAt = parseDatePickerValue(item.expiresAt)
  expireForm.quotaLimits = createQuotaLimitForm(item.limits)
  expireModalOpen.value = true
}

async function confirmExpireChange() {
  const authorization = expireAuthorization.value
  if (!authorization) {
    expireModalOpen.value = false
    return
  }
  try {
    const payload = {
      expiresAt: formatServerDateTimeInput(expireForm.expiresAt),
      limits: quotaLimitsPayload(expireForm.quotaLimits)
    }
    if (isManagementView.value) {
      await api.authorizations.updateExpire(authorization.id, payload, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.updateExpire(authorization.id, payload)
    }
    expireModalOpen.value = false
    expireAuthorization.value = undefined
    message.success('授权配置已更新')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改授权配置失败'))
  }
}

onMounted(async () => {
  applyRouteFilters()
  await Promise.all([loadMetaData(), loadData()])
})

function applyRouteFilters() {
  const hasRouteFilters = [
    route.query.resourceType,
    route.query.resourceId,
    route.query.teamId,
    route.query.granteeSystemAccountId
  ].some((value) => value !== undefined)
  if (!hasRouteFilters) return
  filters.resourceType = 'all'
  filters.resourceId = undefined
  filters.teamId = undefined
  filters.granteeSystemAccountId = undefined
  const resourceType = route.query.resourceType === 'group' ? 'group' : route.query.resourceType === 'account' ? 'account' : undefined
  const resourceId = typeof route.query.resourceId === 'string' ? route.query.resourceId : undefined
  const teamId = typeof route.query.teamId === 'string' ? route.query.teamId : undefined
  const granteeSystemAccountId = typeof route.query.granteeSystemAccountId === 'string' ? route.query.granteeSystemAccountId : undefined
  if (resourceType) {
    filters.resourceType = resourceType
    createForm.resourceType = resourceType
  }
  if (resourceId) {
    filters.resourceId = resourceId
  }
  if (teamId) {
    filters.teamId = teamId
  }
  if (granteeSystemAccountId) {
    filters.granteeSystemAccountId = granteeSystemAccountId
  }
}

function snapshotPageState(): AuthorizationsPageState {
  return {
    filters: { ...filters },
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

</script>

<style scoped>
.authorizations-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

</style>
