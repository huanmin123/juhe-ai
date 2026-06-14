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
                include-all
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
import { computed, nextTick, onBeforeUnmount, onDeactivated, onMounted, reactive, ref } from 'vue'
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import AccountSelect from '@/components/AccountSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
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
  terminalLevelForCheckStatus,
  type ModelCheckTerminalLineLevel
} from './modelCheckFormatters'
import { modelCheckFallbackOptions, modelCheckPageSize } from './modelCheckPageConfig'
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
const terminalLines = ref<TerminalLine[]>([])
const terminalBodyRef = ref<HTMLElement>()
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
const terminalNow = ref(formatClockTime(new Date()))
let terminalLineId = 0
let modelCheckAbortReason: 'manual' | 'deactivated' | 'unmount' | undefined
let terminalClockTimer: number | undefined

type TerminalLine = {
  id: number
  time: string
  level: ModelCheckTerminalLineLevel
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

function appendTerminalLine(level: ModelCheckTerminalLineLevel, text: string) {
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

function updateTerminalNow() {
  terminalNow.value = formatClockTime(new Date())
}

onMounted(async () => {
  updateViewportWidth()
  updateTerminalNow()
  window.addEventListener('resize', updateViewportWidth)
  terminalClockTimer = window.setInterval(updateTerminalNow, 1000)
  await Promise.all([loadOptions(), loadRuns()])
})

onDeactivated(() => {
  stopCurrentModelCheck(false, 'deactivated')
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', updateViewportWidth)
  if (terminalClockTimer !== undefined) {
    window.clearInterval(terminalClockTimer)
    terminalClockTimer = undefined
  }
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
  border: 1px solid #e8edf5;
  border-radius: 16px;
}

.model-checks-form :deep(.ant-form-item) {
  margin-bottom: 0;
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

@media (max-width: 900px) {
  .model-checks-page {
    height: auto;
    min-height: calc(100dvh - 108px);
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
}
</style>
