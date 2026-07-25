import axios from 'axios'

import type { AccountTestTask } from '@/types/domain'

export const accountTestPollIntervalMs = 3000
export const accountDiagnosticAttemptTimeoutsMs = [10_000, 20_000, 30_000] as const
export const accountImageDiagnosticAttemptTimeoutsMs = [120_000] as const

export function accountTestTaskMaxWaitMs(testEndpointMode?: AccountTestTask['testEndpointMode']): number {
  const timeouts = testEndpointMode === 'images_json'
    ? accountImageDiagnosticAttemptTimeoutsMs
    : accountDiagnosticAttemptTimeoutsMs
  return timeouts.reduce((sum, timeoutMs) => sum + timeoutMs, 0)
}

export function accountTestTaskRemainingWaitMs(task: AccountTestTask, nowMs = Date.now()): number {
  if (task.status !== 'running') {
    return accountTestPollIntervalMs
  }
  const startedAt = parseTaskTime(task.startedAt)
  if (startedAt === undefined) {
    return accountTestPollIntervalMs
  }
  return Math.max(0, accountTestTaskMaxWaitMs(task.testEndpointMode) - (nowMs - startedAt))
}

export function parseTaskTime(value?: string): number | undefined {
  if (!value) return undefined
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? timestamp : undefined
}

export function isAbortError(error: unknown): boolean {
  return axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')
}

export async function waitForPollDelay(signal: AbortSignal, maxDelayMs = accountTestPollIntervalMs): Promise<void> {
  if (signal.aborted) {
    throw new DOMException('测试已停止', 'AbortError')
  }
  const delayMs = Math.min(accountTestPollIntervalMs, Math.max(0, maxDelayMs))
  if (delayMs <= 0) return
  await new Promise<void>((resolve, reject) => {
    let settled = false
    const timer = window.setTimeout(() => finish(), delayMs)
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
