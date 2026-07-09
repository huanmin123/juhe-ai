import { getRequestAuthContext } from '../auth/request-context.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { resolveAccessScope } from '../../storage/access-scope.js'

export const activeModelCheckRetryAfterSeconds = 1
export const activeModelCheckConflictMessage = '当前用户已有模型检测正在运行，请等待完成或先手动停止'

export type ActiveModelCheckRunSummary = {
  runId?: string
  traceId?: string
  targetId?: string
  targetName?: string
  model?: string
  startedAt: string
  stopRequested: boolean
}

type ActiveModelCheckRun = ActiveModelCheckRunSummary & {
  key: string
  controller: AbortController
}

const activeModelCheckRuns = new Map<string, ActiveModelCheckRun>()

export function modelCheckActiveRunKey(access?: AccessScope): string {
  const context = getRequestAuthContext()
  const systemAccountId = context?.systemAccountId?.trim() || resolveAccessScope(access)?.systemAccountId?.trim()
  return systemAccountId ? `system-account:${systemAccountId}` : 'anonymous'
}

export function tryStartActiveModelCheckRun(access?: AccessScope): { acquired: true; key: string; controller: AbortController } | { acquired: false; active: ActiveModelCheckRunSummary } {
  const key = modelCheckActiveRunKey(access)
  const active = activeModelCheckRuns.get(key)
  if (active) {
    return { acquired: false, active: activeModelCheckRunSummary(active) }
  }
  const controller = new AbortController()
  activeModelCheckRuns.set(key, {
    key,
    controller,
    startedAt: new Date().toISOString(),
    stopRequested: false
  })
  return { acquired: true, key, controller }
}

export function updateActiveModelCheckRun(key: string, patch: Partial<Omit<ActiveModelCheckRunSummary, 'key' | 'startedAt' | 'stopRequested'>> & { startedAt?: string }): void {
  const active = activeModelCheckRuns.get(key)
  if (!active) return
  Object.assign(active, patch)
}

export function finishActiveModelCheckRun(key: string, controller: AbortController): void {
  const active = activeModelCheckRuns.get(key)
  if (active?.controller !== controller) return
  activeModelCheckRuns.delete(key)
}

export function stopActiveModelCheckRun(access?: AccessScope): ActiveModelCheckRunSummary | undefined {
  const key = modelCheckActiveRunKey(access)
  const active = activeModelCheckRuns.get(key)
  if (!active) return undefined
  active.stopRequested = true
  if (!active.controller.signal.aborted) {
    active.controller.abort(new Error('用户手动停止模型检测'))
  }
  return activeModelCheckRunSummary(active)
}

export function getActiveModelCheckRun(access?: AccessScope): ActiveModelCheckRunSummary | undefined {
  const active = activeModelCheckRuns.get(modelCheckActiveRunKey(access))
  return active ? activeModelCheckRunSummary(active) : undefined
}

function activeModelCheckRunSummary(active: ActiveModelCheckRun): ActiveModelCheckRunSummary {
  return {
    runId: active.runId,
    traceId: active.traceId,
    targetId: active.targetId,
    targetName: active.targetName,
    model: active.model,
    startedAt: active.startedAt,
    stopRequested: active.stopRequested
  }
}
