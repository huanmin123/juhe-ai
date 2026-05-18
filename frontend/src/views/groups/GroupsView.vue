<template>
  <a-card class="page-card groups-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="isManagementView" :show-filters="isManagementView" filter-title="筛选分组" :active-filter-count="activeFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshGroups">
      <template #inline-filters>
        <SystemPrincipalSelect v-if="isManagementView" v-model:value="systemAccountFilter" :accounts="systemAccounts" :active-only="false" include-all class="toolbar-select responsive-list-inline-filter" @change="handleSystemAccountFilterChange" />
      </template>
      <template #actions>
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </template>
      <template #filters>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect v-model:value="systemAccountFilter" :accounts="systemAccounts" :active-only="false" include-all @change="handleSystemAccountFilterChange" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table groups-table" :columns="columns" :data-source="filteredGroups" row-key="id" :loading="loading" :scroll-x="isManagementView ? 1480 : 1300" pull-refresh-enabled :refreshing="loading" @mobile-refresh="refreshGroups">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建一个分组，再到账户页选择账户的归属分组。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="group-name-cell">
            <span>{{ record.name }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ groupSystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'accountCount'">
          <div class="account-count-cell">
            <span class="account-count-row">
              <span class="account-count-label">可用:</span>
              <span class="account-count-value available">{{ groupStats(record).available }}</span>
              <span class="account-count-unit">个账号</span>
            </span>
            <span class="account-count-row">
              <span class="account-count-label">总量:</span>
              <span class="account-count-value">{{ groupStats(record).total }}</span>
              <span class="account-count-unit">个账号</span>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'concurrency'">
          <a-tag color="blue">{{ groupStats(record).currentConcurrency }}</a-tag>
        </template>
        <template v-else-if="column.key === 'usage'">
          <UsageSummaryTags :usage="groupStats(record).todayUsage" />
        </template>
        <template v-else-if="column.key === 'status'">
          <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions v-if="groupActions(record).length" :actions="groupActions(record)" @action-click="handleGroupAction($event, record)" />
          <a-tag v-else-if="isAuthorizedGroup(record)" color="cyan">仅可使用</a-tag>
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              {{ record.name }}
            </div>
            <div class="mobile-list-card-tags">
              <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
              <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
              <a-tag v-if="isAuthorizedGroup(record)" color="cyan">仅可使用</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ groupSystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>可用账号</span>
              <strong>{{ groupStats(record).available }} / {{ groupStats(record).total }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>并发</span>
              <strong>{{ groupStats(record).currentConcurrency }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>用量(日)</span>
              <strong>{{ formatUsageSummary(groupStats(record).todayUsage) }}</strong>
            </div>
          </div>
          <div v-if="groupActions(record).length" class="mobile-list-card-actions">
            <RowActions variant="button" :actions="groupActions(record)" @action-click="handleGroupAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑分组' : '新建分组'" width="640px" :confirm-loading="groupSaving" :ok-button-props="{ type: 'primary', disabled: groupSaving }" @ok="saveGroup">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical">
        <a-form-item label="分组名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="所属供应商" required>
          <a-select v-model:value="form.providerCode" :options="providerOptions" :disabled="providerLocked" />
          <div class="form-help">只有这个供应商下的账户才能选择归入该分组。</div>
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
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
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatCompactUsageAmount, formatNumber, formatUsd } from '@/shared/formatters'
import type { GroupSummary, ProviderDefinition, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, matchesSystemAccountFilter, systemAccountDisplayText } from '@/utils/systemAccountFilter'

const FALLBACK_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

const loading = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('groups')
const groupSaving = submittingRef('groups.save')
const groups = ref<GroupSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const groupOptionsLoaded = ref(false)
const groupOptionsScopeKey = ref('')
type GroupsPageState = {
  systemAccountFilter: string
}
const defaultGroupsPageState = (): GroupsPageState => ({
  systemAccountFilter: allSystemAccountsValue
})
const pageStateCache = usePageStateCache<GroupsPageState>(undefined, defaultGroupsPageState)
const initialPageState = pageStateCache.read()
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const form = reactive({ name: '', providerCode: 'openai', description: '', enabled: true })
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '分组名称', dataIndex: 'name', key: 'name', width: 240 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 120 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '账户数', key: 'accountCount', width: 130 },
    { title: '当前并发', key: 'concurrency', width: 100 },
    { title: '用量(日)', key: 'usage', width: 180 },
    { title: '状态', key: 'status', width: 100 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', width: 100, fixed: 'right' }
  )
  return baseColumns
})

const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const filteredGroups = computed(() => groups.value.filter((group) => matchesSystemAccountFilter(group, systemAccountFilter.value, isManagementView.value)))
const groupScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const providerOptions = computed(() => availableProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code,
  disabled: !provider.enabled
})))
const providerLocked = computed(() => Boolean(editingId.value && groups.value.find((group) => group.id === editingId.value)?.accountStats.total))
const activeFilterCount = computed(() => systemAccountFilter.value === allSystemAccountsValue ? 0 : 1)
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = groupScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName || systemAccounts.value.find((account) => account.id === systemAccountId)?.username || systemAccountId
})

function groupStats(group: GroupSummary) {
  return group.accountStats
}

function groupStatusText(group: GroupSummary) {
  const stats = groupStats(group)
  if (!group.enabled) return '停用'
  if (stats.total === 0) return '未绑定'
  if (stats.available === 0) return '无可用账户'
  return '启用'
}

