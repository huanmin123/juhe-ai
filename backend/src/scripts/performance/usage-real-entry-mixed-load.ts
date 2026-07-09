import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import type { Socket } from 'node:net'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'
import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createApiKeyRecordAsync,
  createGroupAsync,
  createRouteStrategyAsync,
  getSettingsAsync,
  updateSettingsAsync
} from '../../storage/repositories.js'

interface MixedLoadConfig {
  baselineReadSeconds: number
  mixedSeconds: number
  seedWriteRequests: number
  seedWriteConcurrency: number
  writeConcurrency: number
  readConcurrency: number
  requestTimeoutMs: number
  sampleIntervalMs: number
  upstreamLatencyMs: number
  upstreamBodyBytes: number
  accountCount: number
  accountConcurrencyLimit: number
  model: string
  promptBytes: number
  settleSeconds: number
  cleanup: boolean
  maxAllowedWriteErrorRate: number
  maxAllowedReadErrorRate: number
  maxAllowedMixedReadP95Ms: number
  maxAllowedMixedWriteP95Ms: number
  maxAllowedReadP95Ratio: number
  maxAllowedDeadlocks: number
  reportPath: string
}

interface SeededGateway {
  apiKey: string
  apiKeyId: string
  routeStrategyId: string
  groupId: string
  accountIds: string[]
}

interface RequestMetric {
  phase: 'seed_write' | 'baseline_read' | 'mixed_write' | 'mixed_read'
  operation: string
  status: number
  ok: boolean
  latencyMs: number
  bytes: number
  error?: string
}

interface RequestSummary {
  count: number
  ok: number
  errors: number
  errorRate: number
  requestsPerSecond: number
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

interface StorageSnapshot {
  sampledAt: string
  usageRecords: number
  usageCatalogEntries: number
  auditLogs: number
  publicApiLogs: number
}

interface UpstreamRuntime {
  totalRequests: number
  activeRequests: number
  peakActiveRequests: number
  pathCounts: Map<string, number>
  connections: ConnectionTracker
}

interface ConnectionTracker {
  acceptedSockets: number
  closedSockets: number
  activeSockets: Set<Socket>
  socketIds: WeakMap<Socket, number>
  requestsBySocketId: Map<number, number>
  peakActiveSockets: number
}

const access = { systemAccountId: 'sys_admin', role: 'super_admin' } as const
const runId = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14)
const tracePrefix = `usage-mixed-${runId}-${Math.random().toString(16).slice(2, 8)}`
const childOutput = { stdout: '', stderr: '' }

logger.level = 'silent'

const config = loadConfig()
let exitCode = 0

