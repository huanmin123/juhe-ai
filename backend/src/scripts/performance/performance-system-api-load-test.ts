import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { runtimeConfig } from '../../config/runtime.js'
import { captchaAnswerForTest } from '../../modules/auth/captcha.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { logger } from '../../shared/logger.js'
import { createDedicatedRedisClient } from '../../shared/redis-client.js'
import { closeStorageDatabases } from '../../storage/database.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

interface LoadConfig {
  durationMs: number
  concurrency: number
  writeRatio: number
  accountCount: number
  warmupRequests: number
  sampleIntervalMs: number
  requestTimeoutMs: number
  maxAllowedErrorRate: number
  maxAllowedDeadlocks: number
  maxAllowedXactAgeSeconds: number
  maxAllowedActiveQuerySeconds: number
  maxAllowedP95Ms: number
  resetPgStatStatements: boolean
  reportPath?: string
}

interface ApiEnvelope<T> {
  data: T
}

interface LoadFixture {
  groupId: string
  groupName: string
  accountId: string
  accountName: string
  accounts: Array<{ id: string; name: string }>
  apiKeyId: string
  apiKeyName: string
  routeStrategyId: string
}

interface PartialLoadFixture {
  groupId?: string
  accountId?: string
  accountIds?: string[]
  accounts?: Array<{ id: string; name: string }>
  apiKeyId?: string
  routeStrategyId?: string
}

interface RequestMetric {
  operation: string
  status: number
  ok: boolean
  latencyMs: number
  error?: string
}

interface OperationReport {
  count: number
  ok: number
  errors: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  maxMs: number
  statuses: Record<string, number>
}

interface PostgresSample {
  sampledAt: string
  active: number
  idleInTransaction: number
  lockWaiters: number
  notGrantedLocks: number
  maxXactAgeSeconds: number
  maxActiveQuerySeconds: number
}

interface PostgresReport {
  deadlocksBefore: number
  deadlocksAfter: number
  deadlocksDelta: number
  maxActive: number
  maxIdleInTransaction: number
  maxLockWaiters: number
  maxNotGrantedLocks: number
  maxXactAgeSeconds: number
  maxActiveQuerySeconds: number
  samples: PostgresSample[]
  slowStatements: Array<Record<string, unknown>>
}

interface RedisReport {
  cachePingMs: number
  statePingMs: number
  queuePingMs: number
}

interface LoadReport {
  mode: {
    runtimeMode: string
    databaseDriver: string
    cacheDriver: string
    runtimeStateDriver: string
    queueDriver: string
  }
  config: LoadConfig
  startedAt: string
  finishedAt: string
  durationMs: number
  totalRequests: number
  okRequests: number
  errorRequests: number
  errorRate: number
  requestsPerSecond: number
  overall: OperationReport
  operations: Record<string, OperationReport>
  postgres: PostgresReport
  redis: RedisReport
  pass: boolean
  violations: string[]
}

type LoadOperation = (workerIndex: number, requestIndex: number) => Promise<Response>

logger.level = 'silent'

const config = loadConfig()
let exitCode = 0

