<template>
  <a-card class="page-card groups-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="isManagementView" :show-filters="isManagementView" filter-title="筛选分组" :active-filter-count="activeFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshGroups" @search="refreshGroups">
      <template #inline-filters>
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="systemAccountFilter"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          v-model:selected-principal="systemAccountFilterSelection"
          include-all
          class="toolbar-select responsive-list-inline-filter"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
      </template>
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['name']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
        <a-button type="primary" @click="openCreate">新建分组</a-button>
      </template>
      <template #filters>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect
            v-model:value="systemAccountFilter"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="systemAccountFilterSelection"
            include-all
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table groups-table" :columns="managedColumns" :data-source="groups" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="isManagementView ? 1610 : 1430" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileGroups" @mobile-refresh="refreshMobileGroups">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建一个分组，再到账户页把账户加入对应分组。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="group-name-cell">
            <span class="group-name-line">
              <span class="group-name-text">{{ record.name }}</span>
              <a-tooltip v-if="groupInfoTooltip(record)">
                <template #title>
                  <span class="authorized-tooltip-text">{{ groupInfoTooltip(record) }}</span>
                </template>
                <InfoCircleOutlined class="authorized-group-icon" :class="groupInfoIconClass(record)" />
              </a-tooltip>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'groupType'">
          <a-tooltip :title="groupPolicySummary(record)">
            <a-tag :color="groupTypeColor(record.groupType)">{{ groupTypeText(record.groupType) }}</a-tag>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ groupSystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span class="group-description-column-text">{{ groupDisplayDescription(record) || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'accountCount'">
          <a-tooltip :title="groupAccountStatsTooltip(record)">
            <div class="account-count-cell">
              <span class="account-count-row">
                <span class="account-count-label">可用:</span>
                <span class="account-count-value available">{{ groupStats(record).available }}</span>
                <span class="account-count-unit">个账号</span>
              </span>
              <span class="account-count-row">
                <span class="account-count-label">总量:</span>
                <span class="account-count-value">{{ groupStats(record).total }}</span>
                <span class="account-count-unit">个账号</span>
              </span>
            </div>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'concurrency'">
          <a-tooltip :title="groupConcurrencyTooltip(record)">
            <a-tag :color="groupConcurrencyAvailable(record) ? 'blue' : 'default'">{{ groupConcurrencyText(record) }}</a-tag>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'usage'">
          <UsageSummaryTags :usage="groupStats(record).todayUsage" />
        </template>
        <template v-else-if="column.key === 'status'">
          <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions v-if="groupRowActions(record).length || groupMoreActions(record).length" :actions="groupRowActions(record)" :more-actions="groupMoreActions(record)" @action-click="handleGroupAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              <div class="mobile-list-card-name-row">
                <span>{{ record.name }}</span>
                <a-tooltip v-if="groupInfoTooltip(record)">
                  <template #title>
                    <span class="authorized-tooltip-text">{{ groupInfoTooltip(record) }}</span>
                  </template>
                  <InfoCircleOutlined class="authorized-group-icon" :class="groupInfoIconClass(record)" />
                </a-tooltip>
              </div>
            </div>
            <div class="mobile-list-card-tags">
              <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
              <a-tag :color="groupTypeColor(record.groupType)">{{ groupTypeText(record.groupType) }}</a-tag>
              <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ groupSystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ groupDisplayDescription(record) || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>可用账号</span>
              <strong>{{ groupStats(record).available }} / {{ groupStats(record).total }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>并发</span>
              <a-tooltip :title="groupConcurrencyTooltip(record)">
                <strong>{{ groupConcurrencyText(record) }}</strong>
              </a-tooltip>
            </div>
            <div class="mobile-list-meta-item">
              <span>用量(日)</span>
              <strong>{{ formatUsageSummary(groupStats(record).todayUsage) }}</strong>
            </div>
          </div>
          <div v-if="groupRowActions(record).length || groupMoreActions(record).length" class="mobile-list-card-actions">
            <RowActions variant="button" :actions="groupRowActions(record)" :more-actions="groupMoreActions(record)" @action-click="handleGroupAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="groupModalTitle" width="640px" :confirm-loading="groupSaving" :ok-button-props="{ type: 'primary', disabled: groupSaving }" @ok="saveGroup">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical">
        <a-form-item label="分组名称" required>
          <a-input v-model:value="form.name" :disabled="editingAuthorizedGroup" />
        </a-form-item>
        <a-form-item label="所属供应商" required>
          <a-select v-model:value="form.providerCode" :options="providerOptions" :disabled="providerLocked || editingAuthorizedGroup" />
          <div class="form-help">同一供应商下可混合 OAuth / API Key 账户；分组只决定账户归属，不拆统计、会话亲和或缓存边界。</div>
        </a-form-item>
        <a-form-item label="分组类型" required>
          <a-radio-group v-model:value="form.groupType" button-style="solid">
            <a-radio-button value="personal">个人分组</a-radio-button>
            <a-radio-button value="high_concurrency">高并发分组</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <div v-if="form.groupType === 'high_concurrency'" class="scheduling-policy-grid">
          <a-form-item label="最大单账户排队阈值">
            <a-input-number v-model:value="form.schedulingPolicy.defaultSoftConcurrency" :min="1" :max="1000000" />
            <div class="form-help">达到该阈值后优先切到其他账户；实际值不会超过账户并发上限。</div>
          </a-form-item>
          <a-form-item label="最大等待时间（秒）">
            <a-input-number :value="formMaxQueueWaitSeconds" :min="1" :max="3600" @update:value="setFormMaxQueueWaitSeconds" />
            <div class="form-help">所有账户硬并发都满时，请求最多在分组短队列等待这么久；超时后返回 429。</div>
          </a-form-item>
          <a-form-item class="scheduling-policy-wide" label="限制单 IP 并发">
            <a-switch v-model:checked="clientIpLimitEnabled" checked-children="开启" un-checked-children="关闭" />
            <div class="form-help">默认关闭；开启后限制同一 IP 在当前分组和 API Key 下同时占用的请求数。</div>
          </a-form-item>
          <a-form-item label="单 IP 并发上限">
            <a-input-number :value="formClientIpConcurrencyLimit" :min="1" :max="1000000" :disabled="!clientIpLimitEnabled" @update:value="setFormClientIpConcurrencyLimit" />
            <div class="form-help">开启限制时默认 5 个并发；关闭后不限制。</div>
          </a-form-item>
          <a-form-item label="超过限制时">
            <a-segmented v-model:value="form.schedulingPolicy.clientIpConcurrencyOverflowMode" :options="clientIpOverflowModeOptions" :disabled="!clientIpLimitEnabled" block />
            <div class="form-help">立即拒绝会返回 429；排队等待会先等同 IP 请求释放，再进入分组调度。</div>
          </a-form-item>
        </div>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" :disabled="editingAuthorizedGroup" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

  </a-card>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatCompactUsageAmount, formatDateTime, formatNumber, formatUsd, serverDateTimeTimestamp } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountUsageSummary, GroupAccountStats, GroupSchedulingPolicy, GroupSummary, GroupType, ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue, systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { hasQuotaLimits } from '../shared/requestQuotaForm'
import { quotaLimitSummaryText } from '../shared/requestQuotaFormatters'

const OPENAI_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: '',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat']
}

const pageSize = 50
const modalOpen = ref(false)
const editingId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('groups')
const groupSaving = submittingRef('groups.save')
const providers = ref<ProviderDefinition[]>([])
const groupOptionsLoaded = ref(false)
const groupOptionsScopeKey = ref('')
const defaultHighConcurrencySchedulingPolicy: Required<GroupSchedulingPolicy> = {
  mode: 'balanced_fast',
  defaultSoftConcurrency: 5,
  fastFirstEnabled: true,
  fallbackOnQueueEnabled: true,
  breakAffinityOnSoftLimit: true,
  breakAffinityOnQueueWaitMs: 0,
  slowRequestThresholdMs: 30000,
  firstOutputSlowThresholdMs: 15000,
  recentTimeoutWindowSeconds: 120,
  recentTimeoutPenaltyThreshold: 2,
  maxQueueWaitMs: 60000,
  maxQueueSize: 1000,
  perApiKeyQueueLimit: 1000,
  clientIpConcurrencyLimit: 0,
  clientIpConcurrencyOverflowMode: 'reject',
  imageLaneMaxConcurrency: 0
}
const groupSchedulingPolicyKeys = [
  'mode',
  'defaultSoftConcurrency',
  'fastFirstEnabled',
  'fallbackOnQueueEnabled',
  'breakAffinityOnSoftLimit',
  'breakAffinityOnQueueWaitMs',
  'slowRequestThresholdMs',
  'firstOutputSlowThresholdMs',
  'recentTimeoutWindowSeconds',
  'recentTimeoutPenaltyThreshold',
  'maxQueueWaitMs',
  'maxQueueSize',
  'perApiKeyQueueLimit',
  'clientIpConcurrencyLimit',
  'clientIpConcurrencyOverflowMode',
  'imageLaneMaxConcurrency'
] as const
const defaultClientIpConcurrencyLimit = 5
const clientIpOverflowModeOptions = [
  { label: '立即拒绝', value: 'reject' },
  { label: '排队等待', value: 'queue' }
]
type GroupsPageState = {
  pagination?: { current: number; pageSize: number }
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}
const defaultGroupsPageState = (): GroupsPageState => ({
  pagination: { current: 1, pageSize },
  systemAccountFilter: allSystemAccountsValue,
  systemAccountFilterSelection: undefined
})
const pageStateCache = usePageStateCache<GroupsPageState>(undefined, defaultGroupsPageState)
const initialPageState = pageStateCache.read()
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const form = reactive({
  name: '',
  providerCode: 'openai',
  description: '',
  enabled: true,
  groupType: 'personal' as GroupType,
  schedulingPolicy: cloneHighConcurrencySchedulingPolicy()
})
const formMaxQueueWaitSeconds = computed(() => Math.max(1, Math.round((form.schedulingPolicy.maxQueueWaitMs ?? defaultHighConcurrencySchedulingPolicy.maxQueueWaitMs) / 1000)))
const clientIpLimitEnabled = computed({
  get: () => normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) > 0,
  set: (enabled: boolean) => {
    form.schedulingPolicy.clientIpConcurrencyLimit = enabled
      ? normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) || defaultClientIpConcurrencyLimit
      : 0
    form.schedulingPolicy.clientIpConcurrencyOverflowMode = form.schedulingPolicy.clientIpConcurrencyOverflowMode === 'queue' ? 'queue' : 'reject'
  }
})
const formClientIpConcurrencyLimit = computed(() => normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) || defaultClientIpConcurrencyLimit)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const groupsApi = useScopedGroupsApi(isManagementView)
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [systemAccountFilter.value]
})
const {
  items: groups,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileGroups,
  removeItems: removeGroupItems,
  refreshMobile: refreshMobileGroupsData,
  resetPagination,
  updateItems: updateGroupItems
} = useResponsivePagedList<GroupSummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 个分组，还有更多`
    : `共 ${formatNumber(total)} 个分组`,
  fetchPage: async (_options, pageState) => {
    const systemAccountId = isManagementView.value ? groupScopeParams.value?.systemAccountId : undefined
    return groupsApi.listPage(groupListParams(systemAccountId, pageState))
  },
  onError: (error) => {
    console.error(error)
    message.error('加载分组失败')
  }
})

const rawColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '分组名称', dataIndex: 'name', key: 'name', width: 240, fixed: 'left', customHeaderCell: () => ({ class: 'group-name-header-cell' }) },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 120 },
    { title: '类型', dataIndex: 'groupType', key: 'groupType', width: 130 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '账户数', key: 'accountCount', width: 130 },
    { title: '当前并发', key: 'concurrency', width: 100 },
    { title: '用量(日)', key: 'usage', width: 180 },
    { title: '状态', key: 'status', width: 100 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return baseColumns
})
const columnStorageKey = computed(() => (isManagementView.value ? 'groups:management' : 'groups:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})

const availableProviders = computed(() => providers.value.length ? providers.value : [OPENAI_PROVIDER])
const groupScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const providerOptions = computed(() => availableProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code,
  disabled: !provider.enabled
})))
const editingGroup = computed(() => groups.value.find((group) => group.id === editingId.value))
const editingAuthorizedGroup = computed(() => Boolean(editingGroup.value && isAuthorizedGroup(editingGroup.value)))
const groupModalTitle = computed(() => {
  if (!editingId.value) return '新建分组'
  return editingAuthorizedGroup.value ? '编辑授权分组使用配置' : '编辑分组'
})
const providerLocked = computed(() => Boolean(editingId.value && groupStats(editingGroup.value).total))
const activeFilterCount = computed(() => systemAccountFilter.value === allSystemAccountsValue ? 0 : 1)
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = groupScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

function groupStats(group?: GroupSummary): GroupAccountStats {
  const stats = group?.accountStats
  return {
    total: normalizedNumber(stats?.total),
    available: normalizedNumber(stats?.available),
    active: normalizedNumber(stats?.active),
    disabled: normalizedNumber(stats?.disabled),
    error: normalizedNumber(stats?.error),
    rateLimited: normalizedNumber(stats?.rateLimited),
    currentConcurrency: normalizedNumber(stats?.currentConcurrency),
    currentConcurrencyAvailable: stats?.currentConcurrencyAvailable,
    concurrencyLimit: normalizedNumber(stats?.concurrencyLimit),
    todayUsage: stats?.todayUsage ?? emptyUsageSummary(),
    usage: stats?.usage ?? emptyUsageSummary()
  }
}

function normalizedNumber(value: unknown): number {
  const numberValue = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}

function emptyUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function groupAccountStatsTooltip(group: GroupSummary): string {
  const stats = groupStats(group)
  return [
    `可用账号：${formatNumber(stats.available)}`,
    `总账号：${formatNumber(stats.total)}`,
    `正常：${formatNumber(stats.active)}`,
    `停用：${formatNumber(stats.disabled)}`,
    `异常：${formatNumber(stats.error)}`,
    `限流：${formatNumber(stats.rateLimited)}`
  ].join('\n')
}

function cloneHighConcurrencySchedulingPolicy(source?: GroupSchedulingPolicy, options: { requireComplete?: boolean } = {}): Required<GroupSchedulingPolicy> {
  const input = groupSchedulingPolicyRecord(source, options)
  assertGroupSchedulingPolicyKeys(input)
  if (options.requireComplete) {
    assertCompleteGroupSchedulingPolicy(input)
  }
  return {
    mode: normalizeGroupSchedulingMode(input.mode),
    defaultSoftConcurrency: normalizeGroupSchedulingInteger(input.defaultSoftConcurrency, defaultHighConcurrencySchedulingPolicy.defaultSoftConcurrency, 1, 1_000_000, 'defaultSoftConcurrency'),
    fastFirstEnabled: normalizeGroupSchedulingBoolean(input.fastFirstEnabled, defaultHighConcurrencySchedulingPolicy.fastFirstEnabled, 'fastFirstEnabled'),
    fallbackOnQueueEnabled: normalizeGroupSchedulingBoolean(input.fallbackOnQueueEnabled, defaultHighConcurrencySchedulingPolicy.fallbackOnQueueEnabled, 'fallbackOnQueueEnabled'),
    breakAffinityOnSoftLimit: normalizeGroupSchedulingBoolean(input.breakAffinityOnSoftLimit, defaultHighConcurrencySchedulingPolicy.breakAffinityOnSoftLimit, 'breakAffinityOnSoftLimit'),
    breakAffinityOnQueueWaitMs: normalizeGroupSchedulingInteger(input.breakAffinityOnQueueWaitMs, defaultHighConcurrencySchedulingPolicy.breakAffinityOnQueueWaitMs, 0, 1_000_000, 'breakAffinityOnQueueWaitMs'),
    slowRequestThresholdMs: normalizeGroupSchedulingInteger(input.slowRequestThresholdMs, defaultHighConcurrencySchedulingPolicy.slowRequestThresholdMs, 1, 1_000_000, 'slowRequestThresholdMs'),
    firstOutputSlowThresholdMs: normalizeGroupSchedulingInteger(input.firstOutputSlowThresholdMs, defaultHighConcurrencySchedulingPolicy.firstOutputSlowThresholdMs, 1, 1_000_000, 'firstOutputSlowThresholdMs'),
    recentTimeoutWindowSeconds: normalizeGroupSchedulingInteger(input.recentTimeoutWindowSeconds, defaultHighConcurrencySchedulingPolicy.recentTimeoutWindowSeconds, 1, 1_000_000, 'recentTimeoutWindowSeconds'),
    recentTimeoutPenaltyThreshold: normalizeGroupSchedulingInteger(input.recentTimeoutPenaltyThreshold, defaultHighConcurrencySchedulingPolicy.recentTimeoutPenaltyThreshold, 1, 1_000_000, 'recentTimeoutPenaltyThreshold'),
    maxQueueWaitMs: normalizeGroupSchedulingInteger(input.maxQueueWaitMs, defaultHighConcurrencySchedulingPolicy.maxQueueWaitMs, 1, 3_600_000, 'maxQueueWaitMs'),
    maxQueueSize: normalizeGroupSchedulingInteger(input.maxQueueSize, defaultHighConcurrencySchedulingPolicy.maxQueueSize, 1, 1_000_000, 'maxQueueSize'),
    perApiKeyQueueLimit: normalizeGroupSchedulingInteger(input.perApiKeyQueueLimit, defaultHighConcurrencySchedulingPolicy.perApiKeyQueueLimit, 1, 1_000_000, 'perApiKeyQueueLimit'),
    clientIpConcurrencyLimit: normalizeGroupSchedulingInteger(input.clientIpConcurrencyLimit, defaultHighConcurrencySchedulingPolicy.clientIpConcurrencyLimit, 0, 1_000_000, 'clientIpConcurrencyLimit'),
    clientIpConcurrencyOverflowMode: normalizeGroupSchedulingOverflowMode(input.clientIpConcurrencyOverflowMode),
    imageLaneMaxConcurrency: normalizeGroupSchedulingInteger(input.imageLaneMaxConcurrency, defaultHighConcurrencySchedulingPolicy.imageLaneMaxConcurrency, 0, 1_000_000, 'imageLaneMaxConcurrency')
  }
}

function groupSchedulingPolicyRecord(source?: GroupSchedulingPolicy, options: { requireComplete?: boolean } = {}): Record<string, unknown> {
  if (source === undefined || source === null) {
    if (options.requireComplete) {
      throw new Error('分组调度策略缺失，请清理后再编辑')
    }
    return {}
  }
  if (typeof source !== 'object' || Array.isArray(source)) {
    throw new Error('分组调度策略无效')
  }
  return source as Record<string, unknown>
}

function assertGroupSchedulingPolicyKeys(input: Record<string, unknown>): void {
  const allowed = new Set<string>(groupSchedulingPolicyKeys)
  const unknownKeys = Object.keys(input).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`分组调度策略包含未知字段：${unknownKeys.join('、')}`)
  }
}

function assertCompleteGroupSchedulingPolicy(input: Record<string, unknown>): void {
  const missingKeys = groupSchedulingPolicyKeys.filter((key) => input[key] === undefined || input[key] === null)
  if (missingKeys.length) {
    throw new Error(`分组调度策略缺少字段：${missingKeys.join('、')}`)
  }
}

function normalizeGroupSchedulingInteger(value: unknown, fallback: number, min: number, max: number, key: string): number {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`分组调度策略 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`分组调度策略 ${key} 必须在 ${min}-${max} 之间`)
  }
  return value
}

