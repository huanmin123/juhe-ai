import { errorLogFields, logger } from '../../shared/logger.js'
import { rfc3339InstantMilliseconds } from '../../shared/rfc3339.js'

export type WorkerScheduledJobScheduleMode = 'fixedRate' | 'fixedDelay'
export type WorkerScheduledJobOverlapPolicy = 'skip' | 'coalesceOne'

export interface WorkerScheduledJobTaskContext {
  signal: AbortSignal
  scheduledAt: string
  startedAt: string
  deadlineAt?: string
}

export interface WorkerScheduledJobFailureBackoffOptions {
  baseMs: number
  maxMs: number
}

export interface WorkerScheduledJobOptions {
  name: string
  intervalMs: number
  initialDelayMs?: number
  runImmediately?: boolean
  scheduleMode?: WorkerScheduledJobScheduleMode
  overlapPolicy?: WorkerScheduledJobOverlapPolicy
  timeoutMs?: number
  stablePhaseWindowMs?: number
  stablePhaseSeed?: string
  resourceLane?: string
  failureBackoff?: WorkerScheduledJobFailureBackoffOptions
  task: (context: WorkerScheduledJobTaskContext) => void | WorkerScheduledJobTaskResult | Promise<void | WorkerScheduledJobTaskResult>
}

export interface WorkerScheduledJobTaskResult {
  outcome: 'success' | 'partial' | 'skipped'
  warning?: string
  leaseState?: WorkerScheduledJobLeaseState
}

export type WorkerScheduledJobLeaseState = 'not_required' | 'acquired' | 'busy' | 'lost'
export type WorkerScheduledJobOutcome = 'success' | 'partial' | 'failure' | 'timeout' | 'skipped'

export interface WorkerSchedulerOptions {
  stableInstanceId?: string
  workerRole?: string
  clock?: WorkerSchedulerClock
  random?: () => number
}

export interface WorkerSchedulerClock {
  now: () => number
  setTimeout: (callback: () => void, delayMs: number) => unknown
  clearTimeout: (timer: unknown) => void
  queueMicrotask: (callback: () => void) => void
}

export interface WorkerScheduledJobRuntimeSnapshot {
  name: string
  intervalMs: number
  initialDelayMs: number
  stablePhaseOffsetMs: number
  scheduleMode: WorkerScheduledJobScheduleMode
  overlapPolicy: WorkerScheduledJobOverlapPolicy
  timeoutMs?: number
  resourceLane?: string
  running: boolean
  pending: boolean
  queuedForLane: boolean
  timedOut: boolean
  overdueMs: number
  nextRunAt?: string
  runningSince?: string
  lastScheduledAt?: string
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastWarningAt?: string
  lastWarning?: string
  lastSkipAt?: string
  lastSkipReason?: string
  lastOutcome?: WorkerScheduledJobOutcome
  leaseState?: WorkerScheduledJobLeaseState
  lastDurationMs?: number
  maxDurationMs?: number
  consecutiveFailureCount: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
  skippedCount: number
  taskSkippedCount: number
  coalescedCount: number
  timedOutCount: number
}

interface WorkerScheduledJobState {
  regularTimer?: unknown
  deferredTimer?: unknown
  runTimeoutTimer?: unknown
  intervalMs: number
  initialDelayMs: number
  stablePhaseOffsetMs: number
  scheduleMode: WorkerScheduledJobScheduleMode
  overlapPolicy: WorkerScheduledJobOverlapPolicy
  timeoutMs?: number
  resourceLane?: string
  failureBackoff?: WorkerScheduledJobFailureBackoffOptions
  task: WorkerScheduledJobOptions['task']
  running: boolean
  pending: boolean
  queuedForLane: boolean
  timedOut: boolean
  fixedRateAnchorAtMs?: number
  nextRegularRunAtMs?: number
  deferredRunAtMs?: number
  backoffUntilMs?: number
  activeScheduledAtMs?: number
  laneQueuedAtMs?: number
  stopped: boolean
  controller?: AbortController
  nextRunAt?: string
  runningSince?: string
  lastScheduledAt?: string
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastWarningAt?: string
  lastWarning?: string
  lastSkipAt?: string
  lastSkipReason?: string
  lastOutcome?: WorkerScheduledJobOutcome
  leaseState?: WorkerScheduledJobLeaseState
  lastDurationMs?: number
  maxDurationMs?: number
  consecutiveFailureCount: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
  skippedCount: number
  taskSkippedCount: number
  coalescedCount: number
  timedOutCount: number
}

