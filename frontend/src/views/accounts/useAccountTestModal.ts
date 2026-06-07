import axios from 'axios'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onDeactivated, reactive, ref, type ComputedRef } from 'vue'

import { api, type AccountDraftTestPayload } from '@/api/client'
import type { AccountSummary, AccountTestResult, AccountTestTask, ProviderDefinition, ProviderModelPricing } from '@/types/domain'
import {
  type AccountBatchTestItem,
  type AccountTestForm,
  type AccountTestMode,
  accountTestErrorMessage,
  accountTestSuccessMessage,
  buildAccountTestPayload,
  batchTestSummary,
  failedAccountTestResult,
  nextTestModel,
  stoppedAccountTestMessage
} from './accountTestFlow'
import { buildTestModelOptions, defaultTestModelForAccountSelection, isOpenAICompatibleTestSelection, providerCodeForAccountSelection, providerDefaultTestModelForAccountSelection } from './accountDerivedState'
import { GPT_VENDOR_CODE } from '@/shared/providerProtocol'
import { isOpenAIProtocolProfile } from '@/shared/providerProtocol'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { authorizedAccountUnavailableText, canTestAccount } from './accountRules'

interface UseAccountTestModalOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  clearSelection?: () => void
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  providers: ComputedRef<ProviderDefinition[]>
  successfulDraftActivationTest?: { value: SuccessfulDraftActivationTest | undefined }
}

type AccountTestPayload = ReturnType<typeof buildAccountTestPayload>

export interface SuccessfulDraftActivationTest {
  taskId: string
  account: AccountDraftTestPayload['account']
}

const accountBatchTestConcurrency = 3
const accountTestTaskBatchQuerySize = 100
const accountTestPollIntervalMs = 1000

interface SubmittedBatchAccountTestTask {
  account: AccountSummary
  index: number
  task: AccountTestTask
}

