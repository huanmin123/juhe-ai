import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import {
  publishNextAccountHealthJobsInputFromBusinessOutbox
} from './account-health-jobs-input-publisher.service.js'

let stopping = true
let runPromise: Promise<void> | undefined
let wakeTimer: NodeJS.Timeout | undefined

// The DB service is the only process allowed to run this loop for SQLite.
// It publishes durable, signed inputs; it never probes, calls Go/Gateway, or
// writes the account health state owned by the projector.
export function startAccountHealthJobsInputPublisherRuntime(): void {
  if (runtimeConfig.processRole !== 'db-service' || !runtimeConfig.accountHealthJobs.inputPublisherEnabled || runPromise) return
  stopping = false
  runPromise = runPublisherLoop().finally(() => {
    runPromise = undefined
  })
}

export async function stopAccountHealthJobsInputPublisherRuntime(): Promise<void> {
  stopping = true
  if (wakeTimer) {
    clearTimeout(wakeTimer)
    wakeTimer = undefined
  }
  await runPromise
}

async function runPublisherLoop(): Promise<void> {
  while (!stopping) {
    let disposition: string = 'idle'
    try {
      disposition = await publishNextAccountHealthJobsInputFromBusinessOutbox()
    } catch (error) {
      logger.error({
        event: 'account_health_jobs_input_publisher_failed',
        error: error instanceof Error ? error.name : 'unknown'
      }, 'J1 输入发布器执行失败，将按轮询周期重试')
    }
    if (stopping) break
    const delayMs = disposition === 'idle' ? runtimeConfig.accountHealthJobs.inputPublisherPollMs : Math.min(250, runtimeConfig.accountHealthJobs.inputPublisherPollMs)
    await waitForNextPublisherTick(delayMs)
  }
}

function waitForNextPublisherTick(delayMs: number): Promise<void> {
  return new Promise((resolve) => {
    wakeTimer = setTimeout(() => {
      wakeTimer = undefined
      resolve()
    }, delayMs)
  })
}