function normalizeGroupSchedulingBoolean(value: unknown, fallback: boolean, key: string): boolean {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'boolean') {
    throw new Error(`分组调度策略 ${key} 必须是布尔值`)
  }
  return value
}

function normalizeGroupSchedulingMode(value: unknown): 'balanced_fast' {
  if (value === undefined || value === null || value === 'balanced_fast') return 'balanced_fast'
  throw new Error('分组调度策略 mode 无效')
}

function normalizeGroupSchedulingOverflowMode(value: unknown): 'reject' | 'queue' {
  if (value === undefined || value === null) return defaultHighConcurrencySchedulingPolicy.clientIpConcurrencyOverflowMode
  if (value === 'reject' || value === 'queue') return value
  throw new Error('分组调度策略 clientIpConcurrencyOverflowMode 无效')
}

function groupTypeText(groupType?: GroupType): string {
  return groupType === 'high_concurrency' ? '高并发' : '个人'
}

function groupTypeColor(groupType?: GroupType): string {
  return groupType === 'high_concurrency' ? 'purple' : 'blue'
}

function groupPolicySummary(group: GroupSummary): string {
  if (group.groupType !== 'high_concurrency') {
    return '个人分组保持稳定调度'
  }
  let policy: Required<GroupSchedulingPolicy>
  try {
    policy = cloneHighConcurrencySchedulingPolicy(group.schedulingPolicy, { requireComplete: true })
  } catch {
    return '高并发调度策略数据异常，请清理后再编辑'
  }
  const clientIpSummary = policy.clientIpConcurrencyLimit > 0
    ? `单 IP ${policy.clientIpConcurrencyLimit} 并发，超过后${policy.clientIpConcurrencyOverflowMode === 'queue' ? '排队等待' : '立即拒绝'}`
    : '单 IP 不限制'
  return `最大单账户排队 ${policy.defaultSoftConcurrency}，最大等待 ${Math.round(policy.maxQueueWaitMs / 1000)} 秒，${clientIpSummary}，队列上限 ${policy.maxQueueSize}`
}

