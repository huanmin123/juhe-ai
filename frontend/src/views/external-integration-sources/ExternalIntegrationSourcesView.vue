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
        <a-button type="primary" @click="openCreateSource">新增授权</a-button>
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
      :scroll-x="1380"
      @change="handleTableChange"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无公开接口来源授权。" />
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
          <div class="token-preview-cell">
            <span class="token-preview" :title="tokenDisplayTitle(primaryToken(record))">{{ formatTokenPreview(primaryToken(record)) }}</span>
            <a-tooltip title="复制完整 Token">
              <span class="token-copy-button-wrap">
                <a-button
                  class="token-copy-button"
                  type="text"
                  size="small"
                  :loading="tokenCopyingKey === tokenCopyKey(record)"
                  :disabled="!primaryToken(record) || (Boolean(tokenCopyingKey) && tokenCopyingKey !== tokenCopyKey(record))"
                  @click="copyTokenPreview(record)"
                >
                  <template #icon><copy-outlined /></template>
                </a-button>
              </span>
            </a-tooltip>
          </div>
        </template>
        <template v-else-if="column.key === 'scopes'">
          <div class="scope-tag-line">
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
              <strong>{{ formatTokenPreview(primaryToken(record)) }}</strong>
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

    <a-modal
      v-model:open="apiDocsOpen"
      title="公开接口接入文档"
      width="calc(100vw - 60px)"
      wrap-class-name="api-doc-modal-wrap"
      :footer="null"
    >
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
              <div class="api-doc-actions">
                <a-button @click="exportApiMarkdown(selectedApiDoc)">
                  <template #icon><download-outlined /></template>
                  导出 Markdown
                </a-button>
                <a-button type="primary" @click="copyCurl(selectedApiDoc)">
                  <template #icon><copy-outlined /></template>
                  复制 curl
                </a-button>
              </div>
            </div>

            <a-descriptions bordered size="small" :column="1">
              <a-descriptions-item label="调用地址">
                <code>{{ buildApiDocUrl(selectedApiDoc) }}</code>
              </a-descriptions-item>
              <a-descriptions-item label="认证方式">
                <code>Authorization: Bearer &lt;source_token&gt;</code>
              </a-descriptions-item>
              <a-descriptions-item label="接口资源授权">
                <code>{{ selectedApiDoc.scope || '-' }}</code>
              </a-descriptions-item>
            </a-descriptions>

            <div class="api-doc-section">
              <h4>请求头</h4>
              <div class="api-doc-field-table">
                <div class="api-doc-field-row head">
                  <span>名称</span>
                  <span>类型</span>
                  <span>必填</span>
                  <span>说明</span>
                  <span>示例</span>
                </div>
                <div v-for="header in selectedApiDoc.headers" :key="header.name" class="api-doc-field-row">
                  <code>{{ header.name }}</code>
                  <span>HTTP Header</span>
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
                  <span>示例</span>
                </div>
                <div v-for="field in selectedApiDoc.query" :key="field.name" class="api-doc-field-row">
                  <code>{{ field.name }}</code>
                  <span>{{ field.type }}</span>
                  <span>{{ field.required ? '是' : '否' }}</span>
                  <span>{{ field.description }}</span>
                  <code>{{ formatFieldExample(field.example) }}</code>
                </div>
              </div>
              <span v-else class="muted-cell">无</span>
            </div>

            <div class="api-doc-section">
              <h4>请求体</h4>
              <template v-if="selectedApiDoc.requestBody">
                <div class="api-doc-content-type">Content-Type：<code>{{ selectedApiDoc.requestBody.contentType }}</code></div>
                <div v-if="selectedApiDoc.requestBody.fields.length" class="api-doc-field-table">
                  <div class="api-doc-field-row head">
                    <span>名称</span>
                    <span>类型</span>
                    <span>必填</span>
                    <span>说明</span>
                    <span>示例</span>
                  </div>
                  <div v-for="field in selectedApiDoc.requestBody.fields" :key="field.name" class="api-doc-field-row">
                    <code>{{ field.name }}</code>
                    <span>{{ field.type }}</span>
                    <span>{{ field.required ? '是' : '否' }}</span>
                    <span>{{ field.description }}</span>
                    <code>{{ formatFieldExample(field.example) }}</code>
                  </div>
                </div>
                <h5>请求体示例</h5>
                <pre class="api-doc-code">{{ formatJson(selectedApiDoc.requestBody.example) }}</pre>
              </template>
              <span v-else class="muted-cell">无</span>
            </div>

            <div class="api-doc-section">
              <h4>响应字段</h4>
              <div v-if="selectedApiDoc.responseFields.length" class="api-doc-field-table">
                <div class="api-doc-field-row head">
                  <span>名称</span>
                  <span>类型</span>
                  <span>必填</span>
                  <span>说明</span>
                  <span>示例</span>
                </div>
                <div v-for="field in selectedApiDoc.responseFields" :key="field.name" class="api-doc-field-row">
                  <code>{{ field.name }}</code>
                  <span>{{ field.type }}</span>
                  <span>{{ field.required ? '是' : '否' }}</span>
                  <span>{{ field.description }}</span>
                  <code>{{ formatFieldExample(field.example) }}</code>
                </div>
              </div>
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
      :title="editingSourceId ? '编辑来源授权' : '新增授权'"
      width="760px"
      :confirm-loading="sourceSaving"
      ok-text="保存"
      cancel-text="取消"
      :ok-button-props="{ disabled: sourceSaving }"
      @ok="saveSource"
    >
      <a-form layout="vertical">
        <a-form-item label="授权名称" required>
          <a-input v-model:value="sourceForm.name" placeholder="例如 公益站生产授权" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="sourceForm.status" :options="sourceStatusOptions" />
        </a-form-item>
        <a-form-item label="接口资源授权">
          <a-select v-model:value="sourceForm.scopes" mode="multiple" :options="scopeOptions" placeholder="选择允许调用的公开接口" />
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

    <a-modal
      v-model:open="createdTokenOpen"
      title="来源授权已创建"
      width="600px"
      :footer="null"
      :mask-closable="false"
      @cancel="closeCreatedTokenModal"
    >
      <div class="created-token-guide">
        <a-alert
          class="created-token-alert"
          type="success"
          show-icon
          message="生产 Token 已生成"
          description="请复制后保存到外部系统后端；后续可在列表按权限复制完整 Token，不要放进前端包或公开文档。"
        />
        <div class="created-token-guide-section">
          <span class="created-token-step-title">1. 复制 Base URL</span>
          <div class="created-token-copy-row">
            <span class="created-token-label">Base URL</span>
            <code class="created-token-value">{{ publicApiBaseUrl }}</code>
            <a-button type="text" size="small" @click="copyPublicApiBaseUrl">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
        <div class="created-token-guide-section">
          <span class="created-token-step-title">2. 保存生产 Token</span>
          <a-input-group compact class="created-token-input">
            <a-input :value="createdTokenPlain" readonly />
            <a-button type="primary" @click="copyCreatedToken">复制</a-button>
          </a-input-group>
        </div>
        <div class="created-token-guide-section">
          <span class="created-token-step-title">3. 配置请求头</span>
          <pre class="created-token-code">{{ createdTokenAuthHeader }}</pre>
          <a-button size="small" @click="copyCreatedTokenAuthHeader">
            <template #icon><copy-outlined /></template>
            复制认证头
          </a-button>
        </div>
        <div class="created-token-actions">
          <a-button @click="openApiDocs">查看接入文档</a-button>
          <a-button type="primary" @click="closeCreatedTokenModal">我已保存</a-button>
        </div>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'
