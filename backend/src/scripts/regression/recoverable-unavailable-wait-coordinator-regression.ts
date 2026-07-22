import assert from 'node:assert/strict'

import {
  RecoverableUnavailableWaitCoordinator,
  waitForRecoverableUnavailableState
} from '../../modules/gateway/runtime/recoverable-unavailable-wait.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import {
  GatewayRequestWallBudget,
  RouteCoordinationBudget
} from '../../modules/gateway/routing/route-coordination.js'

const auditCapture = {
  addGatewayMetadata() {}
} as unknown as AuditCaptureContext

await testFifoWakeOneWithSingleScopeTimer()
await testAbortRemovesQueuedWaiter()
await testScopeAndGlobalLimits()
await testOriginalStartAndAbsoluteDeadline()
await testRuntimeKeyWakeOnePreservesFifo()
await testDeadlineBeforeNotBeforeDoesNotWakeReady()
await testCoordinationBudgetUsesEarliestRetryAndIgnoresDuplicateWake()
await testCoordinationBudgetPausesOnAbort()
await testCoordinationBudgetConflictIsTemporarilyBlocked()

console.log('recoverable unavailable wait coordinator regression passed')

async function testFifoWakeOneWithSingleScopeTimer(): Promise<void> {
  let activeTimers = 0
  let maxActiveTimers = 0
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    maxWaitersPerScope: 10,
    maxWaitersGlobal: 10,
    setTimer(callback, delayMs) {
      activeTimers += 1
      maxActiveTimers = Math.max(maxActiveTimers, activeTimers)
      const timer = setTimeout(() => {
        activeTimers -= 1
        callback()
      }, delayMs)
      return timer
    },
    clearTimer(timer) {
      clearTimeout(timer as NodeJS.Timeout)
      activeTimers = Math.max(0, activeTimers - 1)
    }
  })
  const wakeOrder: number[] = []
  const waits = [1, 2, 3].map(async (index) => {
    const result = await coordinator.waitForTurn({
      scopeKey: 'scope-a',
      reason: 'precheck_half_open',
      delayMs: 5,
      deadlineAtMs: Date.now() + 1000
    })
    assert.equal(result, 'ready')
    wakeOrder.push(index)
  })
  await Promise.all(waits)
  assert.deepEqual(wakeOrder, [1, 2, 3], '同 scope 等待者必须 FIFO 逐个唤醒')
  assert.equal(maxActiveTimers, 1, '同 scope 任意时刻只能存在一个 timer')
  assert.deepEqual(coordinator.snapshot(), { scopeCount: 0, waiterCount: 0, timerCount: 0 })
}

async function testAbortRemovesQueuedWaiter(): Promise<void> {
  const coordinator = new RecoverableUnavailableWaitCoordinator()
  const abortController = new AbortController()
  const waiting = coordinator.waitForTurn({
    scopeKey: 'scope-abort',
    reason: 'precheck_half_open',
    delayMs: 1000,
    deadlineAtMs: Date.now() + 2000,
    signal: abortController.signal
  })
  abortController.abort()
  assert.equal(await waiting, 'aborted')
  assert.deepEqual(coordinator.snapshot(), { scopeCount: 0, waiterCount: 0, timerCount: 0 })
}

async function testScopeAndGlobalLimits(): Promise<void> {
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    maxWaitersPerScope: 1,
    maxWaitersGlobal: 2
  })
  const firstAbort = new AbortController()
  const secondAbort = new AbortController()
  const first = coordinator.waitForTurn({
    scopeKey: 'scope-limit-a', reason: 'precheck_half_open', delayMs: 1000,
    deadlineAtMs: Date.now() + 2000, signal: firstAbort.signal
  })
  assert.equal(await coordinator.waitForTurn({
    scopeKey: 'scope-limit-a', reason: 'precheck_half_open', delayMs: 1000,
    deadlineAtMs: Date.now() + 2000
  }), 'scope_limit')
  const second = coordinator.waitForTurn({
    scopeKey: 'scope-limit-b', reason: 'precheck_half_open', delayMs: 1000,
    deadlineAtMs: Date.now() + 2000, signal: secondAbort.signal
  })
  assert.equal(await coordinator.waitForTurn({
    scopeKey: 'scope-limit-c', reason: 'precheck_half_open', delayMs: 1000,
    deadlineAtMs: Date.now() + 2000
  }), 'global_limit')
  firstAbort.abort()
  secondAbort.abort()
  assert.equal(await first, 'aborted')
  assert.equal(await second, 'aborted')
  assert.deepEqual(coordinator.snapshot(), { scopeCount: 0, waiterCount: 0, timerCount: 0 })
}