function setFormMaxQueueWaitSeconds(value: unknown) {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 3600) return
  form.schedulingPolicy.maxQueueWaitMs = value * 1000
}

function setFormClientIpConcurrencyLimit(value: unknown) {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 1_000_000) return
  form.schedulingPolicy.clientIpConcurrencyLimit = value
}

function normalizeClientIpConcurrencyLimit(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 1_000_000) {
    return 0
  }
  return value
}

function groupConcurrencyAvailable(group: GroupSummary): boolean {
  return groupStats(group).currentConcurrencyAvailable !== false
}

function groupConcurrencyText(group: GroupSummary): string {
  return groupConcurrencyAvailable(group) ? String(groupStats(group).currentConcurrency) : '暂不可用'
}

function groupConcurrencyTooltip(group: GroupSummary): string {
  return groupConcurrencyAvailable(group) ? '当前正在转发的请求数' : '实时并发快照暂不可用'
}

function groupStatusText(group: GroupSummary) {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return '授权暂停'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return '授权到期'
  if (!group.enabled) return '停用'
  if (stats.total === 0) return '未绑定'
  if (stats.available === 0) return '无可用账户'
  return '启用'
}

function groupStatusColor(group: GroupSummary) {
  const stats = groupStats(group)
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'paused') return 'orange'
  if (isAuthorizedGroup(group) && group.authorizationStatus === 'expired') return 'default'
  if (!group.enabled || stats.total === 0) return 'default'
  if (stats.available === 0) return 'orange'
  return 'green'
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function groupSystemAccountText(group: GroupSummary) {
  return systemAccountDisplayText(group)
}

