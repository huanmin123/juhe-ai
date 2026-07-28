<template>
  <div class="model-checks-page">
    <ModelCheckRunPanel
      :account-select-disabled="accountSelectDisabled"
      :comparison-select-disabled="comparisonSelectDisabled"
      :account-select-placeholder="accountSelectPlaceholder"
      :comparison-options="comparisonOptions"
      :comparison-options-loading="comparisonOptionsLoading"
      :comparison-select-placeholder="comparisonSelectPlaceholder"
      :deep-detection="deepDetection"
      :is-management-view="isManagementView"
      :model="form.model"
      :model-options="runModelOptions"
      :options-loading="optionsLoading"
      :quality-actions-disabled="qualityActionsDisabled"
      :quality-policy="qualityPolicy"
      :quality-policy-loading="qualityPolicyLoading"
      :quality-policy-saving="qualityPolicySaving"
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
      @quality-policy-open="loadQualityPolicy"
      @quality-policy-save="saveQualityPolicy"
      @reset="resetRunForm"
      @stop="stopCurrentModelCheck()"
      @submit="submitRun"
      @schedules-open="openSchedules"
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
      @update:trigger-kind="filters.triggerKind = $event"
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

    <ModelQualitySchedulesModal
      v-model:open="schedulesOpen"
      :account-options="scheduleAccountOptions"
      :account-options-loading="scheduleAccountOptionsLoading"
      :loading="schedulesLoading"
      :model-options="historyModelOptions"
      :page="schedulesPage"
      :page-size="schedulesPageSize"
      :reset-token="scheduleFormResetToken"
      :saving="scheduleSaving"
      :schedules="schedules"
      :total="schedulesTotal"
      @account-change="handleScheduleAccountChange"
      @account-dropdown-visible-change="handleScheduleAccountOptionsDropdown"
      @account-search="loadScheduleAccountOptions"
      @model-dropdown-visible-change="handleScheduleModelOptionsDropdown"
      @delete="deleteSchedule"
      @page-change="handleSchedulePageChange"
      @save="saveSchedule"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { message } from '@/lib/antd'