try {
  validateRuntime()
  const report = await runMixedLoad(config)
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

async function runMixedLoad(input: MixedLoadConfig): Promise<Record<string, unknown> & { pass: boolean; violations: string[] }> {
  const upstreamRuntime: UpstreamRuntime = {
    totalRequests: 0,
    activeRequests: 0,
    peakActiveRequests: 0,
    pathCounts: new Map(),
    connections: createConnectionTracker()
  }
  let upstreamServer: http.Server | undefined
  let backendProcess: ChildProcess | undefined
  let seeded: SeededGateway | undefined
  let settingsSnapshot: Record<string, unknown> | undefined
  let stopSampler = false
  const metrics: RequestMetric[] = []
  const postgresSamples: PostgresSample[] = []
  const startedAt = new Date()
  const startedAtMs = performance.now()

  try {
    settingsSnapshot = await getSettingsAsync()
    await applyLoadSettings(input)

    upstreamServer = createMockOpenAIUpstream(input, upstreamRuntime)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    await cleanupStaleFixtures()
    seeded = await seedGatewayData(input, upstreamBaseUrl)

    const deadlocksBefore = await queryDeadlocks()
    const storageBefore = await sampleStorage()

    const port = await freePort()
    backendProcess = startBackendServer(port)
    const baseUrl = `http://127.0.0.1:${port}`
    await waitForHealth(`${baseUrl}/__aisys__/health`, backendProcess)
    await waitForHealth(`${baseUrl}/__aisys__/api/health`, backendProcess)
    const cookie = await login(baseUrl, input.requestTimeoutMs)

    console.log('真实入口读写混合压测启动')
    console.log(`- 后端：${baseUrl}`)
    console.log(`- 模拟上游：${upstreamBaseUrl}`)
    console.log(`- seedWrites=${input.seedWriteRequests} baselineReads=${input.baselineReadSeconds}s mixed=${input.mixedSeconds}s writeConcurrency=${input.writeConcurrency} readConcurrency=${input.readConcurrency}`)

    if (input.seedWriteRequests > 0) {
      await runFixedWriteRequests({
        baseUrl,
        apiKey: seeded.apiKey,
        config: input,
        totalRequests: input.seedWriteRequests,
        concurrency: input.seedWriteConcurrency,
        metrics,
        phase: 'seed_write'
      })
      if (input.settleSeconds > 0) {
        await sleep(input.settleSeconds * 1000)
      }
    }

    const sampler = samplePostgresUntilStopped(postgresSamples, () => stopSampler, input.sampleIntervalMs)
    await runReadPhase({
      baseUrl,
      cookie,
      seeded,
      config: input,
      durationSeconds: input.baselineReadSeconds,
      metrics,
      phase: 'baseline_read'
    })

    await Promise.all([
      runTimedWritePhase({
        baseUrl,
        apiKey: seeded.apiKey,
        config: input,
        durationSeconds: input.mixedSeconds,
        metrics,
        phase: 'mixed_write'
      }),
      runReadPhase({
        baseUrl,
        cookie,
        seeded,
        config: input,
        durationSeconds: input.mixedSeconds,
        metrics,
        phase: 'mixed_read'
      })
    ])

    if (input.settleSeconds > 0) {
      console.log(`混合请求结束，等待 ${input.settleSeconds}s 观察后台落库`)
      await sleep(input.settleSeconds * 1000)
    }
    stopSampler = true
    await sampler

    await stopProcessTree(backendProcess)
    backendProcess = undefined
    await closeServer(upstreamServer)
    upstreamServer = undefined

    const deadlocksAfter = await queryDeadlocks()
    const storageAfter = await sampleStorage()
    const slowStatements = await querySlowStatements()
    const finishedAt = new Date()
    const durationMs = performance.now() - startedAtMs
    const report = buildReport({
      input,
      startedAt,
      finishedAt,
      durationMs,
      seeded,
      metrics,
      postgresSamples,
      deadlocksBefore,
      deadlocksAfter,
      storageBefore,
      storageAfter,
      slowStatements,
      upstreamRuntime
    })
    mkdirSync(dirname(input.reportPath), { recursive: true })
    writeFileSync(input.reportPath, JSON.stringify(report, null, 2), 'utf8')
    console.log(`压测报告已写入：${input.reportPath}`)
    return report
  } finally {
    stopSampler = true
    await stopProcessTree(backendProcess)
    await closeServer(upstreamServer)
    if (settingsSnapshot) {
      await restoreLoadSettings(settingsSnapshot).catch((error) => {
        console.error(`恢复压测前系统设置失败：${error instanceof Error ? error.message : String(error)}`)
      })
    }
    if (input.cleanup && seeded) {
      await cleanupFixtureAndRecords(seeded).catch((error) => {
        console.error(`清理压测数据失败：${error instanceof Error ? error.message : String(error)}`)
      })
    }
  }
}

async function seedGatewayData(input: MixedLoadConfig, upstreamBaseUrl: string): Promise<SeededGateway> {
  const suffix = runId.replace(/[^a-zA-Z0-9-]/g, '')
  const group = await createGroupAsync({
    name: `混合压测网关分组-${suffix}`,
    providerCode: 'gpt',
    description: 'real entry mixed read write load test group',
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: input.accountConcurrencyLimit,
      maxQueueWaitMs: 30_000,
      clientIpConcurrencyLimit: input.accountConcurrencyLimit,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: Math.max(1, Math.min(100, Math.ceil(input.accountConcurrencyLimit / 10)))
    }
  }, access)
  const accountIds: string[] = []
  for (let index = 0; index < input.accountCount; index += 1) {
    const account = await createAccountAsync({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `混合压测网关账户-${suffix}-${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-usage-mixed-${suffix}-${index + 1}`,
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [input.model],
      modelMappings: [
        {
          sourceModel: input.model,
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: input.model,
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }
      ],
      concurrencyLimit: input.accountConcurrencyLimit,
      priority: index,
      notes: `real entry mixed load test ${runId}`
    }, access)
    accountIds.push(account.id)
  }
  const routeStrategy = await createRouteStrategyAsync({
    name: `混合压测网关策略-${suffix}`,
    description: 'real entry mixed read write load test route strategy',
    mode: 'normal',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }],
    status: 'active'
  }, access)
  const apiKey = await createApiKeyRecordAsync({
    name: `混合压测网关Key-${suffix}`,
    description: 'real entry mixed read write load test key',
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  return {
    apiKey: apiKey.key,
    apiKeyId: apiKey.id,
    routeStrategyId: routeStrategy.id,
    groupId: group.id,
    accountIds
  }
}

async function applyLoadSettings(input: MixedLoadConfig): Promise<void> {
  await updateSettingsAsync({
    systemApiRateLimitIpReadPerMinute: 1_000_000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1_000_000,
    systemApiRateLimitIpWritePerMinute: 1_000_000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1_000_000,
    systemApiRateLimitUserReadPerMinute: 1_000_000,
    systemApiRateLimitUserWritePerMinute: 1_000_000,
    statsAggregationIntervalSeconds: Math.max(5, Math.min(30, input.settleSeconds || 10)),
    statsAggregationBatchSize: 5000,
    statsAggregationMaxBatchesPerRun: 20,
    groupAccountStatsRefreshIntervalSeconds: 30,
    systemMetricsSampleIntervalSeconds: 5,
    accountQualityRefreshIntervalSeconds: 3600,
    accountHealthCheckIntervalHours: 24,
    cooldownAccountRetestIntervalSeconds: 3600
  })
}

async function restoreLoadSettings(snapshot: Record<string, unknown>): Promise<void> {
  const keys = [
    'systemApiRateLimitIpReadPerMinute',
    'systemApiRateLimitIpReadBurstPer10Seconds',
    'systemApiRateLimitIpWritePerMinute',
    'systemApiRateLimitIpWriteBurstPer10Seconds',
    'systemApiRateLimitUserReadPerMinute',
    'systemApiRateLimitUserWritePerMinute',
    'statsAggregationIntervalSeconds',
    'statsAggregationBatchSize',
    'statsAggregationMaxBatchesPerRun',
    'groupAccountStatsRefreshIntervalSeconds',
    'systemMetricsSampleIntervalSeconds',
    'accountQualityRefreshIntervalSeconds',
    'accountHealthCheckIntervalHours',
    'cooldownAccountRetestIntervalSeconds'
  ]
  const restore: Record<string, unknown> = {}
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(snapshot, key)) {
      restore[key] = snapshot[key]
    }
  }
  await updateSettingsAsync(restore)
}

async function login(baseUrl: string, requestTimeoutMs: number): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha', undefined, requestTimeoutMs)
  const captchaCode = await captchaAnswerForLogin(captcha.captchaId)
  assert.ok(captchaCode, '压测登录前应能生成验证码答案')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    signal: AbortSignal.timeout(requestTimeoutMs),
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

