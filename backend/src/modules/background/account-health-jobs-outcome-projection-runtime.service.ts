import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { passiveScheduleDelayMs } from '../../shared/passive-schedule-jitter.js'
import {
  currentAccountHealthProjectionCursor,
  currentAccountHealthProjectionCursorAsync,
  advanceAccountHealthProjectionCursor,
  advanceAccountHealthProjectionCursorAsync
} from '../../storage/account-health-projection-cursor.repository.js'
import {
  projectAccountHealthJobsOutcome,
  projectAccountHealthJobsOutcomeAsync
} from '../../storage/account-health-projection.repository.js'
import { getBusinessDatabase } from '../../storage/database.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { drainAccountHealthJobsOutcomes } from './account-health-jobs-outcome-drain.service.js'
import type { AccountHealthJobsStoreSource } from '../../storage/account-health-jobs-outcome.repository.js'

const consumerKey = 'juhe-ai-account-health-jobs-projector-v1'

let stopping = true
let runPromise: Promise<void> | undefined
let wakeTimer: NodeJS.Timeout | undefined
let wakeResolver: (() => void) | undefined

// DB-service owns the business projection. The jobs store is read-only here;
// Go remains the only writer of outcomes/current state/leases.
export function startAccountHealthJobsOutcomeProjectionRuntime(): void {
  if (runtimeConfig.processRole !== 'db-service'
    || runtimeConfig.blueGreenOwnerMode !== 'active'
    || runtimeConfig.accountHealthJobs.owner !== 'go'
    || !runtimeConfig.accountHealthJobs.projectionEnabled
    || runPromise) return
  stopping = false
  runPromise = runProjectionLoop().finally(() => {
    runPromise = undefined
  })
}

export async function stopAccountHealthJobsOutcomeProjectionRuntime(): Promise<void> {
  stopping = true
  if (wakeTimer) {
    clearTimeout(wakeTimer)
    wakeTimer = undefined
  }
  const resolveWake = wakeResolver
  wakeResolver = undefined
  resolveWake?.()
  await runPromise
}

async function runProjectionLoop(): Promise<void> {
  const source = outcomeStoreSource()
  while (!stopping) {
    try {
      await drainOnce(source)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'account_health_jobs_outcome_projection_failed',
        consumerKey
      }), 'J1 outcome 投影失败，将保留游标并重试')
    }
    if (stopping) break
    await waitForNextTick(runtimeConfig.accountHealthJobs.projectionPollMs)
  }
}

async function drainOnce(source: AccountHealthJobsStoreSource): Promise<void> {
  if (runtimeConfig.databaseDriver === 'sqlite') {
    const database = getBusinessDatabase()
    await drainAccountHealthJobsOutcomes({
      source,
      limit: runtimeConfig.accountHealthJobs.projectionBatchSize,
      dependencies: {
        loadCursor: async () => currentAccountHealthProjectionCursor(consumerKey, database),
        projectAndAdvance: async (outcome, cursor) => {
          projectAccountHealthJobsOutcome(outcome, database)
          return { cursorAdvanced: advanceAccountHealthProjectionCursor(consumerKey, cursor, database) }
        }
      }
    })
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await drainAccountHealthJobsOutcomes({
    source,
    limit: runtimeConfig.accountHealthJobs.projectionBatchSize,
    dependencies: {
      loadCursor: async () => await currentAccountHealthProjectionCursorAsync(client, consumerKey),
      projectAndAdvance: async (outcome, cursor) => {
        await projectAccountHealthJobsOutcomeAsync(client, outcome)
        return { cursorAdvanced: await advanceAccountHealthProjectionCursorAsync(client, consumerKey, cursor) }
      }
    }
  })
}

function outcomeStoreSource(): AccountHealthJobsStoreSource {
  if (runtimeConfig.databaseDriver === 'sqlite') {
    const path = runtimeConfig.accountHealthJobs.outcomeSqlitePath?.trim()
    if (!path) throw new Error('J1 SQLite outcome projection 必须设置 jobs outcome SQLite 路径')
    return { mode: 'sqlite', databasePath: path }
  }
  const postgresUrl = runtimeConfig.accountHealthJobs.outcomePostgresUrl?.trim()
  if (!postgresUrl) throw new Error('J1 PG outcome projection 必须设置 jobs outcome PostgreSQL URL')
  return { mode: 'postgres', postgresUrl }
}

function waitForNextTick(delayMs: number): Promise<void> {
  return new Promise((resolve) => {
    wakeResolver = resolve
    wakeTimer = setTimeout(() => {
      wakeTimer = undefined
      wakeResolver = undefined
      resolve()
    }, passiveScheduleDelayMs(delayMs))
  })
}
