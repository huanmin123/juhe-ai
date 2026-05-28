<template>
  <a-card class="page-card system-account-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keyword" search-placeholder="搜索用户名称" :show-reset="Boolean(keyword.trim())" :refresh-loading="loading" @search="searchAccounts" @reset="resetSearch" @refresh="refreshAccounts">
      <template #actions>
        <a-button type="primary" @click="openCreate">新增系统账户</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table" :columns="columns" :data-source="accounts" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="1190" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileAccounts" @mobile-refresh="refreshMobileAccounts">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'role'">
          <a-tag :color="record.role === 'admin' ? 'geekblue' : 'default'">{{ record.role === 'admin' ? '管理员' : '用户' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'red'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'imageGenerationEnabled'">
          <a-tag :color="record.imageGenerationEnabled ? 'green' : 'default'">{{ record.imageGenerationEnabled ? '支持' : '不支持' }}</a-tag>
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
              <a-tag :color="record.imageGenerationEnabled ? 'green' : 'default'">{{ record.imageGenerationEnabled ? '支持图像' : '禁用图像' }}</a-tag>
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
        <a-form-item label="支持图像生成">
          <a-switch v-model:checked="form.imageGenerationEnabled" checked-children="支持" un-checked-children="不支持" />
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
import { message } from '@/lib/antd'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import type { SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '@/types/domain'

const pageSize = 20
const { submitAction, submittingRef } = useSubmitAction('system-accounts')
const systemAccountSaving = submittingRef('system_accounts.save')
const resetPasswordSaving = submittingRef('system_accounts.reset_password')
const modalOpen = ref(false)
const passwordModalOpen = ref(false)
const editingId = ref<string>()
const resettingId = ref<string>()
const resetPassword = ref('')
const keyword = ref('')

const form = reactive({
  username: '',
  displayName: '',
  description: '',
  password: '',
  role: 'user' as SystemAccountRole,
  status: 'active' as SystemAccountStatus,
  mustChangePassword: true,
  imageGenerationEnabled: false
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
  { title: '图像生成', key: 'imageGenerationEnabled', width: 110 },
  { title: '改密提醒', key: 'mustChangePassword', width: 110 },
  { title: '最后登录', key: 'lastLoginAt', width: 180 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', actionCount: 2, fixed: 'right' }
]

const systemAccountActions: RowActionItem[] = [
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  { key: 'reset-password', label: '重置密码', icon: 'password', tone: 'warning' }
]

const {
  items: accounts,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileAccounts,
  refreshMobile: refreshMobileAccounts,
  resetPagination
} = useResponsivePagedList<SystemAccountSummary>({
  pageSize,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? total - 1)} 个系统账户，还有更多`
    : `共 ${formatNumber(total)} 个系统账户`,
  fetchPage: async (_options, pageState) => api.systemAccounts.listPage({
    keyword: keyword.value.trim() || undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }),
  onError: (error) => {
    console.error(error)
    message.error('加载系统账户失败')
  }
})

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { username: '', displayName: '', description: '', password: '', role: 'user', status: 'active', mustChangePassword: true, imageGenerationEnabled: false })
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
    mustChangePassword: record.mustChangePassword,
    imageGenerationEnabled: record.imageGenerationEnabled
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
      mustChangePassword: form.mustChangePassword,
      imageGenerationEnabled: form.imageGenerationEnabled
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
    resetPagination()
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
    message.error(extractApiErrorMessage(error, '重置密码失败'))
  } finally {
  }
})

function formatDateTime(value?: string): string {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-'
}

function searchAccounts() {
  resetPagination()
  void loadData()
}

function resetSearch() {
  keyword.value = ''
  resetPagination()
  void loadData()
}

function refreshAccounts() {
  void loadData()
}

onMounted(loadData)
</script>

<style scoped>
.system-account-card {
  margin-top: 4px;
}
</style>