function isAuthorizedGroup(group: GroupSummary): boolean {
  return group.accessType === 'authorized'
}

function authorizedGroupTooltip(group: GroupSummary): string {
  const ownerName = group.ownerSystemAccountName || '其他用户'
  const expiresText = group.authorizationExpiresAt ? formatDateTime(group.authorizationExpiresAt) : '长期有效'
  const limitsText = quotaLimitSummaryText(group.authorizationLimits)
  const lines = [
    `授权自 ${ownerName}。`,
    `授权来源：${authorizedGroupSourceText(group)}`,
    `授权到期：${expiresText}`,
    `授权限额：${limitsText}`
  ]
  if (group.authorizationStatus === 'expired') {
    lines.push('授权已到期，当前不可用。')
  } else if (group.authorizationStatus === 'paused') {
    lines.push('授权已暂停，当前不可用。')
  }
  return lines.join('\n')
}

function groupInfoTooltip(group: GroupSummary): string {
  if (!isAuthorizedGroup(group)) return ''
  return authorizedGroupTooltip(group)
}

function groupDisplayDescription(group: GroupSummary): string {
  return group.description?.trim() ?? ''
}

function authorizedGroupSourceText(group: GroupSummary): string {
  const activeSources = group.authorizationSources?.filter((source) => source.status === 'active') ?? []
  if (!activeSources.length && group.authorizationSources?.some((source) => source.sourceType === 'team')) {
    return '团队授权'
  }
  const hasManual = activeSources.some((source) => source.sourceType === 'manual')
  const teamSources = activeSources.filter((source) => source.sourceType === 'team')
  const teamNames = teamSources.map((source) => source.sourceTeamName).filter((name): name is string => Boolean(name))
  if (hasManual && teamSources.length) {
    return teamNames.length ? `个人授权 + 团队授权（${teamNames.join('、')}）` : '个人授权 + 团队授权'
  }
  if (teamSources.length) {
    return teamNames.length ? `团队授权（${teamNames.join('、')}）` : '团队授权'
  }
  return '个人授权'
}

