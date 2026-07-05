import { computed, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, type ComputedRef } from 'vue'

import { message } from '@/lib/antd'
import { type AccountDraftTestPayload, type AccountTestPayload } from '@/api/client'
import type { AccountSummary, AccountTestResult, AccountTestTask, ProviderDefinition } from '@/types/domain'
import {
  type AccountBatchTestItem,
  type AccountTestForm,
  type AccountTestMode,
  accountTestErrorMessage,
  accountTestSuccessMessage,
  buildAccountTestPayload,
  batchTestSummary,
  failedAccountTestResult,
  stoppedAccountTestMessage
} from './accountTestFlow'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { authorizedAccountUnavailableText, canTestAccount } from './accountRules'
import { accountBatchTestChunkSize, runInFixedBatches } from './accountBatchExecution'
import {
  type AccountTestDraftMode,
  cancelAccountTestSession as cancelAccountTestSessionRequest,
  cancelAccountTestTask as cancelAccountTestTaskRequest,
  createAccountTestSession as createAccountTestSessionRequest,
  fetchAccountTestTask as fetchAccountTestTaskRequest,
  heartbeatAccountTestSession as heartbeatAccountTestSessionRequest,
  sendCancelAccountTestSessionOnUnload,
  submitAccountTestTask
} from './accountTestSessionClient'
import {
  isAbortError,
  parseTaskTime,
  taskStatusToBatchStatus
} from './accountTestTaskHelpers'
import { useAccountTestModels } from './useAccountTestModels'
import { waitForAccountTestResult } from './accountTestTaskPolling'
import { isGatewayTestableAccountProfile } from './accountProviderCapabilities'
import { defaultAccountTestEndpointModeForSelection } from './accountEndpointModes'
import { hasSingleProviderProfileForAccountSelection } from './accountDerivedState'
import type { DraftApiKeyTestSnapshot } from './accountDraftApiKeyTestRuntime'

interface UseAccountTestModalOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  clearSelection?: () => void
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  providers: ComputedRef<ProviderDefinition[]>
  draftApiKeyTestSnapshot?: { value: DraftApiKeyTestSnapshot | undefined }
  successfulDraftActivationTest?: { value: SuccessfulDraftActivationTest | undefined }
}

export interface SuccessfulDraftActivationTest {
  taskId: string
  account: AccountDraftTestPayload['account']
}

const accountTestSessionHeartbeatIntervalMs = 2000

