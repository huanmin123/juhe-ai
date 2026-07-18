import { strict as assert } from 'node:assert'
import { URL, URLSearchParams } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { writeClientIpStatsAggregatesFromUsageRowsAsync } from '../../storage/client-ip-stats-writer.js'
import { normalizeClientIpForStats } from '../../storage/client-ip-normalization.js'
import { refreshClientIpUsageRangeWindowsAsync } from '../../storage/client-ip-usage-range-windows.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { applyPostgresSchema, collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import {
  clearUsageStatsTimezoneCache,
  dateKey,
  startOfZonedDateKeyIso,
  usageStatsTimezoneAsync
} from '../../storage/usage-stats-helpers.js'
import type { UsageStatsRecordRow } from '../../storage/usage-stats-types.js'

const fixtureEnabledEnv = 'JUHE_AI_NODE_GO_IP_STATS_FIXTURE'
const fixtureSchemaModeEnv = 'JUHE_AI_NODE_GO_IP_STATS_SCHEMA_MODE'
const fullSchemaMode = 'node-full'
const gooseDetailSchemaMode = 'goose-000041'
const outputPrefix = 'JUHE_AI_NODE_GO_IP_STATS '
const clientIp = '198.18.250.42'
const primaryAccount = {
  id: 'acct_node_go_ip_stats_primary',
  name: 'Node Go IP Stats Primary',
  ownerSystemAccountId: 'sys_node_go_ip_stats_primary',
  ownerSystemAccountName: 'Node Go IP Stats Primary Owner'
} as const
const secondaryAccount = {
  id: 'acct_node_go_ip_stats_secondary',
  name: 'Node Go IP Stats Secondary',
  ownerSystemAccountId: 'sys_node_go_ip_stats_secondary',
  ownerSystemAccountName: 'Node Go IP Stats Secondary Owner'
} as const
const fixtureTimezones = ['Asia/Shanghai', 'America/New_York'] as const
const minimumMidnightDistanceMinutes = 5 * 60
const minuteMs = 60 * 1000
const hourMs = 60 * minuteMs

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
  const schemaMode = process.env[fixtureSchemaModeEnv] ?? fullSchemaMode
  assert(
    schemaMode === fullSchemaMode || schemaMode === gooseDetailSchemaMode,
    `${fixtureSchemaModeEnv} must be ${fullSchemaMode} or ${gooseDetailSchemaMode}`
  )

  const pool = await getPostgresPool()
  try {
    const client = createPostgresDatabaseClient(pool)
    if (schemaMode === gooseDetailSchemaMode) {
      await assertGooseDetailMigrationReady(client)
      await applyGooseDetailWriterSupportSchema(client)
      await assertGooseDetailMigrationReady(client)
    } else {
      await applyPostgresSchema(client)
      await seedPostgresDefaults(client)
    }

    const timezoneSelection = selectFixtureTimezone(new Date())
    await configureFixtureTimezone(client, timezoneSelection.timezone)
    await seedFixtureAccounts(client)
    clearUsageStatsTimezoneCache()
    const timezone = await usageStatsTimezoneAsync()
    assert.equal(timezone, timezoneSelection.timezone, 'production usage statistics timezone should read the fixture setting')

    const statDate = dateKey(new Date(), timezone)
    const nextStatDate = nextZonedDateKey(statDate, timezone)
    const firstCreatedAt = zonedDateTimeIso(statDate, 0, 15, 11, 123, timezone)
    const errorCreatedAt = zonedDateTimeIso(statDate, 12, 30, 22, 456, timezone)
    const lastSuccessCreatedAt = zonedDateTimeIso(statDate, 20, 45, 33, 789, timezone)
    const nextDateCreatedAt = zonedDateTimeIso(nextStatDate, 0, 15, 44, 321, timezone)
    assert(firstCreatedAt < errorCreatedAt, 'fixture 00:15 success should precede the midday error')
    assert(errorCreatedAt < lastSuccessCreatedAt, 'fixture final success should follow the final error')
    assert(lastSuccessCreatedAt < nextDateCreatedAt, 'fixture next-date row should follow the target date rows')
    const identity = normalizeClientIpForStats(clientIp)
    assert(identity, 'fixture client IP should normalize')

    const rows: UsageStatsRecordRow[] = [
      usageRow({
        id: 'node_go_ip_stats_success_early',
        traceId: 'trace_node_go_ip_stats_success_early',
        success: 1,
        statusCode: 200,
        durationMs: 111,
        firstTokenMs: 17,
        inputTokens: 101,
        outputTokens: 11,
        cacheReadTokens: 13,
        cacheReadCostUsd: 0.0013,
        cacheWriteTokens: 17,
        cacheWrite1hTokens: 19,
        cacheWriteCostUsd: 0.0017,
        thinkingTokens: 23,
        inputImageTokens: 29,
        outputImageTokens: 31,
        costUsd: 0.0101,
        accountId: primaryAccount.id,
        createdAt: firstCreatedAt
      }),
      usageRow({
        id: 'node_go_ip_stats_error',
        traceId: 'trace_node_go_ip_stats_error',
        success: 0,
        statusCode: 429,
        durationMs: 246,
        firstTokenMs: 29,
        inputTokens: 202,
        outputTokens: 22,
        cacheReadTokens: 26,
        cacheReadCostUsd: 0.0026,
        cacheWriteTokens: 34,
        cacheWrite1hTokens: 38,
        cacheWriteCostUsd: 0.0034,
        thinkingTokens: 46,
        inputImageTokens: 58,
        outputImageTokens: 62,
        costUsd: 0.0202,
        accountId: primaryAccount.id,
        createdAt: errorCreatedAt
      }),
      usageRow({
        id: 'node_go_ip_stats_success_late',
        traceId: 'trace_node_go_ip_stats_success_late',
        success: 1,
        statusCode: 201,
        durationMs: 357,
        firstTokenMs: 43,
        inputTokens: 303,
        outputTokens: 33,
        cacheReadTokens: 39,
        cacheReadCostUsd: 0.0039,
        cacheWriteTokens: 51,
        cacheWrite1hTokens: 57,
        cacheWriteCostUsd: 0.0051,
        thinkingTokens: 69,
        inputImageTokens: 87,
        outputImageTokens: 93,
        costUsd: 0.0303,
        accountId: secondaryAccount.id,
        createdAt: lastSuccessCreatedAt
      }),
      usageRow({
        id: 'node_go_ip_stats_next_date_registry',
        traceId: 'trace_node_go_ip_stats_next_date_registry',
        success: 1,
        statusCode: 202,
        durationMs: 9991,
        firstTokenMs: 991,
        inputTokens: 9001,
        outputTokens: 9002,
        cacheReadTokens: 9003,
        cacheReadCostUsd: 0.9003,
        cacheWriteTokens: 9004,
        cacheWrite1hTokens: 9005,
        cacheWriteCostUsd: 0.9004,
        thinkingTokens: 9006,
        inputImageTokens: 9007,
        outputImageTokens: 9008,
        costUsd: 9.009,
        accountId: null,
        createdAt: nextDateCreatedAt
      })
    ]

    const fixtureUpdatedAt = new Date().toISOString()
    await client.transaction(async (transaction) => {
      await writeClientIpStatsAggregatesFromUsageRowsAsync(
        transaction,
        rows,
        fixtureUpdatedAt,
        timezone
      )
    })
    const dateKeyBeforeRefresh = dateKey(new Date(), timezone)
    assert.equal(
      dateKeyBeforeRefresh,
      statDate,
      `fixture local date changed before refresh in ${timezone}: selected ${statDate}, current ${dateKeyBeforeRefresh}`
    )
    await refreshClientIpUsageRangeWindowsAsync({ dirtyLimit: 10 })
    const dateKeyAfterRefresh = dateKey(new Date(), timezone)
    assert.equal(
      dateKeyAfterRefresh,
      dateKeyBeforeRefresh,
      `fixture crossed local midnight during refresh in ${timezone}: before ${dateKeyBeforeRefresh}, after ${dateKeyAfterRefresh}`
    )

    console.log(`${outputPrefix}${JSON.stringify({
      ipHash: identity.ipHash,
      aggregateIpKey: identity.aggregateIpKey,
      timezone,
      midnightDistanceMinutes: timezoneSelection.midnightDistanceMinutes,
      startDate: statDate,
      endDate: statDate,
      expected: {
        requestCount: 3,
        successCount: 2,
        errorCount: 1,
        errorRate: 1 / 3,
        inputTokens: 606,
        outputTokens: 66,
        cacheReadTokens: 78,
        cacheReadCost: 0.0078,
        cacheWriteTokens: 102,
        cacheWrite1hTokens: 114,
        cacheWriteCost: 0.0102,
        thinkingTokens: 138,
        inputImageTokens: 174,
        outputImageTokens: 186,
        totalTokens: 672,
        totalCost: 0.0606,
        activeDays: 1,
        averageDurationMs: 238,
        averageFirstTokenMs: 89 / 3,
        maxDurationMs: 357,
        lastSeenAt: nextDateCreatedAt,
        lastUsedAt: lastSuccessCreatedAt,
        lastErrorAt: errorCreatedAt
      }
    })}`)
  } finally {
    await closePostgresPool()
  }
}