function authorizedGroupIconClass(group: GroupSummary): string {
  return `source-${authorizedGroupSourceTone(group)}`
}

function groupInfoIconClass(group: GroupSummary): string {
  return isAuthorizedGroup(group) ? authorizedGroupIconClass(group) : 'source-normal'
}

function authorizedGroupSourceTone(group: GroupSummary): 'normal' | 'warning' | 'danger' {
  if (group.authorizationStatus && group.authorizationStatus !== 'active') return 'danger'
  if (isAuthorizationExpiringSoon(group) || hasQuotaLimits(group.authorizationLimits)) return 'warning'
  return 'normal'
}

function isAuthorizationExpiringSoon(group: GroupSummary): boolean {
  if (!group.authorizationExpiresAt) return false
  const timestamp = serverDateTimeTimestamp(group.authorizationExpiresAt)
  if (timestamp === undefined) return false
  const remainingMs = timestamp - Date.now()
  return remainingMs > 0 && remainingMs <= 3 * 24 * 60 * 60 * 1000
}

function canEditGroup(group: GroupSummary): boolean {
  return !group.isDefault && group.permissions?.canEdit !== false
}

function canDeleteGroup(group: GroupSummary): boolean {
  return !group.isDefault && group.permissions?.canDelete !== false
}

function canReturnAuthorizedGroup(group: GroupSummary): boolean {
  return isAuthorizedGroup(group) && group.permissions?.canReturnAuthorization === true
}

