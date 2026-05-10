<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keywordFilter"
      search-placeholder="搜索摘要或操作人"
      filter-title="操作日志筛选"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    >
      <template #inline-filters>
        <a-select v-model:value="moduleFilter" class="toolbar-select module-filter responsive-list-inline-filter" :options="moduleOptions" @change="applyFilters" />
        <a-select v-model:value="actionFilter" class="toolbar-select action-filter responsive-list-inline-filter" :options="actionOptions" @change="applyFilters" />
        <a-input v-model:value="traceIdFilter" allow-clear class="toolbar-select trace-filter responsive-list-inline-filter" placeholder="traceId" @press-enter="applyFilters" />
        <a-select
          v-if="isManagementView"
          v-model:value="affectedSystemAccountFilter"
          show-search
          class="toolbar-select account-filter responsive-list-inline-filter"
          :options="systemAccountOptions"
          :filter-option="filterSystemAccountOption"
          @change="applyFilters"
        />
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="模块">
            <a-select v-model:value="moduleFilter" :options="moduleOptions" />
          </a-form-item>
          <a-form-item label="动作">
            <a-select v-model:value="actionFilter" :options="actionOptions" />
          </a-form-item>
          <a-form-item label="traceId">
            <a-input v-model:value="traceIdFilter" allow-clear placeholder="输入 traceId" />
          </a-form-item>
          <template v-if="isManagementView">
            <a-form-item label="操作人">
              <a-select v-model:value="actorSystemAccountFilter" show-search :options="systemAccountOptions" :filter-option="filterSystemAccountOption" />
            </a-form-item>
            <a-form-item label="影响用户">
              <a-select v-model:value="affectedSystemAccountFilter" show-search :options="systemAccountOptions" :filter-option="filterSystemAccountOption" />
            </a-form-item>
            <a-form-item label="业务归属">
              <a-select v-model:value="operationScopeSystemAccountFilter" show-search :options="systemAccountOptions" :filter-option="filterSystemAccountOption" />
            </a-form-item>
          </template>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table operation-log-table"
      :columns="columns"
      :data-source="records"
      :mobile-data-source="records"
      row-key="id"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :scroll-x="isManagementView ? 1280 : 1060"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前条件下没有操作日志。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'summary'">
          <div class="summary-cell">
            <span>{{ record.summary }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'module'">
          <a-tag>{{ moduleText(record.module) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'actor'">
          <span class="name-cell">{{ actorText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'scope'">
          <span :class="record.operationScopeSystemAccountName || record.operationScopeSystemAccountId ? 'name-cell' : 'muted-cell'">
            {{ displayName(record.operationScopeSystemAccountName, record.operationScopeSystemAccountId) }}
          </span>
        </template>
        <template v-else-if="column.key === 'traceId'">
          <span :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId ?? '-' }}</span>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="detailActions" @action-click="openDetail(record)" />
        </template>
      </template>
      <template #card="{ record }">
        <button class="operation-log-mobile-card" type="button" @click="openDetail(record)">
          <div class="mobile-card-head">
            <span>{{ moduleText(record.module) }}</span>
            <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
          </div>
          <div class="mobile-card-meta">
            <span>{{ actorText(record) }}</span>
            <span>{{ formatDateTime(record.createdAt) }}</span>
          </div>
          <div class="mobile-card-summary">{{ record.summary }}</div>
        </button>
      </template>
    </ResponsiveDataList>

    <a-drawer v-model:open="detailOpen" width="min(920px, 96vw)" title="操作日志详情" :body-style="{ padding: '18px' }">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
            <a-descriptions-item label="动作">{{ moduleText(detail.module) }} / {{ actionText(detail.action) }}</a-descriptions-item>
            <a-descriptions-item label="操作人">{{ actorText(detail) }}</a-descriptions-item>
            <a-descriptions-item label="业务归属">{{ displayName(detail.operationScopeSystemAccountName, detail.operationScopeSystemAccountId) }}</a-descriptions-item>
            <a-descriptions-item label="资源">{{ resourceText(detail) }}</a-descriptions-item>
            <a-descriptions-item label="可见范围">{{ visibilityText(detail.visibilityScope) }}</a-descriptions-item>
            <a-descriptions-item label="请求">{{ detail.method ?? '-' }} {{ detail.path ?? '' }}</a-descriptions-item>
            <a-descriptions-item label="客户端 IP">{{ detail.clientIp ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="traceId" :span="2">{{ detail.traceId ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="摘要" :span="2">{{ detail.summary }}</a-descriptions-item>
          </a-descriptions>

          <a-tabs>
            <a-tab-pane key="changes" tab="变更内容">
              <ResponsiveDataList size="small" :pagination="false" :columns="changeColumns" :data-source="detail.changes" row-key="field" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #emptyText>
                  <a-empty description="没有字段级变更摘要。" />
                </template>
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'field'">
                    <span class="mono-cell">{{ record.field }}</span>
                  </template>
                  <template v-else-if="column.key === 'before'">
                    <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.before) }}</span>
                  </template>
                  <template v-else-if="column.key === 'after'">
                    <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.after) }}</span>
                  </template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong class="mono-cell">{{ record.field }}</strong>
                    <span>名称：{{ record.label }}</span>
                    <span>变更前：{{ valueText(record.before) }}</span>
                    <span>变更后：{{ valueText(record.after) }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
            <a-tab-pane key="targets" tab="影响对象">
              <ResponsiveDataList size="small" :pagination="false" :columns="targetColumns" :data-source="detail.targets" row-key="id" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #emptyText>
                  <a-empty description="没有额外影响对象。" />
                </template>
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'target'">{{ displayName(record.targetName, record.targetId) }}</template>
                  <template v-else-if="column.key === 'type'"><a-tag>{{ resourceTypeText(record.targetType) }}</a-tag></template>
                  <template v-else-if="column.key === 'owner'">{{ displayName(record.targetOwnerSystemAccountName, record.targetOwnerSystemAccountId) }}</template>
                  <template v-else-if="column.key === 'relation'">{{ relationText(record.relation) }}</template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong>{{ displayName(record.targetName, record.targetId) }}</strong>
                    <span>类型：{{ resourceTypeText(record.targetType) }}</span>
                    <span>归属用户：{{ displayName(record.targetOwnerSystemAccountName, record.targetOwnerSystemAccountId) }}</span>
                    <span>关系：{{ relationText(record.relation) }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
            <a-tab-pane v-if="isManagementView" key="viewers" tab="可见用户">
              <ResponsiveDataList size="small" :pagination="false" :columns="viewerColumns" :data-source="detail.viewers" row-key="systemAccountId" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'user'">{{ displayName(record.systemAccountName, record.systemAccountId) }}</template>
                  <template v-else-if="column.key === 'reason'">{{ visibilityReasonText(record.visibilityReason) }}</template>
                  <template v-else-if="column.key === 'level'">{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong>{{ displayName(record.systemAccountName, record.systemAccountId) }}</strong>
                    <span>可见原因：{{ visibilityReasonText(record.visibilityReason) }}</span>
                    <span>详情级别：{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
          </a-tabs>
        </template>
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api, type OperationLogListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { formatDateTime } from '@/shared/formatters'
import type { OperationLogChange, OperationLogDetail, OperationLogSummary, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

type OperationLogsPageState = {
  actionFilter: string
  actorSystemAccountFilter: string
  affectedSystemAccountFilter: string
  keywordFilter: string
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  pagination: { current: number; pageSize: number }
  traceIdFilter: string
}

const pageSize = 20
const defaultOperationLogsPageState = (): OperationLogsPageState => ({
  actionFilter: 'all',
  actorSystemAccountFilter: allSystemAccountsValue,
  affectedSystemAccountFilter: allSystemAccountsValue,
  keywordFilter: '',
  moduleFilter: 'all',
  operationScopeSystemAccountFilter: allSystemAccountsValue,
  pagination: { current: 1, pageSize },
  traceIdFilter: ''
})

const pageStateCache = usePageStateCache<OperationLogsPageState>(undefined, defaultOperationLogsPageState, { version: 1 })
const initialPageState = pageStateCache.read()
const { isManagementView } = useScopedMenuView()

const loading = ref(false)
const detailLoading = ref(false)
const mobileLoadingMore = ref(false)
const records = ref<OperationLogSummary[]>([])
const detail = ref<OperationLogDetail>()
const detailOpen = ref(false)
const systemAccounts = ref<SystemAccountSummary[]>([])
const systemAccountsLoaded = ref(false)
const keywordFilter = ref(initialPageState.keywordFilter)
const moduleFilter = ref(initialPageState.moduleFilter)
const actionFilter = ref(initialPageState.actionFilter)
const traceIdFilter = ref(initialPageState.traceIdFilter)
const actorSystemAccountFilter = ref(initialPageState.actorSystemAccountFilter)
const affectedSystemAccountFilter = ref(initialPageState.affectedSystemAccountFilter)
const operationScopeSystemAccountFilter = ref(initialPageState.operationScopeSystemAccountFilter)
const pagination = reactive({ current: initialPageState.pagination.current, pageSize: initialPageState.pagination.pageSize, total: 0 })

const detailActions: RowActionItem[] = [{ key: 'detail', label: '详情', icon: 'detail', tone: 'info' }]
const moduleOptions = [
  { label: '全部模块', value: 'all' },
  { label: '系统账户', value: 'system_accounts' },
  { label: 'AI 账户', value: 'accounts' },
  { label: '分组', value: 'groups' },
  { label: 'API Key', value: 'api_keys' },
  { label: '统一授权', value: 'authorizations' },
  { label: '系统团队', value: 'system_teams' },
  { label: '代理', value: 'proxies' },
  { label: '系统设置', value: 'settings' },
  { label: '公告中心', value: 'announcements' },
  { label: 'OpenAI OAuth', value: 'openai_oauth' }
]
const actionOptions = [
  { label: '全部动作', value: 'all' },
  { label: '创建', value: 'create' },
  { label: '创建账户', value: 'create_account' },
  { label: '更新', value: 'update' },
  { label: '更新有效期', value: 'update_expire' },
  { label: '更新全局设置', value: 'update_global' },
  { label: '更新系统设置', value: 'update_settings' },
  { label: '删除', value: 'delete' },
  { label: '绑定分组', value: 'bind_group' },
  { label: '流量迁移', value: 'traffic_migration' },
  { label: '撤销授权', value: 'revoke' },
  { label: '添加成员', value: 'add_members' },
  { label: '移除成员', value: 'remove_member' },
  { label: '发布', value: 'publish' },
  { label: '下线', value: 'unpublish' },
  { label: '刷新 Token', value: 'refresh_token' },
  { label: '重新授权（授权码）', value: 'reauthorize_from_code' },
  { label: '重新授权（Refresh Token）', value: 'reauthorize_from_refresh_token' },
  { label: '恢复', value: 'restore' },
  { label: '重置密码', value: 'reset_password' },
  { label: '检测', value: 'test' },
  { label: '测试改状态', value: 'test_status_changed' }
]

const systemAccountOptions = computed(() => [
  { label: '全部用户', value: allSystemAccountsValue },
  ...systemAccounts.value.map((account) => ({
    label: `${account.displayName}（${account.username}）`,
    value: account.id,
    keywords: `${account.displayName} ${account.username} ${account.id}`
  }))
])
const activeFilterCount = computed(() => {
  let count = 0
  if (keywordFilter.value.trim()) count += 1
  if (moduleFilter.value !== 'all') count += 1
  if (actionFilter.value !== 'all') count += 1
  if (traceIdFilter.value.trim()) count += 1
  if (isManagementView.value && actorSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && affectedSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && operationScopeSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  return count
})
const mobileHasMore = computed(() => records.value.length < pagination.total)
const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 条操作日志`
}))
const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '模块', key: 'module', width: 120 },
    { title: '动作', key: 'action', width: 110 },
    { title: '操作人', key: 'actor', width: 170 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '业务归属', key: 'scope', width: 170 })
  }
  baseColumns.push(
    { title: '摘要', key: 'summary', width: 300, responsiveFlex: true },
    { title: 'traceId', key: 'traceId', width: 190 },
    { title: '时间', key: 'createdAt', width: 180 },
    { title: '操作', key: 'actions', width: 90, fixed: 'right' }
  )
  return baseColumns
})
const changeColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 160 },
  { title: '名称', key: 'label', dataIndex: 'label', width: 160 },
  { title: '变更前', key: 'before', width: 240 },
  { title: '变更后', key: 'after', width: 240 }
]
const targetColumns = [
  { title: '对象', key: 'target', width: 220 },
  { title: '类型', key: 'type', width: 120 },
  { title: '归属用户', key: 'owner', width: 180 },
  { title: '关系', key: 'relation', width: 120 }
]
const viewerColumns = [
  { title: '用户', key: 'user', width: 220 },
  { title: '可见原因', key: 'reason', width: 180 },
  { title: '详情级别', key: 'level', width: 100 }
]

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function refreshRecords(): void {
  pagination.current = 1
  void loadData({ forceOptions: true })
}

function resetFilters(): void {
  const defaults = defaultOperationLogsPageState()
  keywordFilter.value = defaults.keywordFilter
  moduleFilter.value = defaults.moduleFilter
  actionFilter.value = defaults.actionFilter
  traceIdFilter.value = defaults.traceIdFilter
  actorSystemAccountFilter.value = defaults.actorSystemAccountFilter
  affectedSystemAccountFilter.value = defaults.affectedSystemAccountFilter
  operationScopeSystemAccountFilter.value = defaults.operationScopeSystemAccountFilter
  pagination.current = defaults.pagination.current
  pagination.pageSize = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadData()
}

function handleTableChange(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
  void loadData()
}

async function loadMoreMobileRecords(): Promise<void> {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await loadData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileRecords(): Promise<void> {
  pagination.current = 1
  await loadData()
}

async function loadData(options: { append?: boolean; quiet?: boolean; forceOptions?: boolean } = {}): Promise<void> {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const [result] = await Promise.all([
      fetchRecords(),
      loadSystemAccounts(options.forceOptions === true)
    ])
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    pagination.total = result.total
    records.value = options.append ? [...records.value, ...result.items] : result.items
  } catch (error) {
    console.error(error)
    message.error('加载操作日志失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

async function fetchRecords() {
  const params: OperationLogListParams = {
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keywordFilter.value.trim() || undefined,
    module: moduleFilter.value === 'all' ? undefined : moduleFilter.value,
    action: actionFilter.value === 'all' ? undefined : actionFilter.value,
    traceId: traceIdFilter.value.trim() || undefined,
    actorSystemAccountId: adminAccountFilter(actorSystemAccountFilter.value),
    affectedSystemAccountId: adminAccountFilter(affectedSystemAccountFilter.value),
    operationScopeSystemAccountId: adminAccountFilter(operationScopeSystemAccountFilter.value)
  }
  return isManagementView.value ? api.operationLogs.list(params) : api.myOperationLogs.list(params)
}

async function loadSystemAccounts(force = false): Promise<void> {
  if (!isManagementView.value) {
    systemAccounts.value = []
    systemAccountsLoaded.value = true
    return
  }
  if (!force && systemAccountsLoaded.value) return
  systemAccounts.value = await api.systemAccounts.list()
  systemAccountsLoaded.value = true
}

async function openDetail(record: OperationLogSummary): Promise<void> {
  detailOpen.value = true
  detailLoading.value = true
  try {
    detail.value = isManagementView.value ? await api.operationLogs.detail(record.id) : await api.myOperationLogs.detail(record.id)
  } catch (error) {
    console.error(error)
    message.error('加载操作日志详情失败')
  } finally {
    detailLoading.value = false
  }
}

function adminAccountFilter(value: string): string | undefined {
  return isManagementView.value && value !== allSystemAccountsValue ? value : undefined
}

function filterSystemAccountOption(input: string, option?: { label?: string; value?: string; keywords?: string }): boolean {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  return [option?.label, option?.value, option?.keywords].some((item) => String(item ?? '').toLowerCase().includes(keyword))
}

function moduleText(value: string): string {
  return moduleTextMap[value] ?? value
}

function actionText(value: string): string {
  return actionTextMap[value] ?? value
}

function actionColor(value: string): string {
  if (value.includes('delete') || value.includes('revoke') || value.includes('remove')) return 'red'
  if (value.includes('create') || value.includes('publish') || value.includes('add')) return 'green'
  if (value.includes('test') || value.includes('refresh')) return 'cyan'
  if (value.includes('password') || value.includes('restore')) return 'orange'
  return 'blue'
}

function actorText(record: OperationLogSummary): string {
  return displayName(record.actorDisplayName ?? record.actorSystemAccountName ?? record.actorUsername, record.actorSystemAccountId)
}

function displayName(name?: string, id?: string): string {
  return name || id || '-'
}

function resourceText(record: Pick<OperationLogSummary, 'resourceType' | 'resourceName' | 'resourceId'>): string {
  return `${resourceTypeText(record.resourceType)}：${displayName(record.resourceName, record.resourceId)}`
}

function resourceTypeText(value: string): string {
  return resourceTypeTextMap[value] ?? value
}

function valueText(value: unknown): string {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function visibilityText(value: string): string {
  if (value === 'all_users') return '所有用户'
  if (value === 'admin_only') return '仅管理员'
  return '相关用户'
}

function relationText(value: string): string {
  return relationTextMap[value] ?? value
}

function visibilityReasonText(value: string): string {
  return visibilityReasonTextMap[value] ?? value
}

function snapshotPageState(): OperationLogsPageState {
  return {
    actionFilter: actionFilter.value,
    actorSystemAccountFilter: actorSystemAccountFilter.value,
    affectedSystemAccountFilter: affectedSystemAccountFilter.value,
    keywordFilter: keywordFilter.value,
    moduleFilter: moduleFilter.value,
    operationScopeSystemAccountFilter: operationScopeSystemAccountFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    traceIdFilter: traceIdFilter.value
  }
}

const moduleTextMap: Record<string, string> = {
  accounts: 'AI 账户',
  announcements: '公告中心',
  api_keys: 'API Key',
  authorizations: '统一授权',
  groups: '分组',
  openai_oauth: 'OpenAI OAuth',
  proxies: '代理',
  settings: '系统设置',
  system_accounts: '系统账户',
  system_teams: '系统团队'
}
const actionTextMap: Record<string, string> = {
  add_members: '添加成员',
  bind_group: '绑定分组',
  create: '创建',
  create_account: '创建账户',
  delete: '删除',
  publish: '发布',
  reauthorize_from_code: '重新授权',
  reauthorize_from_refresh_token: '重新授权',
  refresh_token: '刷新 Token',
  remove_member: '移除成员',
  reset_password: '重置密码',
  restore: '恢复',
  revoke: '撤销',
  test: '检测',
  test_status_changed: '测试改状态',
  traffic_migration: '流量迁移',
  unpublish: '下线',
  update: '更新',
  update_expire: '更新有效期',
  update_global: '更新全局设置',
  update_settings: '更新系统设置'
}
const resourceTypeTextMap: Record<string, string> = {
  account: 'AI 账户',
  announcement: '公告',
  api_key: 'API Key',
  authorization: '授权',
  global_settings: '全局设置',
  group: '分组',
  proxy: '代理',
  system_account: '系统账户',
  system_settings: '系统设置',
  system_team: '系统团队'
}
const relationTextMap: Record<string, string> = {
  affected: '受影响',
  bound_resource: '绑定资源',
  created: '新建',
  deleted: '删除',
  grantee: '被授权',
  owner: '所有者',
  primary: '主资源',
  team_member: '团队成员'
}
const visibilityReasonTextMap: Record<string, string> = {
  actor_self: '本人操作',
  admin_managed_my_resource: '管理员代操作',
  authorization_grantee: '被授权用户',
  authorization_owner: '资源所有者',
  bound_resource_affected: '绑定资源影响',
  global_affected: '全局影响',
  resource_owner: '资源所有者',
  team_authorization: '团队授权',
  team_member: '团队成员'
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)
</script>

<style scoped>
.module-filter {
  width: 132px;
}

.action-filter {
  width: 126px;
}

.trace-filter {
  width: 190px;
}

.account-filter {
  width: 220px;
}

.operation-log-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.summary-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.summary-cell span {
  max-width: 280px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}

.muted-cell {
  color: #0f172a;
  font-size: 12px;
}

.name-cell {
  display: inline-block;
  max-width: 190px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.mono-cell {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.detail-table-card {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  color: #64748b;
  font-size: 12px;
}

.detail-table-card strong {
  color: #0f172a;
  font-size: 13px;
}

.operation-log-mobile-card {
  display: grid;
  width: 100%;
  gap: 10px;
  padding: 12px;
  text-align: left;
  background: #ffffff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(15 23 42 / 4%);
}

.mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.mobile-card-head > span {
  min-width: 0;
  color: #0f172a;
  line-height: 1.35;
}

.mobile-card-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  color: #0f172a;
  font-size: 12px;
}

.mobile-card-summary {
  color: #0f172a;
  font-size: 13px;
  line-height: 1.4;
}
</style>