export function useAccountTestModal(options: UseAccountTestModalOptions) {
  const testModalOpen = ref(false)
  const testRunning = ref(false)
  const testMode = ref<AccountTestMode>('single')
  const testingAccount = ref<AccountSummary>()
  const batchTestingAccounts = ref<AccountSummary[]>([])
  const batchTestItems = ref<AccountBatchTestItem[]>([])
  const activeSingleTestTask = ref<AccountTestTask>()
  const testResult = ref<AccountTestResult>()
  const draftTestingAccountPayload = ref<AccountDraftTestPayload['account']>()
  const draftTestMode = ref<AccountTestDraftMode>()
  const draftApiKeyTestSnapshot = options.draftApiKeyTestSnapshot ?? ref<DraftApiKeyTestSnapshot>()
  const successfulDraftActivationTest = options.successfulDraftActivationTest ?? ref<SuccessfulDraftActivationTest>()
  const testForm = reactive<AccountTestForm>({ model: '', testEndpointMode: 'account_default' })
  const testTargetAccountSelection = computed(() => (
    testMode.value === 'batch' ? batchTestingAccounts.value : testingAccount.value
  ))
  const {
    defaultModelForSelection,
    loadTestModels,
    testModelOptions,
    testModelsLoading
  } = useAccountTestModels({
    providers: options.providers,
    testForm,
    testTargetAccountSelection
  })

  let accountTestAbortController: AbortController | undefined
  let activeAccountTestSessionId: string | undefined
  let activeAccountTestSessionScopeParams: { systemAccountId: string } | undefined
  let accountTestSessionHeartbeatTimer: number | undefined
  const activeAccountTestTasks = new Map<string, AccountSummary>()

  async function openTestModal(account: AccountSummary) {
    if (!canTestAccount(account)) {
      if (!isGatewayTestableAccountProfile(account)) {
        message.warning('当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户')
      } else if (isAuthorizedAccount(account) && !account.boundGroupId) {
        message.warning('请先把授权账户绑定到你的分组')
      } else if (isAuthorizedAccount(account)) {
        message.warning(authorizedAccountUnavailableText(account) ?? '当前授权账户不能测试')
      } else {
        message.warning('当前账户不能测试')
      }
      return
    }
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = undefined
    draftTestMode.value = undefined
    draftApiKeyTestSnapshot.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(account)
    testForm.testEndpointMode = defaultAccountTestEndpointModeForSelection(account) ?? 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function openDraftTestModal(account: AccountSummary, draftPayload: AccountDraftTestPayload['account']) {
    if (!isGatewayTestableAccountProfile(account)) {
      message.warning('当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户')
      return
    }
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = draftPayload
    draftTestMode.value = 'create'
    draftApiKeyTestSnapshot.value = undefined
    successfulDraftActivationTest.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(account)
    testForm.testEndpointMode = defaultAccountTestEndpointModeForSelection(account, draftPayload) ?? 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function openSavedDraftTestModal(account: AccountSummary, draftPayload: AccountDraftTestPayload['account']) {
    if (!isGatewayTestableAccountProfile(account)) {
      message.warning('当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户')
      return
    }
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = draftPayload
    draftTestMode.value = 'saved'
    draftApiKeyTestSnapshot.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(account)
    testForm.testEndpointMode = defaultAccountTestEndpointModeForSelection(account, draftPayload) ?? 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function runAccountTest() {
    if (testMode.value === 'batch') {
      await runBatchAccountTest()
      return
    }
    await runSingleAccountTest()
  }

  async function openBatchTestModal(accounts: AccountSummary[]) {
    const testableAccounts = accounts.filter(canTestAccount)
    if (!testableAccounts.length) {
      message.warning('请先选择可测试账户')
      return
    }
    if (testableAccounts.length !== accounts.length) {
      message.warning('已跳过不支持测试协议或当前不能测试的账户')
    }
    if (!hasSingleProviderProfileForAccountSelection(testableAccounts)) {
      message.warning('批量测试一次只能选择同一供应商协议的账户，请按供应商或协议分批测试')
      return
    }
    testMode.value = 'batch'
    testingAccount.value = undefined
    batchTestingAccounts.value = [...testableAccounts]
    batchTestItems.value = testableAccounts.map((account) => ({ account, status: 'pending' }))
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = undefined
    draftTestMode.value = undefined
    draftApiKeyTestSnapshot.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(testableAccounts)
    testForm.testEndpointMode = defaultAccountTestEndpointModeForSelection(testableAccounts) ?? 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function runSingleAccountTest() {
    if (!testingAccount.value || testRunning.value) return
    testResult.value = undefined
    testRunning.value = true
    const controller = new AbortController()
    accountTestAbortController = controller
    const startedAt = Date.now()
    const account = testingAccount.value
    const draftPayload = activeDraftTestPayload(account)
    const activationDraftPayload = activeActivationDraftTestPayload(account)
    try {
      const session = await createAccountTestSession(account)
      startAccountTestSessionHeartbeat(session.id, accountTestTaskScopeParams(account))
      if (controller.signal.aborted) {
        await cancelActiveAccountTestSession()
        throw new DOMException('测试已停止', 'AbortError')
      }
      const payload = buildAccountSpecificTestPayload(account)
      const task = await submitAccountTest(account, payload, session.id)
      activeSingleTestTask.value = task
      activeAccountTestTasks.set(task.id, account)
      if (controller.signal.aborted) {
        await cancelCreatedAccountTestTask(task.id, account)
        activeAccountTestTasks.delete(task.id)
        throw new DOMException('测试已停止', 'AbortError')
      }
      const result = await waitForSubmittedAccountTestResult(task, account, controller.signal, (latestTask) => {
        activeSingleTestTask.value = latestTask
        syncDraftActivationTestFromTask(latestTask, activationDraftPayload)
      })
      testResult.value = result
      syncDraftApiKeyTestSnapshot(draftPayload, result)
      if (result.success) {
        if (activationDraftPayload) {
          successfulDraftActivationTest.value = { taskId: task.id, account: activationDraftPayload }
        }
        message.success(accountTestSuccessMessage(account, result))
      } else {
        if (activationDraftPayload) {
          successfulDraftActivationTest.value = undefined
        }
        message.error(accountTestErrorMessage(account, result))
      }
      await options.loadData()
    } catch (error) {
      if (isAbortError(error)) {
        message.info(stoppedAccountTestMessage(account))
        return
      }
      console.error(error)
      const result = failedAccountTestResult({
        account,
        error,
        model: testForm.model,
        testEndpointMode: testForm.testEndpointMode,
        startedAt
      })
      testResult.value = result
      syncDraftApiKeyTestSnapshot(draftPayload, result)
      message.error(`${account.name}: 测试失败`)
      if (activationDraftPayload) {
        successfulDraftActivationTest.value = undefined
      }
    } finally {
      for (const taskId of [...activeAccountTestTasks.keys()]) {
        activeAccountTestTasks.delete(taskId)
      }
      stopAccountTestSessionHeartbeat()
      clearActiveAccountTestSession()
      testRunning.value = false
      if (accountTestAbortController === controller) {
        accountTestAbortController = undefined
      }
    }
  }

  async function runBatchAccountTest() {
    const accounts = [...batchTestingAccounts.value]
    if (!accounts.length || testRunning.value) return
    testResult.value = undefined
    batchTestItems.value = accounts.map((account) => ({ account, status: 'pending' }))
    testRunning.value = true
    const controller = new AbortController()
    accountTestAbortController = controller
    try {
      const session = await createAccountTestSession()
      startAccountTestSessionHeartbeat(session.id, options.accountScopeParams.value)
      if (controller.signal.aborted) {
        await cancelActiveAccountTestSession()
        throw new DOMException('测试已停止', 'AbortError')
      }
      await runInFixedBatches(accounts, accountBatchTestChunkSize, async (account, index) => {
        await runBatchAccountTestItem(account, index, controller, session.id)
      }, controller.signal)

      if (controller.signal.aborted) {
        markPendingBatchTestItemsStopped()
        const completedCount = batchTestItems.value.filter((item) => item.status === 'success' || item.status === 'failed').length
        const stoppedCount = batchTestItems.value.filter((item) => item.status === 'stopped').length
        if (stoppedCount) {
          message.info(`批量测试已停止，已完成 ${completedCount} 个账户，已停止 ${stoppedCount} 个账户`)
        } else {
          showBatchTestSummary(accounts.length)
        }
      } else {
        showBatchTestSummary(accounts.length)
      }
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error('批量测试失败')
    } finally {
      for (const taskId of [...activeAccountTestTasks.keys()]) {
        activeAccountTestTasks.delete(taskId)
      }
      stopAccountTestSessionHeartbeat()
      clearActiveAccountTestSession()
      testRunning.value = false
      if (accountTestAbortController === controller) {
        accountTestAbortController = undefined
      }
    }
  }

  async function runBatchAccountTestItem(account: AccountSummary, index: number, controller: AbortController, sessionId: string): Promise<void> {
    if (controller.signal.aborted) {
      updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
      return
    }
    const submittedAt = Date.now()
    updateBatchTestItem(index, { status: 'queued', message: '提交后台测试任务' })
    const payload = buildAccountSpecificTestPayload(account)
    let task: AccountTestTask | undefined
    try {
      task = await submitAccountTest(account, payload, sessionId)
      activeAccountTestTasks.set(task.id, account)
      if (controller.signal.aborted) {
        await cancelCreatedAccountTestTask(task.id, account)
        activeAccountTestTasks.delete(task.id)
        updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
        return
      }
      updateBatchTestItem(index, {
        taskId: task.id,
        status: taskStatusToBatchStatus(task),
        message: task.message ?? '等待后台测试',
        startedAt: parseTaskTime(task.startedAt)
      })
      const result = await waitForSubmittedAccountTestResult(task, account, controller.signal, (latestTask) => {
        const latestStartedAt = parseTaskTime(latestTask.startedAt)
        updateBatchTestItem(index, {
          taskId: latestTask.id,
          status: taskStatusToBatchStatus(latestTask),
          message: latestTask.message ?? latestTask.result?.message,
          result: latestTask.result,
          startedAt: latestStartedAt ?? batchTestItems.value[index]?.startedAt,
          finishedAt: latestTask.finishedAt ? Date.parse(latestTask.finishedAt) : undefined
        })
      })
      updateBatchTestItem(index, {
        status: result.success ? 'success' : 'failed',
        result,
        message: result.message,
        finishedAt: Date.now()
      })
    } catch (error) {
      if (isAbortError(error)) {
        if (task) {
          await cancelCreatedAccountTestTask(task.id, account)
          activeAccountTestTasks.delete(task.id)
        }
        updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
        return
      }
      console.error(error)
      const result = failedAccountTestResult({
        account,
        error,
        model: payload.model ?? '',
        testEndpointMode: payload.testEndpointMode ?? testForm.testEndpointMode,
        startedAt: submittedAt
      })
      updateBatchTestItem(index, {
        status: 'failed',
        result,
        message: result.message,
        finishedAt: Date.now()
      })
    } finally {
      if (task) {
        activeAccountTestTasks.delete(task.id)
      }
    }
  }

  function stopAccountTest() {
    if (!testRunning.value) return
    accountTestAbortController?.abort()
    stopAccountTestSessionHeartbeat()
    void cancelActiveAccountTestSession().catch((error) => {
      console.error(error)
    })
    for (const [taskId, account] of activeAccountTestTasks) {
      void cancelAccountTestTask(taskId, account).catch((error) => {
        console.error(error)
      })
    }
  }

  function cancelActiveAccountTestSessionOnUnload() {
    const sessionId = activeAccountTestSessionId
    if (!sessionId) return
    sendCancelAccountTestSessionOnUnload({
      isManagementView: options.isManagementView.value,
      scopeParams: activeAccountTestSessionScopeParams,
      sessionId
    })
  }

  function closeTestModal() {
    if (testRunning.value) {
      stopAccountTest()
    }
    testModalOpen.value = false
  }

  onMounted(() => {
    window.addEventListener('beforeunload', cancelActiveAccountTestSessionOnUnload)
  })
  onDeactivated(stopAccountTest)
  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', cancelActiveAccountTestSessionOnUnload)
    cancelActiveAccountTestSessionOnUnload()
    stopAccountTest()
  })

  function buildAccountSpecificTestPayload(account: AccountSummary) {
    return buildAccountTestPayload({
      ...testForm,
      model: testForm.model || defaultModelForSelection(account)
    }, account, activeDraftTestPayload(account))
  }

  async function createAccountTestSession(account?: AccountSummary) {
    const scopeParams = account ? accountTestTaskScopeParams(account) : options.accountScopeParams.value
    return createAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams
    })
  }

  function startAccountTestSessionHeartbeat(sessionId: string, scopeParams?: { systemAccountId: string }) {
    stopAccountTestSessionHeartbeat()
    activeAccountTestSessionId = sessionId
    activeAccountTestSessionScopeParams = scopeParams
    accountTestSessionHeartbeatTimer = window.setInterval(() => {
      void heartbeatAccountTestSession(sessionId, scopeParams).catch((error) => {
        console.error(error)
      })
    }, accountTestSessionHeartbeatIntervalMs)
  }

  function stopAccountTestSessionHeartbeat() {
    if (accountTestSessionHeartbeatTimer !== undefined) {
      window.clearInterval(accountTestSessionHeartbeatTimer)
      accountTestSessionHeartbeatTimer = undefined
    }
  }

  function clearActiveAccountTestSession() {
    activeAccountTestSessionId = undefined
    activeAccountTestSessionScopeParams = undefined
  }

  function heartbeatAccountTestSession(sessionId: string, scopeParams?: { systemAccountId: string }) {
    return heartbeatAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams,
      sessionId
    })
  }

  function cancelActiveAccountTestSession() {
    const sessionId = activeAccountTestSessionId
    if (!sessionId) {
      return Promise.resolve()
    }
    return cancelAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams: activeAccountTestSessionScopeParams,
      sessionId
    })
  }

  function submitAccountTest(account: AccountSummary, payload: AccountTestPayload, sessionId: string): Promise<AccountTestTask> {
    return submitAccountTestTask({
      account,
      accountScopeParams: options.accountScopeParams.value,
      draftMode: draftTestMode.value,
      draftPayload: activeDraftTestPayload(account),
      isManagementView: options.isManagementView.value,
      payload,
      sessionId
    })
  }

  function fetchAccountTestTask(taskId: string, account: AccountSummary, signal?: AbortSignal): Promise<AccountTestTask> {
    return fetchAccountTestTaskRequest({
      isManagementView: options.isManagementView.value,
      scopeParams: accountTestTaskScopeParams(account),
      signal,
      taskId
    })
  }

  function cancelAccountTestTask(taskId: string, account?: AccountSummary): Promise<AccountTestTask> {
    return cancelAccountTestTaskRequest({
      isManagementView: options.isManagementView.value,
      scopeParams: account ? accountTestTaskScopeParams(account) : options.accountScopeParams.value,
      taskId
    })
  }

  function activeDraftTestPayload(account: AccountSummary): AccountDraftTestPayload['account'] | undefined {
    return testingAccount.value?.id === account.id ? draftTestingAccountPayload.value : undefined
  }

  function activeActivationDraftTestPayload(account: AccountSummary): AccountDraftTestPayload['account'] | undefined {
    return draftTestMode.value === 'create' ? activeDraftTestPayload(account) : undefined
  }

  function syncDraftActivationTestFromTask(task: AccountTestTask, activationDraftPayload: AccountDraftTestPayload['account'] | undefined): void {
    if (!activationDraftPayload) return
    if (task.status === 'success' && task.result?.success === true) {
      successfulDraftActivationTest.value = { taskId: task.id, account: activationDraftPayload }
    } else if (task.status === 'failed' || task.status === 'canceled') {
      successfulDraftActivationTest.value = undefined
    }
  }

  function syncDraftApiKeyTestSnapshot(draftPayload: AccountDraftTestPayload['account'] | undefined, result: AccountTestResult): void {
    if (!draftPayload || draftPayload.type !== 'api_key') return
    draftApiKeyTestSnapshot.value = { account: draftPayload, result }
  }

  function accountTestTaskScopeParams(account: AccountSummary): ReturnType<typeof accountOperationScopeParams> {
    return draftTestMode.value === 'create' && activeDraftTestPayload(account)
      ? options.accountScopeParams.value
      : accountOperationScopeParams(account, options.accountScopeParams.value)
  }

  async function cancelCreatedAccountTestTask(taskId: string, account: AccountSummary): Promise<void> {
    try {
      await cancelAccountTestTask(taskId, account)
    } catch (error) {
      console.error(error)
    }
  }

  function waitForSubmittedAccountTestResult(
    initialTask: AccountTestTask,
    account: AccountSummary,
    signal: AbortSignal,
    onUpdate?: (task: AccountTestTask) => void
  ): Promise<AccountTestResult> {
    return waitForAccountTestResult({
      account,
      cancelTask: cancelCreatedAccountTestTask,
      currentTestEndpointMode: () => testForm.testEndpointMode,
      currentModel: () => testForm.model,
      fetchTask: fetchAccountTestTask,
      initialTask,
      onTaskSettled: (taskId) => {
        activeAccountTestTasks.delete(taskId)
      },
      onUpdate,
      signal
    })
  }

  function updateBatchTestItem(index: number, patch: Partial<AccountBatchTestItem>) {
    const current = batchTestItems.value[index]
    if (!current) return
    if (current.status === 'stopped' && patch.status !== 'stopped') return
    batchTestItems.value[index] = { ...current, ...patch }
  }

  function markPendingBatchTestItemsStopped() {
    batchTestItems.value = batchTestItems.value.map((item) => {
      if (item.status !== 'pending' && item.status !== 'queued' && item.status !== 'running') return item
      return { ...item, status: 'stopped', message: '已停止测试', finishedAt: Date.now() }
    })
  }

  function showBatchTestSummary(total: number) {
    const successCount = batchTestItems.value.filter((item) => item.status === 'success').length
    const summary = batchTestSummary(total, successCount)
    if (summary.success) {
      message.success(summary.message)
      options.clearSelection?.()
    } else {
      message.warning(summary.message)
    }
  }

  return {
    activeSingleTestTask,
    batchTestItems,
    batchTestingAccounts,
    closeTestModal,
    openBatchTestModal,
    openDraftTestModal,
    openSavedDraftTestModal,
    openTestModal,
    runAccountTest,
    stopAccountTest,
    testForm,
    testModalOpen,
    testMode,
    testModelOptions,
    testModelsLoading,
    testResult,
    testRunning,
    testingAccount,
    draftTestingAccountPayload,
    draftApiKeyTestSnapshot,
    successfulDraftActivationTest
  }
}