function groupRowActions(group: GroupSummary): RowActionItem[] {
  const actions: RowActionItem[] = []
  if (isAuthorizedGroup(group)) {
    if (canEditGroup(group)) {
      actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
    }
    if (canReturnAuthorizedGroup(group)) {
      actions.push(returnAuthorizedGroupAction(group))
    }
    return actions
  }
  if (canDeleteGroup(group)) {
    actions.push(deleteGroupAction(group))
  }
  return actions
}

function groupMoreActions(group: GroupSummary): RowActionItem[] {
  if (isAuthorizedGroup(group)) return []
  const actions: RowActionItem[] = []
  if (canEditGroup(group)) {
    actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  return actions
}

function deleteGroupAction(group: GroupSummary): RowActionItem {
  return {
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: `确认删除分组「${group.name}」？删除后会从 API Key 路由中移除该分组；如果它是主号池且存在可用备用号池，将自动切到备用号池。`,
    confirmOkText: '删除'
  }
}

function returnAuthorizedGroupAction(group: GroupSummary): RowActionItem {
  return {
    key: 'return-authorization',
    label: '归还',
    icon: 'revoke',
    tone: 'danger',
    confirmTitle: `确认归还授权分组「${group.name}」？归还后你将不再看到或使用它，不影响授权方原分组。`,
    confirmOkText: '归还'
  }
}

function handleGroupAction(key: string, group: GroupSummary) {
  if (key === 'edit') {
    openEdit(group)
    return
  }
  if (key === 'return-authorization') {
    void returnAuthorizationGroup(group.id)
    return
  }
  if (key === 'delete') {
    void removeGroup(group.id)
  }
}

function groupOperationScopeParams(group?: Pick<GroupSummary, 'systemAccountId' | 'accessType'>): { systemAccountId: string } | undefined {
  const systemAccountId = group?.accessType === 'authorized'
    ? groupScopeParams.value?.systemAccountId
    : group?.systemAccountId?.trim() || groupScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function formatUsageSummary(usage: GroupSummary['accountStats']['usage']) {
  return `${formatNumber(usage.requestCount)}req/${formatUsageAmount(usage.totalTokens)}/${formatCost(usage.totalCost)}`
}

function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

function formatCost(value?: number): string {
  return formatUsd(value)
}

function defaultProviderCode() {
  const provider = availableProviders.value.find((item) => item.enabled)
  if (!provider) throw new Error('没有可用供应商')
  return provider.code
}

function groupListParams(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }) {
  return {
    systemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
}

async function loadGroupOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (!force && groupOptionsLoaded.value && groupOptionsScopeKey.value === scopeKey) {
    return
  }

  const [providerList] = await Promise.all([
    api.providers.options()
  ])
  providers.value = providerList.length ? providerList : [OPENAI_PROVIDER]
  groupOptionsLoaded.value = true
  groupOptionsScopeKey.value = scopeKey
}

function refreshGroups() {
  resetSystemAccountOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function refreshMobileGroups() {
  resetSystemAccountOptionsSearch()
  void refreshMobileGroupsData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetPagination()
  void loadData()
}

function resetFilters() {
  systemAccountFilter.value = allSystemAccountsValue
  systemAccountFilterSelection.value = undefined
  resetSystemAccountOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function openCreate() {
  if (isManagementView.value && !groupScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建分组')
    return
  }
  editingId.value = undefined
  void loadGroupOptions()
  Object.assign(form, {
    name: '',
    providerCode: defaultProviderCode(),
    description: '',
    enabled: true,
    groupType: 'personal' as GroupType,
    schedulingPolicy: cloneHighConcurrencySchedulingPolicy()
  })
  modalOpen.value = true
}

function openEdit(group: GroupSummary) {
  if (!canEditGroup(group)) {
    message.warning(group.isDefault ? '默认分组不允许编辑' : '当前分组不能编辑')
    return
  }
  let schedulingPolicy: Required<GroupSchedulingPolicy>
  try {
    schedulingPolicy = group.groupType === 'high_concurrency'
      ? cloneHighConcurrencySchedulingPolicy(group.schedulingPolicy, { requireComplete: true })
      : cloneHighConcurrencySchedulingPolicy()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '分组调度策略数据异常，请清理后再编辑'))
    return
  }
  editingId.value = group.id
  void loadGroupOptions()
  Object.assign(form, {
    name: group.name,
    providerCode: group.providerCode,
    description: group.description ?? '',
    enabled: group.enabled,
    groupType: group.groupType,
    schedulingPolicy
  })
  modalOpen.value = true
}

const saveGroup = submitAction('groups.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写分组名称')
    return
  }
  try {
    const targetId = editingId.value
    if (targetId) {
      const targetGroup = groups.value.find((item) => item.id === targetId)
      const payload = groupFormPayload(targetGroup)
      const updated = await groupsApi.update(targetId, payload, groupOperationScopeParams(targetGroup))
      updateGroupItems((item) => item.id === targetId, () => updated)
      message.success(isAuthorizedGroup(updated) ? '授权分组使用配置已更新' : '分组已更新')
      void loadData({ quiet: true })
    } else {
      const payload = groupFormPayload()
      await groupsApi.create(payload, groupScopeParams.value)
      message.success('分组已创建')
      await loadData()
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存分组失败'))
  }
})

