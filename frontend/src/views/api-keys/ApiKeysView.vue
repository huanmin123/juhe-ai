<template>
  <a-card class="page-card api-keys-page-card" title="API 密钥">
    <div class="page-toolbar">
      <span class="toolbar-note">API Key 绑定分组，系统自动生成完整密钥，不需要手动填写前缀。</span>
      <div class="page-toolbar-actions">
        <a-button type="primary" @click="openCreate">新建 API Key</a-button>
      </div>
    </div>

    <div class="key-summary-grid">
      <div v-for="item in keySummaryCards" :key="item.label" class="summary-card">
        <span class="summary-label">{{ item.label }}</span>
        <strong class="summary-value">{{ item.value }}</strong>
        <span class="summary-hint">{{ item.hint }}</span>
      </div>
    </div>
    <a-table class="page-table api-keys-table" size="middle" :columns="columns" :data-source="apiKeys" row-key="id" :loading="loading" :scroll="{ x: 1200 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组，随后即可复制完整密钥。" />
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
      <a-alert message="明文密钥已保存，列表中也会直接显示完整值。" type="info" show-icon />
      <a-input-group compact class="created-key">
        <a-input :value="createdKey" readonly style="width: calc(100% - 88px)" />
        <a-button type="primary" @click="copyCreatedKey">复制</a-button>
      </a-input-group>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { CopyOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import type { ApiKeySummary, GroupSummary } from '@/types/domain'

const loading = ref(false)
const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const editingId = ref<string>()
const createdKey = ref('')
const apiKeys = ref<ApiKeySummary[]>([])
const groups = ref<GroupSummary[]>([])
const form = reactive({ name: '', groupId: '', status: 'active' as 'active' | 'disabled', expiresAt: undefined as Dayjs | undefined })

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '秘钥', key: 'key', width: 180 },
  { title: '绑定分组', key: 'group', width: 220 },
  { title: '状态', key: 'status', width: 100 },
  { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
  { title: '操作', key: 'actions', width: 140 }
]

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const groupOptions = computed(() => groups.value.map((group) => ({ label: group.name, value: group.id })))

const keySummaryCards = computed(() => {
  const total = apiKeys.value.length
  const active = apiKeys.value.filter((item) => item.status === 'active').length
  const disabled = apiKeys.value.filter((item) => item.status === 'disabled').length
  const groupCount = new Set(apiKeys.value.map((item) => item.groupId)).size
  return [
    { label: '总密钥', value: String(total), hint: '当前系统保存的 API Key 总数' },
    { label: '启用中', value: String(active), hint: '状态为 active 的密钥' },
    { label: '已停用', value: String(disabled), hint: '状态为 disabled 的密钥' },
    { label: '绑定分组', value: String(groupCount), hint: '当前被 API Key 使用的分组数' }
  ]
})

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
    const [keyList, groupList] = await Promise.all([api.apiKeys.list(), api.groups.list()])
    apiKeys.value = keyList
    groups.value = groupList
  } catch (error) {
    console.error(error)
    message.error('加载 API Key 失败')
  } finally {
    loading.value = false
  }
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

async function copyCreatedKey() {
  await copyText(createdKey.value)
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

.key-summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
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

@media (max-width: 992px) {
  .key-summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 768px) {
  .key-summary-grid {
    grid-template-columns: 1fr;
  }
}
</style>
