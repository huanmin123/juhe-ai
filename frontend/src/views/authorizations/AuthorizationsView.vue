<template>
  <a-card class="page-card authorizations-page-card responsive-page-card">
    <AuthorizationFilterToolbar
      :filters="filters"
      :is-management-view="isManagementView"
      :direction-options="directionOptions"
      :resource-type-options="resourceTypeOptions"
      :resource-options="resourceOptions"
      :teams="teams"
      :users="users"
      :active-filter-count="activeFilterCount"
      :loading="loading"
      @reset="resetFilters"
      @refresh="loadData"
      @help="helpOpen = true"
      @create="openCreateModal"
      @resource-type-change="handleResourceTypeChange"
    />

    <AuthorizationList
      :authorizations="authorizations"
      :current-system-account-id="currentSystemAccountId"
      :empty-description="authorizationEmptyDescription"
      :is-management-view="isManagementView"
      :loading="loading"
      @refresh="loadData"
      @usage-detail="openUsageDetail"
      @menu-click="handleActionMenuClick"
    />

    <AuthorizationHelpModal v-model:open="helpOpen" />

    <AuthorizationCreateModal
      v-model:open="createModalOpen"
      :form="createForm"
      :has-grantee-options="hasCreateGranteeOptions"
      :resource-options="createResourceOptions"
      :resource-type-options="createResourceTypeOptions"
      :teams="teams"
      :users="users"
      @ok="createAuthorization"
    />

    <AuthorizationExpireModal
      v-model:open="expireModalOpen"
      :form="expireForm"
      @ok="confirmExpireChange"
    />

    <AuthorizationUsageDetailModal
      v-model:open="usageDetailOpen"
      :authorization="selectedAuthorization"
      :team-usage-columns="teamUsageColumns"
      :team-usage-rows="selectedTeamUsageRows"
      :team-usage-summaries="selectedTeamUsageSummaries"
      :usage-detail-columns="usageDetailColumns"
      :usage-details="selectedAuthorizationUsageDetails"
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
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { AccountSummary, AuthorizationUserUsageDetail, GroupSummary, ResourceAuthorizationSummary, SystemAccountPrincipalSummary, SystemTeamSummary } from '@/types/domain'
import AuthorizationCreateModal from './AuthorizationCreateModal.vue'
import AuthorizationExpireModal from './AuthorizationExpireModal.vue'
import AuthorizationFilterToolbar from './AuthorizationFilterToolbar.vue'
import AuthorizationHelpModal from './AuthorizationHelpModal.vue'
import AuthorizationList from './AuthorizationList.vue'
import AuthorizationUsageDetailModal from './AuthorizationUsageDetailModal.vue'
import type { AuthorizationCreateFormModel, AuthorizationExpireFormModel } from './authorizationFormTypes'
import { createQuotaLimitForm, quotaLimitsPayload } from '../shared/requestQuotaForm'
import {
  buildTeamUsageSummaries,
  extractApiErrorMessage,
  formatServerDateTimeInput,
  normalizeAuthorizationUsageResponse,
  parseDatePickerValue,
  type TeamUsageSummary
} from './authorizationFormatters'
import {
  type AuthorizationDirectionFilter,
  type AuthorizationFilterResourceType,
  authorizationDirectionOptions,
  authorizationResourceTypeOptions,
  authorizationTeamUsageColumns,
  authorizationUsageDetailColumns,
  createAuthorizationResourceTypeOptions
} from './authorizationTableColumns'

const loading = ref(false)
const createModalOpen = ref(false)
const usageDetailOpen = ref(false)
const helpOpen = ref(false)
const expireModalOpen = ref(false)
const route = useRoute()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const authorizations = ref<ResourceAuthorizationSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const groups = ref<GroupSummary[]>([])
const teams = ref<SystemTeamSummary[]>([])
const users = ref<SystemAccountPrincipalSummary[]>([])

const selectedAuthorization = ref<ResourceAuthorizationSummary>()
const expireAuthorization = ref<ResourceAuthorizationSummary>()
const selectedAuthorizationUsageDetails = ref<AuthorizationUserUsageDetail[]>([])
const selectedResourceAuthorizations = ref<ResourceAuthorizationSummary[]>([])

const filters = reactive({
  direction: 'all' as AuthorizationDirectionFilter,
  resourceType: 'all' as AuthorizationFilterResourceType,
  resourceId: undefined as string | undefined,
  teamId: undefined as string | undefined,
  granteeSystemAccountId: undefined as string | undefined
})

const createForm = reactive<AuthorizationCreateFormModel>({
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

const usageDetailColumns = authorizationUsageDetailColumns
const teamUsageColumns = authorizationTeamUsageColumns
const directionOptions = authorizationDirectionOptions
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
    return ownedAccounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return ownedGroups.value.map((group) => ({ label: group.name, value: group.id }))
})

const hasCreateGranteeOptions = computed(() => createForm.granteeType === 'system_account'
  ? users.value.some((user) => user.status === 'active')
  : teams.value.some((team) => team.status === 'active'))