function groupFormPayload(targetGroup?: GroupSummary): Record<string, unknown> {
  const schedulingPolicy = cloneHighConcurrencySchedulingPolicy(form.schedulingPolicy, { requireComplete: true })
  const localSettings = {
    enabled: form.enabled,
    groupType: form.groupType,
    schedulingPolicy: form.groupType === 'high_concurrency'
      ? {
          defaultSoftConcurrency: schedulingPolicy.defaultSoftConcurrency,
          maxQueueWaitMs: schedulingPolicy.maxQueueWaitMs,
          clientIpConcurrencyLimit: clientIpLimitEnabled.value ? formClientIpConcurrencyLimit.value : 0,
          clientIpConcurrencyOverflowMode: clientIpLimitEnabled.value
            ? schedulingPolicy.clientIpConcurrencyOverflowMode
            : 'reject',
          imageLaneMaxConcurrency: schedulingPolicy.imageLaneMaxConcurrency
        }
      : undefined
  }
  if (targetGroup && isAuthorizedGroup(targetGroup)) {
    return localSettings
  }
  return {
    name: form.name,
    providerCode: form.providerCode,
    description: form.description,
    ...localSettings
  }
}

async function removeGroup(id: string) {
  const group = groups.value.find((item) => item.id === id)
  if (group && isAuthorizedGroup(group)) {
    message.warning('授权分组请使用归还操作')
    return
  }
  if (group?.isDefault) {
    message.warning('默认分组不允许删除')
    return
  }
  if (group && !canDeleteGroup(group)) {
    message.warning('当前分组不能删除')
    return
  }
  try {
    await groupsApi.delete(id, groupOperationScopeParams(group))
    removeGroupItems((item) => item.id === id)
    message.success('分组已删除')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除分组失败'))
  }
}

