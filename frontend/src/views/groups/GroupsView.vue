<template>
  <a-card class="page-card groups-page-card">
    <div class="page-toolbar groups-toolbar">
      <div v-if="isAdmin" class="list-filters">
        <a-select v-model:value="systemAccountFilter" show-search option-filter-prop="label" class="toolbar-select" :options="systemAccountOptions" @change="loadData" />
      </div>
    <div class="page-toolbar-actions">
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </div>
    </div>

    <a-table class="page-table groups-table" size="middle" :columns="columns" :data-source="filteredGroups" row-key="id" :loading="loading" :scroll="{ x: isAdmin ? 1430 : 1250 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建一个分组，再到账户页选择账户的归属分组。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="group-name-cell">
            <span>
              {{ record.name }}
              <a-tag v-if="record.isDefault" class="default-group-tag" color="blue">默认</a-tag>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ groupSystemAccountText(record) }}</span>
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
          <div class="usage-cell">
            <span><span class="usage-label">今日:</span> <span class="usage-summary">{{ formatUsageSummary(groupStats(record).todayUsage) }}</span></span>
            <span><span class="usage-label">累计:</span> <span class="usage-summary">{{ formatUsageSummary(groupStats(record).usage) }}</span></span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag class="status-tag" :color="groupStatusColor(record)">{{ groupStatusText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button v-if="canEditGroup(record)" type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm v-if="canDeleteGroup(record)" title="确认删除这个分组？" @confirm="removeGroup(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
            <a-tooltip v-else-if="record.isDefault" title="默认分组不允许删除">
              <a-button type="link" size="small" danger disabled>删除</a-button>
            </a-tooltip>
            <a-tag v-if="isAuthorizedGroup(record)" color="cyan">仅可使用</a-tag>
            <a-dropdown v-if="groupMenuItems(record).length">
              <a-button type="link" size="small">更多</a-button>
              <template #overlay>
                <a-menu @click="handleGroupMenuClick($event, record)">
                  <a-menu-item v-for="item in groupMenuItems(record)" :key="item.key" :danger="item.danger">{{ item.label }}</a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑分组' : '新建分组'" width="640px" :ok-button-props="{ type: 'primary' }" @ok="saveGroup">
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

    <a-modal v-model:open="authorizationModalOpen" :title="authorizationModalTitle" width="920px" :footer="null" @cancel="closeAuthorizationModal">
      <div v-if="authorizationGroup" class="authorization-modal">
        <a-alert class="form-alert" type="info" show-icon message="分组授权后，对方可直接使用该分组里的全部账户；统计会按分组维度汇总，但具体日志仍按实际调用者记录。" />
        <div class="authorization-create-row">
          <a-select v-model:value="authorizationForm.granteeSystemAccountId" show-search option-filter-prop="label" class="authorization-user-select" :options="authorizationUserOptions" placeholder="选择被授权用户" />
          <a-input v-model:value="authorizationForm.remark" allow-clear placeholder="备注（可选）" />
          <a-button type="primary" :loading="authorizationSaving" @click="createAuthorization">新增授权</a-button>
        </div>
        <a-table size="small" :columns="authorizationColumns" :data-source="groupAuthorizations" row-key="id" :loading="authorizationLoading" :pagination="false">
          <template #emptyText>
            <a-empty description="还没有授权记录" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'grantee'">
              {{ record.granteeSystemAccountName || record.granteeSystemAccountId }}
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '生效中' : '已收回' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'usage'">
              <span class="usage-summary">{{ formatAuthorizationUsage(record.usage) }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              {{ formatDateTime(record.createdAt) }}
            </template>
            <template v-else-if="column.key === 'actions'">
              <a-popconfirm v-if="record.status === 'active'" title="确认收回这个授权？" @confirm="revokeAuthorization(record.id)">
                <a-button type="link" size="small" danger>收回</a-button>
              </a-popconfirm>
              <span v-else class="muted-cell">-</span>
            </template>
          </template>
        </a-table>
      </div>
    </a-modal>

  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { AccountUsageSummary, GroupAuthorizationSummary, GroupSummary, ProviderDefinition, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, buildSystemAccountOptions, matchesSystemAccountFilter, selectedSystemAccountId, systemAccountDisplayText } from '@/utils/systemAccountFilter'

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
const authorizationModalOpen = ref(false)
const authorizationLoading = ref(false)
const authorizationSaving = ref(false)
const editingId = ref<string>()
const authorizationGroup = ref<GroupSummary>()
const groupAuthorizations = ref<GroupAuthorizationSummary[]>([])
const groups = ref<GroupSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const systemAccountFilter = ref(allSystemAccountsValue)
const form = reactive({ name: '', providerCode: 'openai', description: '', enabled: true })
const authorizationForm = reactive({ granteeSystemAccountId: undefined as string | undefined, remark: '' })
const isAdmin = authState.isAdmin

interface GroupMenuItem {
  key: string
  label: string
  danger?: boolean
}

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '分组名称', dataIndex: 'name', key: 'name', width: 240 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 120 }
  ]
  if (isAdmin.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '账户数', key: 'accountCount', width: 130 },
    { title: '当前并发', key: 'concurrency', width: 100 },
    { title: '用量', key: 'usage', width: 280 },
    { title: '状态', key: 'status', width: 100 },
    { title: '操作', key: 'actions', width: 150, fixed: 'right' }
  )
  return baseColumns
})

