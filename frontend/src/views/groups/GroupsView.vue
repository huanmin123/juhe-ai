<template>
  <a-card class="page-card groups-page-card">
    <div class="page-toolbar page-toolbar-end">
      <div class="page-toolbar-actions">
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </div>
    </div>

    <a-table class="page-table groups-table" size="middle" :columns="columns" :data-source="groups" row-key="id" :loading="loading" :scroll="{ x: 1260 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建一个分组，再把同供应商账户绑定进来。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="group-name-cell">
            <strong>{{ record.name }}</strong>
            <span v-if="record.description" class="muted-cell">{{ record.description }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountCount'">
          <div class="account-count-cell">
            <span class="account-count-row">
              <span class="account-count-label">可用:</span>
              <strong class="account-count-value available">{{ groupStats(record).available }}</strong>
              <span class="account-count-unit">个账号</span>
            </span>
            <span v-if="groupStats(record).rateLimited > 0" class="account-count-row">
              <span class="account-count-label">限流:</span>
              <strong class="account-count-value limited">{{ groupStats(record).rateLimited }}</strong>
              <span class="account-count-unit">个账号</span>
            </span>
            <span class="account-count-row">
              <span class="account-count-label">总量:</span>
              <strong class="account-count-value">{{ groupStats(record).total }}</strong>
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
          <div class="status-cell">
            <a-tag class="status-tag" :color="groupStatusColor(record)">{{ groupStatusText(record) }}</a-tag>
            <span class="status-message">{{ groupStatusHint(record) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-button type="link" size="small" @click="openBind(record)">绑定账户</a-button>
            <a-popconfirm title="确认删除这个分组？" @confirm="removeGroup(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
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
          <div class="form-help">只有这个供应商下的账户才能绑定到该分组。</div>
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="bindOpen" title="绑定账户" width="680px" :ok-button-props="{ type: 'primary' }" @ok="saveBindings">
      <a-alert class="bind-alert" type="info" show-icon :message="bindHint" />
      <div class="bind-summary">
        <a-tag color="geekblue">{{ providerName(bindingGroup?.providerCode) }}</a-tag>
        <a-tag color="blue">已选 {{ selectedAccountIds.length }}/{{ accountOptions.length }}</a-tag>
      </div>
      <a-select
        v-model:value="selectedAccountIds"
        mode="multiple"
        style="width: 100%"
        placeholder="选择同供应商账户"
        :options="accountOptions"
        :max-tag-count="0"
        :max-tag-placeholder="selectedAccountPlaceholder"
      />
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, GroupSummary, ProviderDefinition } from '@/types/domain'

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
const bindOpen = ref(false)
const editingId = ref<string>()
const bindingGroupId = ref<string>()
const groups = ref<GroupSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const selectedAccountIds = ref<string[]>([])
const form = reactive({ name: '', providerCode: 'openai', description: '', enabled: true })

const columns = [
  { title: '分组名称', dataIndex: 'name', key: 'name', width: 240 },
  { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 120 },
  { title: '账户数', key: 'accountCount', width: 130 },
  { title: '当前并发', key: 'concurrency', width: 100 },
  { title: '用量', key: 'usage', width: 280 },
  { title: '状态', key: 'status', width: 180 },
  { title: '操作', key: 'actions', width: 220, fixed: 'right' }
]

const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const providerOptions = computed(() => availableProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code,
  disabled: !provider.enabled
})))
const bindingGroup = computed(() => groups.value.find((group) => group.id === bindingGroupId.value))
const providerLocked = computed(() => Boolean(editingId.value && groups.value.find((group) => group.id === editingId.value)?.accountStats.total))
const accountOptions = computed(() => accounts.value
  .filter((account) => account.providerCode === bindingGroup.value?.providerCode)
  .map((account) => ({
    label: `${account.name} (${accountTypeText(account.type)} / ${accountStatusText(account)})`,
    value: account.id,
    disabled: account.status === 'disabled'
  })))
const bindHint = computed(() => {
  const provider = providerName(bindingGroup.value?.providerCode)
  return `当前分组只绑定 ${provider} 账户；列表页只展示数量，不展开账户名称。`
})

function groupStats(group: GroupSummary) {
  return group.accountStats
}

function accountTypeText(type: string) {
  if (type === 'oauth') return 'OAuth'
  if (type === 'api_key') return 'API Key'
  return type || '-'
}

function accountStatusText(account: AccountSummary) {
  if (account.status === 'active' && account.schedulable && !isCoolingDown(account)) return '可用'
  if (account.status === 'active' && account.schedulable && isCoolingDown(account)) return '冷却中'
  if (account.status === 'active') return '暂停调度'
  if (account.status === 'disabled') return '停用'
  if (account.status === 'rate_limited') return '限流中'
  if (account.status === 'temporary_unavailable') return '临时不可调用'
  return '错误'
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

function groupStatusHint(group: GroupSummary) {
  const stats = groupStats(group)
  if (!group.enabled) return '该分组不会被 API Key 调度'
  return `启用 ${stats.active}，异常 ${stats.error}，停用 ${stats.disabled}`
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function isCoolingDown(account: AccountSummary) {
  if (!account.cooldownUntil) return false
  const cooldownUntil = new Date(account.cooldownUntil).getTime()
  return Number.isFinite(cooldownUntil) && cooldownUntil > Date.now()
}

function formatUsageSummary(usage: GroupSummary['accountStats']['usage']) {
  return `${formatNumber(usage.requestCount)}req/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
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

function selectedAccountPlaceholder(omittedValues: unknown[]) {
  return `已选择 ${omittedValues.length} 个账户`
}

function defaultProviderCode() {
  return availableProviders.value.find((provider) => provider.enabled)?.code ?? 'openai'
}

async function loadData() {
  loading.value = true
  try {
    const [groupList, accountList, providerList] = await Promise.all([api.groups.list(), api.accounts.list(), api.providers.list()])
    groups.value = groupList
    accounts.value = accountList
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
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
  editingId.value = group.id
  Object.assign(form, { name: group.name, providerCode: group.providerCode, description: group.description ?? '', enabled: group.enabled })
  modalOpen.value = true
}

function openBind(group: GroupSummary) {
  bindingGroupId.value = group.id
  selectedAccountIds.value = [...group.accountIds]
  bindOpen.value = true
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

async function saveBindings() {
  if (!bindingGroupId.value) return
  try {
    await api.groups.setAccounts(bindingGroupId.value, selectedAccountIds.value)
    message.success(`账户绑定已更新：${selectedAccountIds.value.length} 个`)
    bindOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('更新绑定失败')
  }
}

async function removeGroup(id: string) {
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

.form-help,
.status-message {
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
.status-cell,
.account-count-cell,
.usage-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
  line-height: 1.4;
}

.usage-summary {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 700;
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
  font-weight: 700;
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

.bind-alert,
.bind-summary {
  margin-bottom: 12px;
}

.bind-summary {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

</style>
