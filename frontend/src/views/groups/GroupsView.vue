<template>
  <a-card class="page-card groups-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="isManagementView" :show-filters="isManagementView" filter-title="筛选分组" :active-filter-count="activeFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshGroups" @search="refreshGroups">
      <template #inline-filters>
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="systemAccountFilter"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          v-model:selected-principal="systemAccountFilterSelection"
          include-all
          class="toolbar-select responsive-list-inline-filter"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
      </template>
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['name']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </template>
      <template #filters>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect
            v-model:value="systemAccountFilter"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="systemAccountFilterSelection"
            include-all
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </label>
      </template>
    </ResponsiveListToolbar>

    <GroupsList
      :columns="managedColumns"
      :groups="groups"
      :is-management-view="isManagementView"
      :loading="loading"
      :mobile-has-more="mobileHasMore"
      :mobile-loading-more="mobileLoadingMore"
      :provider-name="providerName"
      :table-pagination="tablePagination"
      @action="handleGroupAction"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileGroups"
      @mobile-refresh="refreshMobileGroups"
    />

    <GroupEditModal
      v-model:open="modalOpen"
      v-model:client-ip-limit-enabled="clientIpLimitEnabled"
      :client-ip-concurrency-limit="formClientIpConcurrencyLimit"
      :editing-authorized-group="editingAuthorizedGroup"
      :form="form"
      :max-queue-wait-seconds="formMaxQueueWaitSeconds"
      :provider-locked="providerLocked"
      :provider-options="providerOptions"
      :provider-options-loading="groupOptionsLoading"
      :saving="groupSaving"
      :show-target-alert="!editingId && isManagementView"
      :target-system-account-label="targetSystemAccountLabel"
      :title="groupModalTitle"
      @client-ip-concurrency-limit-change="setFormClientIpConcurrencyLimit"
      @max-queue-wait-seconds-change="setFormMaxQueueWaitSeconds"
      @provider-dropdown-visible-change="handleProviderOptionsDropdown"
      @save="saveGroup"
    />

  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { authState } from '@/composables/useAuth'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { GroupListItem, ProviderDefinition } from '@/types/domain'
import { FALLBACK_PROVIDERS } from '../accounts/accountOptions'
import {
  groupStats
} from './groupDisplay'
import { reconcileCreatedGroup } from './groupListMutation'
import GroupEditModal from './GroupEditModal.vue'
import GroupsList from './GroupsList.vue'
import {
  defaultGroupsPageState,
  groupsActiveFilterCount,
  groupsListParams,
  groupsPageSize,
  groupsProviderOptions,
  groupsTableColumns,
  isAllGroupsSystemAccountFilter,
  type GroupsPageState
} from './groupPageConfig'
import { useGroupFormModel } from './groupFormModel'
import {
  canDeleteGroup,
  canEditGroup,
  canReturnAuthorizedGroup,
  isAuthorizedGroup
} from './groupRowActions'

