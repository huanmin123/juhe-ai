<template>
  <div class="model-checks-page">
    <ModelCheckRunPanel
      :account-select-disabled="accountSelectDisabled"
      :account-select-placeholder="accountSelectPlaceholder"
      :comparison-options="comparisonOptions"
      :comparison-options-loading="comparisonOptionsLoading"
      :comparison-select-placeholder="comparisonSelectPlaceholder"
      :is-management-view="isManagementView"
      :model="form.model"
      :model-options="modelOptions"
      :options-loading="optionsLoading"
      :selected-comparison-account="selectedComparisonAccount"
      :selected-target-account="selectedTargetAccount"
      :submitting="submitting"
      :system-account-filter="systemAccountFilter"
      :system-account-filter-selection="systemAccountFilterSelection"
      :system-account-options-loading="systemAccountOptionsLoading"
      :system-accounts="systemAccounts"
      :target-id="selectValueOrUndefined(form.targetId)"
      :target-options="targetOptions"
      :target-options-loading="targetOptionsLoading"
      :terminal-lines="terminalLines"
      :terminal-status-color="terminalStatusColor"
      :terminal-status-text="terminalStatusText"
      :terminal-visible="terminalVisible"
      :trusted-comparison-account-id="selectValueOrUndefined(form.trustedComparisonAccountId)"
      @comparison-dropdown-visible-change="handleComparisonDropdownVisibleChange"
      @comparison-search="handleComparisonSearch"
      @refresh="loadOptions"
      @reset="resetRunForm"
      @stop="stopCurrentModelCheck()"
      @submit="submitRun"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @target-change="handleTargetChange"
      @target-dropdown-visible-change="handleTargetDropdownVisibleChange"
      @target-search="handleTargetSearch"
      @target-value-update="handleTargetValueUpdate"
      @update:model="form.model = $event"
      @update:selected-comparison-account="selectedComparisonAccount = $event"
      @update:selected-target-account="selectedTargetAccount = $event"
      @update:system-account-filter="systemAccountFilter = $event || allSystemAccountsValue"
      @update:system-account-filter-selection="systemAccountFilterSelection = $event"
      @update:trusted-comparison-account-id="form.trustedComparisonAccountId = $event"
    />

    <ModelCheckRunHistoryList
      :filters="filters"
      :history-target-options="historyTargetOptions"
      :history-target-options-loading="historyTargetOptionsLoading"
      :is-management-view="isManagementView"
      :loading="runsLoading"
      :mobile-has-more="runsMobileHasMore"
      :mobile-loading-more="runsMobileLoadingMore"
      :model-options="modelOptions"
      :runs="runs"
      :selected-history-target-account="selectedHistoryTargetAccount"
      :submitting="submitting"
      :supported-models="options.supportedModels"
      :system-account-filter="systemAccountFilter"
      :system-account-filter-selection="systemAccountFilterSelection"
      :system-account-options-loading="systemAccountOptionsLoading"
      :system-accounts="systemAccounts"
      :table-pagination="runsTablePagination"
      :target-display-name="targetDisplayName"
      @history-target-dropdown-visible-change="handleHistoryTargetDropdownVisibleChange"
      @history-target-search="handleHistoryTargetSearch"
      @mobile-load-more="loadMoreMobileRuns"
      @mobile-refresh="refreshMobileRuns"
      @reload="reloadRuns"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @table-change="handleRunsTableChange"
      @update:level="filters.level = $event"
      @update:model="filters.model = $event"
      @update:selected-history-target-account="selectedHistoryTargetAccount = $event"
      @update:status="filters.status = $event"
      @update:system-account-filter="systemAccountFilter = $event || allSystemAccountsValue"
      @update:system-account-filter-selection="systemAccountFilterSelection = $event"
      @update:target-id="filters.targetId = $event"
      @view-detail="loadRunDetail"
    />

    <ModelCheckRunDetailDrawer
      v-model:open="detailOpen"
      :description-columns="detailDescriptionColumns"
      :loading="detailLoading"
      :run="currentRun"
      :supported-models="options.supportedModels"
      :target-display-name="targetDisplayName"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, onMounted, reactive, ref } from 'vue'
import { message } from '@/lib/antd'

