<template>
  <a-card class="page-card system-account-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新增系统账户</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table" :columns="columns" :data-source="accounts" row-key="id" :loading="loading" :scroll-x="1080" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'role'">
          <a-tag :color="record.role === 'admin' ? 'geekblue' : 'default'">{{ record.role === 'admin' ? '管理员' : '用户' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'red'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'mustChangePassword'">
          <a-tag :color="record.mustChangePassword ? 'warning' : 'success'">{{ record.mustChangePassword ? '提醒' : '不提醒' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'lastLoginAt'">
          <span class="muted-cell">{{ formatDateTime(record.lastLoginAt) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="systemAccountActions" @action-click="handleSystemAccountAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.username }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.role === 'admin' ? 'geekblue' : 'default'">{{ record.role === 'admin' ? '管理员' : '用户' }}</a-tag>
              <a-tag :color="record.status === 'active' ? 'green' : 'red'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
              <a-tag :color="record.mustChangePassword ? 'warning' : 'success'">{{ record.mustChangePassword ? '提醒改密' : '不提醒改密' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>显示名称</span>
              <strong>{{ record.displayName }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最后登录</span>
              <strong>{{ formatDateTime(record.lastLoginAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="systemAccountActions" @action-click="handleSystemAccountAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑系统账户' : '新增系统账户'" :confirm-loading="systemAccountSaving" :ok-button-props="{ disabled: systemAccountSaving }" @ok="handleSave">
      <a-form layout="vertical">
        <a-form-item label="用户名" required>
          <a-input v-model:value="form.username" :disabled="Boolean(editingId)" placeholder="例如 user01" />
        </a-form-item>
        <a-form-item label="显示名称" required>
          <a-input v-model:value="form.displayName" placeholder="例如 业务用户" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" placeholder="可选，填写账户用途或归属说明" />
        </a-form-item>
        <a-form-item v-if="!editingId" label="初始密码" required>
          <a-input-password v-model:value="form.password" placeholder="请输入初始密码" />
        </a-form-item>
        <a-form-item label="角色">
          <a-select v-model:value="form.role" :options="roleOptions" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="form.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="登录后提醒改密">
          <a-switch v-model:checked="form.mustChangePassword" checked-children="是" un-checked-children="否" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="passwordModalOpen" title="重置系统账户密码" :confirm-loading="resetPasswordSaving" :ok-button-props="{ disabled: resetPasswordSaving }" @ok="handleResetPassword">
      <a-form layout="vertical">
        <a-form-item label="新密码" required>
          <a-input-password v-model:value="resetPassword" placeholder="请输入新密码" />
        </a-form-item>
        <a-alert type="info" show-icon message="保存后该账户下次登录会收到修改密码提醒。" />
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from '@/lib/antd'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useSubmitAction } from '@/composables/useSubmitAction'
import type { SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '@/types/domain'

const loading = ref(false)
const { submitAction, submittingRef } = useSubmitAction('system-accounts')
const systemAccountSaving = submittingRef('system_accounts.save')
const resetPasswordSaving = submittingRef('system_accounts.reset_password')
const modalOpen = ref(false)
const passwordModalOpen = ref(false)
const editingId = ref<string>()
const resettingId = ref<string>()
const resetPassword = ref('')
const accounts = ref<SystemAccountSummary[]>([])

const form = reactive({
  username: '',
  displayName: '',
  description: '',
  password: '',
  role: 'user' as SystemAccountRole,
  status: 'active' as SystemAccountStatus,
  mustChangePassword: true
})

const roleOptions = [
  { label: '管理员', value: 'admin' },
  { label: '用户', value: 'user' }
]

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const columns = [
  { title: '用户名', dataIndex: 'username', key: 'username', width: 160 },
  { title: '显示名称', dataIndex: 'displayName', key: 'displayName', width: 180 },
  { title: '角色', key: 'role', width: 110 },
  { title: '状态', key: 'status', width: 100 },
  { title: '改密提醒', key: 'mustChangePassword', width: 110 },
  { title: '最后登录', key: 'lastLoginAt', width: 180 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', width: 100, fixed: 'right' }
]

const systemAccountActions: RowActionItem[] = [
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  { key: 'reset-password', label: '重置密码', icon: 'password', tone: 'warning' }
]

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { username: '', displayName: '', description: '', password: '', role: 'user', status: 'active', mustChangePassword: true })
  modalOpen.value = true
}

function openEdit(record: SystemAccountSummary) {
  editingId.value = record.id
  Object.assign(form, {
    username: record.username,
    displayName: record.displayName,
    description: record.description ?? '',
    password: '',
    role: record.role,
    status: record.status,
    mustChangePassword: record.mustChangePassword
  })
  modalOpen.value = true
}

function openResetPassword(record: SystemAccountSummary) {
  resettingId.value = record.id
  resetPassword.value = ''
  passwordModalOpen.value = true
}

function handleSystemAccountAction(key: string, record: SystemAccountSummary) {
  if (key === 'edit') {
    openEdit(record)
    return
  }
  if (key === 'reset-password') {
    openResetPassword(record)
  }
}

const handleSave = submitAction('system_accounts.save', async () => {
  const username = form.username.trim()
  const displayName = form.displayName.trim()
  if (!username || !displayName) {
    message.warning('请填写用户名和显示名称')
    return
  }
  if (!editingId.value && hasDuplicateUsername(username)) {
    message.warning('用户账户已存在')
    return
  }
  if (hasDuplicateDisplayName(displayName, editingId.value)) {
    message.warning('用户名称已存在')
    return
  }
  if (!editingId.value && form.password.length < 4) {
    message.warning('初始密码至少 4 位')
    return
  }
  try {
    const basePayload = {
      displayName,
      description: form.description,
      role: form.role,
      status: form.status,
      mustChangePassword: form.mustChangePassword
    }
    if (editingId.value) {
      await api.systemAccounts.update(editingId.value, basePayload)
      message.success('系统账户已更新')
    } else {
      const payload = { ...basePayload, username, password: form.password }
      await api.systemAccounts.create(payload)
      message.success('系统账户已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存系统账户失败'))
  } finally {
  }
})

const handleResetPassword = submitAction('system_accounts.reset_password', async () => {
  if (!resettingId.value || resetPassword.value.length < 4) {
    message.warning('新密码至少 4 位')
    return
  }
  try {
    await api.systemAccounts.update(resettingId.value, { password: resetPassword.value, mustChangePassword: true })
    message.success('密码已重置')
    passwordModalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('重置密码失败')
  } finally {
  }
})

async function loadData() {
  loading.value = true
  try {
    accounts.value = await api.systemAccounts.list()
  } catch (error) {
    console.error(error)
    message.error('加载系统账户失败')
  } finally {
    loading.value = false
  }
}

function formatDateTime(value?: string): string {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
}

function hasDuplicateUsername(username: string): boolean {
  const normalized = username.toLocaleLowerCase()
  return accounts.value.some((account) => account.username.toLocaleLowerCase() === normalized)
}

function hasDuplicateDisplayName(displayName: string, excludeId?: string): boolean {
  const normalized = displayName.toLocaleLowerCase()
  return accounts.value.some((account) => account.id !== excludeId && account.displayName.toLocaleLowerCase() === normalized)
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return fallback
}

onMounted(loadData)
</script>

<style scoped>
.system-account-card {
  margin-top: 4px;
}
</style>
