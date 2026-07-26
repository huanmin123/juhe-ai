import assert from 'node:assert/strict'

import { logger } from '../../shared/logger.js'
import {
  failureBackoffDelayMs,
  stableScheduledJobOffsetMs,
  WorkerScheduler,
  type WorkerSchedulerClock
} from '../../modules/background/worker-scheduler.js'

logger.level = 'silent'

assert.equal(stableScheduledJobOffsetMs('same-seed', 1_000), stableScheduledJobOffsetMs('same-seed', 1_000))
assert.notEqual(stableScheduledJobOffsetMs('instance-a', 1_000), stableScheduledJobOffsetMs('instance-b', 1_000))
assert.ok(stableScheduledJobOffsetMs('bounded', 10) >= 0 && stableScheduledJobOffsetMs('bounded', 10) < 10)
assert.equal(failureBackoffDelayMs({ baseMs: 100, maxMs: 1_000 }, 1, () => 1), 100)
assert.equal(failureBackoffDelayMs({ baseMs: 100, maxMs: 1_000 }, 8, () => 1), 1_000)

async function verifyCoalesceOneKeepsFixedRatePhase(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ stableInstanceId: 'coalesce', workerRole: 'stats-worker', clock })
  let releaseFirst: (() => void) | undefined
  let runCount = 0
  scheduler.schedule({
    name: 'coalesce',
    intervalMs: 8,
    overlapPolicy: 'coalesceOne',
    task: async () => {
      runCount += 1
      if (runCount === 1) await new Promise<void>((resolve) => { releaseFirst = resolve })
    }
  })

  await clock.flush()
  assert.equal(runCount, 1)
  await clock.advanceBy(24)
  const pending = scheduler.snapshots()[0]
  assert.equal(pending?.pending, true)
  assert.equal(pending?.coalescedCount, 1, '多次到期只能合并为一个尾随执行')
  releaseFirst?.()
  await clock.flush()
  assert.equal(runCount, 2)
  assert.equal(Date.parse(scheduler.snapshots()[0]?.nextRunAt ?? ''), clock.epochMs + 32, '尾随执行不得漂移固定频率相位')
  scheduler.stop()
}

async function verifyResourceLaneAndOverdue(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ clock })
  const order: string[] = []
  let releaseFirst: (() => void) | undefined
  scheduler.schedule({
    name: 'lane-a',
    intervalMs: 1_000,
    resourceLane: 'heavy',
    overlapPolicy: 'coalesceOne',
    task: async () => {
      order.push('a:start')
      await new Promise<void>((resolve) => { releaseFirst = resolve })
      order.push('a:end')
    }
  })
  scheduler.schedule({
    name: 'lane-b',
    intervalMs: 1_000,
    resourceLane: 'heavy',
    overlapPolicy: 'coalesceOne',
    task: () => { order.push('b') }
  })

  await clock.flush()
  assert.deepEqual(order, ['a:start'])
  await clock.advanceBy(50)
  const queued = scheduler.snapshots().find((item) => item.name === 'lane-b')
  assert.equal(queued?.queuedForLane, true)
  assert.equal(queued?.overdueMs, 50)
  releaseFirst?.()
  await clock.flush()
  assert.deepEqual(order, ['a:start', 'a:end', 'b'])
  scheduler.stop()
}

async function verifyTimeoutKeepsLane(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ clock })
  let releaseTimedOut: (() => void) | undefined
  let secondStarted = false
  scheduler.schedule({
    name: 'timeout-a',
    intervalMs: 1_000,
    timeoutMs: 15,
    resourceLane: 'timeout-lane',
    overlapPolicy: 'coalesceOne',
    task: async ({ signal }) => {
      await new Promise<void>((resolve) => {
        releaseTimedOut = resolve
        signal.addEventListener('abort', () => undefined, { once: true })
      })
    }
  })
  scheduler.schedule({
    name: 'timeout-b',
    intervalMs: 1_000,
    resourceLane: 'timeout-lane',
    overlapPolicy: 'coalesceOne',
    task: () => { secondStarted = true }
  })

  await clock.advanceBy(15)
  assert.equal(secondStarted, false, '忽略 abort 的底层任务未结束前不得释放 lane')
  assert.equal(scheduler.snapshots().find((item) => item.name === 'timeout-a')?.running, true)
  releaseTimedOut?.()
  await clock.flush()
  assert.equal(secondStarted, true)
  const timedOut = scheduler.snapshots().find((item) => item.name === 'timeout-a')
  assert.equal(timedOut?.failureCount, 1)
  assert.equal(timedOut?.lastOutcome, 'timeout')
  scheduler.stop()
}