import { BookOutlined, CopyOutlined, DownloadOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateTime, formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenSummary,
  ExternalPublicApiDocItem,
  ExternalPublicApiField,
  ExternalPublicApiStatus,
  ExternalPublicApiCatalog
} from '@/types/domain'

const pageSize = 20
const sourceAuthDemoScope = 'external_integrations:source_auth_demo:read'
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
const createdTokenOpen = ref(false)
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

const createdTokenPlain = ref('')
const tokenCopyingKey = ref('')
const createdTokenAuthHeader = computed(() => `Authorization: Bearer ${createdTokenPlain.value || '<source_token>'}`)

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const sourceStatusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const columns = [
  { title: '来源授权', key: 'source', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 100, align: 'left' },
  { title: 'Token', key: 'tokens', width: 220, align: 'left' },
  { title: '接口资源授权', key: 'scopes', width: 300, className: 'scope-column', align: 'left' },
  { title: '限频', key: 'rateLimits', width: 180, align: 'left' },
  { title: '到期时间', key: 'expiresAt', width: 180, align: 'left' },
  { title: '最近调用', key: 'lastUsedAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 120, fixed: 'right', align: 'left' }
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
      { value: 'external_integrations:source_auth_demo:read', label: 'GET 来源鉴权 Demo' },
      { value: 'juhe_ai_public:ip_usage:read', label: 'GET IP 维度消费聚合' },
      { value: 'juhe_ai_public:account_usage:read', label: 'GET 账号维度实际消耗聚合' }
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

function exportApiMarkdown(item: ExternalPublicApiDocItem | undefined): void {
  if (!item) return
  downloadTextFile(apiMarkdownFilename(item), buildApiMarkdown(item))
  message.success('Markdown 文档已导出')
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

function buildApiMarkdown(item: ExternalPublicApiDocItem): string {
  const lines = [
    `# ${item.name}`,
    '',
    item.summary,
    '',
    '## 基本信息',
    '',
    `- 状态：${apiStatusText(item.status)}`,
    `- 方法：\`${item.method}\``,
    `- 路径：\`${item.path}\``,
    `- 接口资源授权：\`${item.scope || '-'}\``,
    `- 调用地址：\`${buildApiDocUrl(item)}\``,
    '- 认证方式：`Authorization: Bearer <source_token>`',
    '',
    '## 请求头',
    '',
    markdownFieldTable(item.headers.map((header) => ({
      name: header.name,
      type: '-',
      required: header.required,
      description: header.description,
      example: header.example
    }))),
    '',
    '## 请求参数',
    '',
    item.query.length ? markdownFieldTable(item.query) : '无',
    '',
    '## 请求体',
    ''
  ]
  if (item.requestBody) {
    lines.push(
      `Content-Type：\`${item.requestBody.contentType}\``,
      '',
      '### 请求体字段',
      '',
      item.requestBody.fields.length ? markdownFieldTable(item.requestBody.fields) : '无字段说明',
      '',
      '### 请求体示例',
      '',
      markdownCodeBlock('json', formatJson(item.requestBody.example)),
      ''
    )
  } else {
    lines.push('无', '')
  }
  lines.push(
    '## 响应字段',
    '',
    item.responseFields.length ? markdownFieldTable(item.responseFields) : '无',
    '',
    '## 响应示例',
    '',
    markdownCodeBlock('json', formatResponseExample(item)),
    '',
    '## curl 示例',
    '',
    markdownCodeBlock(curlCommandPlatform.value === 'windows' ? 'powershell' : 'bash', buildCurl(item)),
    ''
  )
  return lines.join('\n')
}

function markdownFieldTable(fields: Array<ExternalPublicApiField | {
  name: string
  type: string
  required: boolean
  description: string
  example?: unknown
}>): string {
  const rows = [
    '| 名称 | 类型 | 必填 | 说明 | 示例 |',
    '| --- | --- | --- | --- | --- |'
  ]
  for (const field of fields) {
    rows.push([
      markdownTableCell(field.name),
      markdownTableCell(field.type),
      field.required ? '是' : '否',
      markdownTableCell(field.description),
      markdownTableCell(field.example === undefined ? '-' : field.example)
    ].join(' | ').replace(/^/, '| ').replace(/$/, ' |'))
  }
  return rows.join('\n')
}

function markdownTableCell(value: unknown): string {
  return formatFieldExample(value).replace(/\|/g, '\\|').replace(/\r?\n/g, '<br>')
}

function formatFieldExample(value: unknown): string {
  if (value === undefined || value === '') {
    return '-'
  }
  if (value === null) {
    return 'null'
  }
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }
  return formatJson(value)
}

function markdownCodeBlock(language: string, content: string): string {
  const fence = content.includes('```') ? '````' : '```'
  return `${fence}${language}\n${content}\n${fence}`
}

function apiMarkdownFilename(item: ExternalPublicApiDocItem): string {
  const base = `${item.method}-${item.path.replace(/^\/+/, '').replace(/[/?#&=]+/g, '-')}`
  const safeBase = base.replace(/[<>:"\\|*]+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  return `${safeBase || item.id}.md`
}

function downloadTextFile(filename: string, content: string): void {
  const blob = new Blob([content], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
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
  createdTokenPlain.value = ''
  createdTokenOpen.value = false
  Object.assign(sourceForm, {
    name: '',
    status: 'active',
    scopes: defaultCreateSourceScopes(),
    rateLimits: [],
    expiresAt: null,
    notes: ''
  })
  sourceModalOpen.value = true
}

function defaultCreateSourceScopes(): string[] {
  return scopeOptions.value.some((item) => item.value === sourceAuthDemoScope)
    ? [sourceAuthDemoScope]
    : []
}

function openEditSource(record: ExternalIntegrationSourceSummary): void {
  let rateLimits: ExternalIntegrationRateLimitRule[]
  let expiresAt: Dayjs | null
  try {
    rateLimits = normalizeRateLimits(record.rateLimits)
    expiresAt = parseStrictDatePickerValue(record.expiresAt, '来源授权过期时间') ?? null
  } catch (error) {
    message.error(extractApiErrorMessage(error, '来源授权数据异常，请清理后再编辑'))
    return
  }
  editingSourceId.value = record.id
  createdTokenPlain.value = ''
  createdTokenOpen.value = false
  Object.assign(sourceForm, {
    name: record.name,
    status: record.status,
    scopes: [...record.scopes],
    rateLimits,
    expiresAt,
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
      expiresAt: formatServerDateTimeInput(sourceForm.expiresAt),
      notes: sourceForm.notes.trim() || null
    }
    if (editingSourceId.value) {
      await api.externalIntegrationSources.update(editingSourceId.value, payload)
      message.success('来源授权已更新')
      sourceModalOpen.value = false
    } else {
      const result = await api.externalIntegrationSources.create(payload)
      createdTokenPlain.value = result.token.token
      sourceModalOpen.value = false
      createdTokenOpen.value = true
      message.success('来源授权已创建')
    }
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '保存来源授权失败'))
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
    record.status === 'active'
      ? { key: 'disable', label: '停用', icon: 'disable', tone: 'danger' }
      : { key: 'enable', label: '启用', icon: 'enable', tone: 'success' },
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: `确认删除来源授权“${record.name}”？`, confirmOkText: '删除' }
  ]
}

function handleSourceAction(key: string, record: ExternalIntegrationSourceSummary): void {
  if (key === 'edit') {
    openEditSource(record)
    return
  }
  if (key === 'enable' || key === 'disable') {
    void updateSourceStatus(record, key === 'enable' ? 'active' : 'disabled')
    return
  }
  if (key === 'delete') {
    void deleteSource(record)
  }
}

async function updateSourceStatus(record: ExternalIntegrationSourceSummary, status: ExternalIntegrationSourceStatus): Promise<void> {
  try {
    await api.externalIntegrationSources.update(record.id, { status })
    message.success(status === 'active' ? '来源授权已启用' : '来源授权已停用')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新来源授权状态失败'))
  }
}

async function deleteSource(record: ExternalIntegrationSourceSummary): Promise<void> {
  try {
    await api.externalIntegrationSources.delete(record.id)
    if (rows.value.length <= 1 && pagination.current > 1) {
      pagination.current -= 1
    }
    message.success('来源授权已删除')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '删除来源授权失败'))
  }
}

