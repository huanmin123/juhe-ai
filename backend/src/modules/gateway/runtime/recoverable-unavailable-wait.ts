import { getRequestLogger } from '../../../shared/request-context.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  defaultGatewayFinalResponseReserveMs,
  type GatewayRequestWallBudget,
  type RouteCoordinationBudget,
  type RouteCoordinationBudgetTransitionResult
} from '../routing/route-coordination.js'

export const recoverableUnavailableMaxWaitMs = 30_000
export const recoverableUnavailableCheckIntervalMs = 5_000
const recoverableUnavailableDueRetryDelayMs = 250

type RecoverableUnavailableWaitSkippedReason =
  | 'no_retry_time'
  | 'retry_after_exceeds_window'
  | 'aborted'
  | 'deadline_exceeded'
  | 'scope_limit'
  | 'global_limit'
  | 'temporarily_blocked_coordination_budget_exhausted'
  | 'temporarily_blocked_coordination_budget_conflict'

export interface RecoverableUnavailableWaitResult<T> {
  state: T
  waitedMs: number
  checkCount: number
  ready: boolean
  timedOut: boolean
  skippedReason?: RecoverableUnavailableWaitSkippedReason
}

interface RecoverableUnavailableWaitInput<T> {
  scopeKey: string
  reason: string
  initialState: T
  refresh: () => T | Promise<T>
  isReady: (state: T) => boolean
  nextRetryAfterMs: (state: T) => number | undefined
  auditCapture: AuditCaptureContext
  signal?: AbortSignal
  waitWithoutRetryAfter?: boolean
  maxWaitMs?: number
  checkIntervalMs?: number
  requestStartedAtMs?: number
  deadlineAtMs?: number
  coordinator?: RecoverableUnavailableWaitCoordinator
  runtimeKeys?: string[]
  routeCoordinationBudget?: RouteCoordinationBudget
  gatewayRequestWallBudget?: GatewayRequestWallBudget
  finalResponseReserveMs?: number
  now?: () => number
}

export type RecoverableUnavailableCoordinatorWaitResult =
  | 'ready'
  | 'aborted'
  | 'deadline_exceeded'
  | 'scope_limit'
  | 'global_limit'

export interface RecoverableUnavailableWaitCoordinatorOptions {
  maxWaitersPerScope?: number
  maxWaitersGlobal?: number
  setTimer?: (callback: () => void, delayMs: number) => unknown
  clearTimer?: (timer: unknown) => void
  now?: () => number
}

export interface RecoverableUnavailableCoordinatorWaitInput {
  scopeKey: string
  reason: string
  delayMs: number
  deadlineAtMs: number
  signal?: AbortSignal
  runtimeKeys?: string[]
}

interface RecoverableUnavailableCoordinatorWaiter {
  id: number
  notBeforeMs: number
  deadlineAtMs: number
  signal?: AbortSignal
  abortListener?: () => void
  resolve: (result: RecoverableUnavailableCoordinatorWaitResult) => void
}

interface RecoverableUnavailableCoordinatorScope {
  waiters: RecoverableUnavailableCoordinatorWaiter[]
  runtimeKeys: Set<string>
  timer?: unknown
}

const defaultMaxWaitersPerScope = 256
const defaultMaxWaitersGlobal = 4096

export class RecoverableUnavailableWaitCoordinator {
  private readonly scopes = new Map<string, RecoverableUnavailableCoordinatorScope>()
  private readonly maxWaitersPerScope: number
  private readonly maxWaitersGlobal: number
  private readonly setTimer: (callback: () => void, delayMs: number) => unknown
  private readonly clearTimer: (timer: unknown) => void
  private readonly now: () => number
  private nextWaiterId = 1
  private waiterCount = 0