async function verifyFixedDelay(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ clock })
  const starts: number[] = []
  scheduler.schedule({
    name: 'fixed-delay',
    intervalMs: 20,
    scheduleMode: 'fixedDelay',
    task: async () => {
      starts.push(clock.now())
      await clock.delay(15)
    }
  })

  await clock.advanceBy(15)
  assert.deepEqual(starts, [clock.epochMs])
  await clock.advanceBy(19)
  assert.equal(starts.length, 1)
  await clock.advanceBy(1)
  assert.deepEqual(starts, [clock.epochMs, clock.epochMs + 35], 'fixedDelay 必须从真实结束时间后再等待周期')
  scheduler.stop()
}

async function verifyFixedDelayLaneSkipSchedulesNextRun(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ clock })
  let releaseLane: (() => void) | undefined
  let fixedDelayRunCount = 0
  scheduler.schedule({
    name: 'fixed-delay-lane-owner',
    intervalMs: 1_000,
    scheduleMode: 'fixedDelay',
    resourceLane: 'fixed-delay-lane',
    task: async () => {
      await new Promise<void>((resolve) => { releaseLane = resolve })
    }
  })
  scheduler.schedule({
    name: 'fixed-delay-lane-skip',
    intervalMs: 20,
    scheduleMode: 'fixedDelay',
    overlapPolicy: 'skip',
    resourceLane: 'fixed-delay-lane',
    task: () => { fixedDelayRunCount += 1 }
  })

  await clock.flush()
  const skipped = scheduler.snapshots().find((item) => item.name === 'fixed-delay-lane-skip')
  assert.equal(skipped?.skippedCount, 1)
  assert.equal(Date.parse(skipped?.nextRunAt ?? ''), clock.epochMs + 20, 'lane 忙导致跳过后必须保留下一次 fixedDelay 调度')
  releaseLane?.()
  await clock.flush()
  await clock.advanceBy(20)
  assert.equal(fixedDelayRunCount, 1, 'lane 释放后 fixedDelay 任务必须能够恢复运行')
  scheduler.stop()
}

async function verifyFailureBackoffKeepsRegularPhase(): Promise<void> {
  const clock = new FakeClock()
  const starts: number[] = []
  const scheduler = new WorkerScheduler({ clock, random: () => 1 })
  scheduler.schedule({
    name: 'backoff',
    intervalMs: 10,
    failureBackoff: { baseMs: 5, maxMs: 20 },
    task: () => {
      starts.push(clock.now())
      if (starts.length === 1) throw new Error('expected regression failure')
    }
  })

  await clock.flush()
  await clock.advanceBy(4)
  assert.equal(starts.length, 1)
  await clock.advanceBy(1)
  assert.deepEqual(starts, [clock.epochMs, clock.epochMs + 5])
  assert.equal(Date.parse(scheduler.snapshots()[0]?.nextRunAt ?? ''), clock.epochMs + 10, '失败退避不得漂移固定频率相位')
  scheduler.stop()
}

async function verifyFailureBackoffDeduplicatesCoincidentRegularTick(): Promise<void> {
  const clock = new FakeClock()
  const starts: number[] = []
  const scheduler = new WorkerScheduler({ clock, random: () => 1 })
  scheduler.schedule({
    name: 'backoff-coincident-regular-tick',
    intervalMs: 20,
    failureBackoff: { baseMs: 20, maxMs: 20 },
    task: () => {
      starts.push(clock.now())
      if (starts.length === 1) throw new Error('expected coincident retry regression failure')
    }
  })

  await clock.flush()
  assert.equal(Date.parse(scheduler.snapshots()[0]?.nextRunAt ?? ''), clock.epochMs + 20)
  await clock.advanceBy(20)
  assert.deepEqual(
    starts,
    [clock.epochMs, clock.epochMs + 20],
    '失败退避与 fixed-rate 正常 tick 同时到期时只能执行一次'
  )
  scheduler.stop()
}

async function verifyFailureBackoffDefersToOverdueRegularTick(): Promise<void> {
  const clock = new FakeClock()
  const starts: number[] = []
  const scheduler = new WorkerScheduler({ clock, random: () => 1 })
  scheduler.schedule({
    name: 'backoff-overdue-regular-tick',
    intervalMs: 80,
    failureBackoff: { baseMs: 40, maxMs: 40 },
    task: () => {
      starts.push(clock.now())
      if (starts.length === 1) throw new Error('expected delayed event loop regression failure')
    }
  })

  await clock.flush()
  clock.blockFor(130)
  await clock.flush()
  assert.deepEqual(
    starts,
    [clock.epochMs, clock.epochMs + 130],
    'event loop 阻塞至 retry 与 regular tick 都逾期时只能由 regular tick 执行一次'
  )
  await clock.advanceBy(30)
  assert.deepEqual(starts, [clock.epochMs, clock.epochMs + 130, clock.epochMs + 160])
  scheduler.stop()
}