async function captchaAnswerForLogin(captchaId: string): Promise<string | undefined> {
  const challenge = await createRuntimeStateStore('auth_captcha').getJson<{ answer?: string; expiresAt?: number }>(`challenge:${captchaId}`)
  if (!challenge || typeof challenge.answer !== 'string') return undefined
  if (typeof challenge.expiresAt === 'number' && challenge.expiresAt <= Date.now()) return undefined
  return challenge.answer
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string | undefined, requestTimeoutMs: number): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: cookie ? { cookie } : undefined,
    signal: AbortSignal.timeout(requestTimeoutMs)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { data?: T }
  return body.data as T
}

async function runFixedWriteRequests(input: {
  baseUrl: string
  apiKey: string
  config: MixedLoadConfig
  totalRequests: number
  concurrency: number
  metrics: RequestMetric[]
  phase: 'seed_write'
}): Promise<void> {
  let next = 0
  await Promise.all(Array.from({ length: input.concurrency }, async (_unused, workerIndex) => {
    while (true) {
      const requestIndex = next
      next += 1
      if (requestIndex >= input.totalRequests) return
      await recordGatewayRequest(input.baseUrl, input.apiKey, input.config, `${workerIndex}-${requestIndex}`, input.phase, input.metrics)
    }
  }))
}

async function runTimedWritePhase(input: {
  baseUrl: string
  apiKey: string
  config: MixedLoadConfig
  durationSeconds: number
  metrics: RequestMetric[]
  phase: 'mixed_write'
}): Promise<void> {
  const endAt = performance.now() + input.durationSeconds * 1000
  await Promise.all(Array.from({ length: input.config.writeConcurrency }, async (_unused, workerIndex) => {
    let sequence = 0
    while (performance.now() < endAt) {
      sequence += 1
      await recordGatewayRequest(input.baseUrl, input.apiKey, input.config, `${workerIndex}-${sequence}`, input.phase, input.metrics)
    }
  }))
}

async function recordGatewayRequest(
  baseUrl: string,
  apiKey: string,
  input: MixedLoadConfig,
  requestId: string,
  phase: 'seed_write' | 'mixed_write',
  metrics: RequestMetric[]
): Promise<void> {
  const started = performance.now()
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json',
        'x-trace-id': `${tracePrefix}-${phase}-${requestId}`
      },
      body: JSON.stringify({
        model: input.model,
        messages: [{ role: 'user', content: promptText(input.promptBytes, requestId) }],
        max_tokens: 16,
        stream: false
      }),
      signal: AbortSignal.timeout(input.requestTimeoutMs)
    })
    const text = await response.text()
    metrics.push({
      phase,
      operation: 'POST /v1/chat/completions',
      status: response.status,
      ok: response.ok,
      latencyMs: performance.now() - started,
      bytes: Buffer.byteLength(text, 'utf8')
    })
  } catch (error) {
    metrics.push({
      phase,
      operation: 'POST /v1/chat/completions',
      status: 0,
      ok: false,
      latencyMs: performance.now() - started,
      bytes: 0,
      error: formatError(error)
    })
  }
}

async function runReadPhase(input: {
  baseUrl: string
  cookie: string
  seeded: SeededGateway
  config: MixedLoadConfig
  durationSeconds: number
  metrics: RequestMetric[]
  phase: 'baseline_read' | 'mixed_read'
}): Promise<void> {
  const endAt = performance.now() + input.durationSeconds * 1000
  await Promise.all(Array.from({ length: input.config.readConcurrency }, async (_unused, workerIndex) => {
    let sequence = 0
    while (performance.now() < endAt) {
      sequence += 1
      const operation = readOperation(input.seeded, workerIndex + sequence)
      const started = performance.now()
      try {
        const response = await fetch(`${input.baseUrl}${operation.path}`, {
          headers: { cookie: input.cookie },
          signal: AbortSignal.timeout(input.config.requestTimeoutMs)
        })
        const text = await response.text()
        input.metrics.push({
          phase: input.phase,
          operation: operation.name,
          status: response.status,
          ok: response.ok,
          latencyMs: performance.now() - started,
          bytes: Buffer.byteLength(text, 'utf8')
        })
      } catch (error) {
        input.metrics.push({
          phase: input.phase,
          operation: operation.name,
          status: 0,
          ok: false,
          latencyMs: performance.now() - started,
          bytes: 0,
          error: formatError(error)
        })
      }
    }
  }))
}

function readOperation(seeded: SeededGateway, sequence: number): { name: string; path: string } {
  const accountId = seeded.accountIds[sequence % Math.max(1, seeded.accountIds.length)] ?? seeded.accountIds[0]
  const operations: Array<{ name: string; path: string }> = [
    { name: 'GET /usage-records today', path: '/__aisys__/api/usage-records?systemAccountId=sys_admin&page=1&pageSize=20&result=all&trafficSource=gateway' },
    { name: 'GET /usage-records trace', path: `/__aisys__/api/usage-records?systemAccountId=sys_admin&page=1&pageSize=20&result=all&traceId=${encodeURIComponent(tracePrefix)}&trafficSource=gateway` },
    { name: 'GET /stats/usage-overview', path: '/__aisys__/api/stats/usage-overview?systemAccountId=sys_admin' },
    { name: 'GET /stats/usage-window', path: '/__aisys__/api/stats/usage-window?systemAccountId=sys_admin' },
    { name: 'GET /stats/ai-performance/accounts', path: `/__aisys__/api/stats/ai-performance/accounts?systemAccountId=sys_admin&limit=20&accountIds=${encodeURIComponent(accountId ?? '')}` },
    { name: 'GET /table-monitor/overview', path: '/__aisys__/api/table-monitor/overview?limit=50' },
    { name: 'GET /table-monitor/history usage_records', path: '/__aisys__/api/table-monitor/history?databaseRole=business&tableName=usage_records&limit=20' }
  ]
  return operations[sequence % operations.length] ?? operations[0]
}