async function assertGooseDetailMigrationReady(client: DatabaseClient): Promise<void> {
  const state = await client.one<{
    version_id?: string
    is_applied?: boolean
    detail_table_exists?: boolean
    detail_index_exists?: boolean
  }>(`
    SELECT
      latest.version_id::text AS version_id,
      latest.is_applied,
      to_regclass('juhe_stats.client_ip_account_usage_range_windows') IS NOT NULL AS detail_table_exists,
      to_regclass('juhe_stats.idx_client_ip_account_range_requests') IS NOT NULL AS detail_index_exists
    FROM (
      SELECT version_id, is_applied
      FROM goose_db_version
      ORDER BY id DESC
      LIMIT 1
    ) latest
  `)
  assert(state, 'Goose migration state should exist before the detail writer fixture runs')
  assert.equal(state.version_id, '41', 'detail writer fixture requires Goose version exactly 41')
  assert.equal(state.is_applied, true, 'Goose version 41 should be applied')
  assert.equal(state.detail_table_exists, true, 'Goose 000041 should create the detail range table')
  assert.equal(state.detail_index_exists, true, 'Goose 000041 should create the detail range index')
}

async function applyGooseDetailWriterSupportSchema(client: DatabaseClient): Promise<void> {
  const supportStatementPrefixes = [
    'CREATE TABLE IF NOT EXISTS client_ip_stats_daily (',
    'CREATE TABLE IF NOT EXISTS client_ip_account_stats_daily (',
    'CREATE INDEX IF NOT EXISTS idx_client_ip_stats_daily_date ',
    'CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_date ',
    'CREATE INDEX IF NOT EXISTS idx_client_ip_account_daily_ip_date '
  ] as const
  const statsStatements = collectPostgresSchemaStatements()
    .filter((statement) => statement.schemaName === 'juhe_stats')

  for (const prefix of supportStatementPrefixes) {
    const matches = statsStatements.filter((statement) => statement.sql.startsWith(prefix))
    assert.equal(matches.length, 1, `production stats schema should contain exactly one ${prefix.trim()} statement`)
    const statement = matches[0]
    assert(statement, `production stats schema statement ${prefix.trim()} should exist`)
    await client.execute(`SET search_path TO "juhe_stats", public;\n${statement.sql}`)
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
  accountId: string | null
  createdAt: string
}

interface ZonedDateTimeParts {
  dateKey: string
  hour: number
  minute: number
  second: number
}

function selectFixtureTimezone(now: Date): {
  timezone: typeof fixtureTimezones[number]
  midnightDistanceMinutes: number
} {
  const candidates = fixtureTimezones.map((timezone) => {
    const parts = zonedDateTimeParts(now, timezone)
    const secondsSinceMidnight = parts.hour * 60 * 60 + parts.minute * 60 + parts.second
    const midnightDistanceSeconds = Math.min(secondsSinceMidnight, 24 * 60 * 60 - secondsSinceMidnight)
    return {
      timezone,
      midnightDistanceMinutes: Math.floor(midnightDistanceSeconds / 60)
    }
  }).sort((left, right) => right.midnightDistanceMinutes - left.midnightDistanceMinutes)
  const selected = candidates[0]
  assert(selected, 'fixture timezone candidates should not be empty')
  assert(
    selected.midnightDistanceMinutes >= minimumMidnightDistanceMinutes,
    `fixture timezone ${selected.timezone} is only ${selected.midnightDistanceMinutes} minutes from local midnight`
  )
  return selected
}

async function configureFixtureTimezone(client: DatabaseClient, timezone: string): Promise<void> {
  const updatedAt = new Date().toISOString()
  await client.transaction(async (transaction) => {
    await transaction.execute(`
      INSERT INTO "juhe_business"."system_accounts" (
        id, username, display_name, description, role, status, password_hash,
        must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO NOTHING
    `, [
      'sys_admin',
      'admin',
      'Node Go IP Stats Admin',
      'Node writer to Go reader integration fixture',
      'super_admin',
      'active',
      'node-go-ip-stats-fixture-password-hash',
      false,
      false,
      updatedAt,
      updatedAt
    ])
    await transaction.execute(`
      INSERT INTO "juhe_business"."system_settings" (system_account_id, key, value_json, updated_at)
      VALUES ('sys_admin', 'usageStatsTimezone', ?, ?)
      ON CONFLICT(system_account_id, key) DO UPDATE SET
        value_json = EXCLUDED.value_json,
        updated_at = EXCLUDED.updated_at
    `, [JSON.stringify(timezone), updatedAt])
  })
}

async function seedFixtureAccounts(client: DatabaseClient): Promise<void> {
  const updatedAt = new Date().toISOString()
  await client.transaction(async (transaction) => {
    for (const account of [primaryAccount, secondaryAccount]) {
      await transaction.execute(`
        INSERT INTO "juhe_business"."system_accounts" (
          id, username, display_name, description, role, status, password_hash,
          must_change_password, image_generation_enabled, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
          display_name = EXCLUDED.display_name,
          updated_at = EXCLUDED.updated_at
      `, [
        account.ownerSystemAccountId,
        account.ownerSystemAccountId,
        account.ownerSystemAccountName,
        'Node writer to Go detail reader integration fixture',
        'user',
        'active',
        'node-go-ip-stats-owner-password-hash',
        false,
        false,
        updatedAt,
        updatedAt
      ])
      await transaction.execute(`
        INSERT INTO "juhe_business"."accounts" (
          id, system_account_id, provider_code, provider_protocol_profile_id,
          protocol_code, protocol_version, name, type, status,
          credentials_encrypted, credential_mask, health_check_model,
          health_check_endpoint_mode, created_at, updated_at
        ) VALUES (?, ?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?,
          'api_key', 'active', '{}', '', 'gpt-5.5', 'responses_sse', ?, ?)
        ON CONFLICT(id) DO UPDATE SET
          system_account_id = EXCLUDED.system_account_id,
          name = EXCLUDED.name,
          deleted_at = NULL,
          updated_at = EXCLUDED.updated_at
      `, [
        account.id,
        account.ownerSystemAccountId,
        account.name,
        updatedAt,
        updatedAt
      ])
    }
  })
}

function nextZonedDateKey(currentDateKey: string, timezone: string): string {
  const startIso = startOfZonedDateKeyIso(currentDateKey, timezone)
  assert(startIso, `fixture could not resolve start of ${currentDateKey} in ${timezone}`)
  const nextDateKey = dateKey(new Date(Date.parse(startIso) + 36 * hourMs), timezone)
  assert.notEqual(nextDateKey, currentDateKey, `fixture could not advance from ${currentDateKey} in ${timezone}`)
  return nextDateKey
}

function zonedDateTimeIso(
  targetDateKey: string,
  targetHour: number,
  targetMinute: number,
  targetSecond: number,
  targetMillisecond: number,
  timezone: string
): string {
  const startIso = startOfZonedDateKeyIso(targetDateKey, timezone)
  assert(startIso, `fixture could not resolve start of ${targetDateKey} in ${timezone}`)
  const targetMinutes = targetHour * 60 + targetMinute
  let timestamp = Date.parse(startIso) + targetMinutes * minuteMs + targetSecond * 1000 + targetMillisecond

  for (let attempt = 0; attempt < 4; attempt += 1) {
    const parts = zonedDateTimeParts(new Date(timestamp), timezone)
    if (parts.dateKey === targetDateKey && parts.hour === targetHour && parts.minute === targetMinute) {
      const result = new Date(timestamp).toISOString()
      assert.equal(dateKey(new Date(result), timezone), targetDateKey)
      return result
    }
    let minuteAdjustment = targetMinutes - (parts.hour * 60 + parts.minute)
    if (parts.dateKey < targetDateKey) minuteAdjustment += 24 * 60
    if (parts.dateKey > targetDateKey) minuteAdjustment -= 24 * 60
    timestamp += minuteAdjustment * minuteMs
  }

  throw new Error(`fixture could not resolve ${targetDateKey} ${targetHour}:${targetMinute} in ${timezone}`)
}

function zonedDateTimeParts(value: Date, timezone: string): ZonedDateTimeParts {
  const values = new Map(
    new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23'
    }).formatToParts(value).map((part) => [part.type, part.value])
  )
  const year = Number(values.get('year'))
  const month = Number(values.get('month'))
  const day = Number(values.get('day'))
  const hour = Number(values.get('hour'))
  const minute = Number(values.get('minute'))
  const second = Number(values.get('second'))
  for (const [name, part] of Object.entries({ year, month, day, hour, minute, second })) {
    assert(Number.isInteger(part), `fixture ${timezone} ${name} should be an integer`)
  }
  return {
    dateKey: `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`,
    hour,
    minute,
    second
  }
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
    account_id: input.accountId,
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
  const secrets = new Set<string>()
  const postgresUrl = process.env.JUHE_AI_POSTGRES_URL
  addSecretVariants(secrets, postgresUrl)
  if (postgresUrl) {
    try {
      const parsed = new URL(postgresUrl)
      addSecretVariants(secrets, parsed.toString())
      addSecretVariants(secrets, parsed.username)
      addSecretVariants(secrets, parsed.password)
    } catch {}
  }
  addSecretVariants(secrets, process.env.PGUSER)
  addSecretVariants(secrets, process.env.PGPASSWORD)
  for (const secret of [...secrets].sort((left, right) => right.length - left.length)) {
    message = message.split(secret).join('[redacted]')
  }
  return message.slice(0, 4096)
}

function addSecretVariants(secrets: Set<string>, input: string | undefined): void {
  if (!input) return
  const decoded = decodeURIComponentSafely(input)
  for (const value of new Set([input, decoded])) {
    if (!value) continue
    const formEncoded = new URLSearchParams({ value }).toString().slice('value='.length)
    for (const variant of [value, encodeURI(value), encodeURIComponent(value), formEncoded]) {
      if (!variant) continue
      secrets.add(variant)
      secrets.add(normalizePercentHexCase(variant, 'upper'))
      secrets.add(normalizePercentHexCase(variant, 'lower'))
    }
  }
}

function decodeURIComponentSafely(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

function normalizePercentHexCase(value: string, mode: 'upper' | 'lower'): string {
  return value.replace(/%[0-9a-f]{2}/gi, (match) => mode === 'upper' ? match.toUpperCase() : match.toLowerCase())
}
