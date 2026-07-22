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
      :saving="groupSaving"
      :show-target-alert="!editingId && isManagementView"
      :target-system-account-label="targetSystemAccountLabel"
      :title="groupModalTitle"
      @client-ip-concurrency-limit-change="setFormClientIpConcurrencyLimit"
      @max-queue-wait-seconds-change="setFormMaxQueueWaitSeconds"
      @save="saveGroup"
    />

  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { GroupSummary, ProviderDefinition } from '@/types/domain'
import { FALLBACK_PROVIDERS } from '../accounts/accountOptions'
import {
  groupStats
} from './groupDisplay'
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
let groupOptionsRequestSequence = 0
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
  groupFormPayload,
  resetGroupFormForCreate,
  setFormClientIpConcurrencyLimit,
  setFormMaxQueueWaitSeconds
} = useGroupFormModel(availableProviders)
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
  loadData: loadGroupPage,
  loadMoreMobile: loadMoreMobileGroups,
  removeItems: removeGroupItems,
  refreshMobile: refreshMobileGroupsData,
  resetPagination,
  updateItems: updateGroupItems
} = useResponsivePagedList<GroupSummary, { forceOptions?: boolean }>({
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
const providerOptions = computed(() => groupsProviderOptions(availableProviders.value))
const editingGroup = computed(() => groups.value.find((group) => group.id === editingId.value))
const editingAuthorizedGroup = computed(() => Boolean(editingGroup.value && isAuthorizedGroup(editingGroup.value)))
const groupModalTitle = computed(() => {
  if (!editingId.value) return '新建分组'
  return editingAuthorizedGroup.value ? '编辑授权分组使用配置' : '编辑分组'
})
const providerLocked = computed(() => Boolean(editingId.value && groupStats(editingGroup.value).total))
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

function handleGroupAction(key: string, group: GroupSummary) {
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

function groupOperationScopeParams(group?: Pick<GroupSummary, 'systemAccountId' | 'accessType'>): { systemAccountId: string } | undefined {
  const systemAccountId = group?.accessType === 'authorized'
    ? groupScopeParams.value?.systemAccountId
    : group?.systemAccountId?.trim() || groupScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

async function loadGroupOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (!force && groupOptionsLoaded.value && groupOptionsScopeKey.value === scopeKey) {
    return
  }

  const requestSequence = ++groupOptionsRequestSequence
  const providerList = await loadProviderOptionsResource({
    force,
    isCurrent: () => requestSequence === groupOptionsRequestSequence
      && scopeKey === (isManagementView.value ? 'management' : 'self'),
    isManagementView: scopeKey === 'management'
  })
  if (requestSequence !== groupOptionsRequestSequence || scopeKey !== (isManagementView.value ? 'management' : 'self')) return
  providers.value = providerList.data.length ? providerList.data : FALLBACK_PROVIDERS
  groupOptionsLoaded.value = true
  groupOptionsScopeKey.value = scopeKey
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
  if (isAllGroupsSystemAccountFilter(systemAccountFilter.value)) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetPagination()
  void loadData()
}

function resetFilters() {
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
  editingId.value = undefined
  void loadGroupOptions()
  resetGroupFormForCreate()
  modalOpen.value = true
}

async function openEdit(group: GroupSummary) {
  if (!canEditGroup(group)) {
    message.warning(group.isDefault ? '默认分组不允许编辑' : '当前分组不能编辑')
    return
  }
  void loadGroupOptions()
  let detail: GroupSummary
  try {
    detail = await groupsApi.detail(group.id, groupOperationScopeParams(group))
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载分组详情失败'))
    return
  }
  try {
    applyGroupToForm(detail)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '分组调度策略数据异常，请清理后再编辑'))
    return
  }
  editingId.value = group.id
  modalOpen.value = true
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
      const targetGroup = groups.value.find((item) => item.id === targetId)
      const payload = groupFormPayload(targetGroup)
      const updated = await groupsApi.update(targetId, payload, groupOperationScopeParams(targetGroup))
      updateGroupItems((item) => item.id === targetId, () => updated)
      message.success(isAuthorizedGroup(updated) ? '授权分组使用配置已更新' : '分组已更新')
      void loadData({ quiet: true })
    } else {
      const payload = groupFormPayload()
      await groupsApi.create(payload, groupScopeParams.value)
      message.success('分组已创建')
      await loadData()
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
    void loadData({ quiet: true })
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
    void loadData({ quiet: true })
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