import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedModelCheckAccountOptionsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { authState } from '@/composables/useAuth'
import {
  accountLabelForId,
  rememberAccountLabel,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, stringUnionOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  ModelCheckLevel,
  ModelCheckModel,
  ModelCheckOptions,
  ModelCheckProgressEvent,
  ModelCheckProfile,
  ModelCheckRunDetail,
  ModelCheckRunPayload,
  ModelCheckRunSummary,
  ModelCheckStatus,
  ModelCheckTriggerKind,
  ModelQualityPolicy,
  ModelQualityPolicyUpdateInput,
  ModelQualitySchedule,
  ModelQualityScheduleMutationInput
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
import ModelQualitySchedulesModal from './ModelQualitySchedulesModal.vue'
import { useModelCheckAccountOptions } from './useModelCheckAccountOptions'

interface ModelChecksPageState {
  filters: {
    targetId?: string
    model?: ModelCheckModel
    level?: ModelCheckLevel
    status?: ModelCheckStatus
    triggerKind?: ModelCheckTriggerKind
  }
  historyTargetAccount?: AccountSelection
  pagination: PagePaginationState
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const modelChecksApi = useScopedModelChecksApi(isManagementView)
const accountsApi = useScopedModelCheckAccountOptionsApi(isManagementView)
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
const qualityPolicyLoading = ref(false)
const qualityPolicySaving = ref(false)
const schedulesOpen = ref(false)
const schedulesLoading = ref(false)
const scheduleSaving = ref(false)
const scheduleFormResetToken = ref(0)
const scheduleAccountOptionsSearchLoading = ref(false)
const scheduleAccountModelOptionsLoading = ref(false)
const scheduleAccountOptionsLoading = computed(() => scheduleAccountOptionsSearchLoading.value || scheduleAccountModelOptionsLoading.value)
const schedules = ref<ModelQualitySchedule[]>([])
const schedulesTotal = ref(0)
const schedulesPage = ref(1)
const schedulesPageSize = 10
const scheduleAccountOptions = ref<Array<{ label: string; value: string; modelCheckModels: string[] }>>([])
let scheduleAccountOptionsRequestId = 0
let scheduleAccountOptionsLoadingKeyword: string | undefined
let scheduleAccountOptionsLoadedKeyword: string | undefined
let scheduleAccountModelOptionsRequestId = 0
let scheduleAccountModelOptionsLoadingId: string | undefined
let scheduleAccountOptionsGeneration = 0
let schedulesRequestId = 0
let pageActive = true
const scheduleAccountModelOptionsLoadedIds = new Set<string>()
const scheduleAccountModelOptionsById = new Map<string, { label: string; value: string; modelCheckModels: string[] }>()
const qualityPolicy = ref<ModelQualityPolicy>({
  systemAccountId: '',
  revision: 0,
  profile: 'quick',
  manualEnforcementEnabled: true,
  penaltyThreshold: 70,
  penaltyAction: 'fallback',
  recoveryIntervalMinutes: 10
})
const submitting = computed(() => modelCheckRunSession.submitting)
const detailLoading = ref(false)
const detailOpen = ref(false)
let runDetailRequestId = 0
const terminalVisible = computed(() => modelCheckRunSession.terminalVisible)
const terminalLines = computed(() => modelCheckRunSession.terminalLines)
const options = ref<ModelCheckOptions>(modelCheckFallbackOptions)
const currentRun = ref<ModelCheckRunDetail>()
const form = reactive<ModelCheckRunPayload>({
  targetType: 'account',
  targetId: '',
  model: modelCheckFallbackOptions.defaultModel,
  profile: 'quick',
  trustedComparison: false,
  trustedComparisonAccountId: undefined
})
const filters = reactive<{
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
  triggerKind?: ModelCheckTriggerKind
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
const qualityActionsDisabled = computed(() => submitting.value || (isManagementView.value && !selectedManagementSystemAccountId.value))

function modelCheckRunListParams(pageState: { current: number; pageSize: number }) {
  return {
    ...modelCheckScopeParams.value,
    page: pageState.current,
    pageSize: pageState.pageSize,
    targetType: 'account' as const,
    targetId: filters.targetId?.trim() || undefined,
    model: filters.model,
    level: filters.level,
    status: filters.status,
    triggerKind: filters.triggerKind
  }
}
const deepDetection = computed(() => form.profile === 'full')
const accountSelectDisabled = computed(() => submitting.value)
const comparisonSelectDisabled = computed(() => submitting.value)
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
  knownTargetName,
  identityKey: computed(() => JSON.stringify([
    isManagementView.value ? 'management' : 'self',
    authState.currentUser.value?.id ?? '',
    authState.currentUser.value?.role ?? '',
    authState.revision.value,
    modelCheckScopeParams.value?.systemAccountId ?? ''
  ]))
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
    form.profile = qualityPolicy.value.profile
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

async function loadQualityPolicy() {
  if (isManagementView.value && !selectedManagementSystemAccountId.value) return
  qualityPolicyLoading.value = true
  try {
    qualityPolicy.value = await modelChecksApi.qualityPolicy(modelCheckScopeParams.value)
    form.profile = qualityPolicy.value.profile
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型质量检测配置失败'))
  } finally {
    qualityPolicyLoading.value = false
  }
}

async function saveQualityPolicy(input: ModelQualityPolicyUpdateInput) {
  qualityPolicySaving.value = true
  try {
    qualityPolicy.value = await modelChecksApi.saveQualityPolicy(input, modelCheckScopeParams.value)
    form.profile = qualityPolicy.value.profile
    message.success('手动检测质量配置已保存')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存手动检测质量配置失败'))
    await loadQualityPolicy()
  } finally {
    qualityPolicySaving.value = false
  }
}

async function openSchedules() {
  if (qualityActionsDisabled.value) return
  schedulesPage.value = 1
  resetScheduleAccountOptionsState()
  schedulesOpen.value = true
  await loadSchedules()
}

async function loadSchedules() {
  const requestId = ++schedulesRequestId
  const requestSignature = schedulesRequestSignature(schedulesPage.value)
  schedulesLoading.value = true
  try {
    const page = await modelChecksApi.qualitySchedules({ ...modelCheckScopeParams.value, page: schedulesPage.value, pageSize: schedulesPageSize })
    if (!isCurrentSchedulesRequest(requestId, requestSignature, schedulesPage.value)) return
    schedules.value = page.items
    schedulesTotal.value = page.total
  } catch (error) {
    if (!isCurrentSchedulesRequest(requestId, requestSignature, schedulesPage.value)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载定时检查计划失败'))
  } finally {
    if (requestId === schedulesRequestId) schedulesLoading.value = false
  }
}

function invalidateSchedulesRequest(): void {
  schedulesRequestId += 1
  schedulesLoading.value = false
}

function schedulesRequestSignature(page: number): string {
  return JSON.stringify([scheduleRequestContextKey(), page])
}

function isCurrentSchedulesRequest(requestId: number, signature: string, page: number): boolean {
  return pageActive
    && schedulesOpen.value
    && requestId === schedulesRequestId
    && signature === schedulesRequestSignature(page)
}

async function loadScheduleAccountOptions(keyword: string, selectedAccountId = '') {
  const normalizedKeyword = keyword.trim()
  const normalizedSelectedAccountId = selectedAccountId.trim()
  const requestKey = JSON.stringify([normalizedKeyword, normalizedSelectedAccountId])
  if (scheduleAccountOptionsLoadingKeyword === requestKey || scheduleAccountOptionsLoadedKeyword === requestKey) return
  const requestId = ++scheduleAccountOptionsRequestId
  const generation = scheduleAccountOptionsGeneration
  const requestContextKey = scheduleRequestContextKey()
  scheduleAccountOptionsLoadingKeyword = requestKey
  scheduleAccountOptionsSearchLoading.value = true
  try {
    const items = await accountsApi.options({
      ...modelCheckScopeParams.value,
      purpose: 'run',
      keyword: normalizedKeyword || undefined,
      selectedIds: normalizedSelectedAccountId ? [normalizedSelectedAccountId] : undefined,
      limit: 50
    })
    if (!isCurrentScheduleAccountOptionsRequest(requestId, generation, requestContextKey)) return
    const nextOptions = items.map((item) => ({
      label: item.name,
      value: item.id,
      modelCheckModels: [...item.modelCheckModels]
    }))
    for (const option of nextOptions) scheduleAccountModelOptionsLoadedIds.add(option.value)
    scheduleAccountOptions.value = mergeScheduleAccountOptions(nextOptions)
    scheduleAccountOptionsLoadedKeyword = requestKey
  } catch (error) {
    if (!isCurrentScheduleAccountOptionsRequest(requestId, generation, requestContextKey)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载检查账户选项失败'))
  } finally {
    if (generation === scheduleAccountOptionsGeneration && requestId === scheduleAccountOptionsRequestId) {
      scheduleAccountOptionsLoadingKeyword = undefined
      scheduleAccountOptionsSearchLoading.value = false
    }
  }
}

function handleScheduleAccountOptionsDropdown(open: boolean, accountId: string) {
  if (!open) return
  void loadScheduleAccountOptions('', accountId)
}

function handleScheduleAccountChange(): void {
  scheduleAccountModelOptionsRequestId += 1
  scheduleAccountModelOptionsLoadingId = undefined
  scheduleAccountModelOptionsLoading.value = false
}

function handleScheduleModelOptionsDropdown(open: boolean, accountId: string) {
  if (!open || !accountId.trim()) return
  void loadScheduleAccountModelOptions(accountId)
}

async function loadScheduleAccountModelOptions(accountId: string): Promise<void> {
  const selectedId = accountId.trim()
  if (!selectedId
    || scheduleAccountModelOptionsLoadedIds.has(selectedId)
    || scheduleAccountModelOptionsLoadingId === selectedId) return
  const requestId = ++scheduleAccountModelOptionsRequestId
  const generation = scheduleAccountOptionsGeneration
  const requestContextKey = scheduleRequestContextKey()
  scheduleAccountModelOptionsLoadingId = selectedId
  scheduleAccountModelOptionsLoading.value = true
  try {
    const items = await accountsApi.options({
      ...modelCheckScopeParams.value,
      purpose: 'run',
      selectedIds: [selectedId],
      limit: 1
    })
    if (!isCurrentScheduleAccountModelOptionsRequest(requestId, generation, requestContextKey)) return
    const item = items.find((option) => option.id === selectedId)
    if (!item) throw new Error('当前检查账户不可用')
    scheduleAccountModelOptionsById.set(selectedId, {
      label: item.name,
      value: item.id,
      modelCheckModels: [...item.modelCheckModels]
    })
    scheduleAccountModelOptionsLoadedIds.add(selectedId)
    scheduleAccountOptions.value = mergeScheduleAccountOptions(scheduleAccountOptions.value)
  } catch (error) {
    if (!isCurrentScheduleAccountModelOptionsRequest(requestId, generation, requestContextKey)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载检查模型失败'))
  } finally {
    if (generation === scheduleAccountOptionsGeneration && requestId === scheduleAccountModelOptionsRequestId) {
      scheduleAccountModelOptionsLoadingId = undefined
      scheduleAccountModelOptionsLoading.value = false
    }
  }
}

function scheduleRequestContextKey(): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    isManagementView.value ? 'management' : 'self',
    modelCheckScopeParams.value?.systemAccountId ?? ''
  ])
}

function isCurrentScheduleAccountOptionsRequest(requestId: number, generation: number, contextKey: string): boolean {
  return pageActive
    && schedulesOpen.value
    && generation === scheduleAccountOptionsGeneration
    && requestId === scheduleAccountOptionsRequestId
    && contextKey === scheduleRequestContextKey()
}

function isCurrentScheduleAccountModelOptionsRequest(requestId: number, generation: number, contextKey: string): boolean {
  return pageActive
    && schedulesOpen.value
    && generation === scheduleAccountOptionsGeneration
    && requestId === scheduleAccountModelOptionsRequestId
    && contextKey === scheduleRequestContextKey()
}

function mergeScheduleAccountOptions(
  options: Array<{ label: string; value: string; modelCheckModels: string[] }>
): Array<{ label: string; value: string; modelCheckModels: string[] }> {
  const optionsById = new Map(options.map((option) => [option.value, option]))
  for (const [id, option] of scheduleAccountModelOptionsById) optionsById.set(id, option)
  return [...optionsById.values()]
}

function resetScheduleAccountOptionsState() {
  scheduleAccountOptionsGeneration += 1
  scheduleAccountOptionsRequestId += 1
  scheduleAccountModelOptionsRequestId += 1
  scheduleAccountOptionsLoadingKeyword = undefined
  scheduleAccountOptionsLoadedKeyword = undefined
  scheduleAccountModelOptionsLoadingId = undefined
  scheduleAccountOptionsSearchLoading.value = false
  scheduleAccountModelOptionsLoading.value = false
  scheduleAccountOptions.value = []
  scheduleAccountModelOptionsLoadedIds.clear()
  scheduleAccountModelOptionsById.clear()
}

async function saveSchedule(input: ModelQualityScheduleMutationInput) {
  scheduleSaving.value = true
  try {
    await modelChecksApi.saveQualitySchedule(input, modelCheckScopeParams.value)
    message.success('定时检查计划已保存')
    await loadSchedules()
    scheduleFormResetToken.value += 1
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存定时检查计划失败'))
  } finally {
    scheduleSaving.value = false
  }
}

async function deleteSchedule(id: string) {
  try {
    await modelChecksApi.deleteQualitySchedule(id, modelCheckScopeParams.value)
    message.success('定时检查计划已删除')
    await loadSchedules()
    scheduleFormResetToken.value += 1
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除定时检查计划失败'))
  }
}

async function handleSchedulePageChange(page: number) {
  schedulesPage.value = page
  await loadSchedules()
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
      commandText: `juhe-ai model-check --account "${targetOptionText(targetId)}" --model ${form.model} --profile ${form.profile}${trustedComparisonAccountId ? ` --trusted-account "${comparisonOptionText(trustedComparisonAccountId)}"` : ''}`,
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
  const requestId = ++runDetailRequestId
  const systemAccountId = modelCheckScopeParams.value?.systemAccountId
  detailOpen.value = true
  detailLoading.value = true
  currentRun.value = undefined
  try {
    const nextRun = await modelChecksApi.detail(id, modelCheckScopeParams.value)
    if (!isCurrentRunDetailRequest(requestId, systemAccountId)) return
    currentRun.value = nextRun
    rememberRunAccountLabels([nextRun])
  } catch (error) {
    if (!isCurrentRunDetailRequest(requestId, systemAccountId)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测详情失败'))
  } finally {
    if (requestId === runDetailRequestId) {
      detailLoading.value = false
    }
  }
}

function isCurrentRunDetailRequest(requestId: number, systemAccountId: string | undefined): boolean {
  return requestId === runDetailRequestId
    && systemAccountId === modelCheckScopeParams.value?.systemAccountId
}

function handleSystemAccountFilterChange() {
  if (!systemAccountFilter.value) {
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetModelCheckScopedState()
  void Promise.all([loadOptions(), loadQualityPolicy()])
  void reloadRuns()
}

function resetModelCheckScopedState() {
  runDetailRequestId += 1
  invalidateSchedulesRequest()
  detailLoading.value = false
  resetAccountOptionsState()
  resetScheduleAccountOptionsState()
  filters.targetId = undefined
  selectedHistoryTargetAccount.value = undefined
  currentRun.value = undefined
  detailOpen.value = false
  schedulesOpen.value = false
  schedules.value = []
  schedulesTotal.value = 0
}

function resetRunForm() {
  resetRunAccountSelection()
  form.model = options.value.defaultModel
  form.profile = qualityPolicy.value.profile
  ensureRunModelMatchesTarget()
}

async function syncActiveModelCheckRun() {
  try {
    const active = await modelChecksApi.active(modelCheckScopeParams.value)
    if (active?.profile) form.profile = active.profile
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
      status: optionalUnion(sourceFilters.status, ['running', 'completed', 'failed', 'canceled']),
      triggerKind: optionalUnion(sourceFilters.triggerKind, ['manual', 'scheduled', 'quality_recovery'])
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
watch(schedulesOpen, (open) => {
  if (open) return
  invalidateSchedulesRequest()
  resetScheduleAccountOptionsState()
})

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
    appendTerminalLine('info', `检测启动：${event.profile === 'full' ? '深度检测' : '快速检测'}，目标 AI 账户 ${targetLabel}，模型 ${event.model}${comparisonText}`)
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
  if (event.type === 'quality_decision') {
    appendTerminalLine(event.triggered ? 'warning' : 'success', event.message)
    return
  }
  if (event.type === 'quality_enforcement_started') {
    appendTerminalLine('warning', event.message)
    return
  }
  if (event.type === 'quality_enforcement_completed') {
    appendTerminalLine(event.result === 'failed' ? 'error' : event.result === 'applied' ? 'success' : 'warning', event.message)
    return
  }
  if (event.type === 'quality_health_sync') {
    appendTerminalLine(event.result === 'applied' ? 'success' : 'error', event.message)
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
  pageActive = true
  updateViewportWidth()
  window.addEventListener('resize', updateViewportWidth)
  await Promise.all([loadOptions(), loadQualityPolicy(), loadRuns()])
  await syncActiveModelCheckRun()
})

onActivated(() => {
  pageActive = true
})

onDeactivated(() => {
  pageActive = false
  invalidateSchedulesRequest()
  schedulesOpen.value = false
  resetScheduleAccountOptionsState()
})

onBeforeUnmount(() => {
  pageActive = false
  runDetailRequestId += 1
  invalidateSchedulesRequest()
  resetScheduleAccountOptionsState()
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