function createMockOpenAIUpstream(input: MixedLoadConfig, runtime: UpstreamRuntime): http.Server {
  const server = http.createServer((req, res) => {
    recordSocketRequest(runtime.connections, req.socket)
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    runtime.totalRequests += 1
    runtime.activeRequests += 1
    runtime.peakActiveRequests = Math.max(runtime.peakActiveRequests, runtime.activeRequests)
    increment(runtime.pathCounts, `${req.method ?? 'GET'} ${url.pathname}`)
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      setTimeout(() => {
        runtime.activeRequests = Math.max(0, runtime.activeRequests - 1)
        if (url.pathname === '/v1/models') {
          res.writeHead(200, { 'content-type': 'application/json' })
          res.end(JSON.stringify({
            object: 'list',
            data: [{ id: input.model, object: 'model', created: 0, owned_by: 'openai' }]
          }))
          return
        }
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          id: 'chatcmpl_usage_mixed',
          object: 'chat.completion',
          created: Math.trunc(Date.now() / 1000),
          model: input.model,
          choices: [{
            index: 0,
            message: { role: 'assistant', content: responseText(input.upstreamBodyBytes) },
            finish_reason: 'stop'
          }],
          usage: { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 }
        }))
      }, input.upstreamLatencyMs)
    })
    req.on('error', () => {
      runtime.activeRequests = Math.max(0, runtime.activeRequests - 1)
    })
  })
  attachConnectionTracker(server, runtime.connections)
  return server
}