interface WorkerScheduledJobLaneState {
  running: boolean
  queue: string[]
}

export class WorkerScheduler {
  private readonly jobs = new Map<string, WorkerScheduledJobState>()
  private readonly lanes = new Map<string, WorkerScheduledJobLaneState>()
  private readonly activeRuns = new Set<Promise<void>>()
  private readonly stableSeed: string
  private readonly clock: WorkerSchedulerClock
  private readonly random: () => number
  private stopped = false

  constructor(options: WorkerSchedulerOptions = {}) {
    this.stableSeed = `${options.stableInstanceId?.trim() || 'local'}:${options.workerRole?.trim() || 'worker'}`
    this.clock = options.clock ?? systemWorkerSchedulerClock
    this.random = options.random ?? Math.random
  }

  schedule(options: WorkerScheduledJobOptions): void {
    if (this.stopped || this.jobs.has(options.name)) {
      return
    }

    const intervalMs = normalizedPositiveMs(options.intervalMs, 1)
    const initialDelayMs = normalizedInitialDelayMs(options.initialDelayMs)
    const stablePhaseWindowMs = normalizedInitialDelayMs(options.stablePhaseWindowMs)
    const stablePhaseOffsetMs = stablePhaseWindowMs > 0
      ? stableScheduledJobOffsetMs(options.stablePhaseSeed ?? `${this.stableSeed}:${options.name}`, stablePhaseWindowMs)
      : 0
    const state: WorkerScheduledJobState = {
      regularTimer: undefined,
      deferredTimer: undefined,
      runTimeoutTimer: undefined,
      intervalMs,
      initialDelayMs,
      stablePhaseOffsetMs,
      scheduleMode: options.scheduleMode ?? 'fixedRate',
      overlapPolicy: options.overlapPolicy ?? 'skip',
      timeoutMs: normalizedOptionalPositiveMs(options.timeoutMs),
      resourceLane: normalizedOptionalName(options.resourceLane),
      failureBackoff: normalizedFailureBackoff(options.failureBackoff),
      task: options.task,
      running: false,
      pending: false,
      queuedForLane: false,
      timedOut: false,
      stopped: false,
      consecutiveFailureCount: 0,
      runCount: 0,
      successCount: 0,
      failureCount: 0,
      partialCount: 0,
      skippedCount: 0,
      taskSkippedCount: 0,
      coalescedCount: 0,
      timedOutCount: 0
    }
    this.jobs.set(options.name, state)

    const runImmediately = options.runImmediately !== false
    const firstDelayMs = initialDelayMs + stablePhaseOffsetMs + (runImmediately ? 0 : intervalMs)
    const firstRunAtMs = this.clock.now() + firstDelayMs
    if (state.scheduleMode === 'fixedRate') state.fixedRateAnchorAtMs = firstRunAtMs
    if (firstDelayMs === 0) {
      state.lastScheduledAt = new Date(firstRunAtMs).toISOString()
      if (state.scheduleMode === 'fixedRate') {
        this.scheduleRegularTimer(options.name, state, firstRunAtMs + state.intervalMs)
      }
      this.triggerRun(options.name, state)
      return
    }
    this.scheduleRegularTimer(options.name, state, firstRunAtMs)
  }

  stop(): void {
    this.requestStop()
    this.jobs.clear()
    this.lanes.clear()
  }