function groupStatusColor(group: GroupSummary) {
  const stats = groupStats(group)
  if (!group.enabled || stats.total === 0) return 'default'
  if (stats.available === 0) return 'orange'
  return 'green'
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function groupSystemAccountText(group: GroupSummary) {
  return systemAccountDisplayText(group)
}

function isAuthorizedGroup(group: GroupSummary): boolean {
  return group.accessType === 'authorized'
}

function canEditGroup(group: GroupSummary): boolean {
  return !group.isDefault && group.permissions?.canEdit !== false
}

function canDeleteGroup(group: GroupSummary): boolean {
  return !group.isDefault && group.permissions?.canDelete !== false
}

function groupActions(group: GroupSummary): RowActionItem[] {
  const actions: RowActionItem[] = []
  if (canEditGroup(group)) {
    actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (canDeleteGroup(group)) {
    actions.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这个分组？',
      confirmOkText: '删除'
    })
  }
  return actions
}

function handleGroupAction(key: string, group: GroupSummary) {
  if (key === 'edit') {
    openEdit(group)
    return
  }
  if (key === 'delete') {
    void removeGroup(group.id)
  }
}

function formatUsageSummary(usage: GroupSummary['accountStats']['usage']) {
  return `${formatNumber(usage.requestCount)}req/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
}

function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

function formatCost(value?: number): string {
  return formatUsd(value)
}

function defaultProviderCode() {
  return availableProviders.value.find((provider) => provider.enabled)?.code ?? 'openai'
}

async function loadData(options: { forceOptions?: boolean } = {}) {
  loading.value = true
  try {
    const systemAccountId = isManagementView.value ? groupScopeParams.value?.systemAccountId : undefined
    const [groupList] = await Promise.all([
      isManagementView.value ? api.groups.list({ systemAccountId }) : api.myGroups.list(),
      loadGroupOptions(options.forceOptions === true)
    ])
    groups.value = groupList
  } catch (error) {
    console.error(error)
    message.error('加载分组失败')
  } finally {
    loading.value = false
  }
}

async function loadGroupOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (!force && groupOptionsLoaded.value && groupOptionsScopeKey.value === scopeKey) {
    return
  }

  const [providerList, systemAccountList] = await Promise.all([
    isManagementView.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
    isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
  ])
  providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
  systemAccounts.value = systemAccountList
  groupOptionsLoaded.value = true
  groupOptionsScopeKey.value = scopeKey
}

function refreshGroups() {
  void loadData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  void loadData()
}

function resetFilters() {
  systemAccountFilter.value = allSystemAccountsValue
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function openCreate() {
  if (isManagementView.value && !groupScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建分组')
    return
  }
  editingId.value = undefined
  Object.assign(form, { name: '', providerCode: defaultProviderCode(), description: '', enabled: true })
  modalOpen.value = true
}

function openEdit(group: GroupSummary) {
  if (!canEditGroup(group)) {
    message.warning(group.isDefault ? '默认分组不允许编辑' : '授权分组不能编辑')
    return
  }
  editingId.value = group.id
  Object.assign(form, { name: group.name, providerCode: group.providerCode, description: group.description ?? '', enabled: group.enabled })
  modalOpen.value = true
}

const saveGroup = submitAction('groups.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写分组名称')
    return
  }
  try {
    if (editingId.value) {
      if (isManagementView.value) {
        await api.groups.update(editingId.value, { ...form }, groupScopeParams.value)
      } else {
        await api.myGroups.update(editingId.value, { ...form })
      }
      message.success('分组已更新')
    } else {
      if (isManagementView.value) {
        await api.groups.create({ ...form }, groupScopeParams.value)
      } else {
        await api.myGroups.create({ ...form })
      }
      message.success('分组已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存分组失败'))
  }
})

async function removeGroup(id: string) {
  const group = groups.value.find((item) => item.id === id)
  if (group?.isDefault) {
    message.warning('默认分组不允许删除')
    return
  }
  if (group && !canDeleteGroup(group)) {
    message.warning('当前分组不能删除')
    return
  }
  try {
    if (isManagementView.value) {
      await api.groups.delete(id, groupScopeParams.value)
    } else {
      await api.myGroups.delete(id)
    }
    message.success('分组已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除分组失败'))
  }
}

function snapshotPageState(): GroupsPageState {
  return {
    systemAccountFilter: systemAccountFilter.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)
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

.form-help {
  color: #64748b;
  font-size: 12px;
}

.groups-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.groups-table :deep(.ant-empty) {
  margin: 12px 0;
}

.group-name-cell,
.account-count-cell,
.account-count-row {
  display: flex;
  align-items: center;
  gap: 4px;
  color: #475569;
}

.account-count-label {
  min-width: 38px;
  text-align: right;
}

.account-count-value {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 400;
}

.account-count-value.available {
  color: #0891b2;
}

.account-count-value.limited {
  color: #f59e0b;
}

.account-count-unit {
  padding: 1px 6px;
  color: #334155;
  background: #f1f5f9;
  border-radius: 4px;
}

.account-count-row,
.account-count-unit {
  color: #64748b;
  font-size: 12px;
}

.usage-label {
  display: inline-block;
  min-width: 38px;
  color: #64748b;
}

.status-tag {
  width: fit-content;
}

</style>
