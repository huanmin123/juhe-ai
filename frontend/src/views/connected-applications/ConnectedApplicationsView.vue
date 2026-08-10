<template>
  <a-card class="page-card responsive-page-card connected-applications-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索已授权应用"
      :refresh-loading="loading"
      :show-filters="false"
      @reset="resetFilters"
      @refresh="loadConnectedApplications"
    />

    <ResponsiveDataList
      table-class="page-table connected-application-table"
      :columns="columns"
      :data-source="filteredApplications"
      row-key="clientId"
      :loading="loading"
      :scroll-x="1280"
      pull-refresh-enabled
      :refreshing="loading"
      @mobile-refresh="loadConnectedApplications"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前账户尚未向第三方应用授予访问权限。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'application'">
          <div class="application-name-cell">
            <div class="application-name-line">
              <strong>{{ record.displayName }}</strong>
              <a v-if="record.websiteUrl" :href="record.websiteUrl" target="_blank" rel="noopener noreferrer">官网</a>
            </div>
            <span class="application-client-id" :title="record.clientId">{{ record.clientId }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tooltip v-if="record.statusReason" :title="record.statusReason">
            <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
          </a-tooltip>
          <a-tag v-else :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'scopes'">
          <div class="scope-list">
            <a-tag v-for="scope in record.scopes" :key="scope">{{ scope }}</a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'authorizedAt'">
          {{ formatDateTime(authorizedAt(record)) }}
        </template>
        <template v-else-if="column.key === 'expiresAt'">
          {{ formatDateTime(record.expiresAt) }}
        </template>
        <template v-else-if="column.key === 'lastTokenRotatedAt'">
          {{ formatDateTime(record.lastTokenRotatedAt) }}
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          {{ formatDateTime(record.lastUsedAt) }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="applicationActions(record)" @action-click="revokeConnectedApplication(record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.displayName }}</div>
            <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>授权时间</span>
              <strong>{{ formatDateTime(authorizedAt(record)) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>到期时间</span>
              <strong>{{ formatDateTime(record.expiresAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近轮换</span>
              <strong>{{ formatDateTime(record.lastTokenRotatedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近使用</span>
              <strong>{{ formatDateTime(record.lastUsedAt) }}</strong>
            </div>
          </div>
          <div class="mobile-list-note">
            <span>Scope</span>
            <div class="scope-list">
              <a-tag v-for="scope in record.scopes" :key="scope">{{ scope }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="applicationActions(record)" @action-click="revokeConnectedApplication(record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import type {
  OAuthConnectedApplicationListResult,
  OAuthConnectedApplicationStatus,
  OAuthConnectedApplicationSummary
} from '@/types/domain'

const columns = [
  { title: '已授权应用', key: 'application', width: 240, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 110, align: 'left' },
  { title: 'Scope', key: 'scopes', width: 320, align: 'left' },
  { title: '授权时间', key: 'authorizedAt', width: 180, align: 'left' },
  { title: '硬到期时间', key: 'expiresAt', width: 180, align: 'left' },
  { title: '最近 Token 轮换', key: 'lastTokenRotatedAt', width: 180, align: 'left' },
  { title: '最近使用', key: 'lastUsedAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 76, fixed: 'right', align: 'left' }
]

const loading = ref(false)
const keyword = ref('')
const applications = ref<OAuthConnectedApplicationSummary[]>([])
const revokingClientId = ref('')
let listRequestId = 0

const filteredApplications = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return applications.value
  return applications.value.filter((application) => (
    application.displayName.toLowerCase().includes(text)
    || application.clientId.toLowerCase().includes(text)
  ))
})

onMounted(() => {
  void loadConnectedApplications()
})

async function loadConnectedApplications(): Promise<void> {
  const requestId = ++listRequestId
  loading.value = true
  try {
    const result = await api.oauthApplications.listConnectedApplications()
    if (requestId !== listRequestId) return
    applications.value = connectedApplicationItems(result)
  } catch (error) {
    if (requestId !== listRequestId) return
    message.error(extractApiErrorMessage(error, '加载已授权应用失败'))
  } finally {
    if (requestId === listRequestId) loading.value = false
  }
}

function resetFilters(): void {
  keyword.value = ''
}

async function revokeConnectedApplication(record: OAuthConnectedApplicationSummary): Promise<void> {
  if (revokingClientId.value || record.status !== 'active') return
  revokingClientId.value = record.clientId
  try {
    await api.oauthApplications.revokeConnectedApplication(record.clientId)
    message.success(`已终止“${record.displayName}”的全部授权`)
    await loadConnectedApplications()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '终止应用授权失败'))
  } finally {
    revokingClientId.value = ''
  }
}

function applicationActions(record: OAuthConnectedApplicationSummary): RowActionItem[] {
  return [{
    key: 'revoke',
    label: '终止授权',
    icon: 'revoke',
    tone: 'danger',
    disabled: Boolean(revokingClientId.value) || record.status !== 'active',
    confirmTitle: `确认终止“${record.displayName}”的全部授权？该应用持有的访问令牌会立即失效。`,
    confirmOkText: '终止授权'
  }]
}

function connectedApplicationItems(
  result: OAuthConnectedApplicationListResult | OAuthConnectedApplicationSummary[]
): OAuthConnectedApplicationSummary[] {
  return Array.isArray(result) ? result : result.items
}

function authorizedAt(record: OAuthConnectedApplicationSummary): string | undefined {
  return record.authorizedAt ?? record.createdAt
}

function statusLabel(status: OAuthConnectedApplicationStatus): string {
  if (status === 'active') return '有效'
  if (status === 'disabled') return '应用已停用'
  if (status === 'expired') return '已到期'
  if (status === 'revoked') return '已终止'
  return '已失效'
}

function statusColor(status: OAuthConnectedApplicationStatus): string {
  if (status === 'active') return 'green'
  if (status === 'expired') return 'orange'
  return 'red'
}
</script>

<style scoped>
.application-name-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.application-name-line {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.application-name-line strong,
.application-client-id {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.application-name-line a {
  flex: none;
  font-size: 12px;
}

.application-client-id {
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.scope-list {
  display: flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
}

.scope-list :deep(.ant-tag) {
  margin-inline-end: 0;
  overflow-wrap: anywhere;
  white-space: normal;
}
</style>