const modalOpen = ref(false)
const editingId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('groups')
const groupSaving = submittingRef('groups.save')
const providers = ref<ProviderDefinition[]>([])
const availableProviders = computed(() => providers.value.length ? providers.value : FALLBACK_PROVIDERS)
const groupOptionsLoaded = ref(false)
const groupOptionsScopeKey = ref('')
const groupOptionsLoading = ref(false)
let groupOptionsRequestId = 0
let groupOptionsLoadingKey: string | undefined
let groupOptionsLoadingPromise: Promise<void> | undefined
let groupEditRequestId = 0
const groupPageEpoch = ref(0)
const groupPageActive = ref(true)
let hasActivated = false
const pageStateCache = usePageStateCache<GroupsPageState>(undefined, defaultGroupsPageState)
const initialPageState = pageStateCache.read()
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const {
  clientIpLimitEnabled,
  form,
  formClientIpConcurrencyLimit,
  formMaxQueueWaitSeconds,
  applyGroupToForm,
  groupCreatePayload,
  groupEditPatch,
  resetGroupFormForCreate,
  setFormClientIpConcurrencyLimit,
  setFormMaxQueueWaitSeconds
} = useGroupFormModel(availableProviders)
type GroupEditTarget = Pick<GroupListItem, 'id' | 'systemAccountId' | 'updatedAt'> & {
  accessType: 'owner' | 'authorized'
  providerLocked: boolean
}
let editingTarget: GroupEditTarget | undefined
const editingAccessType = ref<'owner' | 'authorized'>()
const editingProviderLocked = ref(false)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const groupsApi = useScopedGroupsApi(isManagementView)
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [systemAccountFilter.value]
})
const {
  items: groups,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  invalidatePendingLoads,
  loadData: loadGroupPage,
  loadMoreMobile: loadMoreMobileGroups,
  removeItems: removeGroupItems,
  refreshMobile: refreshMobileGroupsData,
  resetPagination,
  applyResult: applyGroupPageResult,
  updateItems: updateGroupItems
} = useResponsivePagedList<GroupListItem, { forceOptions?: boolean }>({
  pageSize: groupsPageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 个分组，还有更多`
    : `共 ${formatNumber(total)} 个分组`,
  fetchPage: async (_options, pageState) => {
    const systemAccountId = isManagementView.value ? groupScopeParams.value?.systemAccountId : undefined
    const page = await groupsApi.listPage(groupsListParams(systemAccountId, pageState))
    return page
  },
  requestSignature: (_options, pageState) => {
    const systemAccountId = isManagementView.value ? groupScopeParams.value?.systemAccountId : undefined
    return [
      isManagementView.value ? 'management' : 'self',
      authState.revision.value,
      groupPageEpoch.value,
      groupsListParams(systemAccountId, pageState)
    ]
  },
  onError: (error) => {
    console.error(error)
    message.error('加载分组失败')
  }
})

async function loadData(loadOptions: { forceOptions?: boolean; quiet?: boolean } = {}): Promise<void> {
  await loadGroupPage(loadOptions)
}

function groupListScopeKey(): string {
  return [
    isManagementView.value ? 'management' : 'self',
    authState.revision.value,
    groupScopeParams.value?.systemAccountId ?? ''
  ].join(':')
}

const rawColumns = computed(() => groupsTableColumns(isManagementView.value))
const columnStorageKey = computed(() => (isManagementView.value ? 'groups:management' : 'groups:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})

const groupScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
onDeactivated(() => {
  groupPageActive.value = false
  groupPageEpoch.value += 1
  groupEditRequestId += 1
  invalidateGroupOptions()
})

onActivated(() => {
  if (!hasActivated) {
    hasActivated = true
    return
  }
  groupPageActive.value = true
  groupPageEpoch.value += 1
  void loadData({ quiet: true })
})

watch(authState.revision, () => {
  groupEditRequestId += 1
  invalidateGroupOptions()
  if (groupPageActive.value) void loadData({ quiet: true })
})
onBeforeUnmount(() => {
  groupEditRequestId += 1
  invalidateGroupOptions()
})
const providerOptions = computed(() => {
  const options = groupsProviderOptions(availableProviders.value)
  const selectedCode = form.providerCode.trim()
  if (!selectedCode || options.some((option) => option.value === selectedCode)) return options
  const selectedName = providerDisplayName(selectedCode, availableProviders.value)
  return [
    ...options,
    { label: selectedName === '未知供应商' ? selectedCode : selectedName, value: selectedCode, disabled: false }
  ]
})
const editingAuthorizedGroup = computed(() => editingAccessType.value === 'authorized')
const groupModalTitle = computed(() => {
  if (!editingId.value) return '新建分组'
  return editingAuthorizedGroup.value ? '编辑授权分组使用配置' : '编辑分组'
})
const providerLocked = computed(() => Boolean(editingId.value && editingProviderLocked.value))
const activeFilterCount = computed(() => groupsActiveFilterCount(systemAccountFilter.value))
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = groupScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

function providerName(providerCode?: string) {
  return providerDisplayName(providerCode, availableProviders.value)
}

function handleGroupAction(key: string, group: GroupListItem) {
  if (key === 'edit') {
    void openEdit(group)
    return
  }
  if (key === 'return-authorization') {
    void returnAuthorizationGroup(group.id)
    return
  }
  if (key === 'delete') {
    void removeGroup(group.id)
  }
}

function groupOperationScopeParams(group?: Pick<GroupListItem, 'systemAccountId' | 'accessType'>): { systemAccountId: string } | undefined {
  const systemAccountId = group?.accessType === 'authorized'
    ? groupScopeParams.value?.systemAccountId
    : group?.systemAccountId?.trim() || groupScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function groupEditTarget(group: GroupListItem): GroupEditTarget {
  return {
    id: group.id,
    systemAccountId: group.systemAccountId,
    updatedAt: group.updatedAt,
    accessType: isAuthorizedGroup(group) ? 'authorized' : 'owner',
    providerLocked: groupStats(group).total > 0
  }
}

async function loadGroupOptions(force = false): Promise<void> {
  const scopeKey = `${isManagementView.value ? 'management' : 'self'}:${authState.revision.value}`
  if (!force && groupOptionsLoaded.value && groupOptionsScopeKey.value === scopeKey) {
    return
  }
  if (!force && groupOptionsLoadingKey === scopeKey && groupOptionsLoadingPromise) return groupOptionsLoadingPromise

  const requestId = ++groupOptionsRequestId
  groupOptionsLoading.value = true
  const request = (async () => {
    try {
      const providerList = await loadProviderOptionsResource({
        force,
        isCurrent: () => requestId === groupOptionsRequestId && scopeKey === `${isManagementView.value ? 'management' : 'self'}:${authState.revision.value}`,
        isManagementView: isManagementView.value
      })
      if (requestId !== groupOptionsRequestId || scopeKey !== `${isManagementView.value ? 'management' : 'self'}:${authState.revision.value}`) return
      providers.value = providerList.data.length ? providerList.data : FALLBACK_PROVIDERS
      groupOptionsLoaded.value = true
      groupOptionsScopeKey.value = scopeKey
    } catch (error) {
      if (requestId !== groupOptionsRequestId) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载供应商选项失败，请重试'))
    } finally {
      if (requestId === groupOptionsRequestId) {
        groupOptionsLoading.value = false
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
      }
    }
  })()
  groupOptionsLoadingKey = scopeKey
  groupOptionsLoadingPromise = request
  return request
}

function invalidateGroupOptions(): void {
  groupOptionsRequestId += 1
  groupOptionsLoadingKey = undefined
  groupOptionsLoadingPromise = undefined
  groupOptionsLoading.value = false
  groupOptionsLoaded.value = false
  groupOptionsScopeKey.value = ''
  providers.value = []
}

function handleProviderOptionsDropdown(open: boolean): void {
  if (open) void loadGroupOptions()
}

function refreshGroups() {
  resetSystemAccountOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function refreshMobileGroups() {
  resetSystemAccountOptionsSearch()
  void refreshMobileGroupsData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  groupEditRequestId += 1
  if (isAllGroupsSystemAccountFilter(systemAccountFilter.value)) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetPagination()
  void loadData()
}

function resetFilters() {
  groupEditRequestId += 1
  systemAccountFilter.value = defaultGroupsPageState().systemAccountFilter
  systemAccountFilterSelection.value = undefined
  resetSystemAccountOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function openCreate() {
  if (isManagementView.value && !groupScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建分组')
    return
  }
  groupEditRequestId += 1
  editingId.value = undefined
  editingTarget = undefined
  editingAccessType.value = undefined
  editingProviderLocked.value = false
  resetGroupFormForCreate()
  modalOpen.value = true
}

async function openEdit(group: GroupListItem) {
  if (!canEditGroup(group)) {
    message.warning(group.isDefault ? '默认分组不允许编辑' : '当前分组不能编辑')
    return
  }
  const requestId = ++groupEditRequestId
  const target = groupEditTarget(group)
  if (group.groupType !== 'high_concurrency') {
    applyGroupToForm({ ...group, accessType: target.accessType })
    editingTarget = target
    editingAccessType.value = target.accessType
    editingProviderLocked.value = target.providerLocked
    editingId.value = group.id
    modalOpen.value = true
    return
  }
  const managementView = isManagementView.value
  const authRevision = authState.revision.value
  const pageSystemAccountId = groupScopeParams.value?.systemAccountId
  try {
    const detail = await groupsApi.editBasicDetail(group.id, groupOperationScopeParams(group))
    if (requestId !== groupEditRequestId
      || managementView !== isManagementView.value
      || authRevision !== authState.revision.value
      || pageSystemAccountId !== groupScopeParams.value?.systemAccountId) return
    applyGroupToForm({ ...detail, accessType: target.accessType })
    editingTarget = { ...target, updatedAt: detail.updatedAt }
    editingAccessType.value = target.accessType
    editingProviderLocked.value = target.providerLocked
    editingId.value = group.id
    modalOpen.value = true
  } catch (error) {
    if (requestId !== groupEditRequestId
      || managementView !== isManagementView.value
      || authRevision !== authState.revision.value
      || pageSystemAccountId !== groupScopeParams.value?.systemAccountId) return
    message.error(extractApiErrorMessage(error, '加载分组编辑信息失败'))
  }
}

const saveGroup = submitAction('groups.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写分组名称')
    return
  }
  if (!form.providerCode.trim() && !editingAuthorizedGroup.value) {
    message.warning('请选择供应商')
    return
  }
  try {
    const targetId = editingId.value
    if (targetId) {
      const target = editingTarget
      if (!target || target.id !== targetId) {
        modalOpen.value = false
        message.warning('分组列表已变化，请重新打开编辑弹窗')
        return
      }
      const patch = groupEditPatch()
      if (!Object.keys(patch).length) {
        modalOpen.value = false
        message.info('分组配置未发生变化')
        return
      }
      const payload = { ...patch, expectedUpdatedAt: target.updatedAt }
      const updated = await groupsApi.update(targetId, payload, groupOperationScopeParams(target))
      const changedFields = new Set(updated.changedFields)
      updateGroupItems((item) => item.id === targetId, (item) => ({
        ...item,
        updatedAt: updated.updatedAt,
        ...(changedFields.has('name') && typeof patch.name === 'string' ? { name: patch.name } : {}),
        ...(changedFields.has('providerCode') && typeof patch.providerCode === 'string' ? { providerCode: patch.providerCode } : {}),
        ...(changedFields.has('description') && typeof patch.description === 'string' ? { description: patch.description || undefined } : {}),
        ...(changedFields.has('enabled') && typeof patch.enabled === 'boolean' ? { enabled: patch.enabled } : {}),
        ...(changedFields.has('groupType') && (patch.groupType === 'personal' || patch.groupType === 'high_concurrency') ? { groupType: patch.groupType } : {})
      }))
      message.success(target.accessType === 'authorized' ? '授权分组使用配置已更新' : '分组已更新')
    } else {
      const payload = groupCreatePayload()
      const createScopeKey = groupListScopeKey()
      const created = await groupsApi.create(payload, groupScopeParams.value)
      message.success('分组已创建')
      if (createScopeKey === groupListScopeKey()) {
        const mobileLoadWasPending = mobileLoadingMore.value
        invalidatePendingLoads()
        const accumulated = mobileLoadWasPending || (pagination.current > 1 && groups.value.length > pagination.pageSize)
        const state = reconcileCreatedGroup(groups.value, created, {
          accumulated,
          hasMore: mobileHasMore.value,
          page: pagination.current,
          pageSize: pagination.pageSize,
          total: pagination.total
        })
        if (state.requiresReload) {
          await loadData({ quiet: true })
        } else {
          applyGroupPageResult({
            items: state.items,
            page: pagination.current,
            pageSize: pagination.pageSize,
            total: state.total,
            hasMore: state.hasMore,
            currentPageCount: state.currentPageCount
          })
        }
      }
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存分组失败'))
  }
})

async function removeGroup(id: string) {
  const group = groups.value.find((item) => item.id === id)
  if (group && isAuthorizedGroup(group)) {
    message.warning('授权分组请使用归还操作')
    return
  }
  if (group?.isDefault) {
    message.warning('默认分组不允许删除')
    return
  }
  if (group && !canDeleteGroup(group)) {
    message.warning('当前分组不能删除')
    return
  }
  try {
    await groupsApi.delete(id, groupOperationScopeParams(group))
    removeGroupItems((item) => item.id === id)
    message.success('分组已删除')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除分组失败'))
  }
}

async function returnAuthorizationGroup(id: string) {
  const group = groups.value.find((item) => item.id === id)
  if (!group || !isAuthorizedGroup(group)) {
    message.warning('授权分组不存在')
    return
  }
  if (!canReturnAuthorizedGroup(group)) {
    message.warning('当前授权分组不能归还')
    return
  }
  try {
    await groupsApi.returnAuthorization(id, groupOperationScopeParams(group))
    removeGroupItems((item) => item.id === id)
    message.success('授权分组已归还')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '归还授权分组失败'))
  }
}

function snapshotPageState(): GroupsPageState {
  return {
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onMounted(() => {
  void loadData()
})
</script>

<style scoped>
.groups-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

</style>