try {
  validateRuntime()
  const report = await runLoadTest(config)
  outputReport(report)
  if (!report.pass) {
    exitCode = 1
  }
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  closeStorageDatabases()
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function runLoadTest(input: LoadConfig): Promise<LoadReport> {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
  const server = app.listen(0, '127.0.0.1')
  let fixture: LoadFixture | undefined
  let settingsSnapshot: Record<string, unknown> | undefined
  const samples: PostgresSample[] = []
  const metrics: RequestMetric[] = []
  const startedAt = new Date()
  const startedAtMs = performance.now()
  let stopSampler = false

  try {
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const cookie = await login(baseUrl)
    settingsSnapshot = await getEnvelope<Record<string, unknown>>(baseUrl, '/__aisys__/api/settings', cookie, input.requestTimeoutMs)
    await patchEnvelope(baseUrl, '/__aisys__/api/settings', { systemApiRateLimitEnabled: false }, cookie, input.requestTimeoutMs)
    await cleanupStaleLoadFixtures()
    fixture = await createFixture(baseUrl, cookie, input)
    await warmup(baseUrl, cookie, fixture, input)

    const redis = await sampleRedis()
    const deadlocksBefore = await queryDeadlocks()
    if (input.resetPgStatStatements) {
      await resetPgStatStatements()
    }
    const sampler = samplePostgresUntilStopped(samples, () => stopSampler, input.sampleIntervalMs)
    await runWorkers(baseUrl, cookie, fixture, input, metrics)
    stopSampler = true
    await sampler
    const deadlocksAfter = await queryDeadlocks()
    const slowStatements = await querySlowStatements()
    const finishedAt = new Date()
    const durationMs = performance.now() - startedAtMs
    const postgres = summarizePostgres(samples, deadlocksBefore, deadlocksAfter, slowStatements)
    const report = buildReport(input, startedAt, finishedAt, durationMs, metrics, postgres, redis)
    return report
  } finally {
    stopSampler = true
    if (settingsSnapshot) {
      await restoreSettings(server, settingsSnapshot).catch(() => undefined)
    }
    if (fixture) {
      await cleanupFixture(fixture).catch((error) => {
        console.error(error instanceof Error ? error.message : error)
      })
    }
    await closeServer(server)
  }
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha', undefined, config.requestTimeoutMs)
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert.ok(captchaCode, '压测登录前应能生成验证码答案')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    signal: AbortSignal.timeout(config.requestTimeoutMs),
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `压测登录应成功，实际 HTTP ${response.status}: ${text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert.ok(cookie, '压测登录应返回 session cookie')
  return cookie
}

async function createFixture(baseUrl: string, cookie: string, input: LoadConfig): Promise<LoadFixture> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const partial: PartialLoadFixture = { accountIds: [] }
  const group = await postEnvelope<{ id: string; name: string }>(baseUrl, '/__aisys__/api/groups', {
    name: `压测分组${suffix}`,
    providerCode: 'gpt',
    description: 'performance api load group',
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 100,
      maxQueueWaitMs: 30_000,
      clientIpConcurrencyLimit: 100,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 10
    }
  }, cookie, input.requestTimeoutMs)
  partial.groupId = group.id
  try {
    const accounts: Array<{ id: string; name: string }> = []
    for (let index = 0; index < input.accountCount; index += 1) {
      const account = await postEnvelope<{ id: string; name: string }>(baseUrl, '/__aisys__/api/accounts', {
        name: `压测AI账户${suffix}-${index + 1}`,
        providerCode: 'gpt',
        type: 'api_key',
        status: 'temporary_unavailable',
        groupId: group.id,
        credentials: {
          api_key: `sk-load-account-${suffix}-${index + 1}`,
          base_url: 'https://example.invalid/v1'
        },
        supportedModels: ['gpt-5-mini'],
        modelMappings: [{
          sourceModel: 'gpt-5-nano',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'gpt-5-mini',
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }],
        concurrencyLimit: 100,
        priority: 5
      }, cookie, input.requestTimeoutMs)
      partial.accountIds?.push(account.id)
      await patchEnvelope<{ id: string; status: string; schedulable: boolean }>(
        baseUrl,
        `/__aisys__/api/accounts/${account.id}`,
        { clearFailureState: true },
        cookie,
        input.requestTimeoutMs
      )
      accounts.push(account)
    }
    const primaryAccount = accounts[0]
    assert.ok(primaryAccount, '压测至少需要创建一个 AI 账户')
    const routeStrategy = await postEnvelope<{ id: string; name: string }>(baseUrl, '/__aisys__/api/route-strategies', {
      name: `压测路由策略${suffix}`,
      description: 'performance api load route strategy',
      mode: 'normal',
      groupBindings: [{ groupId: group.id, priority: 1, weight: 10, status: 'active' }],
      status: 'active'
    }, cookie, input.requestTimeoutMs)
    partial.routeStrategyId = routeStrategy.id
    const apiKey = await postEnvelope<{ id: string; name: string }>(baseUrl, '/__aisys__/api/api-keys', {
      name: `压测APIKey${suffix}`,
      description: 'performance api load key',
      routeStrategyId: routeStrategy.id,
      status: 'active'
    }, cookie, input.requestTimeoutMs)
    partial.apiKeyId = apiKey.id
    return {
      groupId: group.id,
      groupName: group.name,
      accountId: primaryAccount.id,
      accountName: primaryAccount.name,
      accounts,
      apiKeyId: apiKey.id,
      apiKeyName: apiKey.name,
      routeStrategyId: routeStrategy.id
    }
  } catch (error) {
    await cleanupFixture(partial).catch(() => undefined)
    throw error
  }
}

async function warmup(baseUrl: string, cookie: string, fixture: LoadFixture, input: LoadConfig): Promise<void> {
  for (let index = 0; index < input.warmupRequests; index += 1) {
    const account = fixture.accounts[index % fixture.accounts.length] ?? { id: fixture.accountId }
    await getRaw(baseUrl, `/__aisys__/api/accounts/${account.id}`, cookie, input.requestTimeoutMs)
  }
}

async function runWorkers(
  baseUrl: string,
  cookie: string,
  fixture: LoadFixture,
  input: LoadConfig,
  metrics: RequestMetric[]
): Promise<void> {
  const endAt = performance.now() + input.durationMs
  await Promise.all(Array.from({ length: input.concurrency }, async (_item, workerIndex) => {
    let requestIndex = 0
    while (performance.now() < endAt) {
      const selected = selectOperation(input.writeRatio, fixture)
      const started = performance.now()
      try {
        const response = await selected.run(workerIndex, requestIndex)
        await response.arrayBuffer()
        const latencyMs = performance.now() - started
        metrics.push({
          operation: selected.name,
          status: response.status,
          ok: response.ok,
          latencyMs
        })
      } catch (error) {
        metrics.push({
          operation: selected.name,
          status: 0,
          ok: false,
          latencyMs: performance.now() - started,
          error: error instanceof Error ? error.message : String(error)
        })
      }
      requestIndex += 1
    }
  }))

  function selectOperation(writeRatio: number, loadFixture: LoadFixture): { name: string; run: LoadOperation } {
    if (Math.random() < writeRatio) {
      const writeAccount = randomAccount(loadFixture)
      const patchBody = {
        notes: `performance load worker ${Math.trunc(Math.random() * 100000)}`,
        priority: 1 + Math.trunc(Math.random() * 20),
        groupId: loadFixture.groupId,
        supportedModels: ['gpt-5-mini'],
        modelMappings: [{
          sourceModel: 'gpt-5-nano',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'gpt-5-mini',
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }]
      }
      return {
        name: 'PATCH /accounts/:id',
        run: () => fetch(`${baseUrl}/__aisys__/api/accounts/${writeAccount.id}`, {
          method: 'PATCH',
          headers: {
            cookie,
            'content-type': 'application/json'
          },
          signal: AbortSignal.timeout(input.requestTimeoutMs),
          body: JSON.stringify(patchBody)
        })
      }
    }
    const readAccount = randomAccount(loadFixture)
    const readOperations: Array<{ name: string; path: string }> = [
      { name: 'GET /settings/public', path: '/__aisys__/api/settings/public' },
      { name: 'GET /providers', path: '/__aisys__/api/providers' },
      { name: 'GET /groups', path: `/__aisys__/api/groups?keyword=${encodeURIComponent(loadFixture.groupName)}&page=1&pageSize=20` },
      { name: 'GET /groups/options', path: '/__aisys__/api/groups/options?providerCode=gpt&limit=20' },
      { name: 'GET /accounts', path: `/__aisys__/api/accounts?keyword=${encodeURIComponent(loadFixture.accountName)}&page=1&pageSize=20` },
      { name: 'GET /accounts/:id', path: `/__aisys__/api/accounts/${readAccount.id}` },
      { name: 'GET /accounts/options', path: `/__aisys__/api/accounts/options?ids=${encodeURIComponent(readAccount.id)}&providerCode=gpt&limit=20` },
      { name: 'GET /api-keys', path: `/__aisys__/api/api-keys?keyword=${encodeURIComponent(loadFixture.apiKeyName)}&page=1&pageSize=20` },
      { name: 'GET /api-keys/:id/secret', path: `/__aisys__/api/api-keys/${loadFixture.apiKeyId}/secret` },
      { name: 'GET /auth/me', path: '/__aisys__/api/auth/me' }
    ]
    const selected = readOperations[Math.trunc(Math.random() * readOperations.length)] ?? readOperations[0]
    return {
      name: selected.name,
      run: () => getRaw(baseUrl, selected.path, cookie, input.requestTimeoutMs)
    }
  }
}

function randomAccount(fixture: LoadFixture): { id: string; name: string } {
  return fixture.accounts[Math.trunc(Math.random() * fixture.accounts.length)] ?? {
    id: fixture.accountId,
    name: fixture.accountName
  }
}

async function restoreSettings(server: http.Server, settingsSnapshot: Record<string, unknown>): Promise<void> {
  const address = serverAddress(server)
  const baseUrl = `http://127.0.0.1:${address.port}`
  const cookie = await login(baseUrl)
  await patchEnvelope(baseUrl, '/__aisys__/api/settings', {
    systemApiRateLimitEnabled: settingsSnapshot.systemApiRateLimitEnabled
  }, cookie, config.requestTimeoutMs)
}

async function cleanupStaleLoadFixtures(): Promise<void> {
  const client = await businessClient()
  const rows = await client.query<{ group_id?: string | null; account_id?: string | null; api_key_id?: string | null }>(`
    SELECT id AS group_id, NULL AS account_id, NULL AS api_key_id
    FROM "juhe_business"."groups"
    WHERE name LIKE '压测分组%'
    UNION ALL
    SELECT NULL AS group_id, id AS account_id, NULL AS api_key_id
    FROM "juhe_business"."accounts"
    WHERE name LIKE '压测AI账户%'
    UNION ALL
    SELECT NULL AS group_id, NULL AS account_id, id AS api_key_id
    FROM "juhe_business"."api_keys"
    WHERE name LIKE '压测APIKey%'
  `)
  for (const row of rows) {
    if (row.api_key_id) await cleanupFixture({ apiKeyId: row.api_key_id })
    if (row.account_id) await cleanupFixture({ accountId: row.account_id })
    if (row.group_id) await cleanupFixture({ groupId: row.group_id })
  }
}

async function cleanupFixture(fixture: PartialLoadFixture): Promise<void> {
  const client = await businessClient()
  const accountIds = Array.from(new Set([
    ...(fixture.accountIds ?? []),
    ...(fixture.accounts?.map((account) => account.id) ?? []),
    ...(fixture.accountId ? [fixture.accountId] : [])
  ]))
  await client.transaction(async (tx) => {
    if (fixture.apiKeyId) {
      const routeRows = await tx.query<{ route_strategy_id?: string }>('SELECT route_strategy_id FROM "juhe_business"."api_keys" WHERE id = ?', [fixture.apiKeyId])
      await tx.execute('DELETE FROM "juhe_business"."api_keys" WHERE id = ?', [fixture.apiKeyId])
      for (const routeRow of routeRows) {
        if (!routeRow.route_strategy_id) continue
        await tx.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [routeRow.route_strategy_id])
        await tx.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [routeRow.route_strategy_id])
      }
    } else if (fixture.routeStrategyId) {
      await tx.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [fixture.routeStrategyId])
      await tx.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [fixture.routeStrategyId])
    }
    if (accountIds.length || fixture.groupId) {
      for (const accountId of accountIds) {
        await tx.execute('DELETE FROM "juhe_business"."group_accounts" WHERE account_id = ?', [accountId])
      }
      if (fixture.groupId) {
        await tx.execute('DELETE FROM "juhe_business"."group_accounts" WHERE group_id = ?', [fixture.groupId])
      }
    }
    for (const accountId of accountIds) {
      await tx.execute('DELETE FROM "juhe_business"."account_supported_models" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."account_model_mappings" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."account_tag_bindings" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."account_name_search_terms" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."account_name_search_documents" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."account_api_key_runtime_states" WHERE account_id = ?', [accountId])
      await tx.execute('DELETE FROM "juhe_business"."accounts" WHERE id = ?', [accountId])
    }
    if (fixture.groupId) {
      await tx.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE group_id = ?', [fixture.groupId])
      await tx.execute('DELETE FROM "juhe_business"."groups" WHERE id = ?', [fixture.groupId])
    }
  })
}