function startBackendServer(port: number): ChildProcess {
  childOutput.stdout = ''
  childOutput.stderr = ''
  const child = spawn(process.execPath, ['--import', 'tsx', 'src/server.ts'], {
    cwd: backendRoot,
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_HOST: '127.0.0.1',
      JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_SECRET: runtimeConfig.secret,
      JUHE_AI_LOG_LEVEL: process.env.JUHE_AI_MIXED_LOAD_CHILD_LOG_LEVEL ?? 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: process.env.JUHE_AI_MIXED_LOAD_CHILD_LOG_CONSOLE_ENABLED ?? 'false',
      JUHE_AI_LOG_FILE_ENABLED: process.env.JUHE_AI_MIXED_LOAD_CHILD_LOG_FILE_ENABLED ?? 'false',
      JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true'
    },
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout?.on('data', (chunk: Buffer) => {
    childOutput.stdout = tailText(childOutput.stdout + chunk.toString('utf8'))
  })
  child.stderr?.on('data', (chunk: Buffer) => {
    childOutput.stderr = tailText(childOutput.stderr + chunk.toString('utf8'))
  })
  return child
}

async function sampleStorage(): Promise<StorageSnapshot> {
  const pool = await getPostgresPool()
  const traceLike = `${tracePrefix}-%`
  const [usageRows, catalogRows, auditRows, publicRows] = await Promise.all([
    pool.query('SELECT COUNT(*) AS total FROM juhe_usage.usage_records WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_usage.usage_record_shard_entries WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_dataset.audit_logs WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_dataset.public_api_logs WHERE trace_id LIKE $1', [traceLike])
  ])
  return {
    sampledAt: new Date().toISOString(),
    usageRecords: numberValue(usageRows.rows[0]?.total),
    usageCatalogEntries: numberValue(catalogRows.rows[0]?.total),
    auditLogs: numberValue(auditRows.rows[0]?.total),
    publicApiLogs: numberValue(publicRows.rows[0]?.total)
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
      AND pid <> pg_backend_pid()
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

async function samplePostgresUntilStopped(samples: PostgresSample[], shouldStop: () => boolean, intervalMs: number): Promise<void> {
  while (!shouldStop()) {
    samples.push(await samplePostgres())
    await sleep(intervalMs)
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
        LEFT(regexp_replace(query, '\\s+', ' ', 'g'), 300) AS query
      FROM pg_stat_statements
      WHERE query ILIKE '%juhe\\_%' ESCAPE '\\'
      ORDER BY max_exec_time DESC
      LIMIT 20
    `)
    return result.rows.map((row) => ({ ...row }))
  } catch {
    return []
  }
}

function buildReport(input: {
  input: MixedLoadConfig
  startedAt: Date
  finishedAt: Date
  durationMs: number
  seeded: SeededGateway
  metrics: RequestMetric[]
  postgresSamples: PostgresSample[]
  deadlocksBefore: number
  deadlocksAfter: number
  storageBefore: StorageSnapshot
  storageAfter: StorageSnapshot
  slowStatements: Array<Record<string, unknown>>
  upstreamRuntime: UpstreamRuntime
}): Record<string, unknown> & { pass: boolean; violations: string[] } {
  const seedWrite = summarize(input.metrics.filter((metric) => metric.phase === 'seed_write'), Math.max(0.001, input.input.seedWriteRequests / Math.max(1, input.input.seedWriteConcurrency)))
  const baselineRead = summarize(input.metrics.filter((metric) => metric.phase === 'baseline_read'), input.input.baselineReadSeconds)
  const mixedWrite = summarize(input.metrics.filter((metric) => metric.phase === 'mixed_write'), input.input.mixedSeconds)
  const mixedRead = summarize(input.metrics.filter((metric) => metric.phase === 'mixed_read'), input.input.mixedSeconds)
  const overall = summarize(input.metrics, Math.max(0.001, input.durationMs / 1000))
  const operationSummaries = summarizeByOperation(input.metrics, Math.max(0.001, input.durationMs / 1000))
  const deadlocksDelta = Math.max(0, input.deadlocksAfter - input.deadlocksBefore)
  const usageRecordsDelta = input.storageAfter.usageRecords - input.storageBefore.usageRecords
  const readP95Ratio = baselineRead.p95Ms > 0 ? round(mixedRead.p95Ms / baselineRead.p95Ms, 3) : 0
  const violations: string[] = []

  if (mixedWrite.errorRate > input.input.maxAllowedWriteErrorRate) {
    violations.push(`写入错误率 ${mixedWrite.errorRate} 超过阈值 ${input.input.maxAllowedWriteErrorRate}`)
  }
  if (mixedRead.errorRate > input.input.maxAllowedReadErrorRate) {
    violations.push(`读取错误率 ${mixedRead.errorRate} 超过阈值 ${input.input.maxAllowedReadErrorRate}`)
  }
  if (mixedRead.p95Ms > input.input.maxAllowedMixedReadP95Ms) {
    violations.push(`混合读取 P95 ${mixedRead.p95Ms}ms 超过阈值 ${input.input.maxAllowedMixedReadP95Ms}ms`)
  }
  if (mixedWrite.p95Ms > input.input.maxAllowedMixedWriteP95Ms) {
    violations.push(`混合写入 P95 ${mixedWrite.p95Ms}ms 超过阈值 ${input.input.maxAllowedMixedWriteP95Ms}ms`)
  }
  if (readP95Ratio > input.input.maxAllowedReadP95Ratio) {
    violations.push(`混合读取 P95/基线 P95=${readP95Ratio} 超过阈值 ${input.input.maxAllowedReadP95Ratio}`)
  }
  if (deadlocksDelta > input.input.maxAllowedDeadlocks) {
    violations.push(`Postgres deadlocks ${deadlocksDelta} 超过阈值 ${input.input.maxAllowedDeadlocks}`)
  }
  if (usageRecordsDelta <= 0) {
    violations.push('真实入口写入未观察到 usage_records 落库增量')
  }

  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    config: input.input,
    run: {
      runId,
      tracePrefix,
      startedAt: input.startedAt.toISOString(),
      finishedAt: input.finishedAt.toISOString(),
      durationMs: round(input.durationMs)
    },
    seeded: {
      apiKeyId: input.seeded.apiKeyId,
      routeStrategyId: input.seeded.routeStrategyId,
      groupId: input.seeded.groupId,
      accountCount: input.seeded.accountIds.length
    },
    phases: {
      seedWrite,
      baselineRead,
      mixedWrite,
      mixedRead,
      readP95Ratio
    },
    overall,
    operations: operationSummaries,
    storage: {
      before: input.storageBefore,
      after: input.storageAfter,
      usageRecordsDelta,
      usageCatalogEntriesDelta: input.storageAfter.usageCatalogEntries - input.storageBefore.usageCatalogEntries,
      auditLogsDelta: input.storageAfter.auditLogs - input.storageBefore.auditLogs,
      publicApiLogsDelta: input.storageAfter.publicApiLogs - input.storageBefore.publicApiLogs
    },
    postgres: {
      deadlocksBefore: input.deadlocksBefore,
      deadlocksAfter: input.deadlocksAfter,
      deadlocksDelta,
      maxActive: maxSample(input.postgresSamples, 'active'),
      maxIdleInTransaction: maxSample(input.postgresSamples, 'idleInTransaction'),
      maxLockWaiters: maxSample(input.postgresSamples, 'lockWaiters'),
      maxNotGrantedLocks: maxSample(input.postgresSamples, 'notGrantedLocks'),
      maxXactAgeSeconds: round(maxSample(input.postgresSamples, 'maxXactAgeSeconds')),
      maxActiveQuerySeconds: round(maxSample(input.postgresSamples, 'maxActiveQuerySeconds')),
      samples: input.postgresSamples,
      slowStatements: input.slowStatements
    },
    upstream: {
      totalRequests: input.upstreamRuntime.totalRequests,
      activeRequests: input.upstreamRuntime.activeRequests,
      peakActiveRequests: input.upstreamRuntime.peakActiveRequests,
      pathCounts: Object.fromEntries(input.upstreamRuntime.pathCounts),
      connections: connectionStats(input.upstreamRuntime.connections)
    },
    pass: violations.length === 0,
    violations
  }
}

function outputReport(report: Record<string, unknown> & { pass: boolean; violations: string[] }): void {
  const phases = report.phases as Record<string, RequestSummary | number>
  const baselineRead = phases.baselineRead as RequestSummary
  const mixedRead = phases.mixedRead as RequestSummary
  const mixedWrite = phases.mixedWrite as RequestSummary
  console.log('真实入口读写混合压测结果')
  console.log(`- pass=${report.pass}`)
  console.log(`- mixedWrite: count=${mixedWrite.count} rps=${mixedWrite.requestsPerSecond} p50=${mixedWrite.p50Ms}ms p95=${mixedWrite.p95Ms}ms p99=${mixedWrite.p99Ms}ms errorRate=${mixedWrite.errorRate}`)
  console.log(`- baselineRead: count=${baselineRead.count} rps=${baselineRead.requestsPerSecond} p50=${baselineRead.p50Ms}ms p95=${baselineRead.p95Ms}ms p99=${baselineRead.p99Ms}ms errorRate=${baselineRead.errorRate}`)
  console.log(`- mixedRead: count=${mixedRead.count} rps=${mixedRead.requestsPerSecond} p50=${mixedRead.p50Ms}ms p95=${mixedRead.p95Ms}ms p99=${mixedRead.p99Ms}ms errorRate=${mixedRead.errorRate} p95Ratio=${phases.readP95Ratio}`)
  if (report.violations.length > 0) {
    console.log(`- violations=${report.violations.join('；')}`)
  }
}

function summarize(metrics: RequestMetric[], durationSeconds: number): RequestSummary {
  const sorted = metrics.map((metric) => metric.latencyMs).sort((a, b) => a - b)
  const statuses: Record<string, number> = {}
  for (const metric of metrics) {
    statuses[String(metric.status)] = (statuses[String(metric.status)] ?? 0) + 1
  }
  const ok = metrics.filter((metric) => metric.ok).length
  const errors = metrics.length - ok
  return {
    count: metrics.length,
    ok,
    errors,
    errorRate: metrics.length > 0 ? round(errors / metrics.length, 4) : 0,
    requestsPerSecond: round(metrics.length / Math.max(0.001, durationSeconds)),
    p50Ms: percentile(sorted, 0.5),
    p95Ms: percentile(sorted, 0.95),
    p99Ms: percentile(sorted, 0.99),
    maxMs: sorted.length > 0 ? round(sorted[sorted.length - 1] ?? 0) : 0,
    statuses
  }
}

function summarizeByOperation(metrics: RequestMetric[], durationSeconds: number): Record<string, RequestSummary> {
  const buckets = new Map<string, RequestMetric[]>()
  for (const metric of metrics) {
    const key = `${metric.phase} ${metric.operation}`
    const bucket = buckets.get(key) ?? []
    bucket.push(metric)
    buckets.set(key, bucket)
  }
  return Object.fromEntries([...buckets.entries()].map(([key, bucket]) => [key, summarize(bucket, durationSeconds)]))
}

async function cleanupFixtureAndRecords(seeded: SeededGateway): Promise<void> {
  const pool = await getPostgresPool()
  const traceLike = `${tracePrefix}-%`
  const shardRows = await pool.query('SELECT DISTINCT shard_key FROM juhe_usage.usage_record_shard_entries WHERE trace_id LIKE $1', [traceLike])
  const shardKeys = shardRows.rows.map((row) => row.shard_key).filter(isNonEmptyString)
  const auditIds = await pool.query('SELECT id FROM juhe_dataset.audit_logs WHERE trace_id LIKE $1', [traceLike])
  const auditLogIds = auditIds.rows.map((row) => row.id).filter(isNonEmptyString)
  if (auditLogIds.length > 0) {
    await pool.query('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY($1::text[])', [auditLogIds])
    await pool.query('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY($1::text[])', [auditLogIds])
    await pool.query('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY($1::text[])', [auditLogIds])
    await pool.query(`
      DELETE FROM juhe_dataset.audit_payload_blobs blobs
      WHERE NOT EXISTS (
        SELECT 1
        FROM juhe_dataset.audit_payload_refs refs
        WHERE refs.headers_blob_id = blobs.id OR refs.body_blob_id = blobs.id
      )
    `)
  }
  await pool.query('DELETE FROM juhe_usage.usage_record_shard_entries WHERE trace_id LIKE $1', [traceLike])
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE trace_id LIKE $1', [traceLike])
  if (seeded.accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_usage.usage_record_account_shards WHERE account_id = ANY($1::text[])', [seeded.accountIds])
  }
  await pool.query('DELETE FROM juhe_usage.usage_record_api_key_shards WHERE api_key_id = $1', [seeded.apiKeyId])
  if (shardKeys.length > 0) {
    await pool.query(`
      DELETE FROM juhe_usage.usage_record_shards shards
      WHERE shard_key = ANY($1::text[])
        AND NOT EXISTS (
          SELECT 1 FROM juhe_usage.usage_record_shard_entries entries WHERE entries.shard_key = shards.shard_key
        )
    `, [shardKeys])
  }
  await pool.query('DELETE FROM juhe_dataset.public_api_logs WHERE trace_id LIKE $1', [traceLike])

  await pool.query('DELETE FROM juhe_business.api_keys WHERE id = $1', [seeded.apiKeyId])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = $1', [seeded.routeStrategyId])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = $1', [seeded.routeStrategyId])
  if (seeded.accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [seeded.accountIds])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[]) OR group_id = $2', [seeded.accountIds, seeded.groupId])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [seeded.accountIds])
  }
  await pool.query('DELETE FROM juhe_business.groups WHERE id = $1', [seeded.groupId])
}

async function cleanupStaleFixtures(): Promise<void> {
  const pool = await getPostgresPool()
  const rows = await pool.query(`
    SELECT id, 'api_key' AS kind FROM juhe_business.api_keys WHERE name LIKE '混合压测网关Key-%'
    UNION ALL
    SELECT id, 'account' AS kind FROM juhe_business.accounts WHERE name LIKE '混合压测网关账户-%'
    UNION ALL
    SELECT id, 'group' AS kind FROM juhe_business.groups WHERE name LIKE '混合压测网关分组-%'
  `)
  const apiKeyIds = rows.rows.filter((row) => row.kind === 'api_key').map((row) => row.id).filter(isNonEmptyString)
  const accountIds = rows.rows.filter((row) => row.kind === 'account').map((row) => row.id).filter(isNonEmptyString)
  const groupIds = rows.rows.filter((row) => row.kind === 'group').map((row) => row.id).filter(isNonEmptyString)
  for (const apiKeyId of apiKeyIds) {
    const routeRows = await pool.query('SELECT route_strategy_id FROM juhe_business.api_keys WHERE id = $1', [apiKeyId])
    await pool.query('DELETE FROM juhe_business.api_keys WHERE id = $1', [apiKeyId])
    for (const row of routeRows.rows) {
      const routeStrategyId = row.route_strategy_id
      if (!isNonEmptyString(routeStrategyId)) continue
      await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = $1', [routeStrategyId])
      await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = $1', [routeStrategyId])
    }
  }
  if (accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  for (const groupId of groupIds) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = $1', [groupId])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE group_id = $1', [groupId])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = $1', [groupId])
  }
}

function validateRuntime(): void {
  assert.notEqual(process.env.NODE_ENV, 'production', '真实入口读写混合压测禁止在 NODE_ENV=production 下运行')
  assert.equal(runtimeConfig.runtimeMode, 'performance', '真实入口读写混合压测需要 JUHE_AI_RUNTIME_MODE=performance')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '真实入口读写混合压测需要 JUHE_AI_DATABASE_DRIVER=postgres')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '真实入口读写混合压测需要 JUHE_AI_CACHE_DRIVER=redis')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '真实入口读写混合压测需要 JUHE_AI_RUNTIME_STATE_DRIVER=redis')
  assert.equal(runtimeConfig.queueDriver, 'redis_stream', '真实入口读写混合压测需要 JUHE_AI_QUEUE_DRIVER=redis_stream')
  assert.ok(runtimeConfig.postgres.url, '真实入口读写混合压测需要 JUHE_AI_POSTGRES_URL')
  assert.ok(runtimeConfig.redis.cacheUrl, '真实入口读写混合压测需要 JUHE_AI_REDIS_CACHE_URL')
  assert.ok(runtimeConfig.redis.stateUrl, '真实入口读写混合压测需要 JUHE_AI_REDIS_STATE_URL')
  assert.ok(runtimeConfig.redis.queueUrl, '真实入口读写混合压测需要 JUHE_AI_REDIS_QUEUE_URL')
  assert.equal(process.env.JUHE_AI_MIXED_LOAD_ALLOW_SETTINGS_WRITE, '1', '真实入口读写混合压测会写入临时系统设置，必须显式设置 JUHE_AI_MIXED_LOAD_ALLOW_SETTINGS_WRITE=1')
  assert.equal(process.env.JUHE_AI_MIXED_LOAD_ALLOW_PRIVATE_UPSTREAM, '1', '真实入口读写混合压测使用本机 mock upstream，必须显式设置 JUHE_AI_MIXED_LOAD_ALLOW_PRIVATE_UPSTREAM=1')
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
}

function loadConfig(): MixedLoadConfig {
  const defaultReport = resolve('reports', `usage-real-entry-mixed-load-${runId}.json`)
  return {
    baselineReadSeconds: envInteger('JUHE_AI_MIXED_LOAD_BASELINE_READ_SECONDS', 20, 1, 3600),
    mixedSeconds: envInteger('JUHE_AI_MIXED_LOAD_SECONDS', 60, 1, 7200),
    seedWriteRequests: envInteger('JUHE_AI_MIXED_LOAD_SEED_WRITES', 500, 0, 1_000_000),
    seedWriteConcurrency: envInteger('JUHE_AI_MIXED_LOAD_SEED_WRITE_CONCURRENCY', 32, 1, 5000),
    writeConcurrency: envInteger('JUHE_AI_MIXED_LOAD_WRITE_CONCURRENCY', 64, 1, 5000),
    readConcurrency: envInteger('JUHE_AI_MIXED_LOAD_READ_CONCURRENCY', 32, 1, 5000),
    requestTimeoutMs: envInteger('JUHE_AI_MIXED_LOAD_REQUEST_TIMEOUT_MS', 30_000, 100, 600_000),
    sampleIntervalMs: envInteger('JUHE_AI_MIXED_LOAD_SAMPLE_INTERVAL_MS', 1000, 250, 300_000),
    upstreamLatencyMs: envInteger('JUHE_AI_MIXED_LOAD_UPSTREAM_LATENCY_MS', 50, 0, 600_000),
    upstreamBodyBytes: envInteger('JUHE_AI_MIXED_LOAD_UPSTREAM_BODY_BYTES', 512, 0, 2 * 1024 * 1024),
    accountCount: envInteger('JUHE_AI_MIXED_LOAD_ACCOUNT_COUNT', 32, 1, 1000),
    accountConcurrencyLimit: envInteger('JUHE_AI_MIXED_LOAD_ACCOUNT_CONCURRENCY', 10000, 1, 1_000_000),
    model: envText('JUHE_AI_MIXED_LOAD_MODEL', 'gpt-5-mini'),
    promptBytes: envInteger('JUHE_AI_MIXED_LOAD_PROMPT_BYTES', 64, 1, 1024 * 1024),
    settleSeconds: envInteger('JUHE_AI_MIXED_LOAD_SETTLE_SECONDS', 10, 0, 600),
    cleanup: envBoolean('JUHE_AI_MIXED_LOAD_CLEANUP', true),
    maxAllowedWriteErrorRate: envFloat('JUHE_AI_MIXED_LOAD_MAX_WRITE_ERROR_RATE', 0.01, 0, 1),
    maxAllowedReadErrorRate: envFloat('JUHE_AI_MIXED_LOAD_MAX_READ_ERROR_RATE', 0.01, 0, 1),
    maxAllowedMixedReadP95Ms: envFloat('JUHE_AI_MIXED_LOAD_MAX_READ_P95_MS', 1500, 1, 600_000),
    maxAllowedMixedWriteP95Ms: envFloat('JUHE_AI_MIXED_LOAD_MAX_WRITE_P95_MS', 3000, 1, 600_000),
    maxAllowedReadP95Ratio: envFloat('JUHE_AI_MIXED_LOAD_MAX_READ_P95_RATIO', 3, 0.1, 100),
    maxAllowedDeadlocks: envInteger('JUHE_AI_MIXED_LOAD_MAX_DEADLOCKS', 0, 0, 1000),
    reportPath: envText('JUHE_AI_MIXED_LOAD_REPORT_PATH', defaultReport)
  }
}

function createConnectionTracker(): ConnectionTracker {
  return {
    acceptedSockets: 0,
    closedSockets: 0,
    activeSockets: new Set(),
    socketIds: new WeakMap(),
    requestsBySocketId: new Map(),
    peakActiveSockets: 0
  }
}

function attachConnectionTracker(server: http.Server, tracker: ConnectionTracker): void {
  server.on('connection', (socket: Socket) => {
    tracker.acceptedSockets += 1
    tracker.activeSockets.add(socket)
    tracker.socketIds.set(socket, tracker.acceptedSockets)
    tracker.peakActiveSockets = Math.max(tracker.peakActiveSockets, tracker.activeSockets.size)
    socket.once('close', () => {
      tracker.closedSockets += 1
      tracker.activeSockets.delete(socket)
    })
  })
}

function recordSocketRequest(tracker: ConnectionTracker, socket: Socket): void {
  const socketId = tracker.socketIds.get(socket)
  if (!socketId) return
  tracker.requestsBySocketId.set(socketId, (tracker.requestsBySocketId.get(socketId) ?? 0) + 1)
}

function connectionStats(tracker: ConnectionTracker): Record<string, number> {
  const counts = [...tracker.requestsBySocketId.values()]
  const totalRequests = counts.reduce((total, count) => total + count, 0)
  return {
    acceptedSockets: tracker.acceptedSockets,
    closedSockets: tracker.closedSockets,
    activeSockets: tracker.activeSockets.size,
    peakActiveSockets: tracker.peakActiveSockets,
    socketsWithRequests: counts.length,
    reusedSockets: counts.filter((count) => count > 1).length,
    maxRequestsPerSocket: counts.length ? Math.max(...counts) : 0,
    avgRequestsPerSocket: counts.length ? round(totalRequests / counts.length) : 0
  }
}

function listen(server: http.Server): Promise<void> {
  server.listen({ host: '127.0.0.1', port: 0, backlog: 8192 })
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, reject) => {
    server.once('listening', resolvePromise)
    server.once('error', reject)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('server address unavailable')
  }
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return
  await new Promise<void>((resolvePromise) => {
    server.close(() => resolvePromise())
  })
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  await new Promise<void>((resolvePromise, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
  const address = server.address()
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
  if (!address || typeof address === 'string') {
    throw new Error('无法获取可用端口')
  }
  return address.port
}

async function waitForHealth(url: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  let lastError: unknown
  while (Date.now() - startedAt < 30_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出：exitCode=${child.exitCode}\nstdout=${childOutput.stdout}\nstderr=${childOutput.stderr}`)
    }
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1000) })
      if (response.ok) return
      const body = await response.text().catch(() => '')
      lastError = new Error(`${url} HTTP ${response.status}${body ? ` body=${tailText(body)}` : ''}`)
    } catch (error) {
      lastError = error
    }
    await sleep(250)
  }
  throw new Error(`等待健康检查超时：${lastError instanceof Error ? lastError.message : String(lastError)}\nstdout=${childOutput.stdout}\nstderr=${childOutput.stderr}`)
}

