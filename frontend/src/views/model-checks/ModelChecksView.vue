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
      :model-options="runModelOptions"
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
      :terminal-waiting-text="terminalWaitingText"
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
      @update:model="handleModelUpdate"
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
      :model-options="historyModelOptions"
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
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { message } from '@/lib/antd'

import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedAccountsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import {
  accountLabelForId,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { loadEntityDetailCached } from '@/shared/entityDetailCache'
import { formatNumber } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, stringUnionOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
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
  formatModelCheckDuration as formatDuration,
  levelText,
  progressItemTitle,
  statusText,
  terminalLevelForCheckStatus
} from './modelCheckFormatters'
import { modelCheckFallbackOptions, modelCheckPageSize } from './modelCheckPageConfig'
import {
  canUseModelCheckModelForAccount,
  modelCheckModelsForAccount,
  sameModelCheckAccountProfile
} from './modelCheckProviderCapabilities'
import {
  appendModelCheckTerminalLine,
  ModelCheckSessionBusyError,
  modelCheckRunSession,
  reconcileModelCheckRunSessionWithActiveRun,
  startModelCheckRunSession,
  stopModelCheckRunSession
} from './modelCheckRunSession'
import type { ModelCheckTerminalLine } from './ModelCheckTerminal.vue'
import ModelCheckRunPanel from './ModelCheckRunPanel.vue'
import ModelCheckRunHistoryList from './ModelCheckRunHistoryList.vue'
import ModelCheckRunDetailDrawer from './ModelCheckRunDetailDrawer.vue'
import { useModelCheckAccountOptions } from './useModelCheckAccountOptions'