  async stopAndDrain(timeoutMs = 10_000): Promise<{ drained: boolean; activeCount: number }> {
    this.requestStop()
    const normalizedTimeoutMs = normalizedPositiveMs(timeoutMs, 1)
    const active = [...this.activeRuns]
    if (active.length === 0) {
      this.jobs.clear()
      this.lanes.clear()
      return { drained: true, activeCount: 0 }
    }

    let timeout: unknown
    const drained = await Promise.race([
      Promise.allSettled(active).then(() => true),
      new Promise<boolean>((resolve) => {
        timeout = this.clock.setTimeout(() => resolve(false), normalizedTimeoutMs)
      })
    ])
    if (timeout) this.clock.clearTimeout(timeout)
    const activeCount = this.activeRuns.size
    this.jobs.clear()
    this.lanes.clear()
    return { drained, activeCount }
  }

  snapshots(): WorkerScheduledJobRuntimeSnapshot[] {
    return [...this.jobs.entries()]
      .map(([name, state]) => ({
        name,
        intervalMs: state.intervalMs,
        initialDelayMs: state.initialDelayMs,
        stablePhaseOffsetMs: state.stablePhaseOffsetMs,
        scheduleMode: state.scheduleMode,
        overlapPolicy: state.overlapPolicy,
        timeoutMs: state.timeoutMs,
        resourceLane: state.resourceLane,
        running: state.running,
        pending: state.pending,
        queuedForLane: state.queuedForLane,
        timedOut: state.timedOut,
        overdueMs: currentOverdueMs(state, this.clock.now()),
        nextRunAt: state.nextRunAt,
        runningSince: state.runningSince,
        lastScheduledAt: state.lastScheduledAt,
        lastStartedAt: state.lastStartedAt,
        lastFinishedAt: state.lastFinishedAt,
        lastSuccessAt: state.lastSuccessAt,
        lastErrorAt: state.lastErrorAt,
        lastError: state.lastError,
        lastWarningAt: state.lastWarningAt,
        lastWarning: state.lastWarning,
        lastSkipAt: state.lastSkipAt,
        lastSkipReason: state.lastSkipReason,
        lastOutcome: state.lastOutcome,
        leaseState: state.leaseState,
        lastDurationMs: state.lastDurationMs,
        maxDurationMs: state.maxDurationMs,
        consecutiveFailureCount: state.consecutiveFailureCount,
        runCount: state.runCount,
        successCount: state.successCount,
        failureCount: state.failureCount,
        partialCount: state.partialCount,
        skippedCount: state.skippedCount,
        taskSkippedCount: state.taskSkippedCount,
        coalescedCount: state.coalescedCount,
        timedOutCount: state.timedOutCount
      }))
      .sort((left, right) => left.name.localeCompare(right.name))
  }

  private requestStop(): void {
    if (this.stopped) return
    this.stopped = true
    for (const state of this.jobs.values()) {
      state.stopped = true
      state.pending = false
      state.queuedForLane = false
      state.laneQueuedAtMs = undefined
      state.nextRunAt = undefined
      if (state.regularTimer) this.clock.clearTimeout(state.regularTimer)
      if (state.deferredTimer) this.clock.clearTimeout(state.deferredTimer)
      if (state.runTimeoutTimer !== undefined) this.clock.clearTimeout(state.runTimeoutTimer)
      state.regularTimer = undefined
      state.deferredTimer = undefined
      state.runTimeoutTimer = undefined
      state.nextRegularRunAtMs = undefined
      state.deferredRunAtMs = undefined
      state.controller?.abort(new Error('后台任务调度器正在停止'))
    }
    for (const lane of this.lanes.values()) lane.queue.length = 0
  }