async function stopProcessTree(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await new Promise<void>((resolvePromise) => {
      const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore' })
      killer.once('error', () => resolvePromise())
      killer.once('exit', () => resolvePromise())
    })
  } else {
    child.kill('SIGTERM')
  }
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      resolvePromise()
    }, 5000)
    child.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
}

function promptText(bytes: number, requestId: string): string {
  const prefix = `mixed-load-${requestId}-`
  if (bytes <= prefix.length) return prefix.slice(0, Math.max(1, bytes))
  return prefix + 'x'.repeat(bytes - prefix.length)
}

function responseText(bytes: number): string {
  if (bytes <= 0) return ''
  return 'y'.repeat(bytes)
}

function percentile(sorted: number[], pct: number): number {
  if (sorted.length === 0) return 0
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * pct) - 1))
  return round(sorted[index] ?? 0)
}

function maxSample(samples: PostgresSample[], key: keyof PostgresSample): number {
  return samples.reduce((max, sample) => {
    const value = sample[key]
    return typeof value === 'number' ? Math.max(max, value) : max
  }, 0)
}

function round(value: number, digits = 2): number {
  const scale = 10 ** digits
  return Math.round(value * scale) / scale
}

function numberValue(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string' && value.trim().length > 0) {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return 0
}

function increment(map: Map<string, number>, key: string): void {
  map.set(key, (map.get(key) ?? 0) + 1)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function tailText(value: string, maxChars = 20_000): string {
  return value.length > maxChars ? value.slice(-maxChars) : value
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function envText(name: string, fallback: string): string {
  const value = process.env[name]
  return value && value.trim().length > 0 ? value.trim() : fallback
}

function envBoolean(name: string, fallback: boolean): boolean {
  const value = process.env[name]
  if (value === undefined) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value.toLowerCase())) return true
  if (['0', 'false', 'no', 'off'].includes(value.toLowerCase())) return false
  return fallback
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = process.env[name]
  if (!value) return fallback
  const parsed = Number(value)
  if (!Number.isInteger(parsed)) return fallback
  return Math.min(max, Math.max(min, parsed))
}

function envFloat(name: string, fallback: number, min: number, max: number): number {
  const value = process.env[name]
  if (!value) return fallback
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  return Math.min(max, Math.max(min, parsed))
}