const activeFilterCount = computed(() => {
  let count = 0
  if (!isManagementView.value && filters.direction !== 'all') count += 1
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
    return '暂无授权给我的记录。'
  }
  if (filters.direction === 'outbound') {
    return '暂无我授权出去的记录，可新增授权给其他用户或团队。'
  }
  return activeFilterCount.value > 0 ? '没有符合当前筛选条件的授权记录。' : '暂无授权记录，可先新增授权。'
})
const selectedTeamUsageSummaries = computed<TeamUsageSummary[]>(() => {
  const authorization = selectedAuthorization.value
  if (!authorization) {
    return []
  }
  return buildTeamUsageSummaries(authorization, selectedResourceAuthorizations.value, teams.value, filters.teamId)
})
const selectedTeamUsageRows = computed(() => selectedTeamUsageSummaries.value.flatMap((summary) => summary.members))
const authorizationScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId()
  return systemAccountId ? { systemAccountId } : undefined
})
const ownedAccounts = computed(() => accounts.value.filter((account) => account.permissions?.canAuthorize !== false))
const ownedGroups = computed(() => groups.value.filter((group) => group.permissions?.canAuthorize !== false))

watch(() => createForm.granteeType, () => {
  createForm.granteeId = ''
})

async function loadMetaData() {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId() : undefined
  const [accountResult, groupResult, teamResult, userResult] = await Promise.allSettled([
    isManagementView.value ? api.accounts.list({ systemAccountId, limit: 200 }) : api.myAccounts.list({ limit: 200 }),
    isManagementView.value ? api.groups.list({ systemAccountId }) : api.myGroups.list(),
    isManagementView.value ? api.systemTeams.list() : api.myTeams.list(),
    isManagementView.value ? api.systemAccounts.list() : api.myAuthorizationOptions.granteeAccounts()
  ])
  if (accountResult.status === 'fulfilled') {
    accounts.value = accountResult.value.items
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

async function loadData() {
  loading.value = true
  try {
    const systemAccountId = isManagementView.value ? authorizationScopeParams.value?.systemAccountId : undefined
    const params = {
      resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
      resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
      teamId: isManagementView.value ? filters.teamId : undefined,
      granteeSystemAccountId: isManagementView.value ? filters.granteeSystemAccountId : undefined,
      direction: isManagementView.value ? undefined : filters.direction,
      status: 'all' as const
    }
    const authorizationList = isManagementView.value
      ? await api.authorizations.list(systemAccountId ? { ...params, systemAccountId } : params)
      : await api.myAuthorizations.list(params)
    authorizations.value = authorizationList
  } catch (error) {
    console.error(error)
    message.error('加载授权列表失败')
  } finally {
    loading.value = false
  }
}

function openCreateModal() {
  createForm.resourceType = filters.resourceType === 'group' ? 'group' : 'account'
  createForm.resourceId = ''
  createForm.granteeType = 'system_account'
  createForm.granteeId = ''
  createForm.remark = ''
  createForm.expiresAt = undefined
  createForm.quotaLimits = createQuotaLimitForm()
  createModalOpen.value = true
}

function handleResourceTypeChange() {
  filters.resourceId = undefined
  void loadData()
}

function resetFilters() {
  filters.direction = 'all'
  filters.resourceType = 'all'
  filters.resourceId = undefined
  filters.teamId = undefined
  filters.granteeSystemAccountId = undefined
  void loadData()
}

async function createAuthorization() {
  if (!createForm.resourceId) {
    message.warning('请选择资源')
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
  if (createForm.granteeType === 'team' && !teams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
    message.warning('请选择启用中的团队')
    return
  }
  const selectedResource = createForm.resourceType === 'account'
    ? ownedAccounts.value.find((account) => account.id === createForm.resourceId)
    : ownedGroups.value.find((group) => group.id === createForm.resourceId)
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
      await api.authorizations.create(payload, authorizationScopeParams.value)
    } else {
      await api.myAuthorizations.create(payload)
    }
    createModalOpen.value = false
    message.success(createForm.granteeType === 'team' ? '团队授权已创建，成员会自动展开为用户授权' : '授权已创建')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建授权失败'))
  }
}

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
    message.error('收回个人授权失败')
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
    message.error('收回团队授权失败')
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

async function openUsageDetail(item: ResourceAuthorizationSummary) {
  try {
    const usagePayload = isManagementView.value
      ? await api.authorizations.usage(item.id, authorizationScopeParams.value)
      : await api.myAuthorizations.usage(item.id)
    const detail = normalizeAuthorizationUsageResponse(usagePayload, item)
    selectedResourceAuthorizations.value = [detail]
    selectedAuthorization.value = detail
    selectedAuthorizationUsageDetails.value = detail.usageBySystemAccount ?? []
    usageDetailOpen.value = true
  } catch (error) {
    console.error(error)
    message.error('加载用量明细失败')
  }
}

onMounted(async () => {
  applyRouteFilters()
  await loadMetaData()
  await loadData()
})

function applyRouteFilters() {
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

</script>

<style scoped>
.authorizations-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

</style>
