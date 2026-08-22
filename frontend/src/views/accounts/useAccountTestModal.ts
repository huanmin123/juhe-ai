import { onBeforeUnmount, reactive, ref, type ComputedRef } from 'vue'

import { message } from '@/lib/antd'
import type { AccountDraftTestPayload, AccountTestPayload } from '@/api/client'
import type {
  AccountListItem,
  AccountSupportedEndpointMode,
  AccountTestResult,
  AccountTestTask
} from '@/types/domain'
import {
  type AccountTestForm,
  accountTestSuccessMessage,
  buildAccountTestPayload,
  failedAccountTestResult,
  stoppedAccountTestMessage
} from './accountTestFlow'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { authorizedAccountUnavailableText, canTestAccount } from './accountRules'
import {
  type AccountTestDraftMode,
  cancelAccountTestSession as cancelAccountTestSessionRequest,
  cancelAccountTestTask as cancelAccountTestTaskRequest,
  completeAccountTestSession as completeAccountTestSessionRequest,
  createAccountTestSession as createAccountTestSessionRequest,
  fetchAccountTestTask as fetchAccountTestTaskRequest,
  heartbeatAccountTestSession as heartbeatAccountTestSessionRequest,
  submitAccountTestTask
} from './accountTestSessionClient'
import { isAbortError } from './accountTestTaskHelpers'
import { useAccountTestModels } from './useAccountTestModels'
import { waitForAccountTestResult } from './accountTestTaskPolling'
import { isGatewayTestableAccountProfile } from './accountProviderCapabilities'
import { accountTestEndpointModesForAccount } from './accountEndpointModes'
import type { DraftApiKeyTestSnapshot } from './accountDraftApiKeyTestRuntime'
import {
  type AccountTestRunSessionSnapshot,
  clearAccountTestRunSession as clearStoredAccountTestRunSession,
  readAccountTestRunSession,
  writeAccountTestRunSession
} from './accountTestRunSession'

interface UseAccountTestModalOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  draftApiKeyTestSnapshot?: { value: DraftApiKeyTestSnapshot | undefined }
  onDraftTestSuccess?: (draftPayload: AccountDraftTestPayload['account']) => void
}

const accountTestSessionHeartbeatIntervalMs = 5000

interface AccountTestRunContext {
  account: AccountListItem
  controller: AbortController
  detached: boolean
  draftMode?: AccountTestDraftMode
  draftPayload?: AccountDraftTestPayload['account']
  endpointMode: AccountSupportedEndpointMode
  endpointModes: AccountSupportedEndpointMode[]
  model: string
  modelOptions: Array<{ label: string; value: string }>
  restorable: boolean
  scopeParams?: { systemAccountId: string }
  sessionId?: string
  task?: AccountTestTask
  result?: AccountTestResult
  stopRequested: boolean
  stoppedSessionId?: string
  stoppedTaskId?: string
  viewToken: number
  heartbeatTimer?: number
}

