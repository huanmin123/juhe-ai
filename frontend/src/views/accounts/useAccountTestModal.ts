import { computed, onBeforeUnmount, onMounted, reactive, ref, type ComputedRef } from 'vue'

import { message } from '@/lib/antd'
import { api, type AccountDraftTestPayload, type AccountTestPayload } from '@/api/client'
import type {
  AccountSummary,
  AccountTestResult,
  AccountTestSessionDetail,
  AccountTestTask,
  ProviderDefinition
} from '@/types/domain'
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
import {
  accountDefaultTestModelSaveQueue,
  type AccountDefaultTestModelApplyPhase
} from './accountDefaultTestModelSaveQueue'
import { authorizedAccountUnavailableText, canTestAccount } from './accountRules'
import { accountBatchTestChunkSize, runInFixedBatches } from './accountBatchExecution'
import {
  type AccountTestDraftMode,
  cancelAccountTestSession as cancelAccountTestSessionRequest,
  cancelAccountTestTask as cancelAccountTestTaskRequest,
  completeAccountTestSession as completeAccountTestSessionRequest,
  createAccountTestSession as createAccountTestSessionRequest,
  fetchActiveAccountTestSession as fetchActiveAccountTestSessionRequest,
  fetchAccountTestTask as fetchAccountTestTaskRequest,
  heartbeatAccountTestSession as heartbeatAccountTestSessionRequest,
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
import {
  type AccountTestRunSessionSnapshot,
  clearAccountTestRunSession as clearStoredAccountTestRunSession,
  readAccountTestRunSession,
  writeAccountTestRunSession
} from './accountTestRunSession'

interface UseAccountTestModalOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  applyAccountDefaultTestModel?: (
    accountId: string,
    defaultTestModel?: string,
    phase?: AccountDefaultTestModelApplyPhase
  ) => void
  clearSelection?: () => void
  isManagementView: ComputedRef<boolean>
  loadData: (options?: { shouldApply?: () => boolean }) => Promise<void>
  providers: ComputedRef<ProviderDefinition[]>
  draftApiKeyTestSnapshot?: { value: DraftApiKeyTestSnapshot | undefined }
  successfulDraftActivationTest?: { value: SuccessfulDraftActivationTest | undefined }
  successfulSavedDraftUpdateTest?: { value: SuccessfulDraftActivationTest | undefined }
}

export interface SuccessfulDraftActivationTest {
  taskId: string
  account: AccountDraftTestPayload['account']
}

const accountTestSessionHeartbeatIntervalMs = 2000