import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedAccountsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import {
  accountLabelForId,
  rememberAccountLabel
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
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
import {
  checkStatusText,
  formatClockTime,
  formatModelCheckDuration as formatDuration,
  levelText,
  progressItemTitle,
  statusText,
  terminalLevelForCheckStatus
} from './modelCheckFormatters'
import { modelCheckFallbackOptions, modelCheckPageSize } from './modelCheckPageConfig'
import type { ModelCheckTerminalLine } from './ModelCheckTerminal.vue'
import ModelCheckRunPanel from './ModelCheckRunPanel.vue'
import ModelCheckRunHistoryList from './ModelCheckRunHistoryList.vue'
import ModelCheckRunDetailDrawer from './ModelCheckRunDetailDrawer.vue'
import { useModelCheckAccountOptions } from './useModelCheckAccountOptions'

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const modelChecksApi = useScopedModelChecksApi(isManagementView)
const accountsApi = useScopedAccountsApi(isManagementView)
const systemAccountFilter = ref<string>(allSystemAccountsValue)
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
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
    resetModelCheckScopedState()
    void loadOptions()
    void reloadRuns()
  },
  selectedIds: () => [systemAccountFilter.value]
})
const optionsLoading = ref(false)
const submitting = ref(false)
const detailLoading = ref(false)
const detailOpen = ref(false)
const terminalVisible = ref(false)
const terminalLines = ref<ModelCheckTerminalLine[]>([])
let modelCheckAbortController: AbortController | undefined
const options = ref<ModelCheckOptions>(modelCheckFallbackOptions)
const currentRun = ref<ModelCheckRunDetail>()
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
const accountSelectDisabled = computed(() => submitting.value)
const accountSelectPlaceholder = computed(() => '输入账户名称搜索')
const comparisonSelectPlaceholder = computed(() => '可信对比账户（可选）')
const {
  comparisonOptions,
  comparisonOptionsLoading,
  historyTargetOptions,
  historyTargetOptionsLoading,
  selectedComparisonAccount,
  selectedHistoryTargetAccount,
  selectedTargetAccount,
  targetOptions,
  targetOptionsLoading,
  handleComparisonDropdownVisibleChange,
  handleComparisonSearch,
  handleHistoryTargetDropdownVisibleChange,
  handleHistoryTargetSearch,
  handleTargetChange,
  handleTargetDropdownVisibleChange,
  handleTargetSearch,
  handleTargetValueUpdate,
  resetAccountOptionsState,
  resetRunAccountSelection,
  comparisonOptionText,
  selectValueOrUndefined,
  targetOptionText
} = useModelCheckAccountOptions({
  accountsApi,
  form,
  modelCheckScopeParams,
  knownTargetName
})
const viewportWidth = ref(window.innerWidth)
const detailDescriptionColumns = computed(() => (viewportWidth.value < 900 ? 1 : 2))
const terminalStatusText = computed(() => submitting.value ? '运行中' : terminalLines.value.length ? '最近一次' : '待开始')
const terminalStatusColor = computed(() => submitting.value ? 'blue' : terminalLines.value.length ? 'green' : 'default')
let terminalLineId = 0
let modelCheckAbortReason: 'manual' | 'deactivated' | 'unmount' | undefined

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

async function submitRun() {
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
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetModelCheckScopedState()
  void loadOptions()
  void reloadRuns()
}

function resetModelCheckScopedState() {
  resetAccountOptionsState()
  filters.targetId = undefined
  currentRun.value = undefined
  detailOpen.value = false
}

function resetRunForm() {
  resetRunAccountSelection()
  form.model = options.value.defaultModel
  form.profile = options.value.defaultProfile
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

function appendTerminalLine(level: ModelCheckTerminalLine['level'], text: string) {
  terminalLines.value.push({
    id: ++terminalLineId,
    time: formatClockTime(new Date()),
    level,
    text
  })
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

function knownTargetName(id: string) {
  const targetName = runs.value.find((item) => item.targetId === id)?.targetName?.trim()
  if (targetName) return targetName
  if (currentRun.value?.targetId === id) {
    return currentRun.value.targetName?.trim() || undefined
  }
  return undefined
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

function updateViewportWidth() {
  viewportWidth.value = window.innerWidth
}

onMounted(async () => {
  updateViewportWidth()
  window.addEventListener('resize', updateViewportWidth)
  await Promise.all([loadOptions(), loadRuns()])
})

onDeactivated(() => {
  stopCurrentModelCheck(false, 'deactivated')
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', updateViewportWidth)
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

@media (max-width: 900px) {
  .model-checks-page {
    height: auto;
    min-height: calc(100dvh - 108px);
  }
}
</style>
