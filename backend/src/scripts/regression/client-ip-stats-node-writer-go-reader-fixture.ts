import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { writeClientIpStatsAggregatesFromUsageRowsAsync } from '../../storage/client-ip-stats-writer.js'
import { normalizeClientIpForStats } from '../../storage/client-ip-normalization.js'
import { refreshClientIpUsageRangeWindowsAsync } from '../../storage/client-ip-usage-range-windows.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import type { UsageStatsRecordRow } from '../../storage/usage-stats-types.js'

const fixtureEnabledEnv = 'JUHE_AI_NODE_GO_IP_STATS_FIXTURE'
const outputPrefix = 'JUHE_AI_NODE_GO_IP_STATS '
const clientIp = '198.18.250.42'

await main().catch((error: unknown) => {
  console.error(`client IP stats Node writer fixture failed: ${sanitizeError(error)}`)
  process.exitCode = 1
})

async function main(): Promise<void> {
  assert.equal(process.env[fixtureEnabledEnv], '1', `${fixtureEnabledEnv}=1 is required`)
  assert.equal(runtimeConfig.runtimeMode, 'performance', 'fixture requires performance runtime mode')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', 'fixture requires PostgreSQL')
  assert.equal(runtimeConfig.processRole, 'worker', 'fixture requires worker process role')
  assert.equal(runtimeConfig.workerRole, 'stats-worker', 'fixture requires stats-worker role')

  const pool = await getPostgresPool()
  try {
    const client = createPostgresDatabaseClient(pool)
    await applyPostgresSchema(client)

    const timezone = await usageStatsTimezoneAsync()
    assert.equal(timezone, 'UTC', 'fixture requires UTC usage statistics timezone')

    const statDate = dateKey(new Date(), timezone)
    const firstCreatedAt = `${statDate}T08:15:30.123Z`
    const secondCreatedAt = `${statDate}T09:45:50.789Z`
    const identity = normalizeClientIpForStats(clientIp)
    assert(identity, 'fixture client IP should normalize')

    const rows: UsageStatsRecordRow[] = [
      usageRow({
        id: 'node_go_ip_stats_success',
        traceId: 'trace_node_go_ip_stats_success',
        success: 1,
        statusCode: 200,
        durationMs: 120,
        firstTokenMs: 30,
        inputTokens: 100,
        outputTokens: 20,
        cacheReadTokens: 10,
        cacheReadCostUsd: 0.0001,
        cacheWriteTokens: 6,
        cacheWrite1hTokens: 4,
        cacheWriteCostUsd: 0.0002,
        thinkingTokens: 7,
        inputImageTokens: 2,
        outputImageTokens: 3,
        costUsd: 0.0015,
        createdAt: firstCreatedAt
      }),
      usageRow({
        id: 'node_go_ip_stats_error',
        traceId: 'trace_node_go_ip_stats_error',
        success: 0,
        statusCode: 429,
        durationMs: 240,
        firstTokenMs: 50,
        inputTokens: 40,
        outputTokens: 5,
        cacheReadTokens: 2,
        cacheReadCostUsd: 0.00003,
        cacheWriteTokens: 3,
        cacheWrite1hTokens: 1,
        cacheWriteCostUsd: 0.00007,
        thinkingTokens: 11,
        inputImageTokens: 1,
        outputImageTokens: 4,
        costUsd: 0.0005,
        createdAt: secondCreatedAt
      })
    ]

    await client.transaction(async (transaction) => {
      await writeClientIpStatsAggregatesFromUsageRowsAsync(
        transaction,
        rows,
        secondCreatedAt,
        timezone
      )
    })
    await refreshClientIpUsageRangeWindowsAsync({ dirtyLimit: 10 })

    console.log(`${outputPrefix}${JSON.stringify({
      ipHash: identity.ipHash,
      aggregateIpKey: identity.aggregateIpKey,
      startDate: statDate,
      endDate: statDate,
      expected: {
        requestCount: 2,
        successCount: 1,
        errorCount: 1,
        errorRate: 0.5,
        inputTokens: 140,
        outputTokens: 25,
        cacheReadTokens: 12,
        cacheReadCost: 0.00013,
        cacheWriteTokens: 9,
        cacheWrite1hTokens: 5,
        cacheWriteCost: 0.00027,
        thinkingTokens: 18,
        inputImageTokens: 3,
        outputImageTokens: 7,
        totalTokens: 165,
        totalCost: 0.002,
        activeDays: 1,
        averageDurationMs: 180,
        averageFirstTokenMs: 40,
        maxDurationMs: 240,
        lastSeenAt: secondCreatedAt,
        lastUsedAt: secondCreatedAt,
        lastErrorAt: secondCreatedAt
      }
    })}`)
  } finally {
    await closePostgresPool()
  }
}

interface UsageRowInput {
  id: string
  traceId: string
  success: 0 | 1
  statusCode: number
  durationMs: number
  firstTokenMs: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCostUsd: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCostUsd: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
  costUsd: number
  createdAt: string
}

function usageRow(input: UsageRowInput): UsageStatsRecordRow {
  return {
    id: input.id,
    system_account_id: 'sys_admin',
    trace_id: input.traceId,
    traffic_source: 'gateway',
    client_ip: clientIp,
    api_key_id: null,
    group_id: null,
    account_id: null,
    endpoint: '/v1/responses',
    provider_code: 'openai',
    model: 'gpt-5.1',
    status_code: input.statusCode,
    success: input.success,
    failure_attribution: input.success === 1 ? null : 'account_upstream',
    first_token_ms: input.firstTokenMs,
    duration_ms: input.durationMs,
    input_tokens: input.inputTokens,
    output_tokens: input.outputTokens,
    cache_read_tokens: input.cacheReadTokens,
    cache_read_cost_usd: input.cacheReadCostUsd,
    cache_write_tokens: input.cacheWriteTokens,
    cache_write_1h_tokens: input.cacheWrite1hTokens,
    cache_write_cost_usd: input.cacheWriteCostUsd,
    thinking_tokens: input.thinkingTokens,
    input_image_tokens: input.inputImageTokens,
    output_image_tokens: input.outputImageTokens,
    cost_usd: input.costUsd,
    error_code: input.success === 1 ? null : 'rate_limit',
    error_message: input.success === 1 ? null : 'fixture upstream rate limit',
    account_owner_system_account_id: null,
    group_owner_system_account_id: null,
    account_access_type: null,
    group_access_type: null,
    account_authorization_id: null,
    account_authorization_source_type: null,
    account_authorization_source_team_id: null,
    group_authorization_id: null,
    group_authorization_source_type: null,
    group_authorization_source_team_id: null,
    created_at: input.createdAt
  }
}

function sanitizeError(error: unknown): string {
  let message = error instanceof Error
    ? `${error.name}: ${error.message}`
    : String(error)
  for (const secret of [
    process.env.JUHE_AI_POSTGRES_URL,
    process.env.PGPASSWORD
  ]) {
    if (secret) {
      message = message.split(secret).join('[redacted]')
    }
  }
  return message.slice(0, 4096)
}