async function sampleRedis(): Promise<RedisReport> {
  const [cachePingMs, statePingMs, queuePingMs] = await Promise.all([
    pingRedis(runtimeConfig.redis.cacheUrl),
    pingRedis(runtimeConfig.redis.stateUrl),
    pingRedis(runtimeConfig.redis.queueUrl)
  ])
  return { cachePingMs, statePingMs, queuePingMs }
}

async function pingRedis(url: string | undefined): Promise<number> {
  assert.ok(url, '高性能压测需要 Redis URL')
  const client = await createDedicatedRedisClient(url)
  const started = performance.now()
  try {
    const result = await client.sendCommand(['PING'])
    assert.equal(result, 'PONG', 'Redis PING 应返回 PONG')
    return round(performance.now() - started)
  } finally {
    await client.quit?.().catch(() => undefined)
    try {
      client.destroy?.()
    } catch {
      // node-redis throws when destroy() is called after a clean quit().
    }
  }
}

async function samplePostgresUntilStopped(samples: PostgresSample[], shouldStop: () => boolean, intervalMs: number): Promise<void> {
  while (!shouldStop()) {
    try {
      samples.push(await samplePostgres())
    } catch (error) {
      samples.push({
        sampledAt: new Date().toISOString(),
        active: 0,
        idleInTransaction: 0,
        lockWaiters: 0,
        notGrantedLocks: 0,
        maxXactAgeSeconds: 0,
        maxActiveQuerySeconds: 0
      })
      console.error(error instanceof Error ? error.message : error)
    }
    await delay(intervalMs)
  }
}