function copyCreatedToken(): void {
  void copyTextToClipboard(createdTokenPlain.value, 'Token 已复制')
}

function copyCreatedTokenAuthHeader(): void {
  void copyTextToClipboard(createdTokenAuthHeader.value, '认证头已复制')
}

function copyPublicApiBaseUrl(): void {
  void copyTextToClipboard(publicApiBaseUrl.value, 'Base URL 已复制')
}

function closeCreatedTokenModal(): void {
  createdTokenOpen.value = false
  createdTokenPlain.value = ''
}

async function copyTokenPreview(record: ExternalIntegrationSourceSummary): Promise<void> {
  const token = primaryToken(record)
  if (!token || tokenCopyingKey.value) return
  const copyingKey = tokenCopyKey(record)
  tokenCopyingKey.value = copyingKey
  try {
    const result = await api.externalIntegrationSources.tokenSecret(record.id, token.id)
    await copyTextToClipboard(result.token, '完整 Token 已复制')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '复制完整 Token 失败'))
  } finally {
    if (tokenCopyingKey.value === copyingKey) {
      tokenCopyingKey.value = ''
    }
  }
}

function normalizeRateLimits(rules: ExternalIntegrationRateLimitRule[]): ExternalIntegrationRateLimitRule[] {
  return rules.map((rule, index) => ({
    windowSeconds: normalizeRateLimitInteger(rule.windowSeconds, 1, 86400, `第 ${index + 1} 条限频窗口`),
    maxRequests: normalizeRateLimitInteger(rule.maxRequests, 1, 100000, `第 ${index + 1} 条限频次数`)
  }))
}