const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const filteredGroups = computed(() => groups.value.filter((group) => matchesSystemAccountFilter(group, systemAccountFilter.value, isAdmin.value)))
const systemAccountOptions = computed(() => buildSystemAccountOptions(systemAccounts.value))
const authorizationModalTitle = computed(() => authorizationGroup.value ? `授权管理：${authorizationGroup.value.name}` : '授权管理')
const authorizationColumns = [
  { title: '被授权用户', key: 'grantee', width: 180 },
  { title: '状态', key: 'status', width: 100 },
  { title: '授权后用量', key: 'usage', width: 260 },
  { title: '备注', dataIndex: 'remark', key: 'remark', width: 180 },
  { title: '授权时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', width: 100 }
]
const authorizationUserOptions = computed(() => systemAccounts.value
  .filter((account) => account.status === 'active' && account.id !== authorizationGroup.value?.ownerSystemAccountId)
  .map((account) => ({ label: `${account.displayName || account.username}（${account.username}）`, value: account.id })))
const providerOptions = computed(() => availableProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code,
  disabled: !provider.enabled
})))
const providerLocked = computed(() => Boolean(editingId.value && groups.value.find((group) => group.id === editingId.value)?.accountStats.total))

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
  return group.permissions?.canEdit !== false
}

function canDeleteGroup(group: GroupSummary): boolean {
  return !group.isDefault && group.permissions?.canDelete !== false
}

function canAuthorizeGroup(group: GroupSummary): boolean {
  return group.permissions?.canAuthorize !== false && !isAuthorizedGroup(group)
}

function groupMenuItems(group: GroupSummary): GroupMenuItem[] {
  return canAuthorizeGroup(group) ? [{ key: 'authorizations', label: '授权管理' }] : []
}

function formatUsageSummary(usage: GroupSummary['accountStats']['usage']) {
  return `${formatNumber(usage.requestCount)}req/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
}

function formatAuthorizationUsage(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function formatNumber(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

function formatUsageAmount(value?: number): string {
  const amount = value ?? 0
  const absoluteValue = Math.abs(amount)
  if (absoluteValue >= 1_000_000_000) {
    return `${(amount / 1_000_000_000).toFixed(1)}B`
  }
  if (absoluteValue >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(1)}M`
  }
  if (absoluteValue >= 1_000) {
    return `${(amount / 1_000).toFixed(1)}K`
  }
  return formatNumber(amount)
}

function formatCost(value?: number): string {
  return `$${(value ?? 0).toFixed(2)}`
}

function defaultProviderCode() {
  return availableProviders.value.find((provider) => provider.enabled)?.code ?? 'openai'
}