export function useAccountTestModal(options: UseAccountTestModalOptions) {
  const testModalOpen = ref(false)
  const testRunning = ref(false)
  const testModelsLoading = ref(false)
  const testMode = ref<AccountTestMode>('single')
  const testingAccount = ref<AccountSummary>()
  const batchTestingAccounts = ref<AccountSummary[]>([])
  const batchTestItems = ref<AccountBatchTestItem[]>([])
  const testResult = ref<AccountTestResult>()
  const providerModels = ref<ProviderModelPricing[]>([])
  const providerModelsProviderCode = ref('')
  const draftTestingAccountPayload = ref<AccountDraftTestPayload['account']>()
  const successfulDraftActivationTest = options.successfulDraftActivationTest ?? ref<SuccessfulDraftActivationTest>()
  const testForm = reactive<AccountTestForm>({ model: '', clientCompatibility: 'account_default' })
  const testTargetAccountSelection = computed(() => (
    testMode.value === 'batch' ? batchTestingAccounts.value : testingAccount.value
  ))
  const testTargetProviderCode = computed(() => providerCodeForAccountSelection(testTargetAccountSelection.value))
  const providerDefaultTestModel = computed(() => providerDefaultTestModelForAccountSelection(
    options.providers.value,
    testTargetAccountSelection.value
  ))
  const testModelOptions = computed(() => buildTestModelOptions(
    providerModels.value,
    testTargetAccountSelection.value,
    providerDefaultTestModel.value
  ))
  const defaultTestModel = computed(() => (
    defaultTestModelForAccountSelection(testTargetAccountSelection.value, providerDefaultTestModel.value)
  ))
  const isOpenAICompatibleTestTarget = computed(() => isOpenAICompatibleTestSelection(testTargetAccountSelection.value))

  let accountTestAbortController: AbortController | undefined
  const activeAccountTestTasks = new Map<string, AccountSummary>()

  async function loadTestModels() {
    if (!isOpenAICompatibleTestTarget.value) {
      providerModels.value = []
      providerModelsProviderCode.value = ''
      testForm.model = nextTestModel(testForm.model, testModelOptions.value, defaultTestModel.value)
      return
    }
    const providerCode = testTargetProviderCode.value || GPT_VENDOR_CODE
    if (providerModelsProviderCode.value !== providerCode) {
      providerModels.value = []
      providerModelsProviderCode.value = providerCode
    }
    if (providerModels.value.length || testModelsLoading.value) return
    testModelsLoading.value = true
    try {
      providerModels.value = await api.providers.models(providerCode)
      testForm.model = nextTestModel(testForm.model || defaultTestModel.value, testModelOptions.value, defaultTestModel.value)
    } catch (error) {
      console.error(error)
      testForm.model = nextTestModel(testForm.model, testModelOptions.value, defaultTestModel.value)
      message.warning('测试模型列表加载失败，已使用默认模型')
    } finally {
      testModelsLoading.value = false
    }
  }

  async function openTestModal(account: AccountSummary) {
    if (!canTestAccount(account)) {
      if (!isOpenAIProtocolProfile(account)) {
        message.warning('当前仅支持测试 OpenAI v1 协议账户')
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
    draftTestingAccountPayload.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(account)
    testForm.clientCompatibility = 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function openDraftTestModal(account: AccountSummary, draftPayload: AccountDraftTestPayload['account']) {
    if (!isOpenAIProtocolProfile(account)) {
      message.warning('当前仅支持测试 OpenAI v1 协议账户')
      return
    }
    testMode.value = 'single'
    testingAccount.value = account
    batchTestingAccounts.value = []
    batchTestItems.value = []
    draftTestingAccountPayload.value = draftPayload
    successfulDraftActivationTest.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(account)
    testForm.clientCompatibility = 'account_default'
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
      message.warning('已跳过非 GPT 供应商或当前不能测试的账户')
    }
    testMode.value = 'batch'
    testingAccount.value = undefined
    batchTestingAccounts.value = [...testableAccounts]
    batchTestItems.value = testableAccounts.map((account) => ({ account, status: 'pending' }))
    draftTestingAccountPayload.value = undefined
    testResult.value = undefined
    testForm.model = defaultModelForSelection(testableAccounts)
    testForm.clientCompatibility = 'account_default'
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
    const activationDraftPayload = activeDraftTestPayload(account)
    try {
      const payload = buildAccountSpecificTestPayload(account)
      const task = await submitAccountTest(account, payload)
      activeAccountTestTasks.set(task.id, account)
      if (controller.signal.aborted) {
        await cancelCreatedAccountTestTask(task.id, account)
        activeAccountTestTasks.delete(task.id)
        throw new DOMException('测试已停止', 'AbortError')
      }
      const result = await waitForAccountTestResult(task, account, controller.signal)
      testResult.value = result
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
      if (axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')) {
        message.info(stoppedAccountTestMessage(account))
        return
      }
      console.error(error)
      testResult.value = failedAccountTestResult({
        account,
        error,
        model: testForm.model,
        clientCompatibility: testForm.clientCompatibility,
        startedAt
      })
      message.error(`${account.name}: 测试失败`)
      if (activationDraftPayload) {
        successfulDraftActivationTest.value = undefined
      }
    } finally {
      for (const taskId of [...activeAccountTestTasks.keys()]) {
        activeAccountTestTasks.delete(taskId)
      }
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
    const submittedTasks = new Map<string, SubmittedBatchAccountTestTask>()
    try {
      await runWithConcurrency(accounts, accountBatchTestConcurrency, async (account, index) => {
        if (controller.signal.aborted) {
          updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
          return
        }
        const startedAt = Date.now()
        updateBatchTestItem(index, { status: 'running', message: '提交后台测试任务', startedAt })
        const payload = buildAccountSpecificTestPayload(account)
        try {
          const task = await submitAccountTest(account, payload)
          activeAccountTestTasks.set(task.id, account)
          if (controller.signal.aborted) {
            await cancelCreatedAccountTestTask(task.id, account)
            activeAccountTestTasks.delete(task.id)
            updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
            return
          }
          submittedTasks.set(task.id, { account, index, task })
          updateBatchTestItem(index, { taskId: task.id, status: 'running', message: task.message ?? '等待后台测试', startedAt })
        } catch (error) {
          if (isAbortError(error)) {
            updateBatchTestItem(index, { status: 'stopped', message: '已停止测试', finishedAt: Date.now() })
            return
          }
          console.error(error)
          const result = failedAccountTestResult({
            account,
            error,
            model: payload.model ?? '',
            clientCompatibility: testForm.clientCompatibility,
            startedAt
          })
          updateBatchTestItem(index, {
            status: 'failed',
            result,
            message: result.message,
            finishedAt: Date.now()
          })
        }
      }, controller.signal)

      if (!controller.signal.aborted && submittedTasks.size > 0) {
        try {
          await pollBatchAccountTestTasks(submittedTasks, controller.signal)
        } catch (error) {
          if (!isAbortError(error)) {
            throw error
          }
        }
      }

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
      testRunning.value = false
      if (accountTestAbortController === controller) {
        accountTestAbortController = undefined
      }
    }
  }

  function stopAccountTest() {
    if (!testRunning.value) return
    accountTestAbortController?.abort()
    for (const [taskId, account] of activeAccountTestTasks) {
      void cancelAccountTestTask(taskId, account).catch((error) => {
        console.error(error)
      })
    }
  }

  function closeTestModal() {
    if (testRunning.value) {
      stopAccountTest()
    }
    testModalOpen.value = false
  }

  onDeactivated(stopAccountTest)
  onBeforeUnmount(stopAccountTest)

  function buildAccountSpecificTestPayload(account: AccountSummary, clientCompatibility = testForm.clientCompatibility) {
    return buildAccountTestPayload({
      ...testForm,
      clientCompatibility: account.type === 'oauth' ? 'account_default' : clientCompatibility,
      model: testForm.model || defaultModelForSelection(account)
    })
  }

  function defaultModelForSelection(account: AccountSummary | AccountSummary[] | undefined): string {
    return defaultTestModelForAccountSelection(
      account,
      providerDefaultTestModelForAccountSelection(options.providers.value, account)
    )
  }

  function submitAccountTest(account: AccountSummary, payload: AccountTestPayload): Promise<AccountTestTask> {
    const draftPayload = activeDraftTestPayload(account)
    if (draftPayload) {
      const requestPayload: AccountDraftTestPayload = { account: draftPayload, ...payload }
      return options.isManagementView.value
        ? api.accounts.testDraft(requestPayload, options.accountScopeParams.value)
        : api.myAccounts.testDraft(requestPayload)
    }
    return options.isManagementView.value
      ? api.accounts.test(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value))
      : api.myAccounts.test(account.id, payload)
  }

  function fetchAccountTestTask(taskId: string, account: AccountSummary, signal?: AbortSignal): Promise<AccountTestTask> {
    return options.isManagementView.value
      ? api.accounts.testTask(taskId, accountTestTaskScopeParams(account), { signal })
      : api.myAccounts.testTask(taskId, { signal })
  }

  async function fetchAccountTestTasks(
    taskIds: string[],
    submittedTasks: Map<string, SubmittedBatchAccountTestTask>,
    signal?: AbortSignal
  ): Promise<AccountTestTask[]> {
    if (!options.isManagementView.value) {
      return api.myAccounts.testTasks(taskIds, { signal })
    }
    const taskGroups = new Map<string, { params: ReturnType<typeof accountOperationScopeParams>; taskIds: string[] }>()
    for (const taskId of taskIds) {
      const account = submittedTasks.get(taskId)?.account
      const params = account ? accountOperationScopeParams(account, options.accountScopeParams.value) : options.accountScopeParams.value
      const key = params?.systemAccountId ?? ''
      const group = taskGroups.get(key) ?? { params, taskIds: [] }
      group.taskIds.push(taskId)
      taskGroups.set(key, group)
    }
    const taskChunks = await Promise.all([...taskGroups.values()].map((group) => (
      api.accounts.testTasks(group.taskIds, group.params, { signal })
    )))
    return taskChunks.flat()
  }

  function cancelAccountTestTask(taskId: string, account?: AccountSummary): Promise<AccountTestTask> {
    return options.isManagementView.value
      ? api.accounts.cancelTestTask(taskId, account ? accountTestTaskScopeParams(account) : options.accountScopeParams.value)
      : api.myAccounts.cancelTestTask(taskId)
  }

  function activeDraftTestPayload(account: AccountSummary): AccountDraftTestPayload['account'] | undefined {
    return testingAccount.value?.id === account.id ? draftTestingAccountPayload.value : undefined
  }

  function accountTestTaskScopeParams(account: AccountSummary): ReturnType<typeof accountOperationScopeParams> {
    return activeDraftTestPayload(account)
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

  async function waitForAccountTestResult(
    initialTask: AccountTestTask,
    account: AccountSummary,
    signal: AbortSignal,
    onUpdate?: (task: AccountTestTask) => void
  ): Promise<AccountTestResult> {
    let task = initialTask
    onUpdate?.(task)
    while (true) {
      if (signal.aborted) {
        throw new DOMException('测试已停止', 'AbortError')
      }
      if (task.status === 'success' || task.status === 'failed') {
        activeAccountTestTasks.delete(task.id)
        if (task.result) {
          return task.result
        }
        return failedAccountTestResult({
          account,
          error: new Error(task.message ?? '测试失败'),
          model: task.model ?? testForm.model,
          clientCompatibility: testForm.clientCompatibility,
          startedAt: task.startedAt ? Date.parse(task.startedAt) : Date.now()
        })
      }
      if (task.status === 'canceled') {
        activeAccountTestTasks.delete(task.id)
        throw new DOMException(task.message ?? '测试已停止', 'AbortError')
      }
      await waitForPollDelay(signal)
      task = await fetchAccountTestTask(task.id, account, signal)
      onUpdate?.(task)
    }
  }

  async function pollBatchAccountTestTasks(submittedTasks: Map<string, SubmittedBatchAccountTestTask>, signal: AbortSignal): Promise<void> {
    const pendingTaskIds = new Set(submittedTasks.keys())
    while (pendingTaskIds.size > 0) {
      if (signal.aborted) {
        throw new DOMException('测试已停止', 'AbortError')
      }
      await waitForPollDelay(signal)
      const taskIds = [...pendingTaskIds]
      for (const taskIdChunk of chunkList(taskIds, accountTestTaskBatchQuerySize)) {
        const latestTasks = await fetchAccountTestTasks(taskIdChunk, submittedTasks, signal)
        const latestTaskIds = new Set(latestTasks.map((task) => task.id))
        for (const task of latestTasks) {
          const submitted = submittedTasks.get(task.id)
          if (!submitted || !pendingTaskIds.has(task.id)) continue
          updateBatchTestItem(submitted.index, {
            taskId: task.id,
            status: task.status === 'queued' || task.status === 'running' ? 'running' : taskStatusToBatchStatus(task),
            message: task.message ?? task.result?.message,
            result: task.result,
            finishedAt: task.finishedAt ? Date.parse(task.finishedAt) : undefined
          })
          if (task.status === 'success' || task.status === 'failed') {
            const result = task.result ?? failedAccountTestResult({
              account: submitted.account,
              error: new Error(task.message ?? '测试失败'),
              model: task.model ?? testForm.model,
              clientCompatibility: testForm.clientCompatibility,
              startedAt: task.startedAt ? Date.parse(task.startedAt) : Date.now()
            })
            updateBatchTestItem(submitted.index, {
              status: result.success ? 'success' : 'failed',
              result,
              message: result.message,
              finishedAt: task.finishedAt ? Date.parse(task.finishedAt) : Date.now()
            })
            pendingTaskIds.delete(task.id)
            activeAccountTestTasks.delete(task.id)
          } else if (task.status === 'canceled') {
            updateBatchTestItem(submitted.index, { status: 'stopped', message: task.message ?? '已停止测试', finishedAt: task.finishedAt ? Date.parse(task.finishedAt) : Date.now() })
            pendingTaskIds.delete(task.id)
            activeAccountTestTasks.delete(task.id)
          }
        }
        for (const taskId of taskIdChunk) {
          if (!pendingTaskIds.has(taskId) || latestTaskIds.has(taskId)) continue
          const submitted = submittedTasks.get(taskId)
          if (!submitted) continue
          const result = failedAccountTestResult({
            account: submitted.account,
            error: new Error('测试任务不存在或已过期'),
            model: testForm.model,
            clientCompatibility: testForm.clientCompatibility,
            startedAt: Date.now()
          })
          updateBatchTestItem(submitted.index, {
            status: 'failed',
            result,
            message: result.message,
            finishedAt: Date.now()
          })
          pendingTaskIds.delete(taskId)
          activeAccountTestTasks.delete(taskId)
        }
      }
    }
  }

  function updateBatchTestItem(index: number, patch: Partial<AccountBatchTestItem>) {
    const current = batchTestItems.value[index]
    if (!current) return
    if (current.status === 'stopped' && patch.status !== 'stopped') return
    batchTestItems.value[index] = { ...current, ...patch }
  }

  function markPendingBatchTestItemsStopped() {
    batchTestItems.value = batchTestItems.value.map((item) => {
      if (item.status !== 'pending' && item.status !== 'running') return item
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
    batchTestItems,
    batchTestingAccounts,
    closeTestModal,
    openBatchTestModal,
    openDraftTestModal,
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
    successfulDraftActivationTest
  }
}

async function runWithConcurrency<TItem>(
  items: TItem[],
  concurrency: number,
  task: (item: TItem, index: number) => Promise<void>,
  signal: AbortSignal
): Promise<void> {
  let nextIndex = 0
  const workerCount = Math.min(Math.max(1, concurrency), items.length)
  async function runWorker(): Promise<void> {
    while (nextIndex < items.length && !signal.aborted) {
      const index = nextIndex
      nextIndex += 1
      await task(items[index], index)
    }
  }
  await Promise.all(Array.from({ length: workerCount }, runWorker))
}

function isAbortError(error: unknown): boolean {
  return axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')
}

function taskStatusToBatchStatus(task: AccountTestTask): AccountBatchTestItem['status'] {
  if (task.status === 'success') return 'success'
  if (task.status === 'failed') return 'failed'
  if (task.status === 'canceled') return 'stopped'
  return 'running'
}

function chunkList<TItem>(items: TItem[], size: number): TItem[][] {
  const chunks: TItem[][] = []
  const chunkSize = Math.max(1, Math.trunc(size))
  for (let index = 0; index < items.length; index += chunkSize) {
    chunks.push(items.slice(index, index + chunkSize))
  }
  return chunks
}

async function waitForPollDelay(signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    throw new DOMException('测试已停止', 'AbortError')
  }
  await new Promise<void>((resolve, reject) => {
    let settled = false
    const timer = window.setTimeout(() => finish(), accountTestPollIntervalMs)
    const finish = (error?: Error) => {
      if (settled) return
      settled = true
      window.clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
      if (error) {
        reject(error)
        return
      }
      resolve()
    }
    const onAbort = () => finish(new DOMException('测试已停止', 'AbortError'))
    signal.addEventListener('abort', onAbort, { once: true })
    if (signal.aborted) {
      onAbort()
    }
  })
}