function normalizeRateLimitInteger(value: unknown, min: number, max: number, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
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

function scopeLabel(scope: string): string {
  return scopeOptions.value.find((item) => item.value === scope)?.label ?? scope
}

function formatTokenPreview(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  if (!token) return '未生成'
  return maskSecretPreview('', token.tokenPrefix, token.tokenSuffix)
}

function tokenDisplayTitle(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  return token ? '列表仅显示 Token 标识，点击复制按钮复制完整 Token' : '未生成'
}

function maskSecretPreview(value: string | undefined, prefix?: string, suffix?: string): string {
  if (value) {
    return value.length > 16 ? `${value.slice(0, 8)}...${value.slice(-8)}` : value
  }
  const head = prefix?.slice(0, 8) ?? ''
  const tail = suffix?.slice(-8) ?? ''
  if (head && tail) return `${head}...${tail}`
  if (head) return `${head}...`
  return '未生成'
}

function primaryToken(record: ExternalIntegrationSourceSummary): ExternalIntegrationSourceTokenSummary | undefined {
  return record.tokens[0]
}

function tokenCopyKey(record: ExternalIntegrationSourceSummary): string {
  const token = primaryToken(record)
  return token ? `${record.id}:${token.id}` : ''
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

.scope-tag-line {
  display: flex;
  width: 300px;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
  white-space: normal;
}

.scope-tag-line :deep(.ant-tag) {
  max-width: 100%;
  margin-inline-end: 0;
  overflow-wrap: anywhere;
  white-space: normal;
}

.external-source-table :deep(.scope-column) {
  max-width: 300px;
  white-space: normal;
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
  margin-bottom: 0;
}

.created-token-guide {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.created-token-guide-section {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 10px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 12px;
  background: #fbfdff;
}

.created-token-step-title {
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.created-token-copy-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.created-token-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.created-token-value {
  min-width: 0;
  flex: 1;
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

.created-token-input {
  display: flex;
  margin-top: 2px;
}

.created-token-input :deep(.ant-input) {
  flex: 1;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.created-token-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.created-token-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}

.token-preview-cell {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
  gap: 8px;
}

.token-preview {
  display: inline-flex;
  align-items: center;
  max-width: calc(100% - 32px);
  box-sizing: border-box;
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

.token-copy-button {
  color: #64748b;
}

.token-copy-button-wrap {
  flex: none;
}

.token-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}

:global(.api-doc-modal-wrap .ant-modal) {
  top: 30px;
  width: calc(100vw - 60px) !important;
  max-width: calc(100vw - 60px) !important;
  padding-bottom: 30px;
}

:global(.api-doc-modal-wrap .ant-modal-body) {
  max-height: none;
  overflow: hidden;
  padding: 16px 24px 24px;
}

.api-doc-layout {
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 16px;
  height: calc(100vh - 156px);
  min-height: 0;
}

.api-doc-sidebar {
  display: flex;
  min-height: 0;
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
  min-height: 0;
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

.api-doc-actions {
  display: flex;
  flex-shrink: 0;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
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

.api-doc-section h4,
.api-doc-section h5 {
  margin: 0 0 8px;
}

.api-doc-section h5 {
  margin-top: 12px;
  color: #334155;
  font-size: 13px;
}

.api-doc-content-type {
  margin-bottom: 8px;
  color: #475569;
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
  grid-template-columns: minmax(150px, 1fr) minmax(110px, 0.7fr) 64px minmax(220px, 1.5fr) minmax(140px, 1fr);
  gap: 10px;
  padding: 9px 10px;
  background: #fff;
}

.api-doc-field-row > * {
  min-width: 0;
  overflow-wrap: anywhere;
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
    height: auto;
    max-height: calc(100vh - 156px);
    overflow-y: auto;
  }

  .api-doc-sidebar {
    max-height: 380px;
    border-right: 0;
    border-bottom: 1px solid #edf1f7;
    padding-right: 0;
    padding-bottom: 12px;
  }

  .api-doc-detail {
    overflow: visible;
  }

  .api-doc-detail-head {
    align-items: stretch;
    flex-direction: column;
  }

  .api-doc-actions {
    justify-content: flex-start;
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

  .created-token-copy-row,
  .created-token-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