  constructor(options: RecoverableUnavailableWaitCoordinatorOptions = {}) {
    this.maxWaitersPerScope = normalizePositiveMs(options.maxWaitersPerScope, defaultMaxWaitersPerScope)
    this.maxWaitersGlobal = normalizePositiveMs(options.maxWaitersGlobal, defaultMaxWaitersGlobal)
    this.setTimer = options.setTimer ?? ((callback, delayMs) => {
      const timer = setTimeout(callback, delayMs)
      timer.unref()
      return timer
    })
    this.clearTimer = options.clearTimer ?? ((timer) => clearTimeout(timer as NodeJS.Timeout))
    this.now = options.now ?? Date.now
  }

  waitForTurn(input: RecoverableUnavailableCoordinatorWaitInput): Promise<RecoverableUnavailableCoordinatorWaitResult> {
    if (input.signal?.aborted) return Promise.resolve('aborted')
    const now = this.now()
    const deadlineAtMs = normalizeDeadlineAtMs(input.deadlineAtMs, now)
    if (deadlineAtMs <= now) return Promise.resolve('deadline_exceeded')
    const key = coordinatorScopeKey(input.reason, input.scopeKey)
    const scope = this.scopes.get(key) ?? { waiters: [], runtimeKeys: new Set<string>() }
    if (scope.waiters.length >= this.maxWaitersPerScope) return Promise.resolve('scope_limit')
    if (this.waiterCount >= this.maxWaitersGlobal) return Promise.resolve('global_limit')

    return new Promise((resolve) => {
      const waiter: RecoverableUnavailableCoordinatorWaiter = {
        id: this.nextWaiterId++,
        notBeforeMs: now + normalizeNonNegativeMs(input.delayMs),
        deadlineAtMs,
        signal: input.signal,
        resolve
      }
      if (input.signal) {
        waiter.abortListener = () => this.settleWaiter(key, waiter.id, 'aborted')
        input.signal.addEventListener('abort', waiter.abortListener, { once: true })
      }
      scope.waiters.push(waiter)
      for (const runtimeKey of input.runtimeKeys ?? []) {
        if (runtimeKey.trim()) scope.runtimeKeys.add(runtimeKey.trim())
      }
      this.waiterCount += 1
      this.scopes.set(key, scope)
      this.scheduleScope(key, scope)
    })
  }

  notifyOne(scopeKey: string, reason: string): boolean {
    const key = coordinatorScopeKey(reason, scopeKey)
    const scope = this.scopes.get(key)
    const waiter = scope?.waiters[0]
    return waiter ? this.settleReadyWaiter(key, waiter) : false
  }

  notifyOneForRuntimeKey(runtimeKey: string): boolean {
    const normalized = runtimeKey.trim()
    let selected: { key: string; waiterId: number } | undefined
    for (const [key, scope] of this.scopes) {
      const waiter = scope.waiters[0]
      if (!waiter || !scope.runtimeKeys.has(normalized)) continue
      if (!selected || waiter.id < selected.waiterId) selected = { key, waiterId: waiter.id }
    }
    if (!selected) return false
    const waiter = this.scopes.get(selected.key)?.waiters.find((item) => item.id === selected.waiterId)
    return waiter ? this.settleReadyWaiter(selected.key, waiter) : false
  }

  snapshot(): { scopeCount: number; waiterCount: number; timerCount: number } {
    let timerCount = 0
    for (const scope of this.scopes.values()) {
      if (scope.timer !== undefined) timerCount += 1
    }
    return { scopeCount: this.scopes.size, waiterCount: this.waiterCount, timerCount }
  }

  private scheduleScope(key: string, scope: RecoverableUnavailableCoordinatorScope): void {
    if (scope.timer !== undefined || scope.waiters.length === 0) return
    const head = scope.waiters[0]!
    const dueAtMs = Math.min(head.notBeforeMs, head.deadlineAtMs)
    scope.timer = this.setTimer(() => {
      scope.timer = undefined
      const now = this.now()
      if (head.signal?.aborted) {
        this.settleWaiter(key, head.id, 'aborted')
      } else if (now >= head.deadlineAtMs) {
        this.settleWaiter(key, head.id, 'deadline_exceeded')
      } else if (now >= head.notBeforeMs) {
        this.settleWaiter(key, head.id, 'ready')
      } else {
        this.scheduleScope(key, scope)
      }
    }, Math.max(0, dueAtMs - this.now()))
  }

