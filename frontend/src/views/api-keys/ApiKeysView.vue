<template>
  <a-card class="page-card api-keys-page-card">
    <div class="page-toolbar api-keys-toolbar">
      <div v-if="isAdmin" class="list-filters">
        <a-select v-model:value="systemAccountFilter" show-search option-filter-prop="label" class="toolbar-select" :options="systemAccountOptions" @change="loadData" />
      </div>
      <div class="page-toolbar-actions">
        <a-button @click="helpOpen = true">
          <template #icon><question-circle-outlined /></template>
          接入帮助
        </a-button>
        <a-button type="primary" @click="openCreate">新建 API Key</a-button>
      </div>
    </div>

    <a-table class="page-table api-keys-table" size="middle" :columns="columns" :data-source="filteredApiKeys" row-key="id" :loading="loading" :scroll="{ x: isAdmin ? 1380 : 1200 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组；接入说明可点击右上角帮助查看。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'key'">
          <div class="key-preview-cell">
            <span class="key-preview" :title="record.key || '旧数据未回填，需重新创建或回填'">{{ formatKeyPreview(record.key) }}</span>
            <a-button class="key-copy-button" type="text" size="small" :disabled="!record.key" @click="copyText(record.key)">
              <template #icon><copy-outlined /></template>
            </a-button>
          </div>
        </template>
        <template v-else-if="column.key === 'group'">
          <a-tag color="purple">{{ groupName(record.groupId) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ apiKeySystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个 API Key？" @confirm="removeApiKey(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="helpOpen" title="API Key 接入帮助" width="720px" :footer="null">
      <div class="gateway-help-content">
        <a-alert
          message="当前只兼容 OpenAI 协议"
          description="客户端统一按 OpenAI 兼容格式请求。后续增加其他主流厂商时，也会优先在服务端适配为 OpenAI 兼容协议。"
          type="info"
          show-icon
        />
        <div class="gateway-help-section">
          <div class="gateway-guide-heading">
            <span class="gateway-guide-title">中转请求地址</span>
            <a-tag color="blue">Base URL</a-tag>
          </div>
          <p class="gateway-guide-desc">客户端的 Base URL 填下面地址，API Key 填本页生成的本地网关密钥。</p>
          <div class="gateway-url-row">
            <span class="gateway-url-label">Base URL</span>
            <span class="gateway-url-value">{{ gatewayBaseUrl }}</span>
            <a-button class="gateway-copy-button" type="text" size="small" @click="copyGatewayBaseUrl">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-example-title">常用路径</span>
          <div class="gateway-endpoints">
            <a-tag v-for="endpoint in gatewayEndpoints" :key="endpoint" color="geekblue">{{ endpoint }}</a-tag>
          </div>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-example-title">客户端配置示例</span>
          <pre class="gateway-code">{{ gatewayClientExample }}</pre>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-example-title">调度关系</span>
          <ol class="gateway-flow-list">
            <li>请求携带本页 API Key 进入 `/v1/*`。</li>
            <li>系统校验 API Key，并找到它绑定的分组。</li>
            <li>系统只从该分组内选择可用 OpenAI 账户请求上游。</li>
            <li>返回内容保持 OpenAI 兼容响应格式，并写入使用记录。</li>
          </ol>
        </div>
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑 API Key' : '新建 API Key'" width="640px" :ok-button-props="{ type: 'primary' }" @ok="saveApiKey">
      <a-alert class="modal-alert" message="系统会自动生成完整密钥，创建后直接复制保存即可。" type="info" show-icon />
      <a-form layout="vertical" class="modal-form">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="绑定分组" required>
          <a-select v-model:value="form.groupId" :options="groupOptions" placeholder="选择分组" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="form.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="过期时间">
          <a-date-picker v-model:value="form.expiresAt" show-time allow-clear style="width: 100%" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="createdKeyOpen" title="API Key 已创建" width="640px" :footer="null">
      <a-alert message="明文密钥已保存，列表中也会直接显示完整值。调用客户端还需要同时配置下方 Base URL。" type="info" show-icon />
      <div class="created-key-base-url">
        <span class="created-key-label">Base URL</span>
        <span class="created-key-value">{{ gatewayBaseUrl }}</span>
        <a-button type="link" size="small" @click="copyGatewayBaseUrl">复制</a-button>
      </div>
      <a-input-group compact class="created-key">
        <a-input :value="createdKey" readonly style="width: calc(100% - 88px)" />
        <a-button type="primary" @click="copyCreatedKey">复制</a-button>
      </a-input-group>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { CopyOutlined, QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { ApiKeySummary, GroupSummary, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, buildSystemAccountOptions, matchesSystemAccountFilter, selectedSystemAccountId, systemAccountDisplayText } from '@/utils/systemAccountFilter'

const loading = ref(false)
const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const editingId = ref<string>()
const createdKey = ref('')
const apiKeys = ref<ApiKeySummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const systemAccountFilter = ref(allSystemAccountsValue)
const form = reactive({ name: '', groupId: '', status: 'active' as 'active' | 'disabled', expiresAt: undefined as Dayjs | undefined })
const isAdmin = authState.isAdmin
const gatewayEndpoints = ['/chat/completions', '/responses', '/models']

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
    { title: '密钥', key: 'key', width: 180 }
  ]
  if (isAdmin.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '绑定分组', key: 'group', width: 220 },
    { title: '状态', key: 'status', width: 100 },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
    { title: '操作', key: 'actions', width: 140 }
  )
  return baseColumns
})

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const groupOptions = computed(() => groups.value.map((group) => ({ label: group.name, value: group.id })))
const filteredApiKeys = computed(() => apiKeys.value.filter((apiKey) => matchesSystemAccountFilter(apiKey, systemAccountFilter.value, isAdmin.value)))
const systemAccountOptions = computed(() => buildSystemAccountOptions(systemAccounts.value))
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`base_url = ${gatewayBaseUrl.value}`, 'api_key = <这里填本页 API Key>', 'protocol = OpenAI-compatible /v1'].join('\n'))