async function samplePostgres(): Promise<PostgresSample> {
  const pool = await getPostgresPool()
  const activity = await pool.query(`
    SELECT
      count(*) FILTER (WHERE state = 'active') AS active,
      count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_transaction,
      count(*) FILTER (WHERE wait_event_type = 'Lock') AS lock_waiters,
      COALESCE(max(EXTRACT(EPOCH FROM (now() - xact_start))) FILTER (WHERE xact_start IS NOT NULL), 0) AS max_xact_age_seconds,
      COALESCE(max(EXTRACT(EPOCH FROM (now() - query_start))) FILTER (WHERE state = 'active' AND query_start IS NOT NULL), 0) AS max_active_query_seconds
    FROM pg_stat_activity
    WHERE datname = current_database()
      AND application_name = 'juhe-ai-backend'
  `)
  const locks = await pool.query(`
    SELECT count(*) AS not_granted_locks
    FROM pg_locks
    WHERE database = (SELECT oid FROM pg_database WHERE datname = current_database())
      AND granted = false
  `)
  const activityRow = activity.rows[0] ?? {}
  const locksRow = locks.rows[0] ?? {}
  return {
    sampledAt: new Date().toISOString(),
    active: numberValue(activityRow.active),
    idleInTransaction: numberValue(activityRow.idle_in_transaction),
    lockWaiters: numberValue(activityRow.lock_waiters),
    notGrantedLocks: numberValue(locksRow.not_granted_locks),
    maxXactAgeSeconds: round(numberValue(activityRow.max_xact_age_seconds)),
    maxActiveQuerySeconds: round(numberValue(activityRow.max_active_query_seconds))
  }
}

