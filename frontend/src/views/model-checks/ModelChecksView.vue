<template>
  <div class="model-checks-page">
    <a-card class="page-card model-checks-run-card">
      <a-form class="model-checks-form" layout="vertical">
        <div class="model-checks-control-panel">
          <div class="model-checks-fields">
            <a-form-item v-if="isManagementView" class="model-checks-system-account-field" required>
              <SystemPrincipalSelect
                v-model:value="systemAccountFilter"
                v-model:selected-principal="systemAccountFilterSelection"
                :accounts="systemAccounts"
                :active-only="false"
                allow-clear
                :disabled="submitting"
                :filter-option="false"
                :loading="systemAccountOptionsLoading"
                placeholder="请选择系统账户"
                @change="handleSystemAccountFilterChange"
                @dropdown-visible-change="handleSystemAccountOptionsDropdown"
                @search="handleSystemAccountOptionsSearch"
              />
            </a-form-item>
            <a-form-item class="model-checks-account-field" required>
              <AccountSelect
                :value="selectValueOrUndefined(form.targetId)"
                v-model:selected-account="selectedTargetAccount"
                show-search
                allow-clear
                :disabled="accountSelectDisabled"
                :filter-option="false"
                :loading="targetOptionsLoading"
                :options="targetOptions"
                :placeholder="accountSelectPlaceholder"
                @change="handleTargetChange"
                @dropdown-visible-change="handleTargetDropdownVisibleChange"
                @search="handleTargetSearch"
                @update:value="handleTargetValueUpdate"
              />
            </a-form-item>
            <a-form-item class="model-checks-model-field" required>
              <a-select
                v-model:value="form.model"
                :options="modelOptions"
                :loading="optionsLoading"
                :disabled="submitting"
                placeholder="模型"
              />
            </a-form-item>
            <a-form-item class="model-checks-comparison-field">
              <AccountSelect
                v-model:value="form.trustedComparisonAccountId"
                v-model:selected-account="selectedComparisonAccount"
                show-search
                allow-clear
                :disabled="accountSelectDisabled"
                :filter-option="false"
                :loading="comparisonOptionsLoading"
                :options="comparisonOptions"
                :placeholder="comparisonSelectPlaceholder"
                @dropdown-visible-change="handleComparisonDropdownVisibleChange"
                @search="handleComparisonSearch"
              />
            </a-form-item>
            <a-button :loading="optionsLoading" @click="loadOptions">
              <template #icon>
                <ReloadOutlined />
              </template>
              刷新
            </a-button>
            <a-button :disabled="submitting" @click="resetRunForm">重置</a-button>
          </div>

          <div class="model-checks-toolbar">
            <a-button type="primary" :loading="submitting" @click="submitRun">
              <template #icon>
                <ExperimentOutlined />
              </template>
              开始检测
            </a-button>
          </div>
        </div>

      </a-form>

      <div v-if="terminalVisible" class="model-check-terminal">
        <div class="model-check-terminal-head">
          <div>
            <div class="terminal-title">AI 测试终端</div>
            <div class="terminal-subtitle">按真实检测进度输出探针请求、响应、评分和 Trace ID</div>
          </div>
          <a-space>
            <a-tag :color="terminalStatusColor">{{ terminalStatusText }}</a-tag>
            <a-button v-if="submitting" size="small" danger @click="stopCurrentModelCheck()">停止检测</a-button>
          </a-space>
        </div>
        <div ref="terminalBodyRef" class="model-check-terminal-body">
          <div v-for="line in terminalLines" :key="line.id" class="terminal-line" :class="`terminal-line-${line.level}`">
            <span class="terminal-time">[{{ line.time }}]</span>
            <span class="terminal-prompt">$</span>
            <span class="terminal-text">{{ line.text }}</span>
          </div>
          <div v-if="submitting" class="terminal-line terminal-line-muted">
            <span class="terminal-time">[{{ terminalNow }}]</span>
            <span class="terminal-prompt">_</span>
            <span class="terminal-text terminal-cursor">等待下一个检测事件</span>
          </div>
        </div>
      </div>
    </a-card>

    <a-card class="page-card model-checks-history-card" title="历史检测">
      <div class="history-toolbar">
        <a-space wrap>
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="systemAccountFilter"
            v-model:selected-principal="systemAccountFilterSelection"
            :accounts="systemAccounts"
            :active-only="false"
            allow-clear
            class="history-filter history-system-account-filter"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            placeholder="请选择系统账户"
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
          <a-select v-model:value="filters.model" allow-clear class="history-filter" :options="modelOptions" placeholder="全部模型" @change="reloadRuns" />
          <a-select v-model:value="filters.status" allow-clear class="history-filter" :options="statusOptions" placeholder="全部状态" @change="reloadRuns" />
          <a-select v-model:value="filters.level" allow-clear class="history-filter" :options="levelOptions" placeholder="全部级别" @change="reloadRuns" />
          <AccountSelect
            v-model:value="filters.targetId"
            v-model:selected-account="selectedHistoryTargetAccount"
            show-search
            allow-clear
            class="history-target-filter"
            :disabled="managementScopeRequired"
            :filter-option="false"
            :loading="historyTargetOptionsLoading"
            :options="historyTargetOptions"
            :placeholder="historyAccountSelectPlaceholder"
            @change="() => reloadRuns()"
            @dropdown-visible-change="handleHistoryTargetDropdownVisibleChange"
            @search="handleHistoryTargetSearch"
          />
        </a-space>
        <a-button :loading="runsLoading" @click="reloadRuns">
          <template #icon>
            <ReloadOutlined />
          </template>
          刷新
        </a-button>
      </div>

      <ResponsiveDataList
        class="model-checks-responsive-list"
        table-class="model-checks-table"
        size="middle"
        row-key="id"
        :columns="columns"
        :data-source="runs"
        :mobile-data-source="runs"
        :loading="runsLoading"
        :pagination="runsTablePagination"
        :scroll-x="1100"
        :loading-more="runsMobileLoadingMore"
        :mobile-has-more="runsMobileHasMore"
        mobile-pagination
        pull-refresh-enabled
        :refreshing="runsLoading"
        @change="handleRunsTableChange"
        @mobile-load-more="loadMoreMobileRuns"
        @mobile-refresh="refreshMobileRuns"
      >
        <template #emptyText>
          <a-empty :description="modelCheckHistoryEmptyText" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'target'">
            <div class="target-cell">
              <a-tag>AI 账户</a-tag>
              <span class="target-name-cell">{{ targetDisplayName(record) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'level'">
            <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'model'">
            {{ modelText(record.model) }}
          </template>
          <template v-else-if="column.key === 'createdAt'">
            {{ formatDateTime(record.createdAt) }}
          </template>
          <template v-else-if="column.key === 'summary'">
            <span class="summary-cell">{{ record.message || record.errorMessage || '-' }}</span>
          </template>
          <template v-else-if="column.key === 'actions'">
            <a-button type="link" size="small" @click="loadRunDetail(record.id)">查看</a-button>
          </template>
        </template>
        <template #card="{ record }">
          <article class="model-check-mobile-card">
            <div class="model-check-mobile-head">
              <div>
                <div class="model-check-mobile-title">{{ targetDisplayName(record) }}</div>
                <div class="model-check-mobile-subtitle">AI 账户</div>
              </div>
              <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
            </div>
            <div class="model-check-mobile-tags">
              <a-tag>{{ modelText(record.model) }}</a-tag>
              <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
              <a-tag v-if="runTrustedComparison(record)" color="blue">可信对比</a-tag>
            </div>
            <div class="model-check-mobile-grid">
              <div class="model-check-mobile-metric">
                <span>得分</span>
                <strong>{{ record.score }} / {{ record.maxScore }}</strong>
              </div>
              <div class="model-check-mobile-metric">
                <span>耗时</span>
                <strong>{{ formatDuration(record.durationMs) }}</strong>
              </div>
              <div class="model-check-mobile-metric model-check-mobile-wide">
                <span>创建时间</span>
                <strong>{{ formatDateTime(record.createdAt) }}</strong>
              </div>
              <div class="model-check-mobile-metric model-check-mobile-wide">
                <span>结论</span>
                <strong>{{ record.message || record.errorMessage || '-' }}</strong>
              </div>
            </div>
            <div class="model-check-mobile-actions">
              <a-button size="small" type="primary" @click="loadRunDetail(record.id)">查看</a-button>
            </div>
          </article>
        </template>
      </ResponsiveDataList>
    </a-card>

    <a-drawer
      v-model:open="detailOpen"
      class="model-checks-detail-drawer"
      title="检测结果详情"
      width="720px"
      :body-style="{ padding: '16px' }"
    >
      <a-skeleton v-if="detailLoading" active :paragraph="{ rows: 5 }" />
      <a-empty v-else-if="!currentRun" description="尚未选择检测记录" />
      <div v-else class="run-detail">
        <div class="run-detail-head">
          <div>
            <div class="run-detail-title">{{ targetDisplayName(currentRun) }}</div>
            <div class="run-detail-subtitle">
              检测目标：AI 账户
            </div>
          </div>
          <a-space wrap>
            <a-tag :color="statusColor(currentRun.status)">{{ statusText(currentRun.status) }}</a-tag>
            <a-tag :color="levelColor(currentRun.level)">{{ levelText(currentRun.level) }}</a-tag>
            <a-tag v-if="runTrustedComparison(currentRun)" color="blue">可信对比</a-tag>
            <a-tag>{{ currentRun.score }} / {{ currentRun.maxScore }}</a-tag>
          </a-space>
        </div>

        <a-descriptions bordered size="small" :column="detailDescriptionColumns" class="run-descriptions">
          <a-descriptions-item label="检测 ID">{{ currentRun.id }}</a-descriptions-item>
          <a-descriptions-item label="账户名称">{{ targetDisplayName(currentRun) }}</a-descriptions-item>
          <a-descriptions-item label="模型">{{ modelText(currentRun.model) }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(currentRun.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="完成时间">{{ formatDateTime(currentRun.finishedAt) }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatDuration(currentRun.durationMs) }}</a-descriptions-item>
          <a-descriptions-item label="结论">{{ currentRun.message || currentRun.errorMessage || '-' }}</a-descriptions-item>
          <a-descriptions-item label="Trace ID">{{ currentRun.traceId || '-' }}</a-descriptions-item>
        </a-descriptions>

        <div v-if="currentRun.checks.length" class="check-list">
          <div v-for="check in currentRun.checks" :key="check.id" class="check-item">
            <div class="check-item-head">
              <span>{{ checkTitle(check) }}</span>
              <a-space wrap>
                <a-tag :color="checkStatusColor(check.status)">{{ checkStatusText(check.status) }}</a-tag>
                <a-tag>{{ check.score }} / {{ check.maxScore }}</a-tag>
              </a-space>
            </div>
            <div v-if="checkMessage(check)" class="check-message">{{ checkMessage(check) }}</div>
            <pre v-if="hasCheckExtra(check)" class="json-block">{{ formatJson(checkExtra(check)) }}</pre>
          </div>
        </div>

        <pre class="json-block">{{ formatJson({ request: currentRun.requestSummary, result: currentRun.resultSummary }) }}</pre>
      </div>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onDeactivated, onMounted, reactive, ref } from 'vue'
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import AccountSelect from '@/components/AccountSelect.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedAccountsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import {
  accountLabelForId,
  accountSelectionForId,
  accountSelectOptionLabel,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type {
  AccountOptionSummary,
  ModelCheckCheckResult,
  ModelCheckLevel,
  ModelCheckModel,
  ModelCheckOptions,
  ModelCheckProgressEvent,
  ModelCheckRunDetail,
  ModelCheckRunPayload,
  ModelCheckRunSummary,
  ModelCheckStatus
} from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

const fallbackOptions: ModelCheckOptions = {
  supportedModels: [
    { value: 'gpt-5.5', label: 'gpt-5.5' },
    { value: 'gpt-5.4', label: 'gpt-5.4' }
  ],
  supportedProfiles: [
    { value: 'full', label: '强诊断完整检测', description: '准确优先，不以成本和耗时为约束' }
  ],
  trustedComparison: { enabledByDefault: false, available: true, message: '可信对比默认关闭；选择可信账户后会额外消耗该账户额度。' },
  defaultModel: 'gpt-5.5',
  defaultProfile: 'full'
}

const statusOptions = [
  { label: '检测中', value: 'running' },
  { label: '已完成', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '已取消', value: 'canceled' }
]
const levelOptions = [
  { label: '高可信', value: 'high_confidence' },
  { label: '较可信', value: 'likely' },
  { label: '不确定', value: 'uncertain' },
  { label: '疑似不符', value: 'suspicious' },
  { label: '不可检测', value: 'unavailable' }
]
const columns = [
  { title: '目标', key: 'target', width: 300 },
  { title: '模型', key: 'model', width: 130 },
  { title: '状态', key: 'status', width: 110 },
  { title: '级别', key: 'level', width: 100 },
  { title: '摘要', key: 'summary', width: 320 },
  { title: '创建时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' }
]
const modelCheckPageSize = 20
type AccountSelectOption = { label: string; value: string }
type SelectValue = string | string[] | undefined

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const modelChecksApi = useScopedModelChecksApi(isManagementView)
const accountsApi = useScopedAccountsApi(isManagementView)
const systemAccountFilter = ref<string>()
const systemAccountFilterSelection = ref<PrincipalSelection>()
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: (ids) => {
    if (!systemAccountFilter.value || !ids.includes(systemAccountFilter.value)) return
    systemAccountFilter.value = undefined
    systemAccountFilterSelection.value = undefined
    resetModelCheckScopedState()
    void loadOptions()
    void reloadRuns()
  },
  selectedIds: () => [systemAccountFilter.value]
})
const optionsLoading = ref(false)
const targetOptionsLoading = ref(false)
const comparisonOptionsLoading = ref(false)
const historyTargetOptionsLoading = ref(false)
const submitting = ref(false)
const detailLoading = ref(false)
const detailOpen = ref(false)
const terminalVisible = ref(false)
const terminalLines = ref<TerminalLine[]>([])
const terminalBodyRef = ref<HTMLElement>()
let modelCheckAbortController: AbortController | undefined
const options = ref<ModelCheckOptions>(fallbackOptions)
const targetOptions = ref<AccountSelectOption[]>([])
const comparisonOptions = ref<AccountSelectOption[]>([])
const historyTargetOptions = ref<AccountSelectOption[]>([])
const targetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
const comparisonOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
const historyTargetOptionsCache = createShortLivedQueryCache<AccountSelectOption[]>({ ttlMs: 10_000 })
const currentRun = ref<ModelCheckRunDetail>()
const selectedTargetAccount = ref<AccountSelection>()
const selectedComparisonAccount = ref<AccountSelection>()
const selectedHistoryTargetAccount = ref<AccountSelection>()
const form = reactive<ModelCheckRunPayload>({
  targetType: 'account',
  targetId: '',
  model: 'gpt-5.5',
  profile: 'full',
  trustedComparison: false,
  trustedComparisonAccountId: undefined
})
const filters = reactive<{
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
}>({})
const {
  items: runs,
  loading: runsLoading,
  mobileHasMore: runsMobileHasMore,
  mobileLoadingMore: runsMobileLoadingMore,
  tablePagination: runsTablePagination,
  handleTableChange: handleRunsTableChange,
  loadData: loadRuns,
  loadMoreMobile: loadMoreMobileRuns,
  refreshMobile: refreshMobileRuns,
  resetPagination: resetRunsPagination
} = useResponsivePagedList<ModelCheckRunSummary>({
  pageSize: modelCheckPageSize,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? Math.max(0, total - 1))} 条检测记录，还有更多`
    : `共 ${formatNumber(total)} 条检测记录`,
  fetchPage: (_options, pageState) => {
    if (managementScopeRequired.value) {
      return Promise.resolve({
        items: [],
        page: pageState.current,
        pageSize: pageState.pageSize,
        total: 0,
        hasMore: false
      })
    }
    return modelChecksApi.list({
      ...modelCheckScopeParams.value,
      page: pageState.current,
      pageSize: pageState.pageSize,
      targetType: 'account',
      targetId: filters.targetId?.trim() || undefined,
      model: filters.model,
      level: filters.level,
      status: filters.status
    }).then((page) => {
      rememberRunAccountLabels(page.items)
      return page
    })
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测历史失败'))
  }
})

const modelOptions = computed(() => options.value.supportedModels.map((item) => ({ label: item.label, value: item.value })))
const selectedManagementSystemAccountId = computed(() => isManagementView.value
  ? scopedSystemAccountId(systemAccountFilter.value || allSystemAccountsValue)
  : undefined)
const modelCheckScopeParams = computed(() => {
  const systemAccountId = selectedManagementSystemAccountId.value
  return isManagementView.value && systemAccountId ? { systemAccountId } : undefined
})
const managementScopeRequired = computed(() => isManagementView.value && !selectedManagementSystemAccountId.value)
const accountSelectDisabled = computed(() => submitting.value || managementScopeRequired.value)
const accountSelectPlaceholder = computed(() => managementScopeRequired.value ? '请先选择系统账户' : '输入账户名称搜索')
const comparisonSelectPlaceholder = computed(() => managementScopeRequired.value ? '请先选择系统账户' : '可信对比账户（可选）')
const historyAccountSelectPlaceholder = computed(() => managementScopeRequired.value ? '请先选择系统账户' : '全部账户')
const modelCheckHistoryEmptyText = computed(() => managementScopeRequired.value ? '请先选择系统账户后查看模型检测历史' : '暂无模型检测历史')
const detailDescriptionColumns = computed(() => (window.innerWidth < 900 ? 1 : 2))
const terminalStatusText = computed(() => submitting.value ? '运行中' : terminalLines.value.length ? '最近一次' : '待开始')
const terminalStatusColor = computed(() => submitting.value ? 'blue' : terminalLines.value.length ? 'green' : 'default')
const terminalNow = computed(() => formatClockTime(new Date()))
let targetOptionsRequestId = 0
let comparisonOptionsRequestId = 0
let historyTargetOptionsRequestId = 0
let terminalLineId = 0
let modelCheckAbortReason: 'manual' | 'deactivated' | 'unmount' | undefined

type TerminalLineLevel = 'info' | 'success' | 'warning' | 'error' | 'muted'
type TerminalLine = {
  id: number
  time: string
  level: TerminalLineLevel
  text: string
}

async function loadOptions() {
  optionsLoading.value = true
  try {
    const nextOptions = await modelChecksApi.options(modelCheckScopeParams.value)
    options.value = nextOptions
    form.model = nextOptions.defaultModel
    form.profile = nextOptions.defaultProfile
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测选项失败'))
  } finally {
    optionsLoading.value = false
  }
}

async function loadTargetOptions(keyword = '') {
  if (managementScopeRequired.value) {
    targetOptions.value = []
    targetOptionsLoading.value = false
    return
  }
  const normalizedKeyword = keyword.trim()
  const systemAccountId = modelCheckScopeParams.value?.systemAccountId
  const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword])
  const requestId = ++targetOptionsRequestId
  const cachedOptions = targetOptionsCache.get(requestKey)
  if (cachedOptions) {
    targetOptionsLoading.value = false
    targetOptions.value = cachedOptions
    return
  }
  targetOptionsLoading.value = true
  try {
    const accounts = await accountsApi.options({
      systemAccountId,
      keyword: normalizedKeyword || undefined,
      status: 'active',
      schedulable: 'enabled',
      limit: 50
    })
    const nextOptions = accounts
      .filter((account) => account.providerCode === 'openai')
      .filter((account) => Boolean(account.name.trim()))
      .map(accountTargetOption)
    targetOptionsCache.set(requestKey, nextOptions)
    if (requestId === targetOptionsRequestId) {
      targetOptions.value = nextOptions
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载检测目标失败'))
  } finally {
    if (requestId === targetOptionsRequestId) {
      targetOptionsLoading.value = false
    }
  }
}

async function loadComparisonOptions(keyword = '') {
  if (managementScopeRequired.value) {
    comparisonOptions.value = []
    comparisonOptionsLoading.value = false
    return
  }
  const normalizedKeyword = keyword.trim()
  const systemAccountId = modelCheckScopeParams.value?.systemAccountId
  const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword, form.targetId])
  const requestId = ++comparisonOptionsRequestId
  const cachedOptions = comparisonOptionsCache.get(requestKey)
  if (cachedOptions) {
    comparisonOptionsLoading.value = false
    comparisonOptions.value = cachedOptions
    return
  }
  comparisonOptionsLoading.value = true
  try {
    const accounts = await accountsApi.options({
      systemAccountId,
      keyword: normalizedKeyword || undefined,
      status: 'active',
      schedulable: 'enabled',
      limit: 50
    })
    const nextOptions = accounts
      .filter((account) => account.providerCode === 'openai' && account.id !== form.targetId)
      .filter((account) => Boolean(account.name.trim()))
      .map(accountTargetOption)
    comparisonOptionsCache.set(requestKey, nextOptions)
    if (requestId === comparisonOptionsRequestId) {
      comparisonOptions.value = nextOptions
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载可信对比账户失败'))
  } finally {
    if (requestId === comparisonOptionsRequestId) {
      comparisonOptionsLoading.value = false
    }
  }
}

async function loadHistoryTargetOptions(keyword = '') {
  if (managementScopeRequired.value) {
    historyTargetOptions.value = []
    historyTargetOptionsLoading.value = false
    return
  }
  const normalizedKeyword = keyword.trim()
  const systemAccountId = modelCheckScopeParams.value?.systemAccountId
  const requestKey = JSON.stringify([systemAccountId ?? 'self', normalizedKeyword])
  const requestId = ++historyTargetOptionsRequestId
  const cachedOptions = historyTargetOptionsCache.get(requestKey)
  if (cachedOptions) {
    historyTargetOptionsLoading.value = false
    historyTargetOptions.value = cachedOptions
    return
  }
  historyTargetOptionsLoading.value = true
  try {
    const accounts = await accountsApi.options({
      systemAccountId,
      keyword: normalizedKeyword || undefined,
      limit: 50
    })
    const nextOptions = accounts
      .filter((account) => account.providerCode === 'openai')
      .filter((account) => Boolean(account.name.trim()))
      .map(accountTargetOption)
    historyTargetOptionsCache.set(requestKey, nextOptions)
    if (requestId === historyTargetOptionsRequestId) {
      historyTargetOptions.value = nextOptions
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载历史账户筛选项失败'))
  } finally {
    if (requestId === historyTargetOptionsRequestId) {
      historyTargetOptionsLoading.value = false
    }
  }
}

async function submitRun() {
  if (managementScopeRequired.value) {
    message.warning('请先选择系统账户')
    return
  }
  const targetId = form.targetId.trim()
  const trustedComparisonAccountId = form.trustedComparisonAccountId?.trim()
  if (!targetId) {
    message.warning('请选择 AI 账户')
    return
  }
  if (trustedComparisonAccountId && trustedComparisonAccountId === targetId) {
    message.warning('可信对比账户不能和检测目标相同')
    return
  }
  stopCurrentModelCheck(false)
  const controller = new AbortController()
  modelCheckAbortController = controller
  submitting.value = true
  detailOpen.value = false
  resetTerminal()
  currentRun.value = undefined
  try {
    const payload: ModelCheckRunPayload = {
      targetType: form.targetType,
      targetId,
      model: form.model,
      profile: form.profile,
      trustedComparison: Boolean(trustedComparisonAccountId),
      trustedComparisonAccountId: trustedComparisonAccountId || undefined
    }
    appendTerminalLine('info', `juhe-ai model-check --account "${targetOptionText(targetId)}" --model ${form.model}${trustedComparisonAccountId ? ` --trusted-account "${comparisonOptionText(trustedComparisonAccountId)}"` : ''}`)
    appendTerminalLine('muted', '已连接系统 API，等待后端返回检测进度流')
    currentRun.value = await modelChecksApi.runStream(payload, {
      signal: controller.signal,
      onProgress: handleModelCheckProgress
    }, modelCheckScopeParams.value)
    appendTerminalLine('success', `检测报告已生成：${levelText(currentRun.value.level)}，${currentRun.value.score}/${currentRun.value.maxScore}，${currentRun.value.message || '-'}`)
    message.success('模型检测完成')
    await reloadRuns()
  } catch (error) {
    console.error(error)
    const errorMessage = extractApiErrorMessage(error, '模型检测提交失败')
    if (controller.signal.aborted) {
      if (modelCheckAbortReason !== 'deactivated' && modelCheckAbortReason !== 'unmount') {
        appendTerminalLine('warning', '检测已停止')
        message.info('模型检测已停止')
      }
    } else {
      appendTerminalLine('error', errorMessage)
      message.error(errorMessage)
    }
    if (!controller.signal.aborted) {
      await loadRuns()
    }
  } finally {
    if (modelCheckAbortController === controller) {
      modelCheckAbortController = undefined
    }
    modelCheckAbortReason = undefined
    submitting.value = false
  }
}

async function reloadRuns() {
  resetRunsPagination()
  await loadRuns()
}

async function loadRunDetail(id: string) {
  if (managementScopeRequired.value) {
    message.warning('请先选择系统账户')
    return
  }
  detailOpen.value = true
  detailLoading.value = true
  try {
    currentRun.value = await modelChecksApi.detail(id, modelCheckScopeParams.value)
    rememberRunAccountLabels([currentRun.value])
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测详情失败'))
  } finally {
    detailLoading.value = false
  }
}

function handleSystemAccountFilterChange() {
  if (!systemAccountFilter.value) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetModelCheckScopedState()
  void loadOptions()
  void reloadRuns()
}

function resetModelCheckScopedState() {
  form.targetId = ''
  selectedTargetAccount.value = undefined
  form.trustedComparison = false
  form.trustedComparisonAccountId = undefined
  selectedComparisonAccount.value = undefined
  filters.targetId = undefined
  selectedHistoryTargetAccount.value = undefined
  currentRun.value = undefined
  detailOpen.value = false
  targetOptions.value = []
  comparisonOptions.value = []
  historyTargetOptions.value = []
  targetOptionsLoading.value = false
  comparisonOptionsLoading.value = false
  historyTargetOptionsLoading.value = false
  targetOptionsCache.clear()
  comparisonOptionsCache.clear()
  historyTargetOptionsCache.clear()
  targetOptionsRequestId += 1
  comparisonOptionsRequestId += 1
  historyTargetOptionsRequestId += 1
}

function handleTargetSearch(value: string) {
  void loadTargetOptions(value)
}

function handleTargetValueUpdate(value: SelectValue) {
  form.targetId = typeof value === 'string' ? value : ''
  selectedTargetAccount.value = selectedAccountForId(form.targetId, targetOptions.value)
}

function handleTargetChange() {
  if (form.trustedComparisonAccountId && form.trustedComparisonAccountId === form.targetId) {
    form.trustedComparisonAccountId = undefined
    selectedComparisonAccount.value = undefined
  }
  comparisonOptions.value = comparisonOptions.value.filter((item) => item.value !== form.targetId)
}

function handleTargetDropdownVisibleChange(open: boolean) {
  if (open && !targetOptions.value.length) {
    void loadTargetOptions()
  }
}

function handleComparisonSearch(value: string) {
  void loadComparisonOptions(value)
}

function handleComparisonDropdownVisibleChange(open: boolean) {
  if (open && !comparisonOptions.value.length) {
    void loadComparisonOptions()
  }
}

function handleHistoryTargetSearch(value: string) {
  void loadHistoryTargetOptions(value)
}

function handleHistoryTargetDropdownVisibleChange(open: boolean) {
  if (open && !historyTargetOptions.value.length) {
    void loadHistoryTargetOptions()
  }
}

function resetRunForm() {
  form.targetId = ''
  selectedTargetAccount.value = undefined
  form.model = options.value.defaultModel
  form.profile = options.value.defaultProfile
  form.trustedComparison = false
  form.trustedComparisonAccountId = undefined
  selectedComparisonAccount.value = undefined
}

function resetTerminal() {
  terminalVisible.value = true
  terminalLines.value = []
  terminalLineId = 0
}

function stopCurrentModelCheck(appendLog = true, reason: 'manual' | 'deactivated' | 'unmount' = 'manual') {
  if (modelCheckAbortController && !modelCheckAbortController.signal.aborted) {
    modelCheckAbortReason = reason
    modelCheckAbortController.abort()
    if (appendLog) {
      appendTerminalLine('warning', '已请求停止当前检测')
    }
  }
}

function clearTerminal() {
  stopCurrentModelCheck(false)
  terminalLines.value = []
  terminalVisible.value = false
}

function appendTerminalLine(level: TerminalLineLevel, text: string) {
  terminalLines.value.push({
    id: ++terminalLineId,
    time: formatClockTime(new Date()),
    level,
    text
  })
  void nextTick(scrollTerminalToBottom)
}

function scrollTerminalToBottom() {
  if (!terminalBodyRef.value) return
  terminalBodyRef.value.scrollTop = terminalBodyRef.value.scrollHeight
}

function handleModelCheckProgress(event: ModelCheckProgressEvent) {
  if (event.type === 'run_started') {
    rememberAccountLabelIfUnknown(event.targetId, event.targetName)
    rememberAccountLabelIfUnknown(event.trustedComparisonAccountId, event.trustedComparisonAccountName)
    const targetLabel = event.targetName?.trim() || targetOptionText(event.targetId)
    const comparisonText = event.trustedComparison
      ? `，可信对比 ${event.trustedComparisonAccountName?.trim() || (event.trustedComparisonAccountId ? comparisonOptionText(event.trustedComparisonAccountId) : '未记录账户名称')}`
      : '，可信对比关闭'
    appendTerminalLine('info', `检测启动：目标 AI 账户 ${targetLabel}，模型 ${event.model}${comparisonText}`)
    return
  }
  if (event.type === 'run_created') {
    appendTerminalLine('success', `检测记录已创建：${event.runId}，Trace ${event.traceId}`)
    void loadRuns()
    return
  }
  if (event.type === 'probe_started') {
    appendTerminalLine('info', `AI 请求 -> ${progressItemTitle(event.itemKey)} (${event.method} ${event.path})`)
    return
  }
  if (event.type === 'probe_completed') {
    const output = event.outputPreview ? `，输出 "${event.outputPreview}"` : ''
    const responseModel = event.responseModel ? `，返回模型 ${event.responseModel}` : ''
    appendTerminalLine(event.success ? 'success' : 'warning', `${progressItemTitle(event.itemKey)} 响应完成：HTTP ${event.statusCode}，${formatDuration(event.durationMs)}，Trace ${event.traceId}${responseModel}${output}`)
    return
  }
  if (event.type === 'item_completed') {
    appendTerminalLine(terminalLevelForCheckStatus(event.status), `${progressItemTitle(event.itemKey, event.itemType)} 评分：${checkStatusText(event.status)}，${event.score}/${event.maxScore}，${event.message}`)
    return
  }
  if (event.type === 'run_completed') {
    appendTerminalLine(event.status === 'completed' ? 'success' : 'error', `检测结束：${statusText(event.status)}，${levelText(event.level)}，${event.score}/${event.maxScore}，${event.message}`)
  }
}

function accountTargetOption(account: AccountOptionSummary) {
  const label = accountSelectOptionLabel(account)
  rememberAccountLabel(account.id, label)
  return { label, value: account.id }
}

function targetOptionText(id: string) {
  return selectedTargetAccount.value?.id === id
    ? selectedTargetAccount.value.name
    : accountNameForId(id, targetOptions.value) ?? '未记录账户名称'
}

function comparisonOptionText(id: string) {
  return selectedComparisonAccount.value?.id === id
    ? selectedComparisonAccount.value.name
    : accountNameForId(id, comparisonOptions.value) ?? '未记录账户名称'
}

function knownTargetName(id: string) {
  const targetName = runs.value.find((item) => item.targetId === id)?.targetName?.trim()
  if (targetName) return targetName
  if (currentRun.value?.targetId === id) {
    return currentRun.value.targetName?.trim() || undefined
  }
  return undefined
}

function selectedAccountForId(id: string | undefined, options: AccountSelectOption[]): AccountSelection | undefined {
  return accountSelectionForId(id, [], options)
}

function accountNameForId(id: string, options: AccountSelectOption[]): string | undefined {
  return selectedAccountForId(id, options)?.name || knownTargetName(id) || accountLabelForId(id)
}

function rememberRunAccountLabels(items: Array<Pick<ModelCheckRunSummary, 'targetId' | 'targetName'>>) {
  for (const item of items) {
    rememberAccountLabelIfUnknown(item.targetId, item.targetName)
  }
}

function rememberAccountLabelIfUnknown(id: string | undefined, name: string | undefined) {
  if (!accountLabelForId(id)) {
    rememberAccountLabel(id, name)
  }
}

function targetDisplayName(run: Pick<ModelCheckRunSummary, 'targetName' | 'targetId'>) {
  const name = accountLabelForId(run.targetId) || run.targetName?.trim() || knownTargetName(run.targetId)
  return name || '未记录账户名称'
}

function accountTypeText(value: string) {
  if (value === 'api_key') return 'API Key 账户'
  if (value === 'oauth') return 'OAuth 账户'
  return value
}

function runTrustedComparison(run: Pick<ModelCheckRunSummary, 'trustedComparison'>) {
  return run.trustedComparison
}

function statusText(value: ModelCheckStatus) {
  return statusOptions.find((item) => item.value === value)?.label ?? value
}

function statusColor(value: ModelCheckStatus) {
  if (value === 'completed') return 'green'
  if (value === 'failed') return 'red'
  if (value === 'running') return 'blue'
  return 'default'
}

function levelText(value: ModelCheckLevel) {
  return levelOptions.find((item) => item.value === value)?.label ?? value
}

function levelColor(value: ModelCheckLevel) {
  if (value === 'high_confidence') return 'green'
  if (value === 'likely') return 'blue'
  if (value === 'uncertain') return 'orange'
  if (value === 'suspicious') return 'red'
  return 'default'
}

function checkStatusText(value: NonNullable<ModelCheckCheckResult['status']>) {
  if (value === 'passed') return '通过'
  if (value === 'warning') return '需关注'
  if (value === 'failed') return '失败'
  if (value === 'skipped') return '跳过'
  return value
}

function checkStatusColor(value: NonNullable<ModelCheckCheckResult['status']>) {
  if (value === 'passed') return 'green'
  if (value === 'warning') return 'orange'
  if (value === 'failed') return 'red'
  if (value === 'skipped') return 'default'
  return 'default'
}

function modelText(value: string) {
  return options.value.supportedModels.find((item) => item.value === value)?.label ?? value
}

function selectValueOrUndefined(value?: string) {
  return value?.trim() || undefined
}

function formatDuration(value?: number) {
  if (value === undefined) return '-'
  if (value >= 1000) return `${(value / 1000).toFixed(1)} 秒`
  return `${Math.round(value)} 毫秒`
}

function checkTitle(check: ModelCheckCheckResult) {
  return checkTitleByType(check.itemType, check.itemKey)
}

function checkTitleByType(itemType: string, itemKey: string) {
  const labels: Record<string, string> = {
    model_catalog: '模型目录',
    responses_basic: 'Responses 非流式',
    responses_stream: 'Responses 流式',
    structured_output: '结构化输出',
    tool_calling: '工具调用',
    usage_shape: 'Usage 字段',
    behavior_probe: '行为探针',
    long_context: '长上下文找针',
    stability: '稳定性探针',
    cross_model: '辅助模型对照',
    distribution_similarity: '分布相似度对照',
    trusted_comparison: '可信对比'
  }
  return labels[itemType] ?? itemKey
}

function progressItemTitle(itemKey: string, itemType?: string) {
  if (itemKey.includes('.distribution.')) return '分布相似度采样'
  return checkTitleByType(itemType ?? itemKey.split('.').pop() ?? itemKey, itemKey)
}

function terminalLevelForCheckStatus(status: ModelCheckCheckResult['status']): TerminalLineLevel {
  if (status === 'passed') return 'success'
  if (status === 'warning') return 'warning'
  if (status === 'failed') return 'error'
  return 'muted'
}

function checkMessage(check: ModelCheckCheckResult) {
  const message = check.evidenceSummary.message
  return typeof message === 'string' && message.trim() ? message.trim() : check.errorMessage
}

function hasCheckExtra(check: ModelCheckCheckResult) {
  return Object.keys(check.evidenceSummary).length > 0 || Boolean(check.traceId)
}

function checkExtra(check: ModelCheckCheckResult) {
  return {
    traceId: check.traceId,
    evidence: check.evidenceSummary,
    errorCode: check.errorCode,
    errorMessage: check.errorMessage
  }
}

function formatJson(value: unknown) {
  return JSON.stringify(value, null, 2)
}

function formatClockTime(value: Date) {
  return [value.getHours(), value.getMinutes(), value.getSeconds()]
    .map((item) => String(item).padStart(2, '0'))
    .join(':')
}

onMounted(async () => {
  await Promise.all([loadOptions(), loadRuns()])
})

onDeactivated(() => {
  stopCurrentModelCheck(false, 'deactivated')
})

onBeforeUnmount(() => {
  stopCurrentModelCheck(false, 'unmount')
})
</script>

<style scoped>
.model-checks-page {
  display: flex;
  height: calc(100dvh - 154px);
  min-height: 0;
  flex-direction: column;
  gap: 16px;
}

.model-checks-run-card {
  flex: 0 0 auto;
}

.model-checks-run-card,
.model-checks-history-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
}

.model-checks-history-card {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.model-checks-history-card :deep(.ant-card-body) {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.model-checks-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.history-toolbar,
.run-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.model-checks-control-panel {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.model-checks-fields {
  display: flex;
  flex: 1 1 620px;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
  min-width: 0;
}

.model-checks-system-account-field,
.model-checks-account-field {
  flex: 0 1 300px;
  width: 300px;
  min-width: 240px;
}

.model-checks-model-field {
  flex: 0 0 160px;
  width: 160px;
  min-width: 140px;
}

.model-checks-comparison-field {
  flex: 0 1 300px;
  width: 300px;
  min-width: 240px;
}

.model-checks-toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  flex-wrap: wrap;
}

.model-check-terminal {
  display: flex;
  height: 344px;
  margin-top: 14px;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid #1e293b;
  border-radius: 10px;
  background: #020617;
}

.model-check-terminal-head {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-bottom: 1px solid #1e293b;
  background: #0f172a;
}

.terminal-title {
  color: #e2e8f0;
  font-size: 13px;
  font-weight: 700;
}

.terminal-subtitle {
  margin-top: 2px;
  color: #94a3b8;
  font-size: 12px;
}

.model-check-terminal-body {
  min-height: 0;
  flex: 1 1 auto;
  padding: 12px;
  overflow: auto;
  color: #cbd5e1;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.7;
}

.terminal-line {
  display: grid;
  grid-template-columns: auto auto minmax(0, 1fr);
  gap: 8px;
  align-items: start;
  word-break: break-word;
}

.terminal-time {
  color: #64748b;
  white-space: nowrap;
}

.terminal-prompt {
  color: #38bdf8;
}

.terminal-line-success .terminal-text {
  color: #86efac;
}

.terminal-line-warning .terminal-text {
  color: #fde68a;
}

.terminal-line-error .terminal-text {
  color: #fca5a5;
}

.terminal-line-muted .terminal-text {
  color: #94a3b8;
}

.terminal-cursor::after {
  display: inline-block;
  width: 6px;
  height: 12px;
  margin-left: 4px;
  vertical-align: -2px;
  background: #38bdf8;
  content: '';
  animation: terminal-cursor-blink 1s steps(1) infinite;
}

@keyframes terminal-cursor-blink {
  50% {
    opacity: 0;
  }
}

.run-detail {
  display: grid;
  gap: 14px;
}

.run-detail-title {
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.run-detail-subtitle {
  margin-top: 4px;
  color: #64748b;
  font-size: 13px;
}

.run-descriptions {
  background: #fff;
}

.check-list {
  display: grid;
  gap: 10px;
}

.check-item {
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fbfdff;
}

.check-item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: #0f172a;
  font-weight: 700;
}

.check-message {
  margin-top: 6px;
  color: #475569;
  font-size: 13px;
  line-height: 1.6;
}

.json-block {
  max-height: 320px;
  margin: 10px 0 0;
  padding: 12px;
  overflow: auto;
  color: #dbeafe;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  white-space: pre-wrap;
  word-break: break-word;
  background: #0f172a;
  border-radius: 8px;
}

.history-toolbar {
  flex: 0 0 auto;
  margin-bottom: 14px;
}

.model-checks-responsive-list {
  min-height: 0;
  flex: 1 1 auto;
}

.history-filter {
  width: 140px;
}

.history-target-filter {
  width: 240px;
}

.history-system-account-filter {
  width: 220px;
}

.target-cell {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 8px;
}

.target-name-cell {
  display: block;
  min-width: 0;
  max-width: 210px;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.summary-cell {
  display: block;
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-checks-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.model-check-mobile-card {
  display: grid;
  gap: 12px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.model-check-mobile-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.model-check-mobile-title {
  color: #0f172a;
  font-weight: 700;
  line-height: 1.35;
}

.model-check-mobile-subtitle {
  margin-top: 4px;
  color: #64748b;
  word-break: break-all;
}

.model-check-mobile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.model-check-mobile-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.model-check-mobile-metric {
  min-width: 0;
  padding: 10px;
  border: 1px solid #eef2f7;
  border-radius: 8px;
  background: #f8fafc;
}

.model-check-mobile-wide {
  grid-column: 1 / -1;
}

.model-check-mobile-metric span {
  display: block;
  color: #64748b;
  font-size: 12px;
}

.model-check-mobile-metric strong {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-check-mobile-actions {
  display: flex;
  justify-content: flex-end;
}

.model-checks-detail-drawer :deep(.ant-drawer-content-wrapper) {
  max-width: 100vw;
}

@media (max-width: 900px) {
  .model-checks-page {
    height: auto;
    min-height: calc(100dvh - 108px);
  }

  .history-toolbar,
  .run-detail-head,
  .check-item-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .model-checks-control-panel,
  .model-checks-fields {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
  }

  .model-checks-system-account-field,
  .model-checks-account-field,
  .model-checks-model-field,
  .model-checks-comparison-field {
    width: 100%;
    flex: none;
    min-width: 0;
  }

  .model-checks-toolbar {
    align-items: stretch;
    width: 100%;
    flex-direction: column;
    justify-content: flex-start;
  }

  .model-checks-fields :deep(.ant-btn),
  .model-checks-toolbar :deep(.ant-btn) {
    width: 100%;
  }

  .model-check-terminal-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .model-check-terminal {
    height: 320px;
  }

  .history-filter,
  .history-system-account-filter,
  .history-target-filter {
    width: 100%;
  }
}
</style>