  private settleReadyWaiter(key: string, waiter: RecoverableUnavailableCoordinatorWaiter): boolean {
    return this.settleWaiter(
      key,
      waiter.id,
      this.now() >= waiter.deadlineAtMs ? 'deadline_exceeded' : 'ready'
    )
  }

  private settleWaiter(
    key: string,
    waiterId: number,
    result: RecoverableUnavailableCoordinatorWaitResult
  ): boolean {
    const scope = this.scopes.get(key)
    if (!scope) return false
    const index = scope.waiters.findIndex((waiter) => waiter.id === waiterId)
    if (index < 0) return false
    const wasHead = index === 0
    const [waiter] = scope.waiters.splice(index, 1)
    if (!waiter) return false
    if (waiter.signal && waiter.abortListener) {
      waiter.signal.removeEventListener('abort', waiter.abortListener)
    }
    this.waiterCount = Math.max(0, this.waiterCount - 1)
    if (wasHead && scope.timer !== undefined) {
      this.clearTimer(scope.timer)
      scope.timer = undefined
    }
    if (scope.waiters.length === 0) {
      this.scopes.delete(key)
    } else {
      this.scheduleScope(key, scope)
    }
    waiter.resolve(result)
    return true
  }
}

let defaultRecoverableUnavailableWaitCoordinator = new RecoverableUnavailableWaitCoordinator()

export function installRecoverableUnavailableWaitCoordinatorForTest(
  options: RecoverableUnavailableWaitCoordinatorOptions
): () => void {
  const previous = defaultRecoverableUnavailableWaitCoordinator
  const previousSnapshot = previous.snapshot()
  if (previousSnapshot.waiterCount > 0 || previousSnapshot.timerCount > 0 || previousSnapshot.scopeCount > 0) {
    throw new Error('恢复等待协调器仍有活动等待者，不能替换测试实例')
  }
  const replacement = new RecoverableUnavailableWaitCoordinator(options)
  defaultRecoverableUnavailableWaitCoordinator = replacement
  let restored = false
  return () => {
    if (restored) return
    if (defaultRecoverableUnavailableWaitCoordinator !== replacement) {
      throw new Error('恢复等待协调器测试实例已被其他调用替换')
    }
    const snapshot = replacement.snapshot()
    if (snapshot.waiterCount > 0 || snapshot.timerCount > 0 || snapshot.scopeCount > 0) {
      throw new Error(`恢复等待协调器测试实例仍有活动资源：${JSON.stringify(snapshot)}`)
    }
    defaultRecoverableUnavailableWaitCoordinator = previous
    restored = true
  }
}

export function notifyOneRecoverableUnavailableRuntimeWaiter(runtimeKey: string): boolean {
  return defaultRecoverableUnavailableWaitCoordinator.notifyOneForRuntimeKey(runtimeKey)
}

export function recoverableUnavailableWaitCoordinatorSnapshotForTest(): {
  scopeCount: number
  waiterCount: number
  timerCount: number
} {
  return defaultRecoverableUnavailableWaitCoordinator.snapshot()
}