  private scheduleRegularTimer(name: string, state: WorkerScheduledJobState, targetAtMs: number): void {
    if (this.stopped || state.stopped) return
    if (state.regularTimer) this.clock.clearTimeout(state.regularTimer)
    const scheduledAtMs = Math.max(this.clock.now(), Math.trunc(targetAtMs))
    state.nextRegularRunAtMs = scheduledAtMs
    this.updateNextRunAt(state)
    state.regularTimer = this.clock.setTimeout(() => {
      state.regularTimer = undefined
      state.nextRegularRunAtMs = undefined
      state.lastScheduledAt = new Date(scheduledAtMs).toISOString()
      if (state.scheduleMode === 'fixedRate') {
        const nextScheduledAtMs = nextFixedRateTargetMs(scheduledAtMs, state.intervalMs, this.clock.now())
        this.scheduleRegularTimer(name, state, nextScheduledAtMs)
      }
      if (state.backoffUntilMs !== undefined && this.clock.now() < state.backoffUntilMs) {
        state.skippedCount += 1
        state.lastSkipAt = this.nowIso()
        state.lastSkipReason = 'failure_backoff'
        this.updateNextRunAt(state)
        return
      }
      this.clearDueDeferredRun(state, this.clock.now())
      this.triggerRun(name, state)
    }, Math.max(0, scheduledAtMs - this.clock.now()))
    unrefTimer(state.regularTimer)
  }

  private scheduleDeferredRun(name: string, state: WorkerScheduledJobState, delayMs: number): void {
    if (this.stopped || state.stopped) return
    if (state.deferredTimer) this.clock.clearTimeout(state.deferredTimer)
    const runAtMs = this.clock.now() + normalizedInitialDelayMs(delayMs)
    if (state.scheduleMode === 'fixedRate' && state.nextRegularRunAtMs === runAtMs) {
      state.deferredTimer = undefined
      state.deferredRunAtMs = undefined
      this.updateNextRunAt(state)
      return
    }
    state.deferredRunAtMs = runAtMs
    this.updateNextRunAt(state)
    state.deferredTimer = this.clock.setTimeout(() => {
      state.deferredTimer = undefined
      state.deferredRunAtMs = undefined
      this.updateNextRunAt(state)
      if (state.scheduleMode === 'fixedRate'
        && state.nextRegularRunAtMs !== undefined
        && state.nextRegularRunAtMs <= this.clock.now()) {
        return
      }
      state.lastScheduledAt = new Date(runAtMs).toISOString()
      this.triggerRun(name, state)
    }, Math.max(0, runAtMs - this.clock.now()))
    unrefTimer(state.deferredTimer)
  }

  private updateNextRunAt(state: WorkerScheduledJobState): void {
    const visibleRegularRunAtMs = state.backoffUntilMs !== undefined
      && state.nextRegularRunAtMs !== undefined
      && state.nextRegularRunAtMs < state.backoffUntilMs
      ? undefined
      : state.nextRegularRunAtMs
    const candidates = [visibleRegularRunAtMs, state.deferredRunAtMs]
      .filter((value): value is number => value !== undefined)
    state.nextRunAt = candidates.length > 0
      ? new Date(Math.min(...candidates)).toISOString()
      : undefined
  }

  private clearDueDeferredRun(state: WorkerScheduledJobState, nowMs: number): void {
    if (state.deferredTimer === undefined || state.deferredRunAtMs === undefined || state.deferredRunAtMs > nowMs) return
    this.clock.clearTimeout(state.deferredTimer)
    state.deferredTimer = undefined
    state.deferredRunAtMs = undefined
    this.updateNextRunAt(state)
  }

  private triggerRun(name: string, state: WorkerScheduledJobState): void {
    if (this.stopped || state.stopped) return
    if (state.running || state.queuedForLane) {
      this.handleOverlap(name, state, state.running ? 'running' : 'resource_lane_queue')
      return
    }
    if (!this.acquireLane(name, state)) {
      if (state.scheduleMode === 'fixedDelay' && state.overlapPolicy === 'skip') {
        this.scheduleRegularTimer(name, state, this.clock.now() + state.intervalMs)
      }
      return
    }
    this.startRun(name, state)
  }

