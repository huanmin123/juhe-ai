<template>
  <a-card class="page-card responsive-page-card external-source-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索来源名称"
      filter-title="公开接口授权筛选"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @search="applyFilters"
      @reset="resetFilters"
      @refresh="loadData"
    >
      <template #inline-filters>
        <a-select
          v-model:value="statusFilter"
          class="toolbar-select external-source-status responsive-list-inline-filter"
          :disabled="loading"
          :options="statusOptions"
          @change="applyFilters"
        />
      </template>
      <template #actions>
        <a-button @click="openApiDocs">
          <template #icon><book-outlined /></template>
          接入文档
        </a-button>
        <a-button type="primary" @click="openCreateSource">新增来源</a-button>
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="状态">
            <a-select v-model:value="statusFilter" :disabled="loading" :options="statusOptions" @change="applyFilters" />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table external-source-table"
      :columns="columns"
      :data-source="rows"
      row-key="id"
      :loading="loading"
      :pagination="tablePagination"
      :scroll-x="1180"
      @change="handleTableChange"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无公开接口来源系统。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'source'">
          <div class="source-name-cell">
            <strong>{{ record.name }}</strong>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'tokens'">
          <button class="link-button" type="button" @click="openTokenList(record)">
            {{ record.activeTokenCount }}/{{ record.tokenCount }}
          </button>
        </template>
        <template v-else-if="column.key === 'scopes'">
          <div class="tag-line">
            <a-tag v-for="scope in record.scopes" :key="scope">{{ scopeLabel(scope) }}</a-tag>
            <span v-if="!record.scopes.length" class="muted-cell">未授权</span>
          </div>
        </template>
        <template v-else-if="column.key === 'rateLimits'">
          <span>{{ formatRateLimits(record.rateLimits) }}</span>
        </template>
        <template v-else-if="column.key === 'expiresAt'">
          <span :class="record.expiresAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.expiresAt) }}</span>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          <span :class="record.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.lastUsedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="sourceActions(record)" @action-click="handleSourceAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div>
              <div class="mobile-list-card-title">{{ record.name }}</div>
            </div>
            <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>Token</span>
              <strong>{{ record.activeTokenCount }}/{{ record.tokenCount }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>限频</span>
              <strong>{{ formatRateLimits(record.rateLimits) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>到期</span>
              <strong>{{ formatDateTime(record.expiresAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近调用</span>
              <strong>{{ formatDateTime(record.lastUsedAt) }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="sourceActions(record)" @action-click="handleSourceAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="apiDocsOpen" title="公开接口接入文档" width="1080px" :footer="null">
      <a-spin :spinning="apiDocsLoading">
        <div v-if="apiCatalog" class="api-doc-layout">
          <aside class="api-doc-sidebar">
            <a-input-search
              v-model:value="apiDocKeyword"
              allow-clear
              placeholder="搜索接口名称"
            />
            <div class="test-token-box">
              <div class="test-token-head">
                <span>{{ apiCatalog.testTokenName }}</span>
                <a-button size="small" @click="copyTestToken">
                  <template #icon><copy-outlined /></template>
                  复制
                </a-button>
              </div>
              <code>{{ apiCatalog.testToken }}</code>
            </div>
            <div class="api-doc-list">
              <button
                v-for="item in filteredApiDocs"
                :key="item.id"
                class="api-doc-list-item"
                :class="{ active: selectedApiDoc?.id === item.id }"
                type="button"
                @click="selectApiDoc(item.id)"
              >
                <span class="api-doc-list-title">{{ item.name }}</span>
                <span class="api-doc-list-path">{{ item.method }} {{ item.path }}</span>
              </button>
              <a-empty v-if="!filteredApiDocs.length" image="simple" description="没有匹配的接口。" />
            </div>
          </aside>
          <section v-if="selectedApiDoc" class="api-doc-detail">
            <div class="api-doc-detail-head">
              <div>
                <div class="api-doc-title-line">
                  <a-tag color="blue">{{ selectedApiDoc.method }}</a-tag>
                  <h3>{{ selectedApiDoc.name }}</h3>
                  <a-tag :color="apiStatusColor(selectedApiDoc.status)">{{ apiStatusText(selectedApiDoc.status) }}</a-tag>
                </div>
                <p>{{ selectedApiDoc.summary }}</p>
              </div>
              <a-button type="primary" @click="copyCurl(selectedApiDoc)">
                <template #icon><copy-outlined /></template>
                复制 curl
              </a-button>
            </div>

            <a-descriptions bordered size="small" :column="1">
              <a-descriptions-item label="调用地址">
                <code>{{ buildApiDocUrl(selectedApiDoc) }}</code>
              </a-descriptions-item>
              <a-descriptions-item label="认证方式">
                <code>Authorization: Bearer &lt;source_token&gt;</code>
              </a-descriptions-item>
            </a-descriptions>

            <div class="api-doc-section">
              <h4>请求头</h4>
              <div class="api-doc-field-table">
                <div class="api-doc-field-row head">
                  <span>名称</span>
                  <span>必填</span>
                  <span>说明</span>
                  <span>示例</span>
                </div>
                <div v-for="header in selectedApiDoc.headers" :key="header.name" class="api-doc-field-row">
                  <code>{{ header.name }}</code>
                  <span>{{ header.required ? '是' : '否' }}</span>
                  <span>{{ header.description }}</span>
                  <code>{{ header.example }}</code>
                </div>
              </div>
            </div>

            <div class="api-doc-section">
              <h4>请求参数</h4>
              <div v-if="selectedApiDoc.query.length" class="api-doc-field-table">
                <div class="api-doc-field-row head">
                  <span>名称</span>
                  <span>类型</span>
                  <span>必填</span>
                  <span>说明</span>
                </div>
                <div v-for="field in selectedApiDoc.query" :key="field.name" class="api-doc-field-row">
                  <code>{{ field.name }}</code>
                  <span>{{ field.type }}</span>
                  <span>{{ field.required ? '是' : '否' }}</span>
                  <span>{{ field.description }}</span>
                </div>
              </div>
              <span v-else class="muted-cell">无</span>
            </div>

            <div class="api-doc-section">
              <h4>请求体</h4>
              <pre v-if="selectedApiDoc.requestBody" class="api-doc-code">{{ formatJson(selectedApiDoc.requestBody.example) }}</pre>
              <span v-else class="muted-cell">无</span>
            </div>

            <div class="api-doc-section">
              <h4>响应示例</h4>
              <pre class="api-doc-code">{{ formatResponseExample(selectedApiDoc) }}</pre>
            </div>
          </section>
        </div>
      </a-spin>
    </a-modal>

    <a-modal
      v-model:open="sourceModalOpen"
      :title="editingSourceId ? '编辑来源系统' : '新增来源系统'"
      width="760px"
      :confirm-loading="sourceSaving"
      @ok="saveSource"
    >
      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="sourceForm.name" placeholder="例如 juhe-ai公益站" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="sourceForm.status" :options="sourceStatusOptions" />
        </a-form-item>
        <a-form-item label="授权能力">
          <a-select v-model:value="sourceForm.scopes" mode="multiple" :options="scopeOptions" placeholder="选择允许调用的公开接口能力" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="sourceForm.expiresAt" class="full-control" show-time allow-clear />
        </a-form-item>
        <a-form-item label="限频规则">
          <div class="rate-limit-list">
            <div v-for="(rule, index) in sourceForm.rateLimits" :key="index" class="rate-limit-row">
              <a-input-number v-model:value="rule.windowSeconds" :min="1" :max="86400" :precision="0" addon-after="秒内" />
              <a-input-number v-model:value="rule.maxRequests" :min="1" :max="100000" :precision="0" addon-after="次" />
              <a-button danger @click="removeRateLimit(index)">删除</a-button>
            </div>
            <a-button @click="addRateLimit">新增限频规则</a-button>
            <span v-if="!sourceForm.rateLimits.length" class="muted-cell">默认不限制。</span>
          </div>
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="sourceForm.notes" :rows="3" :maxlength="500" show-count />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="tokenListOpen" title="来源 Token" width="860px" :footer="null">
      <div class="modal-toolbar">
        <div>
          <strong>{{ selectedSource?.name }}</strong>
        </div>
        <a-button type="primary" @click="openCreateToken">生成 Token</a-button>
      </div>
      <a-table
        class="page-table token-table"
        row-key="id"
        :columns="tokenColumns"
        :data-source="selectedSource?.tokens ?? []"
        :pagination="false"
        :scroll="{ x: 760 }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'">
            <a-tag :color="tokenStatusColor(record.status)">{{ tokenStatusText(record.status) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'scopes'">
            <div class="tag-line">
              <a-tag v-for="scope in record.scopes" :key="scope">{{ scopeLabel(scope) }}</a-tag>
              <span v-if="!record.scopes.length" class="muted-cell">未授权</span>
            </div>
          </template>
          <template v-else-if="column.key === 'expiresAt'">
            <span :class="record.expiresAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.expiresAt) }}</span>
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            <span :class="record.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.lastUsedAt) }}</span>
          </template>
          <template v-else-if="column.key === 'actions'">
            <RowActions :actions="tokenActions(record)" @action-click="handleTokenAction($event, record)" />
          </template>
        </template>
      </a-table>
    </a-modal>

    <a-modal
      v-model:open="tokenModalOpen"
      :title="editingTokenId ? '编辑 Token' : '生成 Token'"
      width="680px"
      :confirm-loading="tokenSaving"
      :ok-text="createdTokenPlain ? '已保存' : '保存'"
      :ok-button-props="{ disabled: Boolean(createdTokenPlain) }"
      @ok="saveToken"
    >
      <a-alert
        v-if="createdTokenPlain"
        class="created-token-alert"
        type="success"
        show-icon
        message="Token 只展示这一次"
        description="请把它配置到调用方后端，不要放进前端包或公开文档。"
      />
      <a-input-group v-if="createdTokenPlain" compact class="created-token-input">
        <a-input :value="createdTokenPlain" readonly />
        <a-button @click="copyCreatedToken">复制</a-button>
      </a-input-group>

      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="tokenForm.name" placeholder="例如 公益站生产 token" :disabled="Boolean(createdTokenPlain)" />
        </a-form-item>
        <a-form-item v-if="editingTokenId" label="状态">
          <a-select v-model:value="tokenForm.status" :options="tokenStatusOptions" :disabled="Boolean(createdTokenPlain)" />
        </a-form-item>
        <a-form-item label="授权能力">
          <a-select v-model:value="tokenForm.scopes" mode="multiple" :options="scopeOptions" :disabled="Boolean(createdTokenPlain)" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="tokenForm.expiresAt" class="full-control" show-time allow-clear :disabled="Boolean(createdTokenPlain)" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { BookOutlined, CopyOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateTime } from '@/shared/formatters'
import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenStatus,
  ExternalIntegrationSourceTokenSummary,
  ExternalPublicApiDocItem,
  ExternalPublicApiStatus,
  ExternalPublicApiCatalog
} from '@/types/domain'

const pageSize = 20
const loading = ref(false)
const keyword = ref('')
const statusFilter = ref<ExternalIntegrationSourceStatus | 'all'>('all')
const rows = ref<ExternalIntegrationSourceSummary[]>([])
const paginationUpperBound = ref(0)
const pagination = reactive({ current: 1, pageSize })
const scopeOptions = ref<ExternalIntegrationScopeOption[]>([])

const apiDocsOpen = ref(false)
const apiDocsLoading = ref(false)
const apiDocKeyword = ref('')
const apiCatalog = ref<ExternalPublicApiCatalog>()
const selectedApiDocId = ref<string>()
const publicApiBaseUrl = computed(() => normalizePublicApiBaseUrl(
  (import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined)
    || (import.meta.env.DEV ? import.meta.env.VITE_JUHE_AI_BACKEND_TARGET as string | undefined : undefined)
))
const curlCommandPlatform = computed<CurlCommandPlatform>(() => detectCurlCommandPlatform())

type CurlCommandPlatform = 'windows' | 'posix'

const sourceModalOpen = ref(false)
const sourceSaving = ref(false)
const editingSourceId = ref<string>()
const sourceForm = reactive<{
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt: Dayjs | null
  notes: string
}>({
  name: '',
  status: 'active',
  scopes: [],
  rateLimits: [],
  expiresAt: null,
  notes: ''
})

const tokenListOpen = ref(false)
const selectedSource = ref<ExternalIntegrationSourceSummary>()
const tokenModalOpen = ref(false)
const tokenSaving = ref(false)
const editingTokenId = ref<string>()
const createdTokenPlain = ref('')
const tokenForm = reactive<{
  name: string
  status: ExternalIntegrationSourceTokenStatus
  scopes: string[]
  expiresAt: Dayjs | null
}>({
  name: '',
  status: 'active',
  scopes: [],
  expiresAt: null
})

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const sourceStatusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const tokenStatusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' },
  { label: '撤销', value: 'revoked' }
]

const columns = [
  { title: '来源系统', key: 'source', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 100, align: 'left' },
  { title: 'Token', key: 'tokens', width: 100, align: 'left' },
  { title: '授权能力', key: 'scopes', width: 220, align: 'left' },
  { title: '限频', key: 'rateLimits', width: 180, align: 'left' },
  { title: '到期时间', key: 'expiresAt', width: 180, align: 'left' },
  { title: '最近调用', key: 'lastUsedAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 120, fixed: 'right', align: 'left' }
]

const tokenColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '前缀', dataIndex: 'tokenPrefix', key: 'tokenPrefix', width: 120 },
  { title: '状态', key: 'status', width: 90 },
  { title: '授权能力', key: 'scopes', width: 220 },
  { title: '到期时间', key: 'expiresAt', width: 170 },
  { title: '最近调用', key: 'lastUsedAt', width: 170 },
  { title: '操作', key: 'actions', width: 80, fixed: 'right' }
]

const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (statusFilter.value !== 'all') count += 1
  return count
})

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: paginationUpperBound.value,
  showSizeChanger: true
}))

const filteredApiDocs = computed(() => {
  const keyword = apiDocKeyword.value.trim().toLowerCase()
  const items = apiCatalog.value?.items ?? []
  if (!keyword) return items
  return items.filter((item) => [
    item.name,
    item.path,
    item.summary
  ].some((value) => value.toLowerCase().includes(keyword)))
})

const selectedApiDoc = computed(() => {
  const items = filteredApiDocs.value
  if (!items.length) return undefined
  return items.find((item) => item.id === selectedApiDocId.value) ?? items[0]
})

onMounted(() => {
  void loadScopes()
  void loadData()
})

async function loadScopes(): Promise<void> {
  try {
    scopeOptions.value = await api.externalIntegrationSources.scopes()
  } catch {
    scopeOptions.value = [
      { value: 'external_integrations:source_auth_demo:read', label: '来源鉴权 demo' },
      { value: 'juhe_ai_ip_usage:read', label: 'IP 聚合读取' },
      { value: 'juhe_ai_account_push:write', label: '公开资源写入' }
    ]
  }
}

async function openApiDocs(): Promise<void> {
  apiDocsOpen.value = true
  if (apiCatalog.value) {
    selectedApiDocId.value = selectedApiDoc.value?.id ?? apiCatalog.value.items[0]?.id
    return
  }
  apiDocsLoading.value = true
  try {
    apiCatalog.value = await api.externalIntegrationSources.apiDocs()
    selectedApiDocId.value = apiCatalog.value.items[0]?.id
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载公开接口文档失败'))
  } finally {
    apiDocsLoading.value = false
  }
}

function selectApiDoc(id: string): void {
  selectedApiDocId.value = id
}

function copyTestToken(): void {
  void copyTextToClipboard(apiCatalog.value?.testToken ?? '', '测试 Token 已复制')
}

function copyCurl(item: ExternalPublicApiDocItem | undefined): void {
  void copyTextToClipboard(buildCurl(item), 'curl 已复制')
}

function buildCurl(item: ExternalPublicApiDocItem | undefined): string {
  if (!item) return ''
  const url = buildApiDocUrl(item)
  const platform = curlCommandPlatform.value
  const parts = [
    platform === 'windows' ? 'curl.exe' : 'curl',
    '-X',
    item.method,
    quoteShell(url, platform),
    '-H',
    quoteShell(`Authorization: Bearer ${apiCatalog.value?.testToken ?? '<source_token>'}`, platform)
  ]
  if (item.requestBody) {
    parts.push('-H', quoteShell(`Content-Type: ${item.requestBody.contentType}`, platform))
    parts.push('--data', quoteShell(JSON.stringify(item.requestBody.example), platform))
  }
  return parts.join(' ')
}

function buildApiDocUrl(item: ExternalPublicApiDocItem): string {
  const url = new URL(item.path, `${publicApiBaseUrl.value}/`)
  for (const field of item.query) {
    if (field.example !== undefined) {
      url.searchParams.set(field.name, String(field.example))
    }
  }
  return url.toString()
}

function quoteShell(value: string, platform: CurlCommandPlatform): string {
  if (platform === 'windows') {
    return `"${value.replace(/"/g, '""')}"`
  }
  return `'${value.replace(/'/g, "'\\''")}'`
}

function detectCurlCommandPlatform(): CurlCommandPlatform {
  if (typeof navigator === 'undefined') return 'posix'
  const userAgentData = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData
  const platform = [
    userAgentData?.platform,
    navigator.platform,
    navigator.userAgent
  ].filter(Boolean).join(' ').toLowerCase()
  return platform.includes('win') ? 'windows' : 'posix'
}

function normalizePublicApiBaseUrl(value?: string): string {
  const text = value?.trim().replace(/\/+$/, '')
  if (text && /^https?:\/\//i.test(text)) return text
  return inferPublicApiBaseUrl()
}

function inferPublicApiBaseUrl(): string {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000`
  }
  return window.location.origin
}

function formatJson(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

function formatResponseExample(item: ExternalPublicApiDocItem | undefined): string {
  return formatJson(publicResponseExample(item))
}

function publicResponseExample(item: ExternalPublicApiDocItem | undefined): unknown {
  if (!item) return {}
  if (item.id !== 'source-auth-demo') return item.responseExample
  const response = item.responseExample
  if (!isRecord(response) || !isRecord(response.data)) return response
  const { scopes: _scopes, ...publicData } = response.data
  return { ...response, data: publicData }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function apiStatusText(status: ExternalPublicApiStatus): string {
  return status === 'mock' ? 'Mock' : '可用'
}

function apiStatusColor(status: ExternalPublicApiStatus): string {
  return status === 'mock' ? 'orange' : 'green'
}

async function loadData(): Promise<void> {
  loading.value = true
  try {
    const result = await api.externalIntegrationSources.list(buildListParams())
    rows.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    paginationUpperBound.value = result.pageUpperBound
    syncSelectedSource()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载公开接口授权失败'))
  } finally {
    loading.value = false
  }
}

function buildListParams(): ExternalIntegrationSourceListParams {
  return {
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keyword.value.trim() || undefined,
    status: statusFilter.value
  }
}

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  keyword.value = ''
  statusFilter.value = 'all'
  applyFilters()
}

function handleTableChange(nextPagination: unknown): void {
  const paginationInfo = (nextPagination && typeof nextPagination === 'object' ? nextPagination : {}) as { current?: number; pageSize?: number }
  pagination.current = paginationInfo.current ?? 1
  pagination.pageSize = paginationInfo.pageSize ?? pageSize
  void loadData()
}

function openCreateSource(): void {
  editingSourceId.value = undefined
  Object.assign(sourceForm, {
    name: '',
    status: 'active',
    scopes: scopeOptions.value.map((item) => item.value),
    rateLimits: [],
    expiresAt: null,
    notes: ''
  })
  sourceModalOpen.value = true
}

function openEditSource(record: ExternalIntegrationSourceSummary): void {
  editingSourceId.value = record.id
  Object.assign(sourceForm, {
    name: record.name,
    status: record.status,
    scopes: [...record.scopes],
    rateLimits: record.rateLimits.map((rule) => ({ ...rule })),
    expiresAt: record.expiresAt ? dayjs(record.expiresAt) : null,
    notes: record.notes ?? ''
  })
  sourceModalOpen.value = true
}

async function saveSource(): Promise<void> {
  if (!sourceForm.name.trim()) {
    message.error('请填写来源名称')
    return
  }
  sourceSaving.value = true
  try {
    const payload = {
      name: sourceForm.name.trim(),
      status: sourceForm.status,
      scopes: [...sourceForm.scopes],
      rateLimits: normalizeRateLimits(sourceForm.rateLimits),
      expiresAt: sourceForm.expiresAt?.toISOString() ?? null,
      notes: sourceForm.notes.trim() || null
    }
    if (editingSourceId.value) {
      await api.externalIntegrationSources.update(editingSourceId.value, payload)
      message.success('来源系统已更新')
    } else {
      await api.externalIntegrationSources.create(payload)
      message.success('来源系统已创建')
    }
    sourceModalOpen.value = false
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '保存来源系统失败'))
  } finally {
    sourceSaving.value = false
  }
}

function addRateLimit(): void {
  sourceForm.rateLimits.push({ windowSeconds: 60, maxRequests: 10 })
}

function removeRateLimit(index: number): void {
  sourceForm.rateLimits.splice(index, 1)
}

function sourceActions(record: ExternalIntegrationSourceSummary): RowActionItem[] {
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    { key: 'tokens', label: 'Token', icon: 'settings', tone: 'info' },
    record.status === 'active'
      ? { key: 'disable', label: '停用', icon: 'disable', tone: 'danger' }
      : { key: 'enable', label: '启用', icon: 'enable', tone: 'success' }
  ]
}

function handleSourceAction(key: string, record: ExternalIntegrationSourceSummary): void {
  if (key === 'edit') {
    openEditSource(record)
    return
  }
  if (key === 'tokens') {
    openTokenList(record)
    return
  }
  if (key === 'enable' || key === 'disable') {
    void updateSourceStatus(record, key === 'enable' ? 'active' : 'disabled')
  }
}

async function updateSourceStatus(record: ExternalIntegrationSourceSummary, status: ExternalIntegrationSourceStatus): Promise<void> {
  try {
    await api.externalIntegrationSources.update(record.id, { status })
    message.success(status === 'active' ? '来源系统已启用' : '来源系统已停用')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新来源系统状态失败'))
  }
}

function openTokenList(record: ExternalIntegrationSourceSummary): void {
  selectedSource.value = record
  tokenListOpen.value = true
}

function openCreateToken(): void {
  if (!selectedSource.value) return
  editingTokenId.value = undefined
  createdTokenPlain.value = ''
  Object.assign(tokenForm, {
    name: '',
    status: 'active',
    scopes: [...selectedSource.value.scopes],
    expiresAt: selectedSource.value.expiresAt ? dayjs(selectedSource.value.expiresAt) : null
  })
  tokenModalOpen.value = true
}

function openEditToken(record: ExternalIntegrationSourceTokenSummary): void {
  editingTokenId.value = record.id
  createdTokenPlain.value = ''
  Object.assign(tokenForm, {
    name: record.name,
    status: record.status,
    scopes: [...record.scopes],
    expiresAt: record.expiresAt ? dayjs(record.expiresAt) : null
  })
  tokenModalOpen.value = true
}

async function saveToken(): Promise<void> {
  if (!selectedSource.value || !tokenForm.name.trim()) {
    message.error('请填写 Token 名称')
    return
  }
  tokenSaving.value = true
  try {
    const payload = {
      name: tokenForm.name.trim(),
      status: tokenForm.status,
      scopes: [...tokenForm.scopes],
      expiresAt: tokenForm.expiresAt?.toISOString() ?? null
    }
    if (editingTokenId.value) {
      await api.externalIntegrationSources.updateToken(selectedSource.value.id, editingTokenId.value, payload)
      message.success('Token 已更新')
      tokenModalOpen.value = false
    } else {
      const result = await api.externalIntegrationSources.createToken(selectedSource.value.id, payload)
      createdTokenPlain.value = result.token.token
      message.success('Token 已生成')
    }
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '保存 Token 失败'))
  } finally {
    tokenSaving.value = false
  }
}

function tokenActions(record: ExternalIntegrationSourceTokenSummary): RowActionItem[] {
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    record.status === 'active'
      ? { key: 'disable', label: '停用', icon: 'disable', tone: 'danger' }
      : { key: 'enable', label: '启用', icon: 'enable', tone: 'success', disabled: record.status === 'revoked' }
  ]
}

function handleTokenAction(key: string, record: ExternalIntegrationSourceTokenSummary): void {
  if (key === 'edit') {
    openEditToken(record)
    return
  }
  if ((key === 'enable' || key === 'disable') && selectedSource.value) {
    void updateTokenStatus(record, key === 'enable' ? 'active' : 'disabled')
  }
}

async function updateTokenStatus(record: ExternalIntegrationSourceTokenSummary, status: ExternalIntegrationSourceTokenStatus): Promise<void> {
  if (!selectedSource.value) return
  try {
    await api.externalIntegrationSources.updateToken(selectedSource.value.id, record.id, { status })
    message.success(status === 'active' ? 'Token 已启用' : 'Token 已停用')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新 Token 状态失败'))
  }
}

function syncSelectedSource(): void {
  if (!selectedSource.value) return
  selectedSource.value = rows.value.find((item) => item.id === selectedSource.value?.id)
}

function copyCreatedToken(): void {
  void copyTextToClipboard(createdTokenPlain.value, 'Token 已复制')
}

function normalizeRateLimits(rules: ExternalIntegrationRateLimitRule[]): ExternalIntegrationRateLimitRule[] {
  return rules
    .map((rule) => ({
      windowSeconds: Math.trunc(Number(rule.windowSeconds)),
      maxRequests: Math.trunc(Number(rule.maxRequests))
    }))
    .filter((rule) => rule.windowSeconds >= 1 && rule.maxRequests >= 1)
}

function formatRateLimits(rules: ExternalIntegrationRateLimitRule[]): string {
  return rules.length ? rules.map((rule) => `${rule.windowSeconds}s/${rule.maxRequests}次`).join('，') : '不限制'
}

function sourceStatusText(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? '启用' : '停用'
}

function sourceStatusColor(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? 'green' : 'red'
}

function tokenStatusText(status: ExternalIntegrationSourceTokenStatus): string {
  if (status === 'revoked') return '撤销'
  return status === 'active' ? '启用' : '停用'
}

function tokenStatusColor(status: ExternalIntegrationSourceTokenStatus): string {
  if (status === 'revoked') return 'default'
  return status === 'active' ? 'green' : 'red'
}

function scopeLabel(scope: string): string {
  return scopeOptions.value.find((item) => item.value === scope)?.label ?? scope
}
</script>

<style scoped>
.external-source-card :deep(.ant-card-body) {
  min-width: 0;
}

.external-source-status {
  width: 132px;
}

.source-name-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.source-name-cell span {
  color: #64748b;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12px;
}

.tag-line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.link-button {
  border: 0;
  padding: 0;
  background: transparent;
  color: #1677ff;
  cursor: pointer;
}

.full-control {
  width: 100%;
}

.rate-limit-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rate-limit-row {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) minmax(120px, 1fr) auto;
  gap: 8px;
}

.modal-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.created-token-alert {
  margin-bottom: 12px;
}

.created-token-input {
  display: flex;
  margin-bottom: 16px;
}

.created-token-input :deep(.ant-input) {
  flex: 1;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.api-doc-layout {
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 16px;
  min-height: 620px;
}

.api-doc-sidebar {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
  border-right: 1px solid #edf1f7;
  padding-right: 16px;
}

.test-token-box {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 8px;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  padding: 10px;
  background: #f8fafc;
}

.test-token-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.test-token-box code,
.api-doc-code,
.api-doc-field-row code,
.api-doc-list-path {
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.test-token-box code {
  overflow-wrap: anywhere;
  color: #0f172a;
}

.api-doc-list {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  overflow-y: auto;
}

.api-doc-list-item {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  padding: 10px;
  background: #fff;
  cursor: pointer;
  text-align: left;
}

.api-doc-list-item.active,
.api-doc-list-item:hover {
  border-color: #1677ff;
}

.api-doc-list-title {
  color: #0f172a;
  font-weight: 600;
}

.api-doc-list-path {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.api-doc-detail {
  min-width: 0;
  overflow-y: auto;
  padding-right: 4px;
}

.api-doc-detail-head,
.api-doc-title-line {
  display: flex;
  align-items: center;
  gap: 8px;
}

.api-doc-detail-head {
  justify-content: space-between;
  margin-bottom: 14px;
}

.api-doc-detail-head p {
  margin: 6px 0 0;
  color: #64748b;
}

.api-doc-title-line h3 {
  margin: 0;
  font-size: 18px;
  line-height: 1.35;
}

.api-doc-section {
  margin-top: 16px;
}

.api-doc-section h4 {
  margin: 0 0 8px;
}

.api-doc-field-table {
  display: grid;
  gap: 1px;
  overflow: hidden;
  border: 1px solid #edf1f7;
  border-radius: 8px;
}

.api-doc-field-row {
  display: grid;
  grid-template-columns: minmax(120px, 0.9fr) 72px minmax(180px, 1.4fr) minmax(140px, 1fr);
  gap: 10px;
  padding: 9px 10px;
  background: #fff;
}

.api-doc-field-row.head {
  background: #f8fafc;
  color: #64748b;
  font-weight: 600;
}

.api-doc-code {
  overflow: auto;
  max-height: 260px;
  margin: 0;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  padding: 12px;
  background: #0f172a;
  color: #e5edf7;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (max-width: 720px) {
  .api-doc-layout {
    grid-template-columns: 1fr;
  }

  .api-doc-sidebar {
    border-right: 0;
    border-bottom: 1px solid #edf1f7;
    padding-right: 0;
    padding-bottom: 12px;
  }

  .api-doc-detail-head {
    align-items: stretch;
    flex-direction: column;
  }

  .api-doc-field-row {
    grid-template-columns: 1fr;
  }

  .rate-limit-row {
    grid-template-columns: 1fr;
  }

  .modal-toolbar {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