export async function waitForRecoverableUnavailableState<T>(
  input: RecoverableUnavailableWaitInput<T>
): Promise<RecoverableUnavailableWaitResult<T>> {
  const maxWaitMs = normalizePositiveMs(input.maxWaitMs, recoverableUnavailableMaxWaitMs)
  const checkIntervalMs = normalizePositiveMs(input.checkIntervalMs, recoverableUnavailableCheckIntervalMs)
  const now = input.now ?? Date.now
  const startedAtMs = now()
  const requestStartedAtMs = normalizeOptionalTimestamp(input.requestStartedAtMs) ?? startedAtMs
  const localDeadlineAtMs = requestStartedAtMs + maxWaitMs
  const wallDeadlineAtMs = input.gatewayRequestWallBudget
    ? input.gatewayRequestWallBudget.deadlineAtMs - normalizeNonNegativeMs(input.finalResponseReserveMs ?? defaultGatewayFinalResponseReserveMs)
    : Number.POSITIVE_INFINITY
  const deadlineAtMs = Math.min(
    localDeadlineAtMs,
    normalizeOptionalTimestamp(input.deadlineAtMs) ?? localDeadlineAtMs,
    wallDeadlineAtMs
  )
  const coordinator = input.coordinator ?? defaultRecoverableUnavailableWaitCoordinator
  let state = input.initialState
  let checkCount = 0

  if (input.isReady(state)) {
    return {
      state,
      waitedMs: 0,
      checkCount,
      ready: true,
      timedOut: false
    }
  }

  if (deadlineAtMs <= now()) {
    return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, true, 'deadline_exceeded')
  }

  input.auditCapture.addGatewayMetadata({
    label: 'recoverable_unavailable_wait',
    metadata: {
      reason: input.reason,
      scopeKey: input.scopeKey,
      maxWaitMs,
      checkIntervalMs,
      nextRetryAfterMs: input.nextRetryAfterMs(state)
    }
  })

  while (!input.signal?.aborted) {
    const turnStartedAtMs = now()
    const coordinationRemainingMs = input.routeCoordinationBudget?.remainingMs(turnStartedAtMs)
    if (coordinationRemainingMs !== undefined && coordinationRemainingMs <= 0) {
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        false,
        'temporarily_blocked_coordination_budget_exhausted'
      )
    }
    const remainingMs = Math.min(
      deadlineAtMs - turnStartedAtMs,
      coordinationRemainingMs ?? Number.POSITIVE_INFINITY
    )
    if (remainingMs <= 0) {
      return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, true)
    }

    const delayMs = nextRecoverableWaitDelayMs({
      nextRetryAfterMs: input.nextRetryAfterMs(state),
      remainingMs,
      checkIntervalMs,
      waitWithoutRetryAfter: input.waitWithoutRetryAfter === true
    })
    if (!delayMs.wait) {
      const timedOut = delayMs.skippedReason === 'retry_after_exceeds_window' && checkCount > 0
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        timedOut,
        delayMs.skippedReason
      )
    }

    const coordinationWait = beginRouteCoordinationWait(input, turnStartedAtMs)
    if (coordinationWait?.outcome === 'version_conflict' || coordinationWait?.outcome === 'invalid_transition') {
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        false,
        input.routeCoordinationBudget?.exhausted(turnStartedAtMs)
          ? 'temporarily_blocked_coordination_budget_exhausted'
          : 'temporarily_blocked_coordination_budget_conflict'
      )
    }
    getRequestLogger().info({
      event: 'gateway_recoverable_unavailable_wait_scheduled',
      reason: input.reason,
      scopeKey: input.scopeKey,
      delayMs: delayMs.delayMs,
      remainingMs
    }, '本地可恢复阻塞短等后重新检查调度候选')
    const waitToken = coordinationWait?.snapshot.lastWaitToken
    let turn: RecoverableUnavailableCoordinatorWaitResult
    let pauseResult: RouteCoordinationBudgetTransitionResult | undefined
    try {
      turn = await coordinator.waitForTurn({
        scopeKey: input.scopeKey,
        reason: input.reason,
        delayMs: delayMs.delayMs,
        deadlineAtMs: turnStartedAtMs + remainingMs,
        signal: input.signal,
        runtimeKeys: input.runtimeKeys
      })
    } finally {
      if (input.routeCoordinationBudget && coordinationWait && waitToken) {
        pauseResult = input.routeCoordinationBudget.pauseWait({
          waitToken,
          expectedVersion: coordinationWait.snapshot.version,
          nowMs: now()
        })
      }
    }
    if (pauseResult?.outcome === 'version_conflict' || pauseResult?.outcome === 'invalid_transition') {
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        false,
        'temporarily_blocked_coordination_budget_conflict'
      )
    }
    if (turn !== 'ready') {
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        turn === 'deadline_exceeded',
        turn
      )
    }
    checkCount += 1
    state = await input.refresh()
    if (input.isReady(state)) {
      return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, true, false)
    }
  }

  return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, false, 'aborted')
}