async function verifyStopAndDrain(): Promise<void> {
  const clock = new FakeClock()
  const scheduler = new WorkerScheduler({ clock })
  let aborted = false
  scheduler.schedule({
    name: 'shutdown',
    intervalMs: 1_000,
    timeoutMs: 5,
    task: async ({ signal }) => {
      await new Promise<void>((resolve) => {
        signal.addEventListener('abort', () => {
          aborted = true
          clock.setTimeout(resolve, 10)
        }, { once: true })
      })
    }
  })
  await clock.flush()
  const unrefCountBeforeDrain = clock.unrefCount
  const draining = scheduler.stopAndDrain(200)
  assert.equal(clock.unrefCount, unrefCountBeforeDrain, 'drain 兜底 timer 必须保持引用，不能允许进程提前退出')
  await clock.advanceBy(6)
  const stopping = scheduler.snapshots()[0]
  assert.equal(stopping?.running, true)
  assert.equal(stopping?.timedOutCount, 0, '协作式停机必须取消运行级 timeout')
  assert.equal(stopping?.failureCount, 0, '协作式停机不得计为执行失败')
  await clock.advanceBy(4)
  const result = await draining
  assert.equal(aborted, true)
  assert.equal(result.drained, true)
  assert.equal(result.activeCount, 0)
  await clock.advanceBy(2_000)
  assert.equal(clock.pendingTimerCount, 0, '停止后不得复活周期 timer')
}

interface FakeTimer {
  id: number
  at: number
  callback: () => void
}

interface FakeTimerHandle {
  id: number
  unref: () => void
}

class FakeClock implements WorkerSchedulerClock {
  readonly epochMs = Date.UTC(2026, 6, 26, 0, 0, 0)
  private currentMs = this.epochMs
  private nextTimerId = 1
  private readonly timers = new Map<number, FakeTimer>()
  private readonly microtasks: Array<() => void> = []
  unrefCount = 0

  readonly now = (): number => this.currentMs
  readonly setTimeout = (callback: () => void, delayMs: number): FakeTimerHandle => {
    const id = this.nextTimerId++
    this.timers.set(id, { id, at: this.currentMs + Math.max(0, Math.trunc(delayMs)), callback })
    return {
      id,
      unref: () => { this.unrefCount += 1 }
    }
  }
  readonly clearTimeout = (timer: unknown): void => {
    if (timer && typeof timer === 'object' && 'id' in timer && typeof timer.id === 'number') {
      this.timers.delete(timer.id)
    }
  }
  readonly queueMicrotask = (callback: () => void): void => {
    this.microtasks.push(callback)
  }

  get pendingTimerCount(): number {
    return this.timers.size
  }

  async delay(delayMs: number): Promise<void> {
    await new Promise<void>((resolve) => {
      this.setTimeout(resolve, delayMs)
    })
  }

  blockFor(delayMs: number): void {
    this.currentMs += Math.max(0, Math.trunc(delayMs))
  }

  async advanceBy(delayMs: number): Promise<void> {
    const targetMs = this.currentMs + Math.max(0, Math.trunc(delayMs))
    for (;;) {
      await this.flushMicrotasks()
      const next = this.nextTimer(targetMs)
      if (!next) break
      this.currentMs = next.at
      this.timers.delete(next.id)
      next.callback()
      await Promise.resolve()
    }
    this.currentMs = targetMs
    await this.flush()
  }

  async flush(): Promise<void> {
    for (let iteration = 0; iteration < 100; iteration += 1) {
      await this.flushMicrotasks()
      const due = this.nextTimer(this.currentMs)
      if (due) {
        this.timers.delete(due.id)
        due.callback()
        await Promise.resolve()
        continue
      }
      await Promise.resolve()
      if (this.microtasks.length === 0 && !this.nextTimer(this.currentMs)) return
    }
    throw new Error('fake clock flush exceeded iteration limit')
  }

  private async flushMicrotasks(): Promise<void> {
    while (this.microtasks.length > 0) {
      const callback = this.microtasks.shift()
      callback?.()
      await Promise.resolve()
    }
    await Promise.resolve()
  }

  private nextTimer(maxAtMs: number): FakeTimer | undefined {
    return [...this.timers.values()]
      .filter((timer) => timer.at <= maxAtMs)
      .sort((left, right) => left.at - right.at || left.id - right.id)[0]
  }
}

await verifyCoalesceOneKeepsFixedRatePhase()
await verifyResourceLaneAndOverdue()
await verifyTimeoutKeepsLane()
await verifyFixedDelay()
await verifyFixedDelayLaneSkipSchedulesNextRun()
await verifyFailureBackoffKeepsRegularPhase()
await verifyFailureBackoffDeduplicatesCoincidentRegularTick()
await verifyFailureBackoffDefersToOverdueRegularTick()
await verifyStopAndDrain()

console.log('worker scheduler timing regression passed')