  private startRun(name: string, state: WorkerScheduledJobState): void {
    if (this.stopped || state.stopped) {
      this.releaseLane(state)
      return
    }
    state.activeScheduledAtMs = state.laneQueuedAtMs ?? parsedTimestampMs(state.lastScheduledAt) ?? this.clock.now()
    state.laneQueuedAtMs = undefined
    const run = this.runJob(name, state)
      .finally(() => {
        this.activeRuns.delete(run)
      })
    this.activeRuns.add(run)
  }

  private handleOverlap(name: string, state: WorkerScheduledJobState, reason: string): void {
    const now = this.nowIso()
    if (state.overlapPolicy === 'coalesceOne') {
      let newlyCoalesced = false
      if (!state.pending) {
        state.pending = true
        state.coalescedCount += 1
        newlyCoalesced = true
      }
      state.lastSkipAt = now
      state.lastSkipReason = `${reason}:coalesced`
      if (newlyCoalesced) {
        logger.warn({
          event: 'background_job_coalesced',
          jobName: name,
          reason
        }, '后台任务本轮已合并，上一次运行结束后最多补跑一次')
      }
      return
    }
    state.skippedCount += 1
    state.lastSkipAt = now
    state.lastSkipReason = reason
    state.lastOutcome = 'skipped'
    logger.warn({
      event: 'background_job_skipped_running',
      jobName: name,
      reason
    }, '后台任务已跳过，上一次运行或同资源任务仍未结束')
  }

  private acquireLane(name: string, state: WorkerScheduledJobState): boolean {
    const laneName = state.resourceLane
    if (!laneName) return true
    const lane = this.lanes.get(laneName) ?? { running: false, queue: [] }
    this.lanes.set(laneName, lane)
    if (!lane.running) {
      lane.running = true
      return true
    }
    if (state.overlapPolicy === 'skip') {
      state.skippedCount += 1
      state.lastSkipAt = this.nowIso()
      state.lastSkipReason = `resource_lane_busy:${laneName}`
      state.lastOutcome = 'skipped'
      return false
    }
    if (!state.queuedForLane) {
      state.queuedForLane = true
      state.laneQueuedAtMs = parsedTimestampMs(state.lastScheduledAt) ?? this.clock.now()
      state.lastSkipAt = this.nowIso()
      state.lastSkipReason = `resource_lane_busy:${laneName}`
      lane.queue.push(name)
    }
    return false
  }

  private releaseLane(state: WorkerScheduledJobState): void {
    const laneName = state.resourceLane
    if (!laneName) return
    const lane = this.lanes.get(laneName)
    if (!lane) return
    lane.running = false
    while (lane.queue.length > 0 && !this.stopped) {
      const nextName = lane.queue.shift()
      if (!nextName) continue
      const nextState = this.jobs.get(nextName)
      if (!nextState || nextState.stopped || nextState.running) continue
      nextState.queuedForLane = false
      lane.running = true
      const dequeuedName = nextName
      this.clock.queueMicrotask(() => this.startRun(dequeuedName, nextState))
      return
    }
  }