function finalizeRecoverableUnavailableWait<T>(
  input: RecoverableUnavailableWaitInput<T>,
  state: T,
  startedAtMs: number,
  checkCount: number,
  ready: boolean,
  timedOut: boolean,
  skippedReason?: RecoverableUnavailableWaitSkippedReason
): RecoverableUnavailableWaitResult<T> {
  const waitedMs = (input.now ?? Date.now)() - startedAtMs
  input.auditCapture.addGatewayMetadata({
    label: 'recoverable_unavailable_wait_result',
    metadata: {
      reason: input.reason,
      scopeKey: input.scopeKey,
      waitedMs,
      checkCount,
      ready,
      timedOut,
      skippedReason,
      nextRetryAfterMs: input.nextRetryAfterMs(state)
    }
  })
  return {
    state,
    waitedMs,
    checkCount,
    ready,
    timedOut,
    skippedReason
  }
}

function beginRouteCoordinationWait<T>(
  input: RecoverableUnavailableWaitInput<T>,
  nowMs: number
): RouteCoordinationBudgetTransitionResult | undefined {
  const budget = input.routeCoordinationBudget
  if (!budget) return undefined
  const snapshot = budget.snapshot(nowMs)
  const waitToken = [budget.budgetId, input.reason, input.scopeKey, `v${snapshot.version}`].join(':')
  return budget.beginWait({
    waitToken,
    expectedVersion: snapshot.version,
    nowMs
  })
}

function nextRecoverableWaitDelayMs(input: {
  nextRetryAfterMs: number | undefined
  remainingMs: number
  checkIntervalMs: number
  waitWithoutRetryAfter: boolean
}): { wait: true; delayMs: number } | { wait: false; skippedReason: RecoverableUnavailableWaitSkippedReason } {
  const remainingMs = Math.max(0, Math.trunc(input.remainingMs))
  if (remainingMs <= 0) {
    return { wait: false, skippedReason: 'retry_after_exceeds_window' }
  }
  const nextRetryAfterMs = normalizeOptionalMs(input.nextRetryAfterMs)
  if (nextRetryAfterMs === undefined) {
    return input.waitWithoutRetryAfter
      ? { wait: true, delayMs: Math.min(input.checkIntervalMs, remainingMs) }
      : { wait: false, skippedReason: 'no_retry_time' }
  }
  if (!input.waitWithoutRetryAfter && nextRetryAfterMs > remainingMs) {
    return { wait: false, skippedReason: 'retry_after_exceeds_window' }
  }
  const targetDelayMs = nextRetryAfterMs <= 0 ? recoverableUnavailableDueRetryDelayMs : nextRetryAfterMs
  return {
    wait: true,
    delayMs: Math.min(Math.max(50, targetDelayMs), input.checkIntervalMs, remainingMs)
  }
}

function normalizeOptionalMs(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : undefined
}

function normalizeNonNegativeMs(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function normalizeOptionalTimestamp(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : undefined
}

function normalizeDeadlineAtMs(value: number, now: number): number {
  return Number.isFinite(value) ? Math.trunc(value) : now
}

function coordinatorScopeKey(reason: string, scopeKey: string): string {
  return JSON.stringify([reason, scopeKey])
}

function normalizePositiveMs(value: number | undefined, fallback: number): number {
  const raw = typeof value === 'number' && Number.isFinite(value) ? value : fallback
  return Math.max(1, Math.trunc(raw))
}
