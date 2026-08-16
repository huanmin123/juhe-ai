import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import {
  listAccountHealthJobsOutcomes,
  type AccountHealthJobsOutcomeCursor,
  type AccountHealthJobsStoreSource
} from '../../../storage/account-health-jobs-outcome.repository.js'
import { settleAccountHealthJobsSourceFenceOutcome } from './account-health-jobs-source-fence.consumer.js'

let stopping = true
let runPromise: Promise<void> | undefined
let wakeTimer: NodeJS.Timeout | undefined
let cursor: AccountHealthJobsOutcomeCursor | undefined

// The Gateway owns only the source-fence runtime settlement. It reads the Go
// outcome store and never writes business accounts or dispatches a probe.
export function startAccountHealthJobsSourceFenceConsumerRuntime(): void {
  if (!isGatewayConsumerEnabled() || runPromise) return
  stopping = false
  cursor = {
    observedAt: new Date(Date.now() - runtimeConfig.accountHealthJobs.sourceFenceConsumerLookbackMs).toISOString(),
    outcomeId: '!'
  }
  runPromise = runConsumerLoop().finally(() => {
    runPromise = undefined
    cursor = undefined
  })
}

export async function stopAccountHealthJobsSourceFenceConsumerRuntime(): Promise<void> {
  stopping = true
  if (wakeTimer) {
    clearTimeout(wakeTimer)
    wakeTimer = undefined
  }
  await runPromise
}

async function runConsumerLoop(): Promise<void> {
  const source = outcomeStoreSource()
  while (!stopping) {
    try {
      const outcomes = await listAccountHealthJobsOutcomes(source, {
        ...(cursor ? { after: cursor } : {}),
        limit: runtimeConfig.accountHealthJobs.projectionBatchSize
      })
      for (const outcome of outcomes) {
        if (stopping) break
        await settleAccountHealthJobsSourceFenceOutcome(outcome)
        cursor = { observedAt: outcome.observed_at, outcomeId: outcome.outcome_id }
      }
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'account_health_jobs_source_fence_consumer_failed'
      }), 'J1 source-fence outcome 消费失败，将保留运行态游标重试')
    }
    if (stopping) break
    await waitForNextTick(runtimeConfig.accountHealthJobs.sourceFenceConsumerPollMs)
  }
}

function isGatewayConsumerEnabled(): boolean {
  if (runtimeConfig.processRole !== 'server'
    || runtimeConfig.accountHealthJobs.owner !== 'go'
    || !runtimeConfig.accountHealthJobs.sourceFenceConsumerEnabled) return false
  return runtimeConfig.runtimeMode !== 'performance' || runtimeConfig.performanceNodeRole === 'gateway'
}

function outcomeStoreSource(): AccountHealthJobsStoreSource {
  if (runtimeConfig.databaseDriver === 'sqlite') {
    const path = runtimeConfig.accountHealthJobs.outcomeSqlitePath?.trim()
    if (!path) throw new Error('J1 SQLite source-fence consumer 必须设置 jobs outcome SQLite 路径')
    return { mode: 'sqlite', databasePath: path }
  }
  const postgresUrl = runtimeConfig.accountHealthJobs.outcomePostgresUrl?.trim()
  if (!postgresUrl) throw new Error('J1 PG source-fence consumer 必须设置 jobs outcome PostgreSQL URL')
  return { mode: 'postgres', postgresUrl }
}

function waitForNextTick(delayMs: number): Promise<void> {
  return new Promise((resolve) => {
    wakeTimer = setTimeout(() => {
      wakeTimer = undefined
      resolve()
    }, delayMs)
  })
}