async function testOriginalStartAndAbsoluteDeadline(): Promise<void> {
  const requestStartedAtMs = Date.now() - 100
  let refreshCount = 0
  const result = await waitForRecoverableUnavailableState({
    scopeKey: 'scope-expired',
    reason: 'precheck_half_open',
    initialState: { ready: false },
    refresh: () => {
      refreshCount += 1
      return { ready: false }
    },
    isReady: (state) => state.ready,
    nextRetryAfterMs: () => 1,
    auditCapture,
    requestStartedAtMs,
    deadlineAtMs: requestStartedAtMs + 50,
    maxWaitMs: 1000
  })
  assert.equal(result.timedOut, true)
  assert.equal(result.skippedReason, 'deadline_exceeded')
  assert.equal(refreshCount, 0, '原始请求 deadline 已过时不得重新开启等待窗口')
}

async function testRuntimeKeyWakeOnePreservesFifo(): Promise<void> {
  const coordinator = new RecoverableUnavailableWaitCoordinator()
  const first = coordinator.waitForTurn({
    scopeKey: 'group:model:runtime-a:7',
    reason: 'precheck_half_open',
    runtimeKeys: ['runtime-a'],
    delayMs: 1000,
    deadlineAtMs: Date.now() + 2000
  })
  const secondAbort = new AbortController()
  const second = coordinator.waitForTurn({
    scopeKey: 'group:model:runtime-a:7',
    reason: 'precheck_half_open',
    runtimeKeys: ['runtime-a'],
    delayMs: 1000,
    deadlineAtMs: Date.now() + 2000,
    signal: secondAbort.signal
  })
  assert.equal(coordinator.notifyOneForRuntimeKey('runtime-a'), true)
  assert.equal(await first, 'ready', 'lease 释放必须只唤醒同 runtime scope 的队首等待者')
  assert.equal(coordinator.snapshot().waiterCount, 1)
  secondAbort.abort()
  assert.equal(await second, 'aborted')
  assert.deepEqual(coordinator.snapshot(), { scopeCount: 0, waiterCount: 0, timerCount: 0 })
}

async function testDeadlineBeforeNotBeforeDoesNotWakeReady(): Promise<void> {
  let nowMs = 500
  let timerCallback: (() => void) | undefined
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    now: () => nowMs,
    setTimer(callback) {
      timerCallback = callback
      return callback
    },
    clearTimer() {}
  })
  const waiting = coordinator.waitForTurn({
    scopeKey: 'scope-deadline-before-retry',
    reason: 'account_cooldown_recoverable',
    delayMs: 200,
    deadlineAtMs: 600
  })
  nowMs = 600
  timerCallback?.()
  assert.equal(await waiting, 'deadline_exceeded', '最早重试晚于预算 deadline 时不得提前刷新候选')
}