  private async runJob(name: string, state: WorkerScheduledJobState): Promise<void> {
    state.running = true
    state.queuedForLane = false
    state.timedOut = false
    state.runCount += 1
    state.lastStartedAt = this.nowIso()
    state.runningSince = state.lastStartedAt
    const startedAtMs = this.clock.now()
    const controller = new AbortController()
    state.controller = controller
    let timeout: unknown
    let timedOut = false
    let failureRecorded = false
    if (state.timeoutMs !== undefined) {
      timeout = this.clock.setTimeout(() => {
        if (state.runTimeoutTimer === timeout) state.runTimeoutTimer = undefined
        if (this.stopped || state.stopped) return
        timedOut = true
        state.timedOut = true
        state.timedOutCount += 1
        failureRecorded = true
        const timeoutError = new Error(`后台任务执行超过 ${state.timeoutMs}ms`)
        this.recordFailure(state, timeoutError)
        controller.abort(timeoutError)
        logger.error(errorLogFields(timeoutError, {
          event: 'background_job_timed_out',
          jobName: name
        }), '后台任务执行超时并已请求取消，底层任务结束前仍保持占用')
      }, state.timeoutMs)
      state.runTimeoutTimer = timeout
      unrefTimer(timeout)
    }

    let failed = false
    try {
      const result = await state.task({
        signal: controller.signal,
        scheduledAt: new Date(state.activeScheduledAtMs ?? startedAtMs).toISOString(),
        startedAt: state.lastStartedAt,
        ...(state.timeoutMs !== undefined
          ? { deadlineAt: new Date(startedAtMs + state.timeoutMs).toISOString() }
          : {})
      })
      if (state.stopped && controller.signal.aborted && !timedOut) {
        state.taskSkippedCount += 1
        state.lastOutcome = 'skipped'
        state.lastSkipAt = this.nowIso()
        state.lastSkipReason = 'scheduler_stopped'
      } else if (timedOut) {
        failed = true
      } else if (result?.outcome === 'partial') {
        state.partialCount += 1
        state.consecutiveFailureCount = 0
        state.backoffUntilMs = undefined
        state.lastOutcome = 'partial'
        state.leaseState = result.leaseState
        state.lastWarningAt = this.nowIso()
        state.lastWarning = result.warning ?? '后台任务部分完成'
        state.lastError = undefined
      } else if (result?.outcome === 'skipped') {
        state.taskSkippedCount += 1
        state.consecutiveFailureCount = 0
        state.backoffUntilMs = undefined
        state.lastOutcome = 'skipped'
        state.leaseState = result.leaseState
        state.lastSkipAt = this.nowIso()
        state.lastSkipReason = result.warning ?? 'task_skipped'
        state.lastError = undefined
      } else {
        state.successCount += 1
        state.consecutiveFailureCount = 0
        state.backoffUntilMs = undefined
        state.lastOutcome = 'success'
        state.leaseState = result?.leaseState
        state.lastSuccessAt = this.nowIso()
        state.lastError = undefined
        state.lastWarning = undefined
      }
    } catch (error) {
      if (state.stopped && controller.signal.aborted && !timedOut) {
        state.taskSkippedCount += 1
        state.lastOutcome = 'skipped'
        state.lastSkipAt = this.nowIso()
        state.lastSkipReason = 'scheduler_stopped'
      } else {
        failed = true
        if (!failureRecorded) this.recordFailure(state, error)
      }
      if (!timedOut && !state.stopped) {
        logger.error(errorLogFields(error, {
          event: 'background_job_failed',
          jobName: name
        }), '后台任务执行失败')
      }
    } finally {
      if (timeout !== undefined) this.clock.clearTimeout(timeout)
      if (state.runTimeoutTimer === timeout) state.runTimeoutTimer = undefined
      state.lastDurationMs = Math.max(0, this.clock.now() - startedAtMs)
      state.maxDurationMs = state.maxDurationMs === undefined
        ? state.lastDurationMs
        : Math.max(state.maxDurationMs, state.lastDurationMs)
      state.lastFinishedAt = this.nowIso()
      state.runningSince = undefined
      state.running = false
      state.activeScheduledAtMs = undefined
      state.timedOut = false
      state.controller = undefined
      this.releaseLane(state)

      if (this.stopped || state.stopped) return
      if (failed && state.failureBackoff) {
        const backoffMs = failureBackoffDelayMs(state.failureBackoff, state.consecutiveFailureCount, this.random)
        state.backoffUntilMs = this.clock.now() + backoffMs
        state.pending = false
        this.scheduleDeferredRun(name, state, backoffMs)
        return
      }
      if (state.pending && !state.queuedForLane) {
        state.pending = false
        this.scheduleDeferredRun(name, state, 0)
        return
      }
      if (state.scheduleMode === 'fixedDelay') {
        this.scheduleRegularTimer(name, state, this.clock.now() + state.intervalMs)
      }
    }
  }

  private recordFailure(state: WorkerScheduledJobState, error: unknown): void {
    state.failureCount += 1
    state.consecutiveFailureCount += 1
    state.lastOutcome = state.timedOut ? 'timeout' : 'failure'
    state.lastErrorAt = this.nowIso()
    state.lastError = error instanceof Error ? error.message : String(error)
    state.lastWarning = undefined
  }

