<template>
  <a-card class="page-card system-account-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新增系统账户</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table" :columns="columns" :data-source="accounts" row-key="id" :loading="loading" :scroll-x="1050" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
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
        <template v-else-if="column.key === 'actions'">
          <a-space :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-button type="link" size="small" @click="openResetPassword(record)">重置密码</a-button>
          </a-space>
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
          </div>
          <div class="mobile-list-card-actions two-actions">
            <a-button type="primary" @click="openEdit(record)">编辑</a-button>
            <a-button @click="openResetPassword(record)">重置密码</a-button>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑系统账户' : '新增系统账户'" :confirm-loading="saving" @ok="handleSave">
      <a-form layout="vertical">
        <a-form-item label="用户名" required>
          <a-input v-model:value="form.username" placeholder="例如 user01" />
        </a-form-item>
        <a-form-item label="显示名称" required>
          <a-input v-model:value="form.displayName" placeholder="例如 业务用户" />
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

    <a-modal v-model:open="passwordModalOpen" title="重置系统账户密码" :confirm-loading="saving" @ok="handleResetPassword">
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
import { message } from 'ant-design-vue'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '@/types/domain'

const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const passwordModalOpen = ref(false)
const editingId = ref<string>()
const resettingId = ref<string>()
const resetPassword = ref('')
const accounts = ref<SystemAccountSummary[]>([])

const form = reactive({
  username: '',
  displayName: '',
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
  { title: '操作', key: 'actions', width: 180, fixed: 'right' }
]

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { username: '', displayName: '', password: '', role: 'user', status: 'active', mustChangePassword: true })
  modalOpen.value = true
}

function openEdit(record: SystemAccountSummary) {
  editingId.value = record.id
  Object.assign(form, {
    username: record.username,
    displayName: record.displayName,
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

async function handleSave() {
  if (!form.username.trim() || !form.displayName.trim()) {
    message.warning('请填写用户名和显示名称')
    return
  }
  if (!editingId.value && form.password.length < 4) {
    message.warning('初始密码至少 4 位')
    return
  }
  saving.value = true
  try {
    const payload = {
      username: form.username.trim(),
      displayName: form.displayName.trim(),
      role: form.role,
      status: form.status,
      mustChangePassword: form.mustChangePassword,
      ...(!editingId.value ? { password: form.password } : {})
    }
    if (editingId.value) {
      await api.systemAccounts.update(editingId.value, payload)
      message.success('系统账户已更新')
    } else {
      await api.systemAccounts.create(payload)
      message.success('系统账户已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('保存系统账户失败')
  } finally {
    saving.value = false
  }
}

async function handleResetPassword() {
  if (!resettingId.value || resetPassword.value.length < 4) {
    message.warning('新密码至少 4 位')
    return
  }
  saving.value = true
  try {
    await api.systemAccounts.update(resettingId.value, { password: resetPassword.value, mustChangePassword: true })
    message.success('密码已重置')
    passwordModalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('重置密码失败')
  } finally {
    saving.value = false
  }
}

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

onMounted(loadData)
</script>

<style scoped>
.system-account-card {
  margin-top: 4px;
}
</style>