function groupName(groupId: string) {
  return groups.value.find((group) => group.id === groupId)?.name ?? groupId
}

function formatKeyPreview(value?: string) {
  if (!value) return '未回填'
  if (value.length <= 14) return value
  return `${value.slice(0, 6)}...${value.slice(-4)}`
}

async function loadData() {
  loading.value = true
  try {
    const systemAccountId = selectedSystemAccountId(systemAccountFilter.value, isAdmin.value)
    const [keyList, groupList, systemAccountList] = await Promise.all([
      api.apiKeys.list({ systemAccountId }),
      api.groups.list({ systemAccountId }),
      isAdmin.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    apiKeys.value = keyList
    groups.value = groupList
    systemAccounts.value = systemAccountList
  } catch (error) {
    console.error(error)
    message.error('加载 API Key 失败')
  } finally {
    loading.value = false
  }
}

function apiKeySystemAccountText(apiKey: ApiKeySummary) {
  return systemAccountDisplayText(apiKey)
}

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { name: '', groupId: groups.value[0]?.id ?? '', status: 'active', expiresAt: undefined })
  modalOpen.value = true
}

function openEdit(apiKey: ApiKeySummary) {
  editingId.value = apiKey.id
  Object.assign(form, { name: apiKey.name, groupId: apiKey.groupId, status: apiKey.status, expiresAt: undefined })
  modalOpen.value = true
}

async function saveApiKey() {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  if (!form.groupId) {
    message.warning('请选择绑定分组')
    return
  }
  const payload = {
    name: form.name,
    groupId: form.groupId,
    status: form.status,
    expiresAt: form.expiresAt?.toISOString()
  }
  try {
    if (editingId.value) {
      await api.apiKeys.update(editingId.value, payload)
      message.success('API Key 已更新')
    } else {
      const result = await api.apiKeys.create(payload)
      createdKey.value = result.key
      createdKeyOpen.value = true
      message.success('API Key 已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('保存 API Key 失败')
  }
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

async function copyGatewayBaseUrl() {
  await copyText(gatewayBaseUrl.value)
}

async function copyCreatedKey() {
  await copyText(createdKey.value)
}

function inferGatewayBaseUrl() {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000/v1'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000/v1`
  }
  return `${window.location.origin}/v1`
}

function normalizeGatewayBaseUrl(value: string) {
  const trimmed = value.trim().replace(/\/+$/, '')
  return trimmed.endsWith('/v1') ? trimmed : `${trimmed}/v1`
}

async function removeApiKey(id: string) {
  try {
    await api.apiKeys.delete(id)
    message.success('API Key 已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除 API Key 失败')
  }
}

onMounted(loadData)
</script>

<style scoped>
.api-keys-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.api-keys-toolbar {
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

.gateway-help-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.gateway-help-section {
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.gateway-guide-heading {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}

.gateway-guide-title {
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.gateway-guide-desc {
  max-width: 760px;
  margin: 0 0 14px;
  color: #475569;
  line-height: 1.7;
}

.gateway-url-row,
.created-key-base-url {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.gateway-url-label,
.created-key-label,
.gateway-example-title {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.gateway-url-value,
.created-key-value {
  min-width: 0;
  padding: 4px 10px;
  overflow: hidden;
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 6px;
  background: #ecfeff;
}

.gateway-copy-button {
  flex: none;
}

.gateway-endpoints {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 8px 0 10px;
}

.gateway-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.8);
}

.gateway-flow-list {
  padding-left: 20px;
  margin: 8px 0 0;
  color: #475569;
  line-height: 1.8;
}

.modal-alert {
  margin-bottom: 16px;
}

.modal-form {
  margin-top: 16px;
}

.created-key {
  margin-top: 16px;
}

.created-key-base-url {
  margin-top: 16px;
}

.api-keys-table :deep(.ant-empty) {
  margin: 12px 0;
}

.api-keys-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.key-preview-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.key-preview {
  display: inline-flex;
  align-items: center;
  max-width: 120px;
  padding: 3px 8px;
  overflow: hidden;
  color: #008b8b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  background: #eefafa;
}

.key-copy-button {
  color: #94a3b8;
}

.key-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}

</style>
