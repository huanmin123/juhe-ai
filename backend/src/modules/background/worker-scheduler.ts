import { errorLogFields, logger } from '../../shared/logger.js'

interface WorkerScheduledJobOptions {
  name: string
  intervalMs: number
  runImmediately?: boolean
  task: () => void | Promise<void>
}

export class WorkerScheduler {
  private readonly jobs = new Map<string, { timer: NodeJS.Timeout; running: boolean; stopped: boolean }>()

  schedule(options: WorkerScheduledJobOptions): void {
    if (this.jobs.has(options.name)) {
      return
    }

    const state = {
      timer: setInterval(() => { void this.runJob(options.name, options.task) }, options.intervalMs),
      running: false,
      stopped: false
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

  private async runJob(name: string, task: () => void | Promise<void>): Promise<void> {
    const state = this.jobs.get(name)
    if (!state || state.stopped) {
      return
    }

    if (state.running) {
      logger.warn({
        event: 'background_job_skipped_running',
        jobName: name
      }, '后台任务已跳过，上一次运行仍未结束')
      return
    }

    state.running = true
    try {
      await task()
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'background_job_failed',
        jobName: name
      }), '后台任务执行失败')
    } finally {
      state.running = false
    }
  }
}