  private nowIso(): string {
    return new Date(this.clock.now()).toISOString()
  }
}

export function stableScheduledJobOffsetMs(seed: string, windowMs: number): number {
  const normalizedWindowMs = normalizedInitialDelayMs(windowMs)
  if (normalizedWindowMs <= 0) return 0
  let hash = 2166136261
  for (const character of seed) {
    hash ^= character.charCodeAt(0)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0) % normalizedWindowMs
}

export function failureBackoffDelayMs(options: WorkerScheduledJobFailureBackoffOptions, consecutiveFailureCount: number, random = Math.random): number {
  const normalized = normalizedFailureBackoff(options)
  if (!normalized) return 0
  const exponent = Math.max(0, Math.min(30, Math.trunc(consecutiveFailureCount) - 1))
  const ceiling = Math.min(normalized.maxMs, normalized.baseMs * (2 ** exponent))
  const randomValue = Math.min(1, Math.max(0, Number(random()) || 0))
  if (randomValue >= 1) return ceiling
  return Math.max(0, Math.trunc(randomValue * (ceiling + 1)))
}

function nextFixedRateTargetMs(previousScheduledAtMs: number, intervalMs: number, nowMs: number): number {
  const elapsedMs = Math.max(0, nowMs - previousScheduledAtMs)
  const elapsedIntervals = Math.floor(elapsedMs / intervalMs) + 1
  return previousScheduledAtMs + elapsedIntervals * intervalMs
}

function currentOverdueMs(state: WorkerScheduledJobState, nowMs: number): number {
  if (state.running && state.activeScheduledAtMs !== undefined) {
    const startedAtMs = parsedTimestampMs(state.lastStartedAt) ?? nowMs
    return Math.max(0, startedAtMs - state.activeScheduledAtMs)
  }
  if (state.queuedForLane && state.laneQueuedAtMs !== undefined) {
    return Math.max(0, nowMs - state.laneQueuedAtMs)
  }
  if (state.pending) {
    const scheduledAtMs = parsedTimestampMs(state.lastScheduledAt)
    return scheduledAtMs === undefined ? 0 : Math.max(0, nowMs - scheduledAtMs)
  }
  return 0
}

function parsedTimestampMs(value: string | undefined): number | undefined {
  if (value === undefined) return undefined
  const parsed = rfc3339InstantMilliseconds(value)
  if (parsed === undefined) throw new Error('后台任务调度时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  return parsed
}

function normalizedFailureBackoff(value: WorkerScheduledJobFailureBackoffOptions | undefined): WorkerScheduledJobFailureBackoffOptions | undefined {
  if (!value) return undefined
  const baseMs = normalizedPositiveMs(value.baseMs, 1)
  const maxMs = Math.max(baseMs, normalizedPositiveMs(value.maxMs, baseMs))
  return { baseMs, maxMs }
}

function normalizedInitialDelayMs(value: number | undefined): number {
  const number = Number(value ?? 0)
  return Number.isFinite(number) ? Math.max(0, Math.trunc(number)) : 0
}

function normalizedOptionalPositiveMs(value: number | undefined): number | undefined {
  if (value === undefined) return undefined
  return normalizedPositiveMs(value, 1)
}

function normalizedPositiveMs(value: number, fallback: number): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.max(1, Math.trunc(number)) : fallback
}

function normalizedOptionalName(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

const systemWorkerSchedulerClock: WorkerSchedulerClock = {
  now: Date.now,
  setTimeout: (callback, delayMs) => setTimeout(callback, delayMs),
  clearTimeout: (timer) => clearTimeout(timer as NodeJS.Timeout),
  queueMicrotask
}

function unrefTimer(timer: unknown): void {
  if (timer && typeof timer === 'object' && 'unref' in timer && typeof timer.unref === 'function') {
    timer.unref()
  }
}