async function loadData() {
  loading.value = true
  try {
    const systemAccountId = selectedSystemAccountId(systemAccountFilter.value, isAdmin.value)
    const [groupList, providerList, systemAccountList] = await Promise.all([
      api.groups.list({ systemAccountId }),
      isAdmin.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
      api.systemAccounts.list()
    ])
    groups.value = groupList
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    systemAccounts.value = systemAccountList
  } catch (error) {
    console.error(error)
    message.error('加载分组失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { name: '', providerCode: defaultProviderCode(), description: '', enabled: true })
  modalOpen.value = true
}

function openEdit(group: GroupSummary) {
  if (!canEditGroup(group)) {
    message.warning('授权分组不能编辑')
    return
  }
  editingId.value = group.id
  Object.assign(form, { name: group.name, providerCode: group.providerCode, description: group.description ?? '', enabled: group.enabled })
  modalOpen.value = true
}

async function openAuthorizationModal(group: GroupSummary) {
  if (!canAuthorizeGroup(group)) {
    message.warning('当前分组没有授权管理权限')
    return
  }
  authorizationGroup.value = group
  authorizationForm.granteeSystemAccountId = undefined
  authorizationForm.remark = ''
  authorizationModalOpen.value = true
  authorizationLoading.value = true
  try {
    groupAuthorizations.value = await api.groups.authorizations(group.id)
  } catch (error) {
    console.error(error)
    message.error('加载授权记录失败')
  } finally {
    authorizationLoading.value = false
  }
}

function closeAuthorizationModal() {
  authorizationModalOpen.value = false
  authorizationGroup.value = undefined
  groupAuthorizations.value = []
  authorizationForm.granteeSystemAccountId = undefined
  authorizationForm.remark = ''
}

async function refreshAuthorizationList() {
  if (!authorizationGroup.value) return
  authorizationLoading.value = true
  try {
    groupAuthorizations.value = await api.groups.authorizations(authorizationGroup.value.id)
  } catch (error) {
    console.error(error)
    message.error('刷新授权记录失败')
  } finally {
    authorizationLoading.value = false
  }
}

async function createAuthorization() {
  if (!authorizationGroup.value) return
  if (!authorizationForm.granteeSystemAccountId) {
    message.warning('请选择被授权用户')
    return
  }
  authorizationSaving.value = true
  try {
    await api.groups.createAuthorization(authorizationGroup.value.id, {
      granteeSystemAccountId: authorizationForm.granteeSystemAccountId,
      remark: authorizationForm.remark.trim() || undefined
    })
    message.success('授权已创建')
    authorizationForm.granteeSystemAccountId = undefined
    authorizationForm.remark = ''
    await refreshAuthorizationList()
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('创建授权失败')
  } finally {
    authorizationSaving.value = false
  }
}

async function revokeAuthorization(authorizationId: string) {
  if (!authorizationGroup.value) return
  try {
    await api.groups.revokeAuthorization(authorizationGroup.value.id, authorizationId)
    message.success('授权已收回')
    await refreshAuthorizationList()
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('收回授权失败')
  }
}

function handleGroupMenuClick(event: { key: string | number }, group: GroupSummary) {
  if (String(event.key) === 'authorizations') {
    void openAuthorizationModal(group)
  }
}

async function saveGroup() {
  if (!form.name.trim()) {
    message.warning('请填写分组名称')
    return
  }
  try {
    if (editingId.value) {
      await api.groups.update(editingId.value, { ...form })
      message.success('分组已更新')
    } else {
      await api.groups.create({ ...form })
      message.success('分组已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('保存分组失败')
  }
}

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
    await api.groups.delete(id)
    message.success('分组已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除分组失败')
  }
}

onMounted(loadData)
</script>

<style scoped>
.groups-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.groups-toolbar {
  align-items: center;
}

.list-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  flex: 1 1 260px;
}

.toolbar-select {
  min-width: 180px;
}

.form-help {
  color: #64748b;
  font-size: 12px;
}

.authorization-modal {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.authorization-create-row {
  display: grid;
  grid-template-columns: minmax(180px, 240px) minmax(180px, 1fr) auto;
  gap: 10px;
  align-items: center;
}

.authorization-user-select {
  width: 100%;
}

.groups-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.groups-table :deep(.ant-empty) {
  margin: 12px 0;
}

.group-name-cell,
.account-count-cell,
.usage-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
  line-height: 1.4;
}

.default-group-tag {
  margin-left: 8px;
}

.usage-summary {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 400;
}

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
