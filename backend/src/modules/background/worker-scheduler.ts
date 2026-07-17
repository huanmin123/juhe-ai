import { errorLogFields, logger } from '../../shared/logger.js'

interface WorkerScheduledJobOptions {
  name: string
  intervalMs: number
  initialDelayMs?: number
  runImmediately?: boolean
  task: () => void | WorkerScheduledJobTaskResult | Promise<void | WorkerScheduledJobTaskResult>
}

export interface WorkerScheduledJobTaskResult {
  outcome: 'success' | 'partial'
  warning?: string
}

export interface WorkerScheduledJobRuntimeSnapshot {
  name: string
  intervalMs: number
  initialDelayMs: number
  running: boolean
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastWarningAt?: string
  lastWarning?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
  skippedCount: number
}

interface WorkerScheduledJobState {
  timer?: NodeJS.Timeout
  intervalMs: number
  initialDelayMs: number
  running: boolean
  stopped: boolean
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastWarningAt?: string
  lastWarning?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
  skippedCount: number
}

export class WorkerScheduler {
  private readonly jobs = new Map<string, WorkerScheduledJobState>()

  schedule(options: WorkerScheduledJobOptions): void {
    if (this.jobs.has(options.name)) {
      return
    }

    const state = {
      timer: undefined,
      intervalMs: options.intervalMs,
      initialDelayMs: normalizedInitialDelayMs(options.initialDelayMs),
      running: false,
      stopped: false,
      runCount: 0,
      successCount: 0,
      failureCount: 0,
      partialCount: 0,
      skippedCount: 0
    }
    this.jobs.set(options.name, state)
    this.startJobTimer(options.name, options.task, state, options.runImmediately !== false)
  }

  private startJobTimer(
    name: string,
    task: () => void | WorkerScheduledJobTaskResult | Promise<void | WorkerScheduledJobTaskResult>,
    state: WorkerScheduledJobState,
    runImmediately: boolean
  ): void {
    const startInterval = (): void => {
      if (state.stopped) return
      state.timer = setInterval(() => { void this.runJob(name, task) }, state.intervalMs)
      state.timer.unref()
      if (runImmediately) {
        void this.runJob(name, task)
      }
    }

    if (state.initialDelayMs > 0) {
      state.timer = setTimeout(startInterval, state.initialDelayMs)
      state.timer.unref()
      return
    }

    startInterval()
  }

  stop(): void {
    for (const state of this.jobs.values()) {
      state.stopped = true
      if (state.timer) {
        clearTimeout(state.timer)
      }
    }
    this.jobs.clear()
  }

  snapshots(): WorkerScheduledJobRuntimeSnapshot[] {
    return [...this.jobs.entries()]
      .map(([name, state]) => ({
        name,
        intervalMs: state.intervalMs,
        initialDelayMs: state.initialDelayMs,
        running: state.running,
        lastStartedAt: state.lastStartedAt,
        lastFinishedAt: state.lastFinishedAt,
        lastSuccessAt: state.lastSuccessAt,
        lastErrorAt: state.lastErrorAt,
        lastError: state.lastError,
        lastWarningAt: state.lastWarningAt,
        lastWarning: state.lastWarning,
        lastDurationMs: state.lastDurationMs,
        maxDurationMs: state.maxDurationMs,
        runCount: state.runCount,
        successCount: state.successCount,
        failureCount: state.failureCount,
        partialCount: state.partialCount,
        skippedCount: state.skippedCount
      }))
      .sort((left, right) => left.name.localeCompare(right.name))
  }

  private async runJob(name: string, task: () => void | WorkerScheduledJobTaskResult | Promise<void | WorkerScheduledJobTaskResult>): Promise<void> {
    const state = this.jobs.get(name)
    if (!state || state.stopped) {
      return
    }

    if (state.running) {
      state.skippedCount += 1
      logger.warn({
        event: 'background_job_skipped_running',
        jobName: name
      }, '后台任务已跳过，上一次运行仍未结束')
      return
    }

    state.running = true
    state.runCount += 1
    state.lastStartedAt = new Date().toISOString()
    const startedAtMs = Date.now()
    try {
      const result = await task()
      if (result?.outcome === 'partial') {
        state.partialCount += 1
        state.lastWarningAt = new Date().toISOString()
        state.lastWarning = result.warning ?? '后台任务部分完成'
        state.lastError = undefined
      } else {
        state.successCount += 1
        state.lastSuccessAt = new Date().toISOString()
        state.lastError = undefined
        state.lastWarning = undefined
      }
    } catch (error) {
      state.failureCount += 1
      state.lastErrorAt = new Date().toISOString()
      state.lastError = error instanceof Error ? error.message : String(error)
      state.lastWarning = undefined
      logger.error(errorLogFields(error, {
        event: 'background_job_failed',
        jobName: name
      }), '后台任务执行失败')
    } finally {
      state.lastDurationMs = Math.max(0, Date.now() - startedAtMs)
      state.maxDurationMs = state.maxDurationMs === undefined
        ? state.lastDurationMs
        : Math.max(state.maxDurationMs, state.lastDurationMs)
      state.lastFinishedAt = new Date().toISOString()
      state.running = false
    }
  }
}

function normalizedInitialDelayMs(value: number | undefined): number {
  const number = Number(value ?? 0)
  return Number.isFinite(number) ? Math.max(0, Math.trunc(number)) : 0
}
