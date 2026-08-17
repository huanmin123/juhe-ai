import type { AccountListItem, AccountTestResult, AccountTestTask } from '@/types/domain'

import { type AccountTestEndpointMode, failedAccountTestResult } from './accountTestFlow'
import {
  accountTestTaskMaxWaitMs,
  accountTestTaskRemainingWaitMs,
  parseTaskTime,
  waitForPollDelay
} from './accountTestTaskHelpers'

interface WaitForAccountTestResultOptions {
  account: AccountListItem
  cancelTask: (taskId: string, account: AccountListItem) => Promise<void>
  currentTestEndpointMode: () => AccountTestEndpointMode
  currentModel: () => string
  fetchTask: (taskId: string, account: AccountListItem, signal?: AbortSignal) => Promise<AccountTestTask>
  initialTask: AccountTestTask
  onTaskSettled?: (taskId: string) => void
  onUpdate?: (task: AccountTestTask) => void
  signal: AbortSignal
}

export async function waitForAccountTestResult(options: WaitForAccountTestResultOptions): Promise<AccountTestResult> {
  const {
    account,
    cancelTask,
    currentTestEndpointMode,
    currentModel,
    fetchTask,
    onTaskSettled,
    onUpdate,
    signal
  } = options
  let task = options.initialTask
  onUpdate?.(task)
  while (true) {
    if (signal.aborted) {
      throw new DOMException('测试已停止', 'AbortError')
    }
    const startedAt = parseTaskTime(task.startedAt)
    const startedAtInvalid = (task.startedAt !== undefined && startedAt === undefined)
      || (task.status === 'running' || task.status === 'success' || task.status === 'failed') && startedAt === undefined
    if (startedAtInvalid) {
      const invalidStartedAtResult = failedAccountTestResult({
        account,
        error: new Error(task.startedAt
          ? `后台测试任务 startedAt 无法严格解析：${task.startedAt}`
          : '后台测试任务缺少 startedAt，无法计算运行窗口'),
        model: task.model ?? currentModel(),
        testEndpointMode: currentTestEndpointMode(),
        startedAt
      })
      if (task.status === 'running') {
        try {
          await cancelTask(task.id, account)
        } catch (cancelError) {
          invalidStartedAtResult.message = `${invalidStartedAtResult.message}；停止后台任务失败：${cancelError instanceof Error ? cancelError.message : '未知错误'}`
        }
      }
      onTaskSettled?.(task.id)
      return invalidStartedAtResult
    }
    if (task.status === 'success' || task.status === 'failed') {
      onTaskSettled?.(task.id)
      if (task.result) {
        return task.result
      }
      return failedAccountTestResult({
        account,
        error: new Error(task.message ?? '测试失败'),
        model: task.model ?? currentModel(),
        testEndpointMode: currentTestEndpointMode(),
        startedAt
      })
    }
    if (task.status === 'canceled') {
      onTaskSettled?.(task.id)
      if (signal.aborted) {
        throw new DOMException(task.message ?? '测试已停止', 'AbortError')
      }
      return failedAccountTestResult({
        account,
        error: new Error(task.message ?? '后台测试任务已取消'),
        model: task.model ?? currentModel(),
        testEndpointMode: currentTestEndpointMode(),
        startedAt
      })
    }
    const timeoutResult = accountTestTaskTimeoutResult({
      account,
      testEndpointMode: currentTestEndpointMode(),
      model: currentModel(),
      task
    })
    if (timeoutResult) {
      await cancelTask(task.id, account)
      onTaskSettled?.(task.id)
      return timeoutResult
    }
    await waitForPollDelay(signal, accountTestTaskRemainingWaitMs(task))
    task = await fetchTask(task.id, account, signal)
    onUpdate?.(task)
  }
}

export function accountTestTaskTimeoutResult(input: {
  account: AccountListItem
  testEndpointMode: AccountTestEndpointMode
  model: string
  task: AccountTestTask
}): AccountTestResult | undefined {
  if (input.task.status !== 'running') {
    return undefined
  }
  const startedAt = parseTaskTime(input.task.startedAt)
  const testEndpointMode = input.task.testEndpointMode
    ?? (input.testEndpointMode === 'account_default' ? undefined : input.testEndpointMode)
  const maxWaitMs = accountTestTaskMaxWaitMs(testEndpointMode)
  if (startedAt === undefined) {
    return failedAccountTestResult({
      account: input.account,
      error: new Error(input.task.startedAt
        ? `后台测试任务 startedAt 无法严格解析：${input.task.startedAt}`
        : '后台测试任务缺少 startedAt，无法计算运行窗口'),
      model: input.task.model ?? input.model,
      testEndpointMode: input.testEndpointMode,
      startedAt
    })
  }
  if (Date.now() - startedAt < maxWaitMs) {
    return undefined
  }
  const maxWaitText = `${Math.ceil(maxWaitMs / 1000)}s`
  const message = `账号测试运行超过 ${maxWaitText} 未完成，已自动停止`
  return failedAccountTestResult({
    account: input.account,
    error: new Error(message),
    model: input.task.model ?? input.model,
    testEndpointMode: input.testEndpointMode,
    startedAt
  })
}