async function testCoordinationBudgetUsesEarliestRetryAndIgnoresDuplicateWake(): Promise<void> {
  let nowMs = 1_000
  const timers: Array<{ callback: () => void; delayMs: number }> = []
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    now: () => nowMs,
    setTimer(callback, delayMs) {
      timers.push({ callback, delayMs })
      return callback
    },
    clearTimer() {}
  })
  const routeCoordinationBudget = new RouteCoordinationBudget({
    requestId: 'request-coordination-wake',
    now: () => nowMs
  })
  const gatewayRequestWallBudget = new GatewayRequestWallBudget({
    requestAcceptedAtMs: nowMs,
    budgetMs: 10_000,
    now: () => nowMs
  })
  let refreshCount = 0
  const waiting = waitForRecoverableUnavailableState({
    scopeKey: 'scope-coordination-wake',
    reason: 'account_cooldown_recoverable',
    initialState: { ready: false },
    refresh: () => {
      refreshCount += 1
      return { ready: true }
    },
    isReady: state => state.ready,
    nextRetryAfterMs: () => 100,
    auditCapture,
    maxWaitMs: 5_000,
    requestStartedAtMs: nowMs,
    deadlineAtMs: nowMs + 5_000,
    routeCoordinationBudget,
    gatewayRequestWallBudget,
    coordinator,
    now: () => nowMs
  })
  await Promise.resolve()
  assert.equal(timers.length, 1)
  assert.equal(timers[0]?.delayMs, 100, '本轮等待必须裁剪到最早账号重试时间')
  assert.equal(routeCoordinationBudget.snapshot().activeSinceMs, 1_000)

  nowMs = 1_100
  timers[0]?.callback()
  timers[0]?.callback()
  const result = await waiting
  assert.equal(result.ready, true)
  assert.equal(refreshCount, 1, '重复 wake 不得重复刷新或结算等待预算')
  assert.equal(routeCoordinationBudget.remainingMs(), 2_900)
  assert.equal(routeCoordinationBudget.snapshot().activeSinceMs, undefined)
  assert.equal(routeCoordinationBudget.snapshot().version, 2)
}

async function testCoordinationBudgetPausesOnAbort(): Promise<void> {
  let nowMs = 2_000
  const coordinator = new RecoverableUnavailableWaitCoordinator({
    now: () => nowMs,
    setTimer(callback) { return callback },
    clearTimer() {}
  })
  const routeCoordinationBudget = new RouteCoordinationBudget({
    requestId: 'request-coordination-abort',
    now: () => nowMs
  })
  const abortController = new AbortController()
  const waiting = waitForRecoverableUnavailableState({
    scopeKey: 'scope-coordination-abort',
    reason: 'local_account_suppression',
    initialState: { ready: false },
    refresh: () => ({ ready: false }),
    isReady: state => state.ready,
    nextRetryAfterMs: () => 1_000,
    auditCapture,
    maxWaitMs: 5_000,
    requestStartedAtMs: nowMs,
    deadlineAtMs: nowMs + 5_000,
    routeCoordinationBudget,
    coordinator,
    signal: abortController.signal,
    now: () => nowMs
  })
  await Promise.resolve()
  nowMs = 2_050
  abortController.abort()
  const result = await waiting
  assert.equal(result.skippedReason, 'aborted')
  assert.equal(routeCoordinationBudget.remainingMs(), 2_950)
  assert.equal(routeCoordinationBudget.snapshot().activeSinceMs, undefined, 'abort 后协调预算必须立即暂停')
}

async function testCoordinationBudgetConflictIsTemporarilyBlocked(): Promise<void> {
  const nowMs = 3_000
  const routeCoordinationBudget = new RouteCoordinationBudget({
    requestId: 'request-coordination-conflict',
    now: () => nowMs
  })
  assert.equal(routeCoordinationBudget.beginWait({
    waitToken: 'foreign-wait',
    expectedVersion: 0,
    nowMs
  }).outcome, 'applied')
  const result = await waitForRecoverableUnavailableState({
    scopeKey: 'scope-coordination-conflict',
    reason: 'local_account_suppression',
    initialState: { ready: false },
    refresh: () => ({ ready: false }),
    isReady: state => state.ready,
    nextRetryAfterMs: () => 100,
    auditCapture,
    maxWaitMs: 5_000,
    requestStartedAtMs: nowMs,
    deadlineAtMs: nowMs + 5_000,
    routeCoordinationBudget,
    now: () => nowMs
  })
  assert.equal(result.skippedReason, 'temporarily_blocked_coordination_budget_conflict')
  assert.equal(routeCoordinationBudget.snapshot().lastWaitToken, 'foreign-wait')
  assert.equal(routeCoordinationBudget.snapshot().activeSinceMs, nowMs, 'CAS 冲突不得结算或覆盖其他活动 wait')
}