interface AccountTestRunContext {
  controller: AbortController
  tasks: Map<string, AccountSummary>
  sessionId?: string
  sessionScopeParams?: { systemAccountId: string }
  heartbeatTimer?: number
}

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
  const successfulSavedDraftUpdateTest = options.successfulSavedDraftUpdateTest ?? ref<SuccessfulDraftActivationTest>()
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
    accountScopeParams: options.accountScopeParams,
    providers: options.providers,
    testForm,
    testTargetAccountSelection
  })

  let activeTestRun: AccountTestRunContext | undefined
  let pendingTestRunDetach: Promise<void> | undefined

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
    await detachActiveAccountTestRun()
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = undefined
    draftTestMode.value = undefined
    draftApiKeyTestSnapshot.value = undefined
    successfulSavedDraftUpdateTest.value = undefined
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
    await detachActiveAccountTestRun()
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = draftPayload
    draftTestMode.value = 'create'
    draftApiKeyTestSnapshot.value = undefined
    successfulDraftActivationTest.value = undefined
    successfulSavedDraftUpdateTest.value = undefined
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
    await detachActiveAccountTestRun()
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = draftPayload
    draftTestMode.value = 'saved'
    draftApiKeyTestSnapshot.value = undefined
    successfulSavedDraftUpdateTest.value = undefined
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

  function updateAccountTestModel(model: string): void {
    const normalizedModel = model.trim()
    testForm.model = normalizedModel
    const account = testingAccount.value
    if (
      !normalizedModel
      || testMode.value !== 'single'
      || draftTestMode.value
      || !account
      || !(account.supportedModels ?? []).includes(normalizedModel)
    ) {
      return
    }
    accountDefaultTestModelSaveQueue.enqueue(account, normalizedModel, {
      persist: async (targetAccount, targetModel) => {
        const result = options.isManagementView.value
          ? await api.accounts.setDefaultTestModel(
            targetAccount.id,
            targetModel,
            accountOperationScopeParams(targetAccount, options.accountScopeParams.value)
          )
          : await api.myAccounts.setDefaultTestModel(targetAccount.id, targetModel)
        return result.defaultTestModel
      },
      onLatestFailure: (error) => {
        console.error(error)
        message.warning('账户默认测试模型保存失败，已恢复原设置')
      },
      onSupersededFailure: (error) => {
        console.error(error)
      }
    })
  }

  function applyAccountDefaultTestModel(
    accountId: string,
    model: string | undefined,
    phase: AccountDefaultTestModelApplyPhase
  ): void {
    if (testingAccount.value?.id === accountId) {
      testingAccount.value = {
        ...testingAccount.value,
        defaultTestModel: model?.trim() || undefined
      }
    }
    options.applyAccountDefaultTestModel?.(accountId, model, phase)
  }
  const unsubscribeAccountDefaultTestModelSaveQueue = accountDefaultTestModelSaveQueue.subscribe(
    applyAccountDefaultTestModel
  )

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
    const batchScopeIds = [...new Set(testableAccounts
      .map((account) => accountOperationScopeParams(account, options.accountScopeParams.value)?.systemAccountId)
      .filter((systemAccountId): systemAccountId is string => Boolean(systemAccountId)))]
    if (batchScopeIds.length > 1) {
      message.warning('批量测试一次只能选择同一系统账户下的 AI 账户')
      return
    }
    await detachActiveAccountTestRun()
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
    const run = beginAccountTestRun()
    const startedAt = Date.now()
    const account = testingAccount.value
    const draftPayload = activeDraftTestPayload(account)
    const activationDraftPayload = activeActivationDraftTestPayload(account)
    const savedDraftUpdatePayload = activeSavedDraftUpdateTestPayload(account)
    const activeDraftMode = draftTestMode.value
    const taskScopeParams = accountTestTaskScopeParams(account, activeDraftMode, draftPayload)
    const payload = buildAccountTestPayload({
      model: testForm.model || defaultModelForSelection(account),
      testEndpointMode: testForm.testEndpointMode
    }, account, draftPayload)
    try {
      const session = await createAccountTestSession(taskScopeParams)
      startAccountTestSessionHeartbeat(run, session.id, taskScopeParams)
      persistAccountTestRunSession(run, true)
      if (run.controller.signal.aborted) {
        await cancelAccountTestRunSession(run)
        throw new DOMException('测试已停止', 'AbortError')
      }
      const task = await submitAccountTest(account, payload, session.id, activeDraftMode, draftPayload)
      if (isActiveAccountTestRun(run)) {
        activeSingleTestTask.value = task
      }
      run.tasks.set(task.id, account)
      persistAccountTestRunSession(run, true)
      if (run.controller.signal.aborted) {
        await cancelCreatedAccountTestTask(task.id, account)
        run.tasks.delete(task.id)
        throw new DOMException('测试已停止', 'AbortError')
      }
      const result = await waitForSubmittedAccountTestResult(run, task, account, payload, (latestTask) => {
        if (!isActiveAccountTestRun(run)) return
        activeSingleTestTask.value = latestTask
        syncDraftActivationTestFromTask(latestTask, activationDraftPayload)
        syncSavedDraftUpdateTestFromTask(latestTask, savedDraftUpdatePayload)
        persistAccountTestRunSession(run, true)
      })
      if (!isActiveAccountTestRun(run)) return
      testResult.value = result
      syncDraftApiKeyTestSnapshot(draftPayload, result)
      if (result.success) {
        if (activationDraftPayload) {
          successfulDraftActivationTest.value = { taskId: task.id, account: activationDraftPayload }
        }
        if (savedDraftUpdatePayload) {
          successfulSavedDraftUpdateTest.value = { taskId: task.id, account: savedDraftUpdatePayload }
        }
        message.success(accountTestSuccessMessage(account, result))
      } else {
        if (activationDraftPayload) {
          successfulDraftActivationTest.value = undefined
        }
        if (savedDraftUpdatePayload) {
          successfulSavedDraftUpdateTest.value = undefined
        }
        message.error(accountTestErrorMessage(account, result))
      }
      await options.loadData({ shouldApply: () => isActiveAccountTestRun(run) })
    } catch (error) {
      if (isAbortError(error)) {
        if (isActiveAccountTestRun(run)) {
          message.info(stoppedAccountTestMessage(account))
        }
        return
      }
      console.error(error)
      if (!isActiveAccountTestRun(run)) return
      const result = failedAccountTestResult({
        account,
        error,
        model: payload.model ?? '',
        testEndpointMode: payload.testEndpointMode ?? 'account_default',
        startedAt
      })
      testResult.value = result
      syncDraftApiKeyTestSnapshot(draftPayload, result)
      message.error(`${account.name}: 测试失败`)
      if (activationDraftPayload) {
        successfulDraftActivationTest.value = undefined
      }
      if (savedDraftUpdatePayload) {
        successfulSavedDraftUpdateTest.value = undefined
      }
    } finally {
      if (!run.controller.signal.aborted) {
        await completeAccountTestRunSession(run)
      }
      if (isActiveAccountTestRun(run)) {
        persistAccountTestRunSession(run, false)
      }
      run.tasks.clear()
      stopAccountTestSessionHeartbeat(run)
      clearAccountTestRunSession(run)
      finishAccountTestRun(run)
    }
  }

  async function runBatchAccountTest() {
    const accounts = [...batchTestingAccounts.value]
    if (!accounts.length || testRunning.value) return
    testResult.value = undefined
    batchTestItems.value = accounts.map((account) => ({ account, status: 'pending' }))
    const run = beginAccountTestRun()
    const formSnapshot: AccountTestForm = {
      model: testForm.model,
      testEndpointMode: testForm.testEndpointMode
    }
    try {
      const sessionScopeParams = options.accountScopeParams.value
      const session = await createAccountTestSession(sessionScopeParams)
      startAccountTestSessionHeartbeat(run, session.id, sessionScopeParams)
      persistAccountTestRunSession(run, true)
      if (run.controller.signal.aborted) {
        await cancelAccountTestRunSession(run)
        throw new DOMException('测试已停止', 'AbortError')
      }
      await runInFixedBatches(accounts, accountBatchTestChunkSize, async (account, index) => {
        await runBatchAccountTestItem(run, account, index, formSnapshot, session.id)
      }, run.controller.signal)

      if (!isActiveAccountTestRun(run)) return
      if (run.controller.signal.aborted) {
        markPendingBatchTestItemsStopped(run)
        const completedCount = batchTestItems.value.filter((item) => item.status === 'success' || item.status === 'failed').length
        const stoppedCount = batchTestItems.value.filter((item) => item.status === 'stopped').length
        if (stoppedCount) {
          message.info(`批量测试已停止，已完成 ${completedCount} 个账户，已停止 ${stoppedCount} 个账户`)
        } else {
          showBatchTestSummary(run, accounts.length)
        }
      } else {
        showBatchTestSummary(run, accounts.length)
      }
      await options.loadData({ shouldApply: () => isActiveAccountTestRun(run) })
    } catch (error) {
      if (isAbortError(error)) return
      console.error(error)
      if (isActiveAccountTestRun(run)) {
        message.error('批量测试失败')
      }
    } finally {
      if (!run.controller.signal.aborted) {
        await completeAccountTestRunSession(run)
      }
      if (isActiveAccountTestRun(run)) {
        persistAccountTestRunSession(run, false)
      }
      run.tasks.clear()
      stopAccountTestSessionHeartbeat(run)
      clearAccountTestRunSession(run)
      finishAccountTestRun(run)
    }
  }

  async function runBatchAccountTestItem(
    run: AccountTestRunContext,
    account: AccountSummary,
    index: number,
    formSnapshot: AccountTestForm,
    sessionId: string
  ): Promise<void> {
    if (run.controller.signal.aborted) {
      updateBatchTestItem(run, index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
      return
    }
    const submittedAt = Date.now()
    const currentItem = batchTestItems.value[index]
    if (currentItem && (currentItem.status === 'success' || currentItem.status === 'failed' || currentItem.status === 'stopped')) {
      return
    }
    updateBatchTestItem(run, index, {
      status: 'queued',
      message: currentItem?.taskId ? '恢复后台测试任务' : '提交后台测试任务'
    })
    const payload = buildAccountTestPayload({
      ...formSnapshot,
      model: formSnapshot.model || defaultModelForSelection(account)
    }, account)
    let task: AccountTestTask | undefined
    try {
      task = currentItem?.taskId
        ? await fetchAccountTestTask(currentItem.taskId, account, run.controller.signal)
        : await submitAccountTest(account, payload, sessionId)
      run.tasks.set(task.id, account)
      if (run.controller.signal.aborted) {
        await cancelCreatedAccountTestTask(task.id, account)
        run.tasks.delete(task.id)
        updateBatchTestItem(run, index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
        return
      }
      updateBatchTestItem(run, index, {
        taskId: task.id,
        status: taskStatusToBatchStatus(task),
        message: task.message ?? '等待后台测试',
        startedAt: parseTaskTime(task.startedAt)
      })
      const result = await waitForSubmittedAccountTestResult(run, task, account, payload, (latestTask) => {
        const latestStartedAt = parseTaskTime(latestTask.startedAt)
        updateBatchTestItem(run, index, {
          taskId: latestTask.id,
          status: taskStatusToBatchStatus(latestTask),
          message: latestTask.message ?? latestTask.result?.message,
          result: latestTask.result,
          startedAt: latestStartedAt ?? batchTestItems.value[index]?.startedAt,
          finishedAt: latestTask.finishedAt ? Date.parse(latestTask.finishedAt) : undefined
        })
        persistAccountTestRunSession(run, true)
      })
      updateBatchTestItem(run, index, {
        status: result.success ? 'success' : 'failed',
        result,
        message: result.message,
        finishedAt: Date.now()
      })
      persistAccountTestRunSession(run, true)
    } catch (error) {
      if (isAbortError(error)) {
        if (task) {
          await cancelCreatedAccountTestTask(task.id, account)
          run.tasks.delete(task.id)
        }
        updateBatchTestItem(run, index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
        persistAccountTestRunSession(run, true)
        return
      }
      console.error(error)
      const result = failedAccountTestResult({
        account,
        error,
        model: payload.model ?? '',
        testEndpointMode: payload.testEndpointMode ?? formSnapshot.testEndpointMode,
        startedAt: submittedAt
      })
      updateBatchTestItem(run, index, {
        status: 'failed',
        result,
        message: result.message,
        finishedAt: Date.now()
      })
      persistAccountTestRunSession(run, true)
    } finally {
      if (task) {
        run.tasks.delete(task.id)
      }
    }
  }

  function stopAccountTest() {
    const run = activeTestRun
    if (!run || !testRunning.value) return
    if (testMode.value === 'batch') {
      batchTestItems.value = batchTestItems.value.map((item) => (
        item.status === 'pending' || item.status === 'queued' || item.status === 'running'
          ? { ...item, status: 'stopped', message: '已停止测试', finishedAt: Date.now() }
          : item
      ))
      message.info('批量测试已停止')
    } else {
      if (activeSingleTestTask.value?.status === 'queued' || activeSingleTestTask.value?.status === 'running') {
        activeSingleTestTask.value = {
          ...activeSingleTestTask.value,
          status: 'canceled',
          message: '已停止测试'
        }
      }
      const account = testingAccount.value
      if (account) {
        message.info(stoppedAccountTestMessage(account))
      }
    }
    persistAccountTestRunSession(run, false)
    void detachActiveAccountTestRun()
  }

  function closeTestModal() {
    void detachActiveAccountTestRun()
    testModalOpen.value = false
  }

  onMounted(() => {
    void restoreAccountTestRunSession()
  })
  onBeforeUnmount(() => {
    unsubscribeAccountDefaultTestModelSaveQueue()
    const run = activeTestRun
    if (!run) return
    persistAccountTestRunSession(run, true)
    stopAccountTestSessionHeartbeat(run)
  })

  async function createAccountTestSession(scopeParams?: { systemAccountId: string }) {
    return createAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams
    })
  }

  function startAccountTestSessionHeartbeat(run: AccountTestRunContext, sessionId: string, scopeParams?: { systemAccountId: string }) {
    stopAccountTestSessionHeartbeat(run)
    run.sessionId = sessionId
    run.sessionScopeParams = scopeParams
    run.heartbeatTimer = window.setInterval(() => {
      void heartbeatAccountTestSession(sessionId, scopeParams).catch((error) => {
        console.error(error)
      })
    }, accountTestSessionHeartbeatIntervalMs)
  }

  function stopAccountTestSessionHeartbeat(run: AccountTestRunContext) {
    if (run.heartbeatTimer !== undefined) {
      window.clearInterval(run.heartbeatTimer)
      run.heartbeatTimer = undefined
    }
  }

  function clearAccountTestRunSession(run: AccountTestRunContext) {
    run.sessionId = undefined
    run.sessionScopeParams = undefined
  }

  function heartbeatAccountTestSession(sessionId: string, scopeParams?: { systemAccountId: string }) {
    return heartbeatAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams,
      sessionId
    })
  }

  function cancelAccountTestRunSession(run: AccountTestRunContext) {
    const sessionId = run.sessionId
    if (!sessionId) {
      return Promise.resolve()
    }
    return cancelAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams: run.sessionScopeParams,
      sessionId
    })
  }

  async function completeAccountTestRunSession(run: AccountTestRunContext): Promise<void> {
    const sessionId = run.sessionId
    if (!sessionId) return
    try {
      await completeAccountTestSessionRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.sessionScopeParams,
        sessionId
      })
    } catch (error) {
      console.error(error)
    }
  }

  function persistAccountTestRunSession(run: AccountTestRunContext, running: boolean): void {
    const sessionId = run.sessionId
    if (!sessionId) return
    writeAccountTestRunSession({
      sessionId,
      isManagementView: options.isManagementView.value,
      scopeParams: run.sessionScopeParams,
      mode: testMode.value,
      model: testForm.model,
      testEndpointMode: testForm.testEndpointMode,
      testingAccount: testingAccount.value,
      batchTestingAccounts: batchTestingAccounts.value,
      activeSingleTestTask: activeSingleTestTask.value,
      batchTestItems: batchTestItems.value,
      result: testResult.value,
      running
    })
  }

  async function restoreAccountTestRunSession(): Promise<void> {
    const snapshot = readAccountTestRunSession(options.isManagementView.value)
    if (!snapshot) return
    applyAccountTestRunSessionSnapshot(snapshot)
    let activeDetail: AccountTestSessionDetail | null
    try {
      activeDetail = await fetchActiveAccountTestSessionRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: snapshot.scopeParams ?? options.accountScopeParams.value
      })
    } catch (error) {
      console.error(error)
      testRunning.value = false
      message.warning('测试进度暂时无法恢复，后台任务仍会继续执行')
      return
    }
    if (
      !activeDetail
      || activeDetail.session.id !== snapshot.sessionId
      || activeDetail.session.status !== 'running'
    ) {
      testRunning.value = false
      clearStoredAccountTestRunSession(options.isManagementView.value)
      return
    }
    applyAccountTestRunSessionSnapshot(snapshot, activeDetail.tasks)
    testModalOpen.value = true
    const run = beginAccountTestRun()
    run.sessionId = activeDetail.session.id
    run.sessionScopeParams = snapshot.scopeParams
    for (const task of activeDetail.tasks) {
      const account = task.accountId === testingAccount.value?.id
        ? testingAccount.value
        : batchTestingAccounts.value.find((candidate) => candidate.id === task.accountId)
      if (account) {
        run.tasks.set(task.id, account)
      }
    }
    startAccountTestSessionHeartbeat(run, activeDetail.session.id, snapshot.scopeParams)
    persistAccountTestRunSession(run, true)
    try {
      if (testMode.value === 'single') {
        await resumeSingleAccountTest(run, activeDetail.tasks)
      } else {
        await resumeBatchAccountTest(run, activeDetail.session.id)
      }
      await options.loadData({ shouldApply: () => isActiveAccountTestRun(run) })
    } catch (error) {
      if (!isAbortError(error)) {
        console.error(error)
        message.warning('测试进度恢复中断，后台任务仍会继续执行')
      }
    } finally {
      if (!run.controller.signal.aborted) {
        await completeAccountTestRunSession(run)
      }
      if (isActiveAccountTestRun(run)) {
        persistAccountTestRunSession(run, false)
      }
      run.tasks.clear()
      stopAccountTestSessionHeartbeat(run)
      clearAccountTestRunSession(run)
      finishAccountTestRun(run)
    }
  }

  function applyAccountTestRunSessionSnapshot(
    snapshot: AccountTestRunSessionSnapshot,
    backendTasks: AccountTestTask[] = []
  ): void {
    testMode.value = snapshot.mode
    testForm.model = snapshot.model
    testForm.testEndpointMode = snapshot.testEndpointMode
    testingAccount.value = snapshot.testingAccount
    batchTestingAccounts.value = [...snapshot.batchTestingAccounts]
    activeSingleTestTask.value = mergeRestoredSingleTask(snapshot.activeSingleTestTask, backendTasks)
    batchTestItems.value = snapshot.batchTestItems.map((item) => {
      const task = backendTasks.find((candidate) => (
        candidate.id === item.taskId
        || (!item.taskId && candidate.accountId === item.account.id)
      ))
      if (!task) return item
      return {
        ...item,
        taskId: task.id,
        status: taskStatusToBatchStatus(task),
        message: task.message ?? task.result?.message ?? item.message,
        result: task.result ?? item.result,
        startedAt: parseTaskTime(task.startedAt) ?? item.startedAt,
        finishedAt: parseTaskTime(task.finishedAt) ?? item.finishedAt
      }
    })
    testResult.value = activeSingleTestTask.value?.result ?? snapshot.result
    draftTestingAccountPayload.value = undefined
    draftTestMode.value = undefined
    draftApiKeyTestSnapshot.value = undefined
    testRunning.value = snapshot.running
    testModalOpen.value = snapshot.running
  }

  function mergeRestoredSingleTask(
    storedTask: AccountTestTask | undefined,
    backendTasks: AccountTestTask[]
  ): AccountTestTask | undefined {
    if (!backendTasks.length) return storedTask
    if (storedTask) {
      return backendTasks.find((task) => task.id === storedTask.id) ?? storedTask
    }
    return backendTasks[0]
  }

  async function resumeSingleAccountTest(run: AccountTestRunContext, tasks: AccountTestTask[]): Promise<void> {
    const account = testingAccount.value
    if (!account) return
    const task = mergeRestoredSingleTask(activeSingleTestTask.value, tasks)
    if (!task) {
      testResult.value = failedAccountTestResult({
        account,
        error: new Error('未找到可恢复的后台测试任务'),
        model: testForm.model,
        testEndpointMode: testForm.testEndpointMode,
        startedAt: Date.now()
      })
      return
    }
    activeSingleTestTask.value = task
    run.tasks.set(task.id, account)
    const payload = buildAccountTestPayload({
      model: testForm.model || defaultModelForSelection(account),
      testEndpointMode: testForm.testEndpointMode
    }, account)
    const result = await waitForSubmittedAccountTestResult(run, task, account, payload, (latestTask) => {
      if (!isActiveAccountTestRun(run)) return
      activeSingleTestTask.value = latestTask
      persistAccountTestRunSession(run, true)
    })
    if (!isActiveAccountTestRun(run)) return
    testResult.value = result
    persistAccountTestRunSession(run, true)
  }

  async function resumeBatchAccountTest(run: AccountTestRunContext, sessionId: string): Promise<void> {
    const accounts = [...batchTestingAccounts.value]
    const formSnapshot: AccountTestForm = {
      model: testForm.model,
      testEndpointMode: testForm.testEndpointMode
    }
    await runInFixedBatches(accounts, accountBatchTestChunkSize, async (account, index) => {
      await runBatchAccountTestItem(run, account, index, formSnapshot, sessionId)
    }, run.controller.signal)
    if (!run.controller.signal.aborted) {
      showBatchTestSummary(run, accounts.length)
    }
  }

  function submitAccountTest(
    account: AccountSummary,
    payload: AccountTestPayload,
    sessionId: string,
    activeDraftMode?: AccountTestDraftMode,
    draftPayload?: AccountDraftTestPayload['account']
  ): Promise<AccountTestTask> {
    return submitAccountTestTask({
      account,
      accountScopeParams: options.accountScopeParams.value,
      draftMode: activeDraftMode,
      draftPayload,
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

  function activeSavedDraftUpdateTestPayload(account: AccountSummary): AccountDraftTestPayload['account'] | undefined {
    return draftTestMode.value === 'saved' ? activeDraftTestPayload(account) : undefined
  }

  function syncDraftActivationTestFromTask(task: AccountTestTask, activationDraftPayload: AccountDraftTestPayload['account'] | undefined): void {
    if (!activationDraftPayload) return
    if (task.status === 'success' && task.result?.success === true) {
      successfulDraftActivationTest.value = { taskId: task.id, account: activationDraftPayload }
    } else if (task.status === 'failed' || task.status === 'canceled') {
      successfulDraftActivationTest.value = undefined
    }
  }

  function syncSavedDraftUpdateTestFromTask(task: AccountTestTask, savedDraftUpdatePayload: AccountDraftTestPayload['account'] | undefined): void {
    if (!savedDraftUpdatePayload) return
    if (task.status === 'success' && task.result?.success === true) {
      successfulSavedDraftUpdateTest.value = { taskId: task.id, account: savedDraftUpdatePayload }
    } else if (task.status === 'failed' || task.status === 'canceled') {
      successfulSavedDraftUpdateTest.value = undefined
    }
  }

  function syncDraftApiKeyTestSnapshot(draftPayload: AccountDraftTestPayload['account'] | undefined, result: AccountTestResult): void {
    if (!draftPayload || draftPayload.type !== 'api_key') return
    draftApiKeyTestSnapshot.value = { account: draftPayload, result }
  }

  function accountTestTaskScopeParams(
    account: AccountSummary,
    activeDraftMode = draftTestMode.value,
    draftPayload = activeDraftTestPayload(account)
  ): ReturnType<typeof accountOperationScopeParams> {
    return activeDraftMode === 'create' && draftPayload
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
    run: AccountTestRunContext,
    initialTask: AccountTestTask,
    account: AccountSummary,
    payload: AccountTestPayload,
    onUpdate?: (task: AccountTestTask) => void
  ): Promise<AccountTestResult> {
    return waitForAccountTestResult({
      account,
      cancelTask: cancelCreatedAccountTestTask,
      currentTestEndpointMode: () => payload.testEndpointMode ?? 'account_default',
      currentModel: () => payload.model ?? '',
      fetchTask: fetchAccountTestTask,
      initialTask,
      onTaskSettled: (taskId) => {
        run.tasks.delete(taskId)
      },
      onUpdate,
      signal: run.controller.signal
    })
  }

  function updateBatchTestItem(run: AccountTestRunContext, index: number, patch: Partial<AccountBatchTestItem>) {
    if (!isActiveAccountTestRun(run)) return
    const current = batchTestItems.value[index]
    if (!current) return
    if (current.status === 'stopped' && patch.status !== 'stopped') return
    batchTestItems.value[index] = { ...current, ...patch }
    persistAccountTestRunSession(run, testRunning.value)
  }

  function markPendingBatchTestItemsStopped(run: AccountTestRunContext) {
    if (!isActiveAccountTestRun(run)) return
    batchTestItems.value = batchTestItems.value.map((item) => {
      if (item.status !== 'pending' && item.status !== 'queued' && item.status !== 'running') return item
      return { ...item, status: 'stopped', message: '已停止测试', finishedAt: Date.now() }
    })
    persistAccountTestRunSession(run, testRunning.value)
  }

  function showBatchTestSummary(run: AccountTestRunContext, total: number) {
    if (!isActiveAccountTestRun(run)) return
    const successCount = batchTestItems.value.filter((item) => item.status === 'success').length
    const summary = batchTestSummary(total, successCount)
    if (summary.success) {
      message.success(summary.message)
      options.clearSelection?.()
    } else {
      message.warning(summary.message)
    }
  }

  function beginAccountTestRun(): AccountTestRunContext {
    const run: AccountTestRunContext = {
      controller: new AbortController(),
      tasks: new Map()
    }
    activeTestRun = run
    testRunning.value = true
    return run
  }

  function isActiveAccountTestRun(run: AccountTestRunContext): boolean {
    return activeTestRun === run
  }

  function finishAccountTestRun(run: AccountTestRunContext): void {
    if (!isActiveAccountTestRun(run)) return
    activeTestRun = undefined
    testRunning.value = false
  }

  async function detachActiveAccountTestRun(): Promise<void> {
    const previousDetach = pendingTestRunDetach
    const run = activeTestRun
    if (!run) {
      await previousDetach
      testRunning.value = false
      return
    }
    persistAccountTestRunSession(run, false)
    activeTestRun = undefined
    testRunning.value = false
    clearStoredAccountTestRunSession(options.isManagementView.value)
    const detach = (async () => {
      await previousDetach
      await cancelAccountTestRun(run)
    })()
    pendingTestRunDetach = detach
    try {
      await detach
    } finally {
      if (pendingTestRunDetach === detach) {
        pendingTestRunDetach = undefined
      }
    }
  }

  async function cancelAccountTestRun(run: AccountTestRunContext): Promise<void> {
    run.controller.abort()
    stopAccountTestSessionHeartbeat(run)
    const cancellations: Promise<unknown>[] = [
      cancelAccountTestRunSession(run)
    ]
    for (const [taskId, account] of run.tasks) {
      cancellations.push(cancelAccountTestTask(taskId, account))
    }
    await Promise.all(cancellations.map(async (cancellation) => {
      try {
        await cancellation
      } catch (error) {
        console.error(error)
      }
    }))
    if (run.sessionId) {
      clearAccountTestRunSession(run)
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
    updateAccountTestModel,
    draftTestingAccountPayload,
    draftApiKeyTestSnapshot,
    successfulDraftActivationTest
  }
}