async function returnAuthorizationGroup(id: string) {
  const group = groups.value.find((item) => item.id === id)
  if (!group || !isAuthorizedGroup(group)) {
    message.warning('授权分组不存在')
    return
  }
  if (!canReturnAuthorizedGroup(group)) {
    message.warning('当前授权分组不能归还')
    return
  }
  try {
    await groupsApi.returnAuthorization(id, groupOperationScopeParams(group))
    removeGroupItems((item) => item.id === id)
    message.success('授权分组已归还')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '归还授权分组失败'))
  }
}

function snapshotPageState(): GroupsPageState {
  return {
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onMounted(() => {
  void loadData()
})
</script>

<style scoped>
.groups-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.form-help {
  color: #64748b;
  font-size: 12px;
}

.scheduling-policy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
}

.scheduling-policy-grid :deep(.ant-input-number) {
  width: 100%;
}

.scheduling-policy-wide {
  grid-column: 1 / -1;
}

.groups-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.groups-table :deep(.ant-empty) {
  margin: 12px 0;
}

.groups-table :deep(.group-name-header-cell),
.groups-table :deep(.group-name-header-cell .ant-table-column-title) {
  font-weight: 400;
}

.group-name-cell {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.group-name-line,
.mobile-list-card-name-row,
.account-count-cell,
.account-count-row {
  display: flex;
  align-items: center;
  gap: 4px;
  color: #475569;
}

.group-name-text,
.group-description-column-text,
.mobile-list-card-name-row span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-name-text {
  color: #0f172a;
  font-weight: 400;
}

.groups-page-card .mobile-list-card-title {
  font-weight: 400;
}

.group-description-column-text {
  color: #64748b;
  font-size: 12px;
}

.account-count-cell {
  flex-direction: column;
  align-items: flex-start;
}

.account-count-label {
  min-width: 38px;
  text-align: right;
}

.account-count-value {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 400;
}

.account-count-value.available {
  color: #0891b2;
}

.account-count-value.limited {
  color: #f59e0b;
}

.account-count-unit {
  padding: 1px 6px;
  color: #334155;
  background: #f1f5f9;
  border-radius: 4px;
}

.account-count-row,
.account-count-unit {
  color: #64748b;
  font-size: 12px;
}

.usage-label {
  display: inline-block;
  min-width: 38px;
  color: #64748b;
}

.status-tag {
  width: fit-content;
}

.authorized-group-icon {
  flex: none;
  color: #08979c;
  cursor: help;
  font-size: 14px;
}

.authorized-group-icon.source-danger {
  color: #cf1322;
}

.authorized-group-icon.source-warning {
  color: #d48806;
}

.authorized-tooltip-text {
  white-space: pre-line;
}

@media (max-width: 640px) {
  .scheduling-policy-grid {
    grid-template-columns: 1fr;
  }
}

</style>