async function queryDeadlocks(): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT deadlocks
    FROM pg_stat_database
    WHERE datname = current_database()
    LIMIT 1
  `)
  return numberValue(result.rows[0]?.deadlocks)
}

async function querySlowStatements(): Promise<Array<Record<string, unknown>>> {
  const pool = await getPostgresPool()
  try {
    const result = await pool.query(`
      SELECT
        calls,
        ROUND(mean_exec_time::numeric, 3) AS mean_exec_time_ms,
        ROUND(max_exec_time::numeric, 3) AS max_exec_time_ms,
        rows,
        LEFT(regexp_replace(query, '\\s+', ' ', 'g'), 240) AS query
      FROM pg_stat_statements
      WHERE query ILIKE '%juhe_business%'
      ORDER BY max_exec_time DESC
      LIMIT 10
    `)
    return result.rows.map((row) => ({ ...row }))
  } catch {
    return []
  }
}

async function resetPgStatStatements(): Promise<void> {
  const pool = await getPostgresPool()
  try {
    await pool.query('SELECT pg_stat_statements_reset()')
  } catch {
    // Some deployments expose pg_stat_statements for reads but do not grant reset.
  }
}

function summarizePostgres(
  samples: PostgresSample[],
  deadlocksBefore: number,
  deadlocksAfter: number,
  slowStatements: Array<Record<string, unknown>>
): PostgresReport {
  return {
    deadlocksBefore,
    deadlocksAfter,
    deadlocksDelta: Math.max(0, deadlocksAfter - deadlocksBefore),
    maxActive: maxSample(samples, 'active'),
    maxIdleInTransaction: maxSample(samples, 'idleInTransaction'),
    maxLockWaiters: maxSample(samples, 'lockWaiters'),
    maxNotGrantedLocks: maxSample(samples, 'notGrantedLocks'),
    maxXactAgeSeconds: round(maxSample(samples, 'maxXactAgeSeconds')),
    maxActiveQuerySeconds: round(maxSample(samples, 'maxActiveQuerySeconds')),
    samples,
    slowStatements
  }
}

function buildReport(
  input: LoadConfig,
  startedAt: Date,
  finishedAt: Date,
  durationMs: number,
  metrics: RequestMetric[],
  postgres: PostgresReport,
  redis: RedisReport
): LoadReport {
  const overall = summarizeOperation(metrics)
  const operations: Record<string, OperationReport> = {}
  for (const operation of new Set(metrics.map((item) => item.operation))) {
    operations[operation] = summarizeOperation(metrics.filter((item) => item.operation === operation))
  }
  const totalRequests = metrics.length
  const errorRequests = metrics.filter((item) => !item.ok).length
  const errorRate = totalRequests > 0 ? errorRequests / totalRequests : 0
  const violations = collectViolations(input, overall, errorRate, postgres)
  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    config: input,
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: round(durationMs),
    totalRequests,
    okRequests: totalRequests - errorRequests,
    errorRequests,
    errorRate: round(errorRate),
    requestsPerSecond: round(totalRequests / Math.max(durationMs / 1000, 0.001)),
    overall,
    operations,
    postgres,
    redis,
    pass: violations.length === 0,
    violations
  }
}

function collectViolations(input: LoadConfig, overall: OperationReport, errorRate: number, postgres: PostgresReport): string[] {
  const violations: string[] = []
  if (errorRate > input.maxAllowedErrorRate) {
    violations.push(`HTTP error rate ${round(errorRate * 100)}% > ${round(input.maxAllowedErrorRate * 100)}%`)
  }
  if (postgres.deadlocksDelta > input.maxAllowedDeadlocks) {
    violations.push(`PostgreSQL deadlocks delta ${postgres.deadlocksDelta} > ${input.maxAllowedDeadlocks}`)
  }
  if (postgres.maxXactAgeSeconds > input.maxAllowedXactAgeSeconds) {
    violations.push(`PostgreSQL max transaction age ${postgres.maxXactAgeSeconds}s > ${input.maxAllowedXactAgeSeconds}s`)
  }
  if (postgres.maxActiveQuerySeconds > input.maxAllowedActiveQuerySeconds) {
    violations.push(`PostgreSQL max active query age ${postgres.maxActiveQuerySeconds}s > ${input.maxAllowedActiveQuerySeconds}s`)
  }
  if (overall.p95Ms > input.maxAllowedP95Ms) {
    violations.push(`Overall p95 ${overall.p95Ms}ms > ${input.maxAllowedP95Ms}ms`)
  }
  return violations
}

function summarizeOperation(items: RequestMetric[]): OperationReport {
  const latencies = items.map((item) => item.latencyMs)
  const statuses: Record<string, number> = {}
  for (const item of items) {
    const key = String(item.status)
    statuses[key] = (statuses[key] ?? 0) + 1
  }
  return {
    count: items.length,
    ok: items.filter((item) => item.ok).length,
    errors: items.filter((item) => !item.ok).length,
    p50Ms: percentile(latencies, 0.50),
    p95Ms: percentile(latencies, 0.95),
    p99Ms: percentile(latencies, 0.99),
    maxMs: percentile(latencies, 1),
    statuses
  }
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string | undefined, timeoutMs: number): Promise<T> {
  const response = await getRaw(baseUrl, path, cookie, timeoutMs)
  const text = await response.text()
  assert.equal(response.status, 200, `${path} 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postEnvelope<T>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string, timeoutMs: number): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    signal: AbortSignal.timeout(timeoutMs),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 201, `${path} POST 应返回 HTTP 201，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function patchEnvelope<T = unknown>(baseUrl: string, path: string, body: Record<string, unknown>, cookie: string, timeoutMs: number): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    signal: AbortSignal.timeout(timeoutMs),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} PATCH 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function getRaw(baseUrl: string, path: string, cookie: string | undefined, timeoutMs: number): Promise<Response> {
  return fetch(`${baseUrl}${path}`, {
    ...(cookie ? { headers: { cookie } } : {}),
    signal: AbortSignal.timeout(timeoutMs)
  })
}

async function businessClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function validateRuntime(): void {
  assert.equal(runtimeConfig.runtimeMode, 'performance', '高性能压测必须设置 JUHE_AI_RUNTIME_MODE=performance')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '高性能压测必须使用 PostgreSQL')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '高性能压测必须使用 Redis cache')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '高性能压测必须使用 Redis state')
  assert.equal(runtimeConfig.queueDriver, 'redis_stream', '高性能压测必须使用 Redis Stream 队列')
}

function loadConfig(): LoadConfig {
  const concurrency = intEnv('JUHE_PERFORMANCE_LOAD_CONCURRENCY', 50, 1, 500)
  const reportPath = process.env.JUHE_PERFORMANCE_LOAD_REPORT_PATH?.trim()
  return {
    durationMs: intEnv('JUHE_PERFORMANCE_LOAD_DURATION_MS', 30_000, 1_000, 600_000),
    concurrency,
    writeRatio: numberEnv('JUHE_PERFORMANCE_LOAD_WRITE_RATIO', 0.15, 0, 1),
    accountCount: intEnv('JUHE_PERFORMANCE_LOAD_ACCOUNT_COUNT', Math.min(concurrency, 20), 1, 1000),
    warmupRequests: intEnv('JUHE_PERFORMANCE_LOAD_WARMUP_REQUESTS', 100, 0, 10_000),
    sampleIntervalMs: intEnv('JUHE_PERFORMANCE_LOAD_SAMPLE_INTERVAL_MS', 1000, 100, 10_000),
    requestTimeoutMs: intEnv('JUHE_PERFORMANCE_LOAD_REQUEST_TIMEOUT_MS', 15_000, 1000, 120_000),
    maxAllowedErrorRate: numberEnv('JUHE_PERFORMANCE_LOAD_MAX_ERROR_RATE', 0, 0, 1),
    maxAllowedDeadlocks: intEnv('JUHE_PERFORMANCE_LOAD_MAX_DEADLOCKS', 0, 0, 1000),
    maxAllowedXactAgeSeconds: numberEnv('JUHE_PERFORMANCE_LOAD_MAX_XACT_AGE_SECONDS', 5, 0.1, 3600),
    maxAllowedActiveQuerySeconds: numberEnv('JUHE_PERFORMANCE_LOAD_MAX_ACTIVE_QUERY_SECONDS', 5, 0.1, 3600),
    maxAllowedP95Ms: numberEnv('JUHE_PERFORMANCE_LOAD_MAX_P95_MS', 1500, 1, 120_000),
    resetPgStatStatements: boolEnv('JUHE_PERFORMANCE_LOAD_RESET_PG_STAT_STATEMENTS', false),
    ...(reportPath ? { reportPath: resolve(reportPath) } : {})
  }
}

function outputReport(report: LoadReport): void {
  const text = JSON.stringify(report, null, 2)
  if (report.config.reportPath) {
    mkdirSync(dirname(report.config.reportPath), { recursive: true })
    writeFileSync(report.config.reportPath, `${text}\n`, 'utf8')
  }
  console.log(text)
}

function intEnv(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(parsed)))
}

function numberEnv(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, parsed))
}

function boolEnv(name: string, fallback: boolean): boolean {
  const value = process.env[name]?.trim().toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  return fallback
}

function percentile(samples: number[], p: number): number {
  if (!samples.length) return 0
  const ordered = [...samples].sort((a, b) => a - b)
  const index = Math.min(ordered.length - 1, Math.max(0, Math.ceil(ordered.length * p) - 1))
  return round(ordered[index] ?? 0)
}

function maxSample<T extends keyof PostgresSample>(samples: PostgresSample[], key: T): number {
  return Math.max(0, ...samples.map((sample) => Number(sample[key]) || 0))
}

function numberValue(value: unknown): number {
  if (typeof value === 'number') return Number.isFinite(value) ? value : 0
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return 0
}

function round(value: number): number {
  return Math.round(value * 100) / 100
}

function delay(ms: number): Promise<void> {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms))
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolveListen, rejectListen) => {
    server.once('listening', resolveListen)
    server.once('error', rejectListen)
  })
}

function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolveClose, rejectClose) => {
    server.close((error) => {
      if (error) rejectClose(error)
      else resolveClose()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert.ok(address && typeof address === 'object', '压测服务监听地址应可读取')
  return { port: address.port }
}
