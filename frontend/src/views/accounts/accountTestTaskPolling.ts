import type { AccountSummary, AccountTestResult, AccountTestTask } from '@/types/domain'

import { type AccountTestClientCompatibility, failedAccountTestResult } from './accountTestFlow'
import {
  accountTestTaskMaxWaitMs,
  accountTestTaskRemainingWaitMs,
  parseTaskTime,
  waitForPollDelay
} from './accountTestTaskHelpers'

interface WaitForAccountTestResultOptions {
  account: AccountSummary
  cancelTask: (taskId: string, account: AccountSummary) => Promise<void>
  currentClientCompatibility: () => AccountTestClientCompatibility
  currentModel: () => string
  fetchTask: (taskId: string, account: AccountSummary, signal?: AbortSignal) => Promise<AccountTestTask>
  initialTask: AccountTestTask
  onTaskSettled?: (taskId: string) => void
  onUpdate?: (task: AccountTestTask) => void
  signal: AbortSignal
}

export async function waitForAccountTestResult(options: WaitForAccountTestResultOptions): Promise<AccountTestResult> {
  const {
    account,
    cancelTask,
    currentClientCompatibility,
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
    if (task.status === 'success' || task.status === 'failed') {
      onTaskSettled?.(task.id)
      if (task.result) {
        return task.result
      }
      return failedAccountTestResult({
        account,
        error: new Error(task.message ?? '测试失败'),
        model: task.model ?? currentModel(),
        clientCompatibility: currentClientCompatibility(),
        startedAt: task.startedAt ? Date.parse(task.startedAt) : Date.now()
      })
    }
    if (task.status === 'canceled') {
      onTaskSettled?.(task.id)
      throw new DOMException(task.message ?? '测试已停止', 'AbortError')
    }
    const timeoutResult = accountTestTaskTimeoutResult({
      account,
      clientCompatibility: currentClientCompatibility(),
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
  account: AccountSummary
  clientCompatibility: AccountTestClientCompatibility
  model: string
  task: AccountTestTask
}): AccountTestResult | undefined {
  if (input.task.status !== 'running') {
    return undefined
  }
  const startedAt = parseTaskTime(input.task.startedAt)
  if (startedAt === undefined || Date.now() - startedAt < accountTestTaskMaxWaitMs) {
    return undefined
  }
  const maxWaitText = `${Math.ceil(accountTestTaskMaxWaitMs / 1000)}s`
  const message = `账号测试运行超过 ${maxWaitText} 未完成，已自动停止`
  return failedAccountTestResult({
    account: input.account,
    error: new Error(message),
    model: input.task.model ?? input.model,
    clientCompatibility: input.clientCompatibility,
    startedAt
  })
}