export function useAccountTestModal(options: UseAccountTestModalOptions) {
  const testModalOpen = ref(false)
  const testRunning = ref(false)
  const testingAccount = ref<AccountListItem>()
  const activeSingleTestTask = ref<AccountTestTask>()
  const testResult = ref<AccountTestResult>()
  const draftTestingAccountPayload = ref<AccountDraftTestPayload['account']>()
  const draftTestMode = ref<AccountTestDraftMode>()
  const draftApiKeyTestSnapshot = options.draftApiKeyTestSnapshot ?? ref<DraftApiKeyTestSnapshot>()
  const testForm = reactive<AccountTestForm>({ model: '', testEndpointMode: 'account_default' })
  const {
    initializeSavedAccountTestOptions,
    loadTestModelOptions,
    resetTestModels,
    restoreTestSelection,
    testEndpointModes,
    testModelOptions,
    testModelReadonly,
    testModelsError,
    testModelsLoading,
    testModelsReady,
    updateSelectableTestModel,
    useFixedTestModel
  } = useAccountTestModels({
    accountScopeParams: options.accountScopeParams,
    isManagementView: options.isManagementView,
    testForm
  })

  let activeTestRun: AccountTestRunContext | undefined
  let testViewToken = 0
  let modelSearchTimer: ReturnType<typeof setTimeout> | undefined
  let pendingModelSearchResolve: (() => void) | undefined

  async function openTestModal(account: AccountListItem): Promise<void> {
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

    const viewToken = beginTestView(account)
    initializeSavedAccountTestOptions(
      account,
      account.healthCheckModel,
      account.healthCheckEndpointMode
    )
    testModalOpen.value = true
    void restoreSavedAccountTestRun(account, viewToken)
  }

  function openDraftTestModal(
    account: AccountListItem,
    draftPayload: AccountDraftTestPayload['account'],
    fixedHealthCheckModel: string,
    fixedEndpointModes?: AccountSupportedEndpointMode[]
  ): void {
    openFixedDraftTestModal(account, draftPayload, fixedHealthCheckModel, 'create', fixedEndpointModes)
  }

  function openSavedDraftTestModal(
    account: AccountListItem,
    draftPayload: AccountDraftTestPayload['account'],
    fixedHealthCheckModel: string,
    fixedEndpointModes?: AccountSupportedEndpointMode[]
  ): void {
    openFixedDraftTestModal(account, draftPayload, fixedHealthCheckModel, 'saved', fixedEndpointModes)
  }

  function openFixedDraftTestModal(
    account: AccountListItem,
    draftPayload: AccountDraftTestPayload['account'],
    fixedHealthCheckModel: string,
    mode: AccountTestDraftMode,
    fixedEndpointModes?: AccountSupportedEndpointMode[]
  ): void {
    if (!isGatewayTestableAccountProfile(account)) {
      message.warning('当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户')
      return
    }
    const model = fixedHealthCheckModel.trim()
    if (!model) {
      message.warning('请先选择检查模型')
      return
    }
    beginTestView(account)
    draftTestingAccountPayload.value = draftPayload
    draftTestMode.value = mode
    useFixedTestModel(model, fixedEndpointModes ?? accountTestEndpointModesForAccount(account, draftPayload))
    testModalOpen.value = true
  }

  async function runAccountTest(): Promise<void> {
    const account = testingAccount.value
    if (
      !account
      || testRunning.value
      || testModelsLoading.value
      || !testModelsReady.value
      || Boolean(testModelsError.value)
      || !testForm.model.trim()
      || testForm.testEndpointMode === 'account_default'
    ) {
      return
    }

    const viewToken = testViewToken
    const draftPayload = draftTestingAccountPayload.value
    const activeDraftMode = draftTestMode.value
    const startedAt = Date.now()
    const payload = buildAccountTestPayload({
      model: testForm.model,
      testEndpointMode: testForm.testEndpointMode
    }, account, draftPayload)
    const endpointMode = payload.testEndpointMode
    if (!endpointMode) return
    const run = beginAccountTestRun({
      account,
      draftMode: activeDraftMode,
      draftPayload,
      endpointMode,
      endpointModes: [...testEndpointModes.value],
      model: payload.model ?? testForm.model,
      modelOptions: testModelOptions.value.map((option) => ({ ...option })),
      scopeParams: accountTestTaskScopeParams(account, activeDraftMode, draftPayload),
      viewToken
    })

    try {
      const session = await createAccountTestSession(run.scopeParams)
      run.sessionId = session.id
      if (run.stopRequested) {
        await cancelAccountTestRunBackend(run)
        return
      }
      if (run.detached) return
      startAccountTestSessionHeartbeat(run)

      const task = await submitAccountTest(run, payload)
      run.task = task
      persistAccountTestRunSession(run, true)
      if (isRunAttached(run)) {
        activeSingleTestTask.value = task
      }
      if (run.stopRequested) {
        await cancelAccountTestRunBackend(run)
        return
      }
      if (run.detached) return

      const result = await waitForSubmittedAccountTestResult(run, task, payload)
      run.result = result
      if (!isRunAttached(run)) return
      testResult.value = result
      syncDraftApiKeyTestSnapshot(run.draftPayload, result)
      if (result.success) {
        if (run.draftPayload) options.onDraftTestSuccess?.(run.draftPayload)
        message.success(accountTestSuccessMessage(account, result))
      }
    } catch (error) {
      if (isAbortError(error)) {
        if (isRunAttached(run) && run.stopRequested) {
          message.info(stoppedAccountTestMessage(account))
        }
        return
      }
      console.error(error)
      if (!isRunAttached(run)) return
      const result = failedAccountTestResult({
        account,
        error,
        model: payload.model ?? '',
        testEndpointMode: payload.testEndpointMode ?? testForm.testEndpointMode,
        startedAt
      })
      run.result = result
      testResult.value = result
      syncDraftApiKeyTestSnapshot(run.draftPayload, result)
    } finally {
      await finishAccountTestRunLifecycle(run)
    }
  }

  async function loadAccountTestModelOptions(open: boolean, keyword = ''): Promise<void> {
    const account = testingAccount.value
    if (!open || !account || testModelReadonly.value) return
    const normalizedKeyword = keyword.trim()
    if (normalizedKeyword) {
      clearModelSearchTimer()
      await new Promise<void>((resolve) => {
        pendingModelSearchResolve = resolve
        modelSearchTimer = setTimeout(() => {
          modelSearchTimer = undefined
          pendingModelSearchResolve = undefined
          void loadAccountTestModelOptionsNow(account, normalizedKeyword).finally(resolve)
        }, 250)
      })
      return
    }
    clearModelSearchTimer()
    await loadAccountTestModelOptionsNow(account, '')
  }

  async function loadAccountTestModelOptionsNow(account: AccountListItem, keyword: string): Promise<void> {
    try {
      await loadTestModelOptions(account, keyword)
    } catch (error) {
      if (isAbortError(error)) return
      console.error(error)
    }
  }

  function updateAccountTestModel(model: string): void {
    updateSelectableTestModel(model)
  }

  function terminateAttachedTestRun(notify: boolean): boolean {
    const run = activeTestRun
    if (!run || !isRunAttached(run)) return false
    run.stopRequested = true
    if (run.task?.status === 'queued' || run.task?.status === 'running') {
      run.task = {
        ...run.task,
        status: 'canceled',
        message: '已停止测试'
      }
      activeSingleTestTask.value = run.task
    }
    clearRunSessionSnapshot(run)
    activeTestRun = undefined
    testRunning.value = false
    stopAccountTestSessionHeartbeat(run)
    run.controller.abort()
    if (notify) {
      message.info(stoppedAccountTestMessage(run.account))
    }
    void cancelAccountTestRunBackend(run)
    return true
  }

  function stopAccountTest(): void {
    terminateAttachedTestRun(true)
  }

  function closeTestModal(): void {
    const canceled = terminateAttachedTestRun(true)
    if (!canceled) {
      detachCurrentTestView()
    }
    nextTestViewToken()
    resetTestModels()
    testModalOpen.value = false
    clearModelSearchTimer()
  }

  onBeforeUnmount(() => {
    clearModelSearchTimer()
    nextTestViewToken()
    resetTestModels()
    detachCurrentTestView()
  })

  function clearModelSearchTimer(): void {
    if (modelSearchTimer) {
      clearTimeout(modelSearchTimer)
      modelSearchTimer = undefined
    }
    pendingModelSearchResolve?.()
    pendingModelSearchResolve = undefined
  }

  function beginTestView(account: AccountListItem): number {
    const viewToken = nextTestViewToken()
    detachCurrentTestView()
    resetVisibleTestState()
    testingAccount.value = account
    return viewToken
  }

  function resetVisibleTestState(): void {
    resetTestModels()
    activeSingleTestTask.value = undefined
    draftTestingAccountPayload.value = undefined
    draftTestMode.value = undefined
    draftApiKeyTestSnapshot.value = undefined
    testResult.value = undefined
    testRunning.value = false
  }

  function beginAccountTestRun(input: {
    account: AccountListItem
    draftMode?: AccountTestDraftMode
    draftPayload?: AccountDraftTestPayload['account']
    endpointMode: AccountSupportedEndpointMode
    endpointModes: AccountSupportedEndpointMode[]
    model: string
    modelOptions: Array<{ label: string; value: string }>
    scopeParams?: { systemAccountId: string }
    sessionId?: string
    task?: AccountTestTask
    viewToken: number
  }): AccountTestRunContext {
    const run: AccountTestRunContext = {
      account: input.account,
      controller: new AbortController(),
      detached: false,
      draftMode: input.draftMode,
      draftPayload: input.draftPayload,
      endpointMode: input.endpointMode,
      endpointModes: input.endpointModes,
      model: input.model,
      modelOptions: input.modelOptions,
      restorable: !input.draftPayload,
      scopeParams: input.scopeParams,
      sessionId: input.sessionId,
      task: input.task,
      stopRequested: false,
      viewToken: input.viewToken
    }
    activeTestRun = run
    testRunning.value = true
    return run
  }

  async function restoreSavedAccountTestRun(account: AccountListItem, viewToken: number): Promise<void> {
    const snapshot = readAccountTestRunSession(options.isManagementView.value, account.id)
    if (
      !snapshot
      || !snapshot.running
      || !isCurrentTestView(viewToken, account.id)
      || activeTestRun
    ) {
      return
    }

    restoreTestSelection(snapshot.model, snapshot.testEndpointMode, snapshot.testEndpointModes)
    activeSingleTestTask.value = snapshot.activeTask
    testResult.value = snapshot.result
    const snapshotModelStillAvailable = testModelOptions.value.some((option) => option.value === snapshot.model)
    const run = beginAccountTestRun({
      account,
      endpointMode: snapshot.testEndpointMode === 'account_default'
        ? snapshot.testEndpointModes[0] ?? 'chat_sse'
        : snapshot.testEndpointMode,
      endpointModes: snapshotModelStillAvailable && testEndpointModes.value.length
        ? [...testEndpointModes.value]
        : [...snapshot.testEndpointModes],
      model: snapshot.model,
      modelOptions: snapshotModelStillAvailable
        ? testModelOptions.value.map((option) => ({ ...option }))
        : snapshot.modelOptions.map((option) => ({ ...option })),
      scopeParams: snapshot.scopeParams,
      sessionId: snapshot.sessionId,
      task: snapshot.activeTask,
      viewToken
    })
    const restoreStartedAt = Date.now()
    startAccountTestSessionHeartbeat(run)
    try {
      const latestTask = await fetchAccountTestTask(run, snapshot.activeTask.id)
      run.task = latestTask
      if (!isRunAttached(run)) return
      activeSingleTestTask.value = latestTask
      persistAccountTestRunSession(run, true)
      const payload = buildAccountTestPayload({
        model: snapshot.model,
        testEndpointMode: snapshot.testEndpointMode
      }, account)
      const result = await waitForSubmittedAccountTestResult(run, latestTask, payload)
      run.result = result
      if (!isRunAttached(run)) return
      testResult.value = result
      if (result.success) {
        message.success(accountTestSuccessMessage(account, result))
      }
    } catch (error) {
      if (isAbortError(error)) return
      console.error(error)
      if (isRunAttached(run)) {
        const result = failedAccountTestResult({
          account,
          error: new Error(`测试进度恢复中断，后台任务仍会继续执行：${error instanceof Error ? error.message : '未知错误'}`),
          model: snapshot.model,
          testEndpointMode: snapshot.testEndpointMode,
          startedAt: restoreStartedAt
        })
        run.result = result
        testResult.value = result
      }
    } finally {
      await finishAccountTestRunLifecycle(run)
    }
  }

  async function finishAccountTestRunLifecycle(run: AccountTestRunContext): Promise<void> {
    if (!run.detached && !run.stopRequested) {
      await completeAccountTestRunSession(run)
      clearRunSessionSnapshot(run)
    }
    stopAccountTestSessionHeartbeat(run)
    finishAccountTestRun(run)
  }

  function detachCurrentTestView(): void {
    const run = activeTestRun
    if (!run) {
      testRunning.value = false
      return
    }
    run.detached = true
    persistAccountTestRunSession(run, true)
    activeTestRun = undefined
    testRunning.value = false
    stopAccountTestSessionHeartbeat(run)
    run.controller.abort()
  }

  function finishAccountTestRun(run: AccountTestRunContext): void {
    if (activeTestRun !== run) return
    activeTestRun = undefined
    testRunning.value = false
  }

  function isRunAttached(run: AccountTestRunContext): boolean {
    return (
      activeTestRun === run
      && !run.detached
      && run.viewToken === testViewToken
      && testingAccount.value?.id === run.account.id
    )
  }

  function nextTestViewToken(): number {
    testViewToken += 1
    return testViewToken
  }

  function isCurrentTestView(viewToken: number, accountId: string): boolean {
    return viewToken === testViewToken && testingAccount.value?.id === accountId
  }

  function createAccountTestSession(scopeParams?: { systemAccountId: string }) {
    return createAccountTestSessionRequest({
      isManagementView: options.isManagementView.value,
      scopeParams
    })
  }

  function startAccountTestSessionHeartbeat(run: AccountTestRunContext): void {
    const sessionId = run.sessionId
    if (!sessionId || run.detached || run.stopRequested) return
    stopAccountTestSessionHeartbeat(run)
    run.heartbeatTimer = window.setInterval(() => {
      void heartbeatAccountTestSessionRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.scopeParams,
        sessionId
      }).catch((error) => {
        console.error(error)
      })
    }, accountTestSessionHeartbeatIntervalMs)
  }

  function stopAccountTestSessionHeartbeat(run: AccountTestRunContext): void {
    if (run.heartbeatTimer === undefined) return
    window.clearInterval(run.heartbeatTimer)
    run.heartbeatTimer = undefined
  }

  async function completeAccountTestRunSession(run: AccountTestRunContext): Promise<void> {
    if (!run.sessionId) return
    try {
      await completeAccountTestSessionRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.scopeParams,
        sessionId: run.sessionId
      })
    } catch (error) {
      console.error(error)
    }
  }

  async function cancelAccountTestRunBackend(run: AccountTestRunContext): Promise<void> {
    const cancellations: Promise<unknown>[] = []
    if (run.task && run.stoppedTaskId !== run.task.id) {
      run.stoppedTaskId = run.task.id
      cancellations.push(cancelAccountTestTaskRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.scopeParams,
        taskId: run.task.id
      }))
    }
    if (run.sessionId && run.stoppedSessionId !== run.sessionId) {
      run.stoppedSessionId = run.sessionId
      cancellations.push(cancelAccountTestSessionRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.scopeParams,
        sessionId: run.sessionId
      }))
    }
    await Promise.all(cancellations.map(async (cancellation) => {
      try {
        await cancellation
      } catch (error) {
        console.error(error)
      }
    }))
  }

  function submitAccountTest(run: AccountTestRunContext, payload: AccountTestPayload): Promise<AccountTestTask> {
    if (!run.sessionId) {
      return Promise.reject(new Error('测试会话尚未创建'))
    }
    return submitAccountTestTask({
      account: run.account,
      accountScopeParams: run.scopeParams,
      draftMode: run.draftMode,
      draftPayload: run.draftPayload,
      isManagementView: options.isManagementView.value,
      payload,
      sessionId: run.sessionId
    })
  }

  function fetchAccountTestTask(run: AccountTestRunContext, taskId: string): Promise<AccountTestTask> {
    return fetchAccountTestTaskRequest({
      isManagementView: options.isManagementView.value,
      scopeParams: run.scopeParams,
      signal: run.controller.signal,
      taskId
    })
  }

  function waitForSubmittedAccountTestResult(
    run: AccountTestRunContext,
    initialTask: AccountTestTask,
    payload: AccountTestPayload
  ): Promise<AccountTestResult> {
    return waitForAccountTestResult({
      account: run.account,
      cancelTask: async (taskId) => {
        await cancelAccountTestTaskRequest({
          isManagementView: options.isManagementView.value,
          scopeParams: run.scopeParams,
          taskId
        })
      },
      currentTestEndpointMode: () => payload.testEndpointMode ?? 'account_default',
      currentModel: () => payload.model ?? '',
      fetchTask: (_taskId, _account, signal) => fetchAccountTestTaskRequest({
        isManagementView: options.isManagementView.value,
        scopeParams: run.scopeParams,
        signal,
        taskId: _taskId
      }),
      initialTask,
      onTaskSettled: () => undefined,
      onUpdate: (task) => {
        run.task = task
        persistAccountTestRunSession(run, true)
        if (isRunAttached(run)) {
          activeSingleTestTask.value = task
        }
      },
      signal: run.controller.signal
    })
  }

  function persistAccountTestRunSession(run: AccountTestRunContext, running: boolean): void {
    if (!run.restorable || !run.sessionId || !run.task) return
    writeAccountTestRunSession({
      sessionId: run.sessionId,
      isManagementView: options.isManagementView.value,
      scopeParams: run.scopeParams,
      model: run.model,
      modelOptions: run.modelOptions,
      testEndpointMode: run.task.testEndpointMode ?? run.endpointMode,
      testEndpointModes: run.endpointModes,
      testingAccount: run.account,
      activeTask: run.task,
      result: run.result ?? run.task.result,
      running
    })
  }

  function clearRunSessionSnapshot(run: AccountTestRunContext): void {
    if (!run.restorable) return
    clearStoredAccountTestRunSession(options.isManagementView.value, run.account.id)
  }

  function accountTestTaskScopeParams(
    account: AccountListItem,
    activeDraftMode: AccountTestDraftMode | undefined,
    draftPayload: AccountDraftTestPayload['account'] | undefined
  ): { systemAccountId: string } | undefined {
    return activeDraftMode === 'create' && draftPayload
      ? options.accountScopeParams.value
      : accountOperationScopeParams(account, options.accountScopeParams.value)
  }

  function syncDraftApiKeyTestSnapshot(
    draftPayload: AccountDraftTestPayload['account'] | undefined,
    result: AccountTestResult
  ): void {
    if (!draftPayload || draftPayload.type !== 'api_key') return
    draftApiKeyTestSnapshot.value = { account: draftPayload, result }
  }

  return {
    activeSingleTestTask,
    closeTestModal,
    draftTestingAccountPayload,
    loadAccountTestModelOptions,
    openDraftTestModal,
    openSavedDraftTestModal,
    openTestModal,
    runAccountTest,
    stopAccountTest,
    testEndpointModes,
    testForm,
    testModalOpen,
    testModelOptions,
    testModelReadonly,
    testModelsError,
    testModelsLoading,
    testModelsReady,
    testResult,
    testRunning,
    testingAccount,
    updateAccountTestModel
  }
}
