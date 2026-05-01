<template>
  <a-card class="page-card groups-page-card" title="分组">
    <div class="page-toolbar">
      <span class="toolbar-note">分组绑定账户，API Key 绑定分组。</span>
      <div class="page-toolbar-actions">
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </div>
    </div>

    <div class="group-summary-grid">
      <div v-for="item in groupSummaryCards" :key="item.label" class="summary-card">
        <span class="summary-label">{{ item.label }}</span>
        <strong class="summary-value">{{ item.value }}</strong>
        <span class="summary-hint">{{ item.hint }}</span>
      </div>
    </div>

    <a-table class="page-table groups-table" size="middle" :columns="columns" :data-source="groups" row-key="id" :loading="loading" :scroll="{ x: 1220 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建一个分组，再把 OpenAI 账户绑定进来。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountCount'">
          <a-tag color="blue">{{ record.accountIds.length }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accounts'">
          <a-space wrap>
            <a-tag v-for="accountId in record.accountIds" :key="accountId" color="cyan">{{ accountName(accountId) }}</a-tag>
            <span v-if="record.accountIds.length === 0">-</span>
          </a-space>
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
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="bindOpen" title="绑定账户" width="640px" :ok-button-props="{ type: 'primary' }" @ok="saveBindings">
      <a-select v-model:value="selectedAccountIds" mode="multiple" style="width: 100%" placeholder="选择账户" :options="accountOptions" />
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, GroupSummary } from '@/types/domain'

const loading = ref(false)
const modalOpen = ref(false)
const bindOpen = ref(false)
const editingId = ref<string>()
const bindingGroupId = ref<string>()
const groups = ref<GroupSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const selectedAccountIds = ref<string[]>([])
const form = reactive({ name: '', description: '', enabled: true })

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '状态', key: 'status' },
  { title: '账户数', key: 'accountCount' },
  { title: '绑定账户', key: 'accounts' },
  { title: '说明', dataIndex: 'description', key: 'description' },
  { title: '操作', key: 'actions', width: 220 }
]

const accountOptions = computed(() => accounts.value.map((account) => ({ label: `${account.name} (${account.type})`, value: account.id })))

const groupSummaryCards = computed(() => {
  const total = groups.value.length
  const enabled = groups.value.filter((group) => group.enabled).length
  const disabled = groups.value.filter((group) => !group.enabled).length
  const totalBindings = groups.value.reduce((sum, group) => sum + group.accountIds.length, 0)
  return [
    { label: '总分组', value: String(total), hint: '当前系统中的分组数量' },
    { label: '启用中', value: String(enabled), hint: '状态为 enabled 的分组' },
    { label: '已停用', value: String(disabled), hint: '状态为 disabled 的分组' },
    { label: '账户绑定', value: String(totalBindings), hint: '全部分组已绑定的账户数总和' }
  ]
})

function accountName(accountId: string) {
  return accounts.value.find((account) => account.id === accountId)?.name ?? accountId
}

async function loadData() {
  loading.value = true
  try {
    const [groupList, accountList] = await Promise.all([api.groups.list(), api.accounts.list()])
    groups.value = groupList
    accounts.value = accountList
  } catch (error) {
    console.error(error)
    message.error('加载分组失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { name: '', description: '', enabled: true })
  modalOpen.value = true
}

function openEdit(group: GroupSummary) {
  editingId.value = group.id
  Object.assign(form, { name: group.name, description: group.description ?? '', enabled: group.enabled })
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
    message.success('账户绑定已更新')
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

.group-summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.groups-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.groups-table :deep(.ant-empty) {
  margin: 12px 0;
}

@media (max-width: 992px) {
  .group-summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 768px) {
  .group-summary-grid {
    grid-template-columns: 1fr;
  }
}
</style>
