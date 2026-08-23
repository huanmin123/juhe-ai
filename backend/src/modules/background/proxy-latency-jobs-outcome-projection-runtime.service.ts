import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { currentProxyLatencyProjectionCursorAsync, advanceProxyLatencyProjectionCursorAsync } from '../../storage/proxy-latency-projection-cursor.repository.js'
import { listProxyLatencyJobsOutcomes, type ProxyLatencyJobsStoreSource } from '../../storage/proxy-latency-jobs-outcome.repository.js'
import { projectProxyLatencyJobsOutcomeAsync } from './proxy-latency-jobs-projector.service.js'

const consumerKey = 'juhe-ai-proxy-latency-jobs-projector-v1'
let stopping = true
let running: Promise<void> | undefined
let projectionReady = false
let projectionReadyUntil = 0

export function proxyLatencyJobsOutcomeProjectionRuntimeReady(): boolean {
  return !stopping && running !== undefined && projectionReady && projectionReadyUntil > Date.now()
}

export function startProxyLatencyJobsOutcomeProjectionRuntime(): void {
  if (runtimeConfig.processRole !== 'db-service' || runtimeConfig.databaseDriver !== 'postgres' || running) return
  if (process.env.JUHE_AI_PROXY_LATENCY_PROJECTION_ENABLED?.trim().toLowerCase() !== 'true') return
  if (process.env.JUHE_AI_PROXY_LATENCY_JOBS_OWNER?.trim().toLowerCase() !== 'go') return
  const postgresUrl = (process.env.JUHE_AI_PROXY_LATENCY_JOBS_OUTCOME_POSTGRES_URL ?? process.env.JUHE_AI_PROXY_LATENCY_POSTGRES_URL)?.trim()
  if (!postgresUrl) throw new Error('启用 J3a outcome projector 必须设置 jobs PG URL')
  stopping = false
  projectionReady = false
  projectionReadyUntil = 0
  running = loop({ mode: 'postgres', postgresUrl }).finally(() => {
    running = undefined
    projectionReady = false
    projectionReadyUntil = 0
  })
}

export async function stopProxyLatencyJobsOutcomeProjectionRuntime(): Promise<void> {
  stopping = true
  projectionReady = false
  projectionReadyUntil = 0
  await running
}

async function loop(source: ProxyLatencyJobsStoreSource): Promise<void> {
  const delay = bounded(process.env.JUHE_AI_PROXY_LATENCY_PROJECTION_POLL_MS, 1_000, 100, 60_000)
  const batch = bounded(process.env.JUHE_AI_PROXY_LATENCY_PROJECTION_BATCH_SIZE, 100, 1, 1_000)
  while (!stopping) {
    try {
      await drain(source, batch)
      projectionReady = true
      projectionReadyUntil = Date.now() + Math.max(5_000, delay * 2)
    } catch (error) {
      projectionReady = false
      projectionReadyUntil = 0
      logger.error(errorLogFields(error, { event: 'proxy_latency_jobs_outcome_projection_failed', consumerKey }), 'J3a outcome 投影失败，将保留游标并重试')
    }
    if (!stopping) await new Promise<void>((resolve) => setTimeout(resolve, delay))
  }
}

async function drain(source: ProxyLatencyJobsStoreSource, limit: number): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  let cursor = await currentProxyLatencyProjectionCursorAsync(client, consumerKey)
  const outcomes = await listProxyLatencyJobsOutcomes(source, { ...(cursor ? { after: cursor } : {}), limit })
  for (const outcome of outcomes) {
    const result = await projectProxyLatencyJobsOutcomeAsync(client, outcome)
    if (!['applied', 'stale', 'ignored'].includes(result.disposition)) throw new Error(`J3a outcome ${outcome.outcomeId} 未处理: ${result.reason ?? 'rejected'}`)
    const next = { storedAt: outcome.storageObservedAt, outcomeId: outcome.outcomeId }
    if (!await advanceProxyLatencyProjectionCursorAsync(client, consumerKey, next)) throw new Error('J3a projection cursor 未前进')
    cursor = next
  }
}

function bounded(value: string | undefined, fallback: number, min: number, max: number): number {
  const parsed = value === undefined || !value.trim() ? fallback : Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < min || parsed > max) throw new Error('J3a outcome projector 参数无效')
  return parsed
}
