import { errorLogFields, logger } from '../../shared/logger.js'

interface WorkerScheduledJobOptions {
  name: string
  intervalMs: number
  runImmediately?: boolean
  task: () => void | Promise<void>
}

export interface WorkerScheduledJobRuntimeSnapshot {
  name: string
  intervalMs: number
  running: boolean
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  skippedCount: number
}

interface WorkerScheduledJobState {
  timer: NodeJS.Timeout
  intervalMs: number
  running: boolean
  stopped: boolean
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  skippedCount: number
}

export class WorkerScheduler {
  private readonly jobs = new Map<string, WorkerScheduledJobState>()

  schedule(options: WorkerScheduledJobOptions): void {
    if (this.jobs.has(options.name)) {
      return
    }

    const state = {
      timer: setInterval(() => { void this.runJob(options.name, options.task) }, options.intervalMs),
      intervalMs: options.intervalMs,
      running: false,
      stopped: false,
      runCount: 0,
      successCount: 0,
      failureCount: 0,
      skippedCount: 0
    }
    state.timer.unref()
    this.jobs.set(options.name, state)

    if (options.runImmediately !== false) {
      void this.runJob(options.name, options.task)
    }
  }

  stop(): void {
    for (const state of this.jobs.values()) {
      state.stopped = true
      clearInterval(state.timer)
    }
    this.jobs.clear()
  }

  snapshots(): WorkerScheduledJobRuntimeSnapshot[] {
    return [...this.jobs.entries()]
      .map(([name, state]) => ({
        name,
        intervalMs: state.intervalMs,
        running: state.running,
        lastStartedAt: state.lastStartedAt,
        lastFinishedAt: state.lastFinishedAt,
        lastSuccessAt: state.lastSuccessAt,
        lastErrorAt: state.lastErrorAt,
        lastError: state.lastError,
        lastDurationMs: state.lastDurationMs,
        maxDurationMs: state.maxDurationMs,
        runCount: state.runCount,
        successCount: state.successCount,
        failureCount: state.failureCount,
        skippedCount: state.skippedCount
      }))
      .sort((left, right) => left.name.localeCompare(right.name))
  }

  private async runJob(name: string, task: () => void | Promise<void>): Promise<void> {
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
      await task()
      state.successCount += 1
      state.lastSuccessAt = new Date().toISOString()
      state.lastError = undefined
      state.lastErrorAt = undefined
    } catch (error) {
      state.failureCount += 1
      state.lastErrorAt = new Date().toISOString()
      state.lastError = error instanceof Error ? error.message : String(error)
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
