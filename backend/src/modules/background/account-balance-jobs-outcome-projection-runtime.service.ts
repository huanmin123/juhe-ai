import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { passiveScheduleDelayMs } from '../../shared/passive-schedule-jitter.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { currentAccountBalanceProjectionCursorAsync, advanceAccountBalanceProjectionCursorAsync } from '../../storage/account-balance-projection-cursor.repository.js'
import { listAccountBalanceJobsOutcomes, type AccountBalanceJobsStoreSource } from '../../storage/account-balance-jobs-outcome.repository.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { projectAccountBalanceJobsOutcome } from './account-balance-jobs-projector.service.js'
import { accountBalanceGoOwnerEnabled } from './account-balance-handover.js'

const consumerKey = 'juhe-ai-account-balance-jobs-projector-v1'
let stopping = true
let running: Promise<void> | undefined
let projectionReady = false
let projectionReadyUntil = 0

/** A drain result is usable only while it remains fresh for the next poll. */
export function accountBalanceJobsOutcomeProjectionRuntimeFresh(
  active: boolean,
  ready: boolean,
  readyUntil: number,
  now = Date.now()
): boolean {
  return active && ready && Number.isFinite(readyUntil) && readyUntil > now
}

/** True only after the active projector has completed a recent successful drain. */
export function accountBalanceJobsOutcomeProjectionRuntimeReady(): boolean {
  return accountBalanceJobsOutcomeProjectionRuntimeFresh(!stopping && running !== undefined, projectionReady, projectionReadyUntil)
}

/** This runtime is opt-in and deliberately separate from the legacy Node
 * scheduler.  It cannot be enabled without a Go-owned J2 deployment. */
export function startAccountBalanceJobsOutcomeProjectionRuntime(): void {
  if (runtimeConfig.processRole !== 'db-service'
    || runtimeConfig.blueGreenOwnerMode === 'drain'
    || !accountBalanceGoOwnerEnabled()
    || process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_PROJECTION_ENABLED?.trim().toLowerCase() !== 'true'
    || running) return
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('J2 outcome projector 仅支持 PostgreSQL 业务/统计原子投影；SQLite owner 切换保持关闭')
  }
  const postgresUrl = process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_OUTCOME_POSTGRES_URL?.trim()
  if (!postgresUrl) throw new Error('启用 J2 outcome projector 必须设置 PG outcome URL')
  stopping = false
  projectionReady = false
  projectionReadyUntil = 0
  const source: AccountBalanceJobsStoreSource = { mode: 'postgres', postgresUrl }
  running = loop(source).finally(() => { running = undefined; projectionReady = false; projectionReadyUntil = 0 })
}

export async function stopAccountBalanceJobsOutcomeProjectionRuntime(): Promise<void> { stopping = true; projectionReady = false; projectionReadyUntil = 0; await running }

async function loop(source: AccountBalanceJobsStoreSource): Promise<void> {
  const delay = boundedInteger(process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_PROJECTION_POLL_MS, 1_000, 100, 60_000)
  const batch = boundedInteger(process.env.JUHE_AI_ACCOUNT_BALANCE_JOBS_PROJECTION_BATCH_SIZE, 100, 1, 1_000)
  while (!stopping) {
    try {
      await drain(source, batch)
      projectionReady = true
      projectionReadyUntil = Date.now() + Math.max(5_000, delay * 2)
    } catch (error) {
      projectionReady = false
      projectionReadyUntil = 0
      logger.error(errorLogFields(error, { event: 'account_balance_jobs_outcome_projection_failed', consumerKey }), 'J2 outcome 投影失败，将保留游标并重试')
    }
    if (!stopping) await new Promise<void>((resolve) => setTimeout(resolve, passiveScheduleDelayMs(delay)))
  }
}

async function drain(source: AccountBalanceJobsStoreSource, limit: number): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  let cursor = await currentAccountBalanceProjectionCursorAsync(client, consumerKey)
  const outcomes = await listAccountBalanceJobsOutcomes(source, { ...(cursor ? { after: cursor } : {}), limit })
  for (const outcome of outcomes) {
    const result = await projectAccountBalanceJobsOutcome(outcome)
    if (!result.projected && result.reason !== 'stale') throw new Error(`J2 outcome ${outcome.outcomeId} 未投影: ${result.reason ?? 'unknown'}`)
    const next = { observedAt: outcome.storageObservedAt, outcomeId: outcome.outcomeId }
    if (!await advanceAccountBalanceProjectionCursorAsync(client, consumerKey, next)) throw new Error('J2 projection cursor 未前进')
    cursor = next
  }
}

function boundedInteger(value: string | undefined, fallback: number, min: number, max: number): number { const parsed = value === undefined || !value.trim() ? fallback : Number(value); if (!Number.isInteger(parsed) || parsed < min || parsed > max) throw new Error('J2 outcome projector 参数无效'); return parsed }