interface ModelChecksPageState {
  filters: {
    targetId?: string
    model?: ModelCheckModel
    level?: ModelCheckLevel
    status?: ModelCheckStatus
  }
  historyTargetAccount?: AccountSelection
  pagination: PagePaginationState
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const modelChecksApi = useScopedModelChecksApi(isManagementView)
const accountsApi = useScopedAccountsApi(isManagementView)
const pageStateCache = usePageStateCache<ModelChecksPageState>(undefined, defaultModelChecksPageState, {
  sanitize: sanitizeModelChecksPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const systemAccountFilter = ref<string>(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
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
const submitting = computed(() => modelCheckRunSession.submitting)
const detailLoading = ref(false)
const detailOpen = ref(false)
const terminalVisible = computed(() => modelCheckRunSession.terminalVisible)
const terminalLines = computed(() => modelCheckRunSession.terminalLines)
const options = ref<ModelCheckOptions>(modelCheckFallbackOptions)
const currentRun = ref<ModelCheckRunDetail>()
const form = reactive<ModelCheckRunPayload>({
  targetType: 'account',
  targetId: '',
  model: modelCheckFallbackOptions.defaultModel,
  profile: 'full',
  trustedComparison: false,
  trustedComparisonAccountId: undefined
})
const filters = reactive<{
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
}>({ ...initialPageState.filters })
const {
  items: runs,
  loading: runsLoading,
  mobileHasMore: runsMobileHasMore,
  mobileLoadingMore: runsMobileLoadingMore,
  pagination: runsPagination,
  tablePagination: runsTablePagination,
  handleTableChange: handleRunsTableChange,
  loadData: loadRuns,
  loadMoreMobile: loadMoreMobileRuns,
  refreshMobile: refreshMobileRuns,
  resetPagination: resetRunsPagination
} = useResponsivePagedList<ModelCheckRunSummary>({
  pageSize: modelCheckPageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? Math.max(0, total - 1))} 条检测记录，还有更多`
    : `共 ${formatNumber(total)} 条检测记录`,
  fetchPage: (_options, pageState) => {
    return modelChecksApi.list(modelCheckRunListParams(pageState)).then((page) => {
      rememberRunAccountLabels(page.items)
      return page
    })
  },
  requestSignature: (_options, pageState) => modelCheckRunListParams(pageState),
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测历史失败'))
  }
})

const selectedManagementSystemAccountId = computed(() => isManagementView.value
  ? scopedSystemAccountId(systemAccountFilter.value || allSystemAccountsValue)
  : undefined)
const modelCheckScopeParams = computed(() => {
  const systemAccountId = selectedManagementSystemAccountId.value
  return isManagementView.value && systemAccountId ? { systemAccountId } : undefined
})

function modelCheckRunListParams(pageState: { current: number; pageSize: number }) {
  return {
    ...modelCheckScopeParams.value,
    page: pageState.current,
    pageSize: pageState.pageSize,
    targetType: 'account' as const,
    targetId: filters.targetId?.trim() || undefined,
    model: filters.model,
    level: filters.level,
    status: filters.status
  }
}
const accountSelectDisabled = computed(() => submitting.value)
const accountSelectPlaceholder = computed(() => '输入账户名称搜索')
const comparisonSelectPlaceholder = computed(() => '可信对比账户（可选）')
const {
  comparisonOptions,
  comparisonOptionsLoading,
  historyTargetOptions,
  historyTargetOptionsLoading,
  selectedComparisonAccount,
  selectedComparisonAccountProfile,
  selectedHistoryTargetAccount,
  selectedTargetAccount,
  selectedTargetAccountProfile,
  targetOptions,
  targetOptionsLoading,
  handleComparisonDropdownVisibleChange,
  handleComparisonSearch,
  handleHistoryTargetDropdownVisibleChange,
  handleHistoryTargetSearch,
  handleTargetChange: handleTargetAccountChange,
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
selectedHistoryTargetAccount.value = initialPageState.historyTargetAccount
const historyModelOptions = computed(() => options.value.supportedModels.map((item) => ({ label: item.label, value: item.value })))
const runModelOptions = computed(() => {
  const accountModels = modelCheckModelsForAccount(selectedTargetAccountProfile.value)
  const supportedModels = accountModels.length
    ? options.value.supportedModels.filter((item) => accountModels.includes(item.value))
    : options.value.supportedModels
  return supportedModels.map((item) => ({ label: item.label, value: item.value }))
})
const viewportWidth = ref(window.innerWidth)
const detailDescriptionColumns = computed(() => (viewportWidth.value < 900 ? 1 : 2))
const terminalStatusText = computed(() => submitting.value ? modelCheckRunSession.detached ? '后台运行' : '运行中' : terminalLines.value.length ? '最近一次' : '待开始')
const terminalStatusColor = computed(() => submitting.value ? 'blue' : terminalLines.value.length ? 'green' : 'default')
const terminalWaitingText = computed(() => modelCheckRunSession.detached ? '后端检测继续运行，旧进度流不会回放' : '等待下一个检测事件')

async function loadOptions() {
  optionsLoading.value = true
  try {
    const nextOptions = await modelChecksApi.options(modelCheckScopeParams.value)
    options.value = nextOptions
    form.model = nextOptions.defaultModel
    form.profile = nextOptions.defaultProfile
    ensureRunModelMatchesTarget()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测选项失败'))
  } finally {
    optionsLoading.value = false
  }
}

function handleModelUpdate(model: ModelCheckModel) {
  form.model = model
  clearIncompatibleComparisonAccount()
}

function handleTargetChange() {
  handleTargetAccountChange()
  ensureRunModelMatchesTarget()
  clearIncompatibleComparisonAccount()
}

function ensureRunModelMatchesTarget() {
  const currentModel = form.model?.trim()
  const nextModel = runModelOptions.value[0]?.value
  if (nextModel && !runModelOptions.value.some((item) => item.value === currentModel)) {
    form.model = nextModel
  }
}

function clearIncompatibleComparisonAccount() {
  if (!form.trustedComparisonAccountId) return
  const comparisonProfile = selectedComparisonAccountProfile.value
  if (!comparisonProfile) return
  const targetProfile = selectedTargetAccountProfile.value
  const sameProfile = targetProfile ? sameModelCheckAccountProfile(targetProfile, comparisonProfile) : true
  if (sameProfile && canUseModelCheckModelForAccount(comparisonProfile, form.model)) return
  form.trustedComparison = false
  form.trustedComparisonAccountId = undefined
  selectedComparisonAccount.value = undefined
}

async function submitRun() {
  if (modelCheckRunSession.submitting) {
    message.warning('当前已有模型检测正在运行，请等待完成或先手动停止')
    return
  }
  const targetId = form.targetId.trim()
  const trustedComparisonAccountId = form.trustedComparisonAccountId?.trim()
  if (!targetId) {
    message.warning('请选择 AI 账户')
    return
  }
  if (selectedTargetAccountProfile.value && !canUseModelCheckModelForAccount(selectedTargetAccountProfile.value, form.model)) {
    message.warning('请选择该账户支持的完整模型 ID')
    ensureRunModelMatchesTarget()
    return
  }
  if (trustedComparisonAccountId && trustedComparisonAccountId === targetId) {
    message.warning('可信对比账户不能和检测目标相同')
    return
  }
  detailOpen.value = false
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
    currentRun.value = await startModelCheckRunSession({
      commandText: `juhe-ai model-check --account "${targetOptionText(targetId)}" --model ${form.model}${trustedComparisonAccountId ? ` --trusted-account "${comparisonOptionText(trustedComparisonAccountId)}"` : ''}`,
      onProgress: handleModelCheckProgress,
      run: (signal, onProgress) => modelChecksApi.runStream(payload, {
        signal,
        onProgress
      }, modelCheckScopeParams.value)
    })
    if (currentRun.value.status === 'completed') {
      appendTerminalLine('success', `检测报告已生成：${levelText(currentRun.value.level)}，${currentRun.value.score}/${currentRun.value.maxScore}，${currentRun.value.message || '-'}`)
      message.success('模型检测完成')
    } else if (currentRun.value.status === 'canceled') {
      appendTerminalLine('warning', '检测已停止')
      message.info('模型检测已停止')
    } else {
      appendTerminalLine('error', `检测结束：${statusText(currentRun.value.status)}，${currentRun.value.errorMessage || currentRun.value.message || '-'}`)
      message.error('模型检测未完成')
    }
    await reloadRuns()
  } catch (error) {
    console.error(error)
    if (error instanceof ModelCheckSessionBusyError) {
      message.warning(error.message)
      return
    }
    const errorMessage = extractApiErrorMessage(error, '模型检测提交失败')
    if (isAbortError(error)) {
      appendTerminalLine('warning', '检测已停止')
      message.info('模型检测已停止')
    } else {
      appendTerminalLine('error', errorMessage)
      message.error(errorMessage)
    }
    if (!isAbortError(error)) {
      await loadRuns()
    }
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
    currentRun.value = await loadEntityDetailCached({
      id,
      load: () => modelChecksApi.detail(id, modelCheckScopeParams.value),
      namespace: 'model-check-run-detail',
      scope: JSON.stringify(modelCheckScopeParams.value ?? {})
    })
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
  selectedHistoryTargetAccount.value = undefined
  currentRun.value = undefined
  detailOpen.value = false
}

function resetRunForm() {
  resetRunAccountSelection()
  form.model = options.value.defaultModel
  form.profile = options.value.defaultProfile
  ensureRunModelMatchesTarget()
}

async function syncActiveModelCheckRun() {
  try {
    const active = await modelChecksApi.active(modelCheckScopeParams.value)
    reconcileModelCheckRunSessionWithActiveRun(active)
    if (!active && modelCheckRunSession.terminalLines.length) {
      await loadRuns()
    }
  } catch (error) {
    console.error(error)
  }
}

function stopCurrentModelCheck(appendLog = true) {
  void stopModelCheckRunSession({
    appendLog,
    stopRequest: () => modelChecksApi.stop(modelCheckScopeParams.value)
  })
}

function clearTerminal() {
  if (modelCheckRunSession.submitting) {
    stopCurrentModelCheck(false)
  }
  modelCheckRunSession.terminalLines = []
  modelCheckRunSession.terminalVisible = false
}

function defaultModelChecksPageState(): ModelChecksPageState {
  return {
    filters: {},
    historyTargetAccount: undefined,
    pagination: { current: 1, pageSize: modelCheckPageSize },
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}

function sanitizeModelChecksPageState(value: unknown, fallback: ModelChecksPageState): ModelChecksPageState {
  const source = value && typeof value === 'object' ? value as Partial<ModelChecksPageState> : {}
  const sourceFilters = source.filters && typeof source.filters === 'object'
    ? source.filters as Partial<ModelChecksPageState['filters']>
    : {}
  return {
    filters: {
      targetId: optionalString(sourceFilters.targetId),
      model: optionalString(sourceFilters.model),
      level: optionalUnion(sourceFilters.level, ['high_confidence', 'likely', 'uncertain', 'suspicious', 'unavailable']),
      status: optionalUnion(sourceFilters.status, ['running', 'completed', 'failed', 'canceled'])
    },
    historyTargetAccount: sanitizeAccountSelection(source.historyTargetAccount),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination),
    systemAccountFilter: stringOrFallback(source.systemAccountFilter, fallback.systemAccountFilter) || fallback.systemAccountFilter,
    systemAccountFilterSelection: sanitizeSystemAccountSelection(source.systemAccountFilterSelection)
  }
}

function optionalString(value: unknown): string | undefined {
  const normalized = stringOrFallback(value).trim()
  return normalized || undefined
}

function optionalUnion<T extends string>(value: unknown, allowed: readonly T[]): T | undefined {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? value as T : undefined
}

function sanitizeSystemAccountSelection(value: unknown): PrincipalSelection | undefined {
  if (!value || typeof value !== 'object') return undefined
  const selection = value as Partial<PrincipalSelection>
  const id = stringOrFallback(selection.id).trim()
  const name = stringOrFallback(selection.name).trim()
  if (!id || !name || selection.kind !== 'system_account') return undefined
  return { id, name, kind: 'system_account' }
}

function sanitizeAccountSelection(value: unknown): AccountSelection | undefined {
  if (!value || typeof value !== 'object') return undefined
  const selection = value as Partial<AccountSelection>
  const id = stringOrFallback(selection.id).trim()
  const name = stringOrFallback(selection.name).trim()
  if (!id || !name) return undefined
  const accessType = optionalUnion(selection.accessType, ['owner', 'authorized'])
  const ownerSystemAccountName = optionalString(selection.ownerSystemAccountName)
  return ownerSystemAccountName
    ? { id, name, accessType, ownerSystemAccountName }
    : { id, name, accessType }
}

function snapshotPageState(): ModelChecksPageState {
  return {
    filters: { ...filters },
    historyTargetAccount: selectedHistoryTargetAccount.value,
    pagination: { current: runsPagination.current, pageSize: runsPagination.pageSize },
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

function appendTerminalLine(level: ModelCheckTerminalLine['level'], text: string) {
  appendModelCheckTerminalLine(level, text)
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
    const mappingDetails = modelCheckMappingProgressText(event)
    appendTerminalLine(event.success ? 'success' : 'warning', `${progressItemTitle(event.itemKey)} 响应完成：HTTP ${event.statusCode}，${formatDuration(event.durationMs)}，Trace ${event.traceId}${mappingDetails}${responseModel}${output}`)
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

function modelCheckMappingProgressText(event: Extract<ModelCheckProgressEvent, { type: 'probe_completed' }>): string {
  const requestModel = event.requestModel || '未记录'
  const upstreamModel = event.upstreamModel || event.expectedModel
  if (event.modelMappingApplied) {
    const sourceFamily = modelCheckEndpointFamilyText(event.sourceEndpointFamily) || '当前请求'
    const upstreamFamily = modelCheckEndpointFamilyText(event.upstreamEndpointFamily) || '上游'
    const mappingSource = event.modelMappingSource ? `，来源 ${event.modelMappingSource}` : ''
    return `，模型映射 ${sourceFamily} / ${requestModel} -> ${upstreamFamily} / ${upstreamModel || requestModel}${mappingSource}`
  }
  return upstreamModel ? `，实际上游模型 ${upstreamModel}` : ''
}

function modelCheckEndpointFamilyText(value?: string): string {
  if (value === 'responses') return 'Responses'
  if (value === 'chat_completions') return 'Chat Completions'
  if (value === 'messages') return 'Messages'
  if (value === 'generate_content') return 'Gemini GenerateContent'
  if (value === 'stream_generate_content') return 'Gemini StreamGenerateContent'
  return value?.trim() || ''
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

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

onMounted(async () => {
  updateViewportWidth()
  window.addEventListener('resize', updateViewportWidth)
  await Promise.all([loadOptions(), loadRuns(), syncActiveModelCheckRun()])
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', updateViewportWidth)
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
