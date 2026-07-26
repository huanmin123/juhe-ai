import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import type { Socket } from 'node:net'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { readAuditLogSettings } from '../../modules/audit-logs/audit-log-settings.js'
import { rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync } from '../../modules/model-pricing/published-model-catalog.service.js'
import { logger } from '../../shared/logger.js'
import { redisNamespacedGroup, redisNamespacedKey } from '../../shared/redis-namespace.js'
import { cleanupUnreferencedAuditPayloadBlobsByIdsAsync } from '../../storage/audit-log-payload-blobs.js'
import { closeStorageDatabases } from '../../storage/database.js'
import { findGatewayModelCatalogSnapshotAsync } from '../../storage/gateway-model-catalog-snapshot.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createApiKeyRecordAsync,
  createGroupAsync,
  createRouteStrategyAsync,
  forceActivatePendingAccountAsync,
  getSettingsAsync,
  updateSettingsAsync
} from '../../storage/repositories.js'

type ScenarioName = 'models' | 'responses' | 'chat' | 'responses_stream'
type RequestShape = 'simple' | 'historical_responses'
type PromptSizeProfile = 'fixed' | 'historical'
type PromptContentMode = 'repetitive' | 'deterministic_entropy'
type PromptSizeTierName = 'fixed' | 'historical_5k' | 'historical_500k' | 'historical_2m' | 'historical_4m'

interface PromptSizeTierConfig {
  name: Exclude<PromptSizeTierName, 'fixed'>
  bytes: number
  permille: number
}

interface SelectedPromptSize {
  tier: PromptSizeTierName
  bytes: number
}

interface GatewayLoadConfig {
  targetBaseUrl?: string
  scenarios: ScenarioName[]
  durationSeconds: number
  warmupSeconds: number
  settleSeconds: number
  concurrency: number
  requestStartSpreadMs: number
  requestTimeoutMs: number
  sampleIntervalMs: number
  upstreamLatencyMs: number
  upstreamStreamChunks: number
  upstreamStreamChunkIntervalMs: number
  upstreamStreamTotalMinMs: number
  upstreamStreamTotalMaxMs: number
  upstreamBodyBytes: number
  upstreamErrorRate: number
  accountCount: number
  accountConcurrencyLimit: number
  clientIpConcurrencyLimit?: number
  groupMaxQueueWaitMs: number
  model: string
  promptBytes: number
  promptSizeProfile: PromptSizeProfile
  historicalPromptSizeTiers: PromptSizeTierConfig[]
  promptContentMode: PromptContentMode
  requestShape: RequestShape
  enableStatsWorkerObservation: boolean
  cleanup: boolean
  assertAccountConcurrency: boolean
  maxAllowedErrorRate: number
  maxAllowedNonStreamP95Ms: number
  maxAllowedNonStreamP99Ms?: number
  maxAllowedNonStreamMaxMs?: number
  maxAllowedSseTtfbP99Ms?: number
  maxAllowedSseTtfbMaxMs?: number
  maxAllowedSseTotalP99Ms?: number
  maxAllowedSseTotalMaxMs?: number
  maxAllowedDeadlocks: number
  maxAllowedLockWaiters: number
  maxAllowedKnownCoordinationWaitMs: number
  maxAllowedRedisPending: number
  resetPgStatStatements: boolean
  reportPath: string
}

interface SeededGateway {
  apiKey: string
  apiKeyId: string
  routeStrategyId: string
  groupId: string
  accountIds: string[]
}

interface LoadStats {
  startedAtMs: number
  loadDurationMs: number
  startedRequests: number
  inFlightRequests: number
  peakInFlightRequests: number
  latenciesMs: number[]
  totalRequests: number
  successRequests: number
  failedRequests: number
  statusCounts: Map<string, number>
  errorCounts: Map<string, number>
  statusSamples: Map<string, string>
  responseBytes: number
  successfulTraceIds: string[]
  scenarioStats: Map<ScenarioName, ScenarioLoadStats>
  promptSizeSequence: number
  promptSizeStats: Map<PromptSizeTierName, PromptSizeLoadStats>
}

interface ScenarioLoadStats {
  totalRequests: number
  successRequests: number
  failedRequests: number
  latenciesMs: number[]
  headersLatenciesMs: number[]
  firstByteLatenciesMs: number[]
  terminalSamples: number
  statusCounts: Map<string, number>
  errorCounts: Map<string, number>
  responseBytes: number
}

interface PromptSizeLoadStats {
  count: number
  success: number
  errors: number
  totalPromptBytes: number
  latenciesMs: number[]
  errorCounts: Map<string, number>
}

interface PostgresSample {
  sampledAt: string
  active: number
  idleInTransaction: number
  lockWaiters: number
  notGrantedLocks: number
  maxXactAgeSeconds: number
  maxActiveQuerySeconds: number
  uniqueCoordinationLockWaiters: number
  accountLastUsedCoordinationLockWaiters: number
  otherLockWaiters: number
  lockWaits: PostgresLockWait[]
}

interface PostgresLockWait {
  pid: number
  queryStart?: string
  waitEvent?: string
  waitDurationMs: number
  queryType: 'unique_coordination' | 'account_last_used_coordination' | 'other'
  query: string
}

interface PostgresLockObservation extends PostgresLockWait {
  samples: number
  maxWaitDurationMs: number
}

interface PostgresLockAnalysis {
  maxWaitDurationMs: number
  maxKnownCoordinationWaitDurationMs: number
  maxOtherWaiters: number
  queryTypes: Record<string, number>
  transientKnownCoordinationWaits: PostgresLockObservation[]
  persistentOrSlowKnownCoordinationWaits: PostgresLockObservation[]
  observations: PostgresLockObservation[]
}

interface StorageSnapshot {
  sampledAt: string
  usageRecords: number
  usageCatalogEntries: number
  auditLogs: number
  operationLogs: number
  publicApiLogs: number
  usageStatsTotalsRowsForFixture: number
  statsJobState?: Record<string, unknown>
}

interface RedisStreamSnapshot {
  length: number
  pendingCount: number
  lagCount: number
  backlogCount: number
  minPendingId?: string
  maxPendingId?: string
  consumers: Array<{ name: string; pending: number }>
  error?: string
}

interface RedisStreamsSnapshot {
  sampledAt: string
  pendingCount: number
  backlogCount: number
  usageRecords: RedisStreamSnapshot
  auditLogs: RedisStreamSnapshot
  operationLogs: RedisStreamSnapshot
  publicApiLogs: RedisStreamSnapshot
  recordMaintenance: RedisStreamSnapshot
  error?: string
}

interface AccountConcurrencySnapshot {
  sampledAt: string
  total: number
  byAccount: Record<string, number>
  error?: string
}

interface RedisStreamDeltaSnapshot {
  pendingDelta: number
  backlogDelta: number
  positivePendingDelta: number
  positiveBacklogDelta: number
}

interface RedisStreamsDeltaSnapshot extends RedisStreamDeltaSnapshot {
  usageRecords: RedisStreamDeltaSnapshot
  auditLogs: RedisStreamDeltaSnapshot
  operationLogs: RedisStreamDeltaSnapshot
  publicApiLogs: RedisStreamDeltaSnapshot
  recordMaintenance: RedisStreamDeltaSnapshot
}

interface RedisSampleClient {
  connect(): Promise<unknown>
  sendCommand(command: string[]): Promise<unknown>
  quit?(): Promise<unknown>
  destroy?(): void
  on(event: string, listener: (...args: unknown[]) => void): unknown
}

interface MetricSnapshot {
  elapsedSeconds: number
  requests: {
    total: number
    started: number
    inFlight: number
    peakInFlight: number
    success: number
    failed: number
    qps: number
    successQps: number
    latencyMs: LatencySummary
    statusCounts: Record<string, number>
    errorCounts: Record<string, number>
  }
  upstream: {
    totalRequests: number
    activeRequests: number
    peakActiveRequests: number
    streamRequests: number
    activeStreamRequests: number
    peakActiveStreamRequests: number
    completedStreamRequests: number
    abortedStreamRequests: number
  }
  storage: StorageSnapshot
  postgres: PostgresSample
  redis: RedisStreamsSnapshot
  accountConcurrency: AccountConcurrencySnapshot
}

interface LatencySummary {
  min: number
  avg: number
  p50: number
  p90: number
  p95: number
  p99: number
  max: number
}

interface ScenarioSummary {
  count: number
  success: number
  errors: number
  errorRate: number
  totalLatencyMs: LatencySummary
  statusCounts: Record<string, number>
  errorCounts: Record<string, number>
  responseBytes: number
  stream?: {
    headersSamples: number
    ttfbSamples: number
    terminalSamples: number
    incompleteTerminalSamples: number
    headersLatencyMs: LatencySummary
    ttfbMs: LatencySummary
    totalLatencyMs: LatencySummary
  }
}

interface PromptSizeSummary {
  configuredBytes: number
  configuredPermille: number
  count: number
  success: number
  errors: number
  errorRate: number
  totalPromptBytes: number
  totalLatencyMs: LatencySummary
  errorCounts: Record<string, number>
}

interface UpstreamRuntime {
  totalRequests: number
  activeRequests: number
  peakActiveRequests: number
  streamRequests: number
  activeStreamRequests: number
  peakActiveStreamRequests: number
  completedStreamRequests: number
  abortedStreamRequests: number
  streamDurationsMs: number[]
  pathCounts: Map<string, number>
  connections: ConnectionTracker
}

interface ConnectionTracker {
  acceptedSockets: number
  closedSockets: number
  peakActiveSockets: number
  activeSockets: Set<Socket>
  socketIds: WeakMap<Socket, number>
  requestsBySocketId: Map<number, number>
}

interface ProcessOutput {
  stdout: string
  stderr: string
}

const access = { systemAccountId: 'sys_admin', role: 'super_admin' as const }
const runId = `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`
const tracePrefix = `perf-gateway-${runId}`
const usageRecordRedisStreamKey = redisNamespacedKey('juhe-ai:queue:usage-records')
const usageRecordRedisStreamGroup = redisNamespacedGroup('juhe-ai:usage-record-writers')
const auditLogRedisStreamKey = redisNamespacedKey('juhe-ai:queue:audit-logs')
const auditLogRedisStreamGroup = redisNamespacedGroup('juhe-ai:audit-log-writers')
const operationLogRedisStreamKey = redisNamespacedKey('juhe-ai:queue:operation-logs')
const operationLogRedisStreamGroup = redisNamespacedGroup('juhe-ai:operation-log-writers')
const publicApiLogRedisStreamKey = redisNamespacedKey('juhe-ai:queue:public-api-logs')
const publicApiLogRedisStreamGroup = redisNamespacedGroup('juhe-ai:public-api-log-writers')
const recordMaintenanceRedisStreamKey = redisNamespacedKey('juhe-ai:queue:record-maintenance')
const recordMaintenanceRedisStreamGroup = redisNamespacedGroup('juhe-ai:record-maintenance-writers')
const redisAccountConcurrencySampleScript = `
local now_ms = tonumber(ARGV[1])
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms)
if #expired > 0 then
  redis.call('HDEL', KEYS[2], unpack(expired))
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
if redis.call('HLEN', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2])
end
return redis.call('ZCARD', KEYS[1])
`
const childOutput: ProcessOutput = { stdout: '', stderr: '' }
let deterministicEntropyCorpus = ''

logger.level = 'silent'

const config = loadConfig()
initializePromptContent(config)
let exitCode = 0

try {
  validateRuntime()
  const report = await runGatewayLoadTest(config)
  printReport(report)
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

async function runGatewayLoadTest(input: GatewayLoadConfig): Promise<Record<string, unknown> & { pass: boolean; violations: string[] }> {
  const upstreamRuntime: UpstreamRuntime = {
    totalRequests: 0,
    activeRequests: 0,
    peakActiveRequests: 0,
    streamRequests: 0,
    activeStreamRequests: 0,
    peakActiveStreamRequests: 0,
    completedStreamRequests: 0,
    abortedStreamRequests: 0,
    streamDurationsMs: [],
    pathCounts: new Map(),
    connections: createConnectionTracker()
  }
  let upstreamServer: http.Server | undefined
  let backendProcess: ChildProcess | undefined
  let seeded: SeededGateway | undefined
  let settingsSnapshot: Record<string, unknown> | undefined
  const samples: MetricSnapshot[] = []
  const loadStats = createLoadStats()
  const startedAt = new Date()
  const startedAtMs = performance.now()

  try {
    settingsSnapshot = await getSettingsAsync()
    await applyLoadTestSettings(input)

    upstreamServer = createMockOpenAIUpstream(input, upstreamRuntime)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    await cleanupStaleFixtures()
    seeded = await seedGatewayData(input, upstreamBaseUrl)
    await ensurePublishedModelCatalogFixture(input)

    if (input.resetPgStatStatements) {
      await resetPgStatStatements()
    }
    const deadlocksBefore = await queryDeadlocks()
    const redisBefore = await sampleRedisStreams()
    const storageBefore = await sampleStorage(seeded)

    let baseUrl = input.targetBaseUrl
    if (!input.targetBaseUrl) {
      const port = await freePort()
      backendProcess = startBackendServer(port)
      baseUrl = `http://127.0.0.1:${port}`
    }
    assert.ok(baseUrl)
    await waitForHealth(`${baseUrl}/__aisys__/health`, backendProcess)
    await waitForHealth(`${baseUrl}/__aisys__/api/health`, backendProcess)

    console.log('高性能网关真实链路压测启动')
    console.log(`- 后端：${baseUrl}`)
    console.log(`- 模拟上游：${upstreamBaseUrl}`)
    console.log(`- scenarios=${input.scenarios.join(',')} concurrency=${input.concurrency} duration=${input.durationSeconds}s warmup=${input.warmupSeconds}s settle=${input.settleSeconds}s`)
    console.log(`- promptSizeProfile=${input.promptSizeProfile} promptContentMode=${input.promptContentMode} tiers=${promptSizeTierConfigs(input).map((tier) => `${tier.tier}:${tier.bytes}B/${tier.permille}‰`).join(',')}`)
    console.log(`- upstream stream chunks=${input.upstreamStreamChunks} interval=${input.upstreamStreamChunkIntervalMs}ms randomTotal=${input.upstreamStreamTotalMinMs}-${input.upstreamStreamTotalMaxMs}ms`)
    console.log(`- accounts=${seeded.accountIds.length} queue=${runtimeConfig.queueDriver} cache=${runtimeConfig.cacheDriver} state=${runtimeConfig.runtimeStateDriver}`)

    if (input.warmupSeconds > 0) {
      await runLoadPhase({
        baseUrl,
        apiKey: seeded.apiKey,
        config: input,
        durationSeconds: input.warmupSeconds,
        stats: createLoadStats(),
        record: false
      })
    }

    let stopSampler = false
    loadStats.startedAtMs = performance.now()
    const sampler = sampleLoop({
      stats: loadStats,
      samples,
      seeded,
      upstreamRuntime,
      shouldStop: () => stopSampler,
      intervalMs: input.sampleIntervalMs
    })
    await runLoadPhase({
      baseUrl,
      apiKey: seeded.apiKey,
      config: input,
      durationSeconds: input.durationSeconds,
      stats: loadStats,
      record: true
    })
    loadStats.loadDurationMs = performance.now() - loadStats.startedAtMs
    if (input.settleSeconds > 0) {
      console.log(`请求结束，等待 ${input.settleSeconds}s 观察 Redis Stream/worker 消化`)
      await sleep(input.settleSeconds * 1000)
    }
    stopSampler = true
    await sampler
    samples.push(await collectSnapshot(loadStats, seeded, upstreamRuntime))

    await stopProcessTree(backendProcess)
    backendProcess = undefined
    if (input.cleanup) {
      await quiesceGatewayFixture(seeded)
      await sleep(2000)
    }
    await closeServer(upstreamServer)
    upstreamServer = undefined

    const deadlocksAfter = await queryDeadlocks()
    const redisAfter = await sampleRedisStreams()
    const storageAfter = await sampleStorage(seeded)
    const postgresSamples = samples.map((sample) => sample.postgres)
    const slowStatements = await querySlowStatements()
    const finishedAt = new Date()
    const durationMs = performance.now() - startedAtMs
    const report = buildReport({
      input,
      startedAt,
      finishedAt,
      durationMs,
      seeded,
      stats: loadStats,
      samples,
      deadlocksBefore,
      deadlocksAfter,
      redisBefore,
      redisAfter,
      storageBefore,
      storageAfter,
      postgresSamples,
      slowStatements,
      upstreamRuntime
    })
    mkdirSync(dirname(input.reportPath), { recursive: true })
    writeFileSync(input.reportPath, JSON.stringify(report, null, 2), 'utf8')
    console.log(`压测报告已写入：${input.reportPath}`)
    return report
  } finally {
    await stopProcessTree(backendProcess)
    if (input.cleanup && seeded && upstreamServer) {
      await quiesceGatewayFixture(seeded)
      await sleep(2000)
    }
    await closeServer(upstreamServer)
    if (settingsSnapshot) {
      await restoreLoadTestSettings(settingsSnapshot).catch((error) => {
        console.error(`恢复压测前系统设置失败：${error instanceof Error ? error.message : String(error)}`)
      })
    }
    if (input.cleanup && seeded) {
      await cleanupFixtureAndRecords(seeded)
    }
  }
}

async function seedGatewayData(input: GatewayLoadConfig, upstreamBaseUrl: string): Promise<SeededGateway> {
  const suffix = runId.replace(/[^a-zA-Z0-9-]/g, '')
  const group = await createGroupAsync({
    name: `压测网关分组-${suffix}`,
    providerCode: 'gpt',
    description: 'performance gateway load test group',
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: input.accountConcurrencyLimit,
      maxQueueWaitMs: input.groupMaxQueueWaitMs,
      clientIpConcurrencyLimit: input.clientIpConcurrencyLimit ?? input.accountConcurrencyLimit,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: Math.max(1, Math.min(100, Math.ceil(input.accountConcurrencyLimit / 10)))
    }
  }, access)
  const accountIds: string[] = []
  for (let index = 0; index < input.accountCount; index += 1) {
    const account = await createAccountAsync({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `压测网关账户-${suffix}-${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-perf-gateway-${suffix}-${index + 1}`,
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
      notes: `performance gateway load test ${runId}`
    }, access)
    const activation = await forceActivatePendingAccountAsync(account.id, access)
    assert.equal(activation.account?.status, 'active', `压测账号未能进入 active：${account.id}`)
    assert.equal(activation.account?.schedulable, true, `压测账号未能进入 schedulable：${account.id}`)
    accountIds.push(account.id)
  }
  const routeStrategy = await createRouteStrategyAsync({
    name: `压测网关策略-${suffix}`,
    description: 'performance gateway load test route strategy',
    mode: 'normal',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 100, status: 'active' }],
    status: 'active'
  }, access)
  const apiKey = await createApiKeyRecordAsync({
    name: `压测网关Key-${suffix}`,
    description: 'performance gateway load test key',
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

async function ensurePublishedModelCatalogFixture(input: GatewayLoadConfig): Promise<void> {
  if (!input.scenarios.includes('models')) return
  const personalSnapshot = await findGatewayModelCatalogSnapshotAsync({
    systemAccountId: access.systemAccountId,
    protocol: 'openai',
    variant: 'default'
  })
  const globalSnapshot = personalSnapshot
    ? undefined
    : await findGatewayModelCatalogSnapshotAsync({
        systemAccountId: '',
        protocol: 'openai',
        variant: 'default'
      })
  if (personalSnapshot || globalSnapshot) return

  await rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(access.systemAccountId)
  const rebuiltSnapshot = await findGatewayModelCatalogSnapshotAsync({
    systemAccountId: access.systemAccountId,
    protocol: 'openai',
    variant: 'default'
  })
  assert.ok(rebuiltSnapshot, 'models 压测夹具首次运行必须生成 sys_admin 发布模型目录快照')
}

async function applyLoadTestSettings(input: GatewayLoadConfig): Promise<void> {
  await updateSettingsAsync({
    systemApiRateLimitIpReadPerMinute: 1_000_000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1_000_000,
    systemApiRateLimitIpWritePerMinute: 1_000_000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1_000_000,
    systemApiRateLimitUserReadPerMinute: 1_000_000,
    systemApiRateLimitUserWritePerMinute: 1_000_000,
    statsAggregationIntervalSeconds: input.enableStatsWorkerObservation ? 5 : 3600,
    statsAggregationBatchSize: input.enableStatsWorkerObservation ? 5000 : 1000,
    statsAggregationMaxBatchesPerRun: input.enableStatsWorkerObservation ? 20 : 1,
    groupAccountStatsRefreshIntervalSeconds: input.enableStatsWorkerObservation ? 15 : 3600,
    systemMetricsSampleIntervalSeconds: 5,
    accountQualityRefreshIntervalSeconds: 3600,
    accountHealthCheckIntervalHours: 24,
    cooldownAccountRetestIntervalSeconds: 3600
  })
}

async function restoreLoadTestSettings(snapshot: Record<string, unknown>): Promise<void> {
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
      JUHE_AI_LOG_LEVEL: process.env.JUHE_AI_GATEWAY_LOAD_CHILD_LOG_LEVEL ?? 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: process.env.JUHE_AI_GATEWAY_LOAD_CHILD_LOG_CONSOLE_ENABLED ?? 'false',
      JUHE_AI_LOG_FILE_ENABLED: process.env.JUHE_AI_GATEWAY_LOAD_CHILD_LOG_FILE_ENABLED ?? 'true',
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

async function runLoadPhase(input: {
  baseUrl: string
  apiKey: string
  config: GatewayLoadConfig
  durationSeconds: number
  stats: LoadStats
  record: boolean
}): Promise<void> {
  const endAt = performance.now() + input.durationSeconds * 1000
  await Promise.all(Array.from({ length: input.config.concurrency }, (_, workerIndex) => loadWorker(workerIndex, endAt, input)))
}

async function loadWorker(
  workerIndex: number,
  endAt: number,
  input: {
    baseUrl: string
    apiKey: string
    config: GatewayLoadConfig
    stats: LoadStats
    record: boolean
  }
): Promise<void> {
  if (input.config.requestStartSpreadMs > 0) {
    await sleep(Math.round((workerIndex / Math.max(1, input.config.concurrency)) * input.config.requestStartSpreadMs))
  }
  let sequence = 0
  while (performance.now() < endAt) {
    sequence += 1
    const scenario = input.config.scenarios[(workerIndex + sequence) % input.config.scenarios.length] ?? 'responses'
    const requestId = `${workerIndex}-${sequence}`
    const traceId = `${tracePrefix}-${requestId}`
    const promptSize = scenario === 'models' ? undefined : selectPromptSize(input.stats, input.config)
    const started = performance.now()
    if (input.record) {
      beginLoadRequest(input.stats)
    }
    try {
      const response = await fetchScenarioWithTimeout(input.baseUrl, input.apiKey, scenario, input.config, requestId, promptSize?.bytes)
      if (input.record) {
        input.stats.latenciesMs.push(response.totalLatencyMs)
        input.stats.totalRequests += 1
        input.stats.responseBytes += response.responseBytes
        increment(input.stats.statusCounts, String(response.status))
        rememberStatusSample(input.stats.statusSamples, response.status, response.text)
        recordScenarioResponse(input.stats, scenario, response)
        if (promptSize) {
          recordPromptSizeResponse(input.stats, promptSize, response)
        }
        if (response.ok) {
          input.stats.successRequests += 1
          input.stats.successfulTraceIds.push(traceId)
        } else {
          input.stats.failedRequests += 1
          increment(input.stats.errorCounts, `HTTP ${response.status}`)
        }
      }
    } catch (error) {
      if (input.record) {
        const latencyMs = performance.now() - started
        input.stats.latenciesMs.push(latencyMs)
        input.stats.totalRequests += 1
        input.stats.failedRequests += 1
        const formattedError = formatLoadError(error)
        increment(input.stats.errorCounts, formattedError)
        recordScenarioError(input.stats, scenario, latencyMs, formattedError)
        if (promptSize) {
          recordPromptSizeError(input.stats, promptSize, latencyMs, formattedError)
        }
      }
    } finally {
      if (input.record) {
        finishLoadRequest(input.stats)
      }
    }
  }
}

interface FetchScenarioResult {
  status: number
  ok: boolean
  text: string
  responseBytes: number
  headersLatencyMs: number
  firstByteLatencyMs?: number
  streamTerminalSeen?: boolean
  totalLatencyMs: number
}

async function fetchScenarioWithTimeout(
  baseUrl: string,
  apiKey: string,
  scenario: ScenarioName,
  input: GatewayLoadConfig,
  requestId: string,
  promptBytes = input.promptBytes
): Promise<FetchScenarioResult> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(new Error('请求超时')), input.requestTimeoutMs)
  const startedAt = performance.now()
  try {
    const request = buildScenarioRequest(scenario, input, requestId, promptBytes)
    const response = await fetch(`${baseUrl}${request.path}`, {
      method: request.method,
      headers: {
        authorization: `Bearer ${apiKey}`,
        'x-trace-id': `${tracePrefix}-${requestId}`,
        ...request.headers
      },
      body: request.body,
      signal: controller.signal
    })
    const headersLatencyMs = performance.now() - startedAt
    if (scenario === 'responses_stream') {
      const streamedBody = await readStreamResponseBody(response, startedAt)
      return {
        status: response.status,
        ok: response.ok,
        text: streamedBody.preview,
        responseBytes: streamedBody.bytes,
        headersLatencyMs,
        firstByteLatencyMs: streamedBody.firstByteLatencyMs,
        streamTerminalSeen: streamedBody.terminalSeen,
        totalLatencyMs: performance.now() - startedAt
      }
    }
    const text = await response.text()
    return {
      status: response.status,
      ok: response.ok,
      text,
      responseBytes: Buffer.byteLength(text, 'utf8'),
      headersLatencyMs,
      totalLatencyMs: performance.now() - startedAt
    }
  } finally {
    clearTimeout(timeout)
  }
}

async function readStreamResponseBody(response: Response, startedAt: number): Promise<{
  preview: string
  bytes: number
  firstByteLatencyMs?: number
  terminalSeen: boolean
}> {
  if (!response.body) {
    return { preview: '', bytes: 0, terminalSeen: false }
  }

  const reader = response.body.getReader()
  const previewChunks: Buffer[] = []
  const maxPreviewBytes = 500
  let previewBytes = 0
  let bytes = 0
  let firstByteLatencyMs: number | undefined
  let searchTail = ''
  let terminalSeen = false
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (value.byteLength === 0) continue
    firstByteLatencyMs ??= performance.now() - startedAt
    bytes += value.byteLength
    searchTail = `${searchTail}${Buffer.from(value).toString('utf8')}`.slice(-8192)
    terminalSeen ||= searchTail.includes('event: response.completed')
      || searchTail.includes('"type":"response.completed"')
      || searchTail.includes('data: [DONE]')
    if (previewBytes < maxPreviewBytes) {
      const chunk = Buffer.from(value.subarray(0, maxPreviewBytes - previewBytes))
      previewChunks.push(chunk)
      previewBytes += chunk.byteLength
    }
  }
  return {
    preview: Buffer.concat(previewChunks).toString('utf8'),
    bytes,
    firstByteLatencyMs,
    terminalSeen
  }
}

function buildScenarioRequest(scenario: ScenarioName, input: GatewayLoadConfig, requestId: string, promptBytes: number): {
  method: string
  path: string
  headers?: Record<string, string>
  body?: string
} {
  if (scenario === 'models') {
    return { method: 'GET', path: '/v1/models' }
  }
  if (scenario === 'chat') {
    return {
      method: 'POST',
      path: '/v1/chat/completions',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: input.model,
        messages: [{ role: 'user', content: promptText(promptBytes, requestId, input.promptContentMode) }],
        max_tokens: 16,
        stream: false
      })
    }
  }
  return {
    method: 'POST',
    path: '/v1/responses',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(buildResponsesRequestBody(input, requestId, scenario === 'responses_stream', promptBytes))
  }
}

function buildResponsesRequestBody(
  input: GatewayLoadConfig,
  requestId: string,
  stream: boolean,
  promptBytes: number
): Record<string, unknown> {
  const prompt = promptText(promptBytes, requestId, input.promptContentMode)
  if (input.requestShape === 'historical_responses') {
    return {
      client_metadata: {
        client: 'codex_cli_rs',
        request_id: `load-${requestId}`,
        turn_metadata: { turn_id: requestId }
      },
      include: ['reasoning.encrypted_content'],
      input: [{
        role: 'user',
        content: [{ type: 'input_text', text: prompt }]
      }],
      model: input.model,
      parallel_tool_calls: true,
      prompt_cache_key: `juhe-ai-load-${requestId}`,
      reasoning: { effort: 'medium', summary: 'auto' },
      service_tier: 'auto',
      store: false,
      stream,
      text: { format: { type: 'text' }, verbosity: 'medium' },
      tool_choice: 'auto'
    }
  }
  return {
    model: input.model,
    input: prompt,
    max_output_tokens: 16,
    stream
  }
}

function createMockOpenAIUpstream(input: GatewayLoadConfig, runtime: UpstreamRuntime): http.Server {
  const server = http.createServer((req, res) => {
    recordSocketRequest(runtime.connections, req.socket)
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    runtime.totalRequests += 1
    const finishUpstreamRequest = beginUpstreamRequest(runtime)
    res.once('finish', finishUpstreamRequest)
    res.once('close', finishUpstreamRequest)
    increment(runtime.pathCounts, `${req.method ?? 'GET'} ${url.pathname}`)
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const shouldError = input.upstreamErrorRate > 0 && Math.random() < input.upstreamErrorRate
      setTimeout(() => {
        if (shouldError) {
          sendUpstreamError(res)
          return
        }
        if (url.pathname === '/v1/models') {
          sendModels(res, input)
          return
        }
        if (url.pathname === '/v1/chat/completions') {
          sendChatCompletion(res, input, bodyText, runtime)
          return
        }
        if (url.pathname === '/v1/responses') {
          const stream = parseJsonBody(bodyText).stream === true
          if (stream) {
            sendResponseStream(res, input, runtime)
          } else {
            sendResponseJson(res, input)
          }
          return
        }
        sendResponseJson(res, input)
      }, input.upstreamLatencyMs)
    })
  })
  attachConnectionTracker(server, runtime.connections)
  return server
}

function sendModels(res: http.ServerResponse, input: GatewayLoadConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    object: 'list',
    data: [{ id: input.model, object: 'model', created: 0, owned_by: 'openai' }]
  }))
}

function sendChatCompletion(res: http.ServerResponse, input: GatewayLoadConfig, bodyText: string, runtime: UpstreamRuntime): void {
  if (parseJsonBody(bodyText).stream === true) {
    sendChatCompletionStream(res, input, runtime)
    return
  }
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'chatcmpl_perf_gateway',
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
}

function sendChatCompletionStream(res: http.ServerResponse, input: GatewayLoadConfig, runtime: UpstreamRuntime): void {
  const streamPlan = createStreamPlan(input)
  const finishStream = beginUpstreamStream(runtime)
  res.once('finish', () => finishStream(true))
  res.once('close', () => finishStream(false))
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  let index = 0
  const writeNext = () => {
    if (res.destroyed || res.writableEnded) {
      return
    }
    if (index < input.upstreamStreamChunks) {
      const chunk = {
        id: 'chatcmpl_perf_gateway_stream',
        object: 'chat.completion.chunk',
        created: Math.trunc(Date.now() / 1000),
        model: input.model,
        choices: [{
          index: 0,
          delta: { content: responseText(Math.max(1, Math.ceil(input.upstreamBodyBytes / input.upstreamStreamChunks))) },
          finish_reason: null
        }]
      }
      res.write(`data: ${JSON.stringify(chunk)}\n\n`)
      index += 1
      setTimeout(writeNext, streamPlan.chunkIntervalMs)
      return
    }
    const completed = {
      id: 'chatcmpl_perf_gateway_stream',
      object: 'chat.completion.chunk',
      created: Math.trunc(Date.now() / 1000),
      model: input.model,
      choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
      usage: { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 }
    }
    res.write(`data: ${JSON.stringify(completed)}\n\n`)
    res.write('data: [DONE]\n\n')
    res.end()
  }
  writeNext()
}

function sendResponseJson(res: http.ServerResponse, input: GatewayLoadConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'resp_perf_gateway',
    object: 'response',
    status: 'completed',
    model: input.model,
    output: [{
      type: 'message',
      role: 'assistant',
      content: [{ type: 'output_text', text: responseText(input.upstreamBodyBytes) }]
    }],
    usage: {
      input_tokens: 12,
      output_tokens: 8,
      input_tokens_details: { cached_tokens: 0 }
    }
  }))
}

function sendResponseStream(res: http.ServerResponse, input: GatewayLoadConfig, runtime: UpstreamRuntime): void {
  const streamPlan = createStreamPlan(input)
  const finishStream = beginUpstreamStream(runtime)
  res.once('finish', () => finishStream(true))
  res.once('close', () => finishStream(false))
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  let index = 0
  const writeNext = () => {
    if (res.destroyed || res.writableEnded) {
      return
    }
    if (index < input.upstreamStreamChunks) {
      const event = {
        type: 'response.output_text.delta',
        delta: responseText(Math.max(1, Math.ceil(input.upstreamBodyBytes / input.upstreamStreamChunks)))
      }
      res.write(`event: response.output_text.delta\ndata: ${JSON.stringify(event)}\n\n`)
      index += 1
      setTimeout(writeNext, streamPlan.chunkIntervalMs)
      return
    }
    const completed = {
      type: 'response.completed',
      response: {
        status: 'completed',
        usage: { input_tokens: 12, output_tokens: 8 }
      }
    }
    res.write(`event: response.completed\ndata: ${JSON.stringify(completed)}\n\n`)
    res.end()
  }
  writeNext()
}

function sendUpstreamError(res: http.ServerResponse): void {
  res.writeHead(500, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    error: {
      type: 'server_error',
      code: 'mock_upstream_error',
      message: '模拟上游错误'
    }
  }))
}

function createStreamPlan(input: GatewayLoadConfig): { totalMs: number; chunkIntervalMs: number } {
  const minMs = Math.max(0, Math.trunc(input.upstreamStreamTotalMinMs))
  const maxMs = Math.max(0, Math.trunc(input.upstreamStreamTotalMaxMs))
  if (minMs <= 0 && maxMs <= 0) {
    return {
      totalMs: input.upstreamStreamChunks * input.upstreamStreamChunkIntervalMs,
      chunkIntervalMs: input.upstreamStreamChunkIntervalMs
    }
  }
  const lower = minMs > 0 && maxMs > 0 ? Math.min(minMs, maxMs) : Math.max(minMs, maxMs)
  const upper = Math.max(minMs, maxMs, lower)
  const totalMs = lower === upper
    ? lower
    : lower + Math.floor(Math.random() * (upper - lower + 1))
  return {
    totalMs,
    chunkIntervalMs: Math.max(0, Math.round(totalMs / Math.max(1, input.upstreamStreamChunks)))
  }
}

function beginUpstreamRequest(runtime: UpstreamRuntime): () => void {
  runtime.activeRequests += 1
  runtime.peakActiveRequests = Math.max(runtime.peakActiveRequests, runtime.activeRequests)
  let finished = false
  return () => {
    if (finished) return
    finished = true
    runtime.activeRequests = Math.max(0, runtime.activeRequests - 1)
  }
}

function beginUpstreamStream(runtime: UpstreamRuntime): (completed: boolean) => void {
  const startedAt = performance.now()
  runtime.streamRequests += 1
  runtime.activeStreamRequests += 1
  runtime.peakActiveStreamRequests = Math.max(runtime.peakActiveStreamRequests, runtime.activeStreamRequests)
  let finished = false
  return (completed: boolean) => {
    if (finished) return
    finished = true
    runtime.activeStreamRequests = Math.max(0, runtime.activeStreamRequests - 1)
    if (completed) {
      runtime.completedStreamRequests += 1
    } else {
      runtime.abortedStreamRequests += 1
    }
    runtime.streamDurationsMs.push(performance.now() - startedAt)
  }
}

async function sampleLoop(input: {
  stats: LoadStats
  samples: MetricSnapshot[]
  seeded: SeededGateway
  upstreamRuntime: UpstreamRuntime
  shouldStop: () => boolean
  intervalMs: number
}): Promise<void> {
  while (!input.shouldStop()) {
    input.samples.push(await collectSnapshot(input.stats, input.seeded, input.upstreamRuntime))
    await sleep(input.intervalMs)
  }
}

async function collectSnapshot(stats: LoadStats, seeded: SeededGateway, upstreamRuntime: UpstreamRuntime): Promise<MetricSnapshot> {
  const elapsedSeconds = Math.max(0.001, (performance.now() - stats.startedAtMs) / 1000)
  const loadElapsedSeconds = formalLoadElapsedSeconds(stats, elapsedSeconds)
  const [storage, postgres, redis, accountConcurrency] = await Promise.all([
    sampleStorage(seeded),
    samplePostgres(),
    sampleRedisStreams(),
    sampleAccountConcurrency(seeded)
  ])
  return {
    elapsedSeconds: round(elapsedSeconds, 3),
    requests: {
      total: stats.totalRequests,
      started: stats.startedRequests,
      inFlight: stats.inFlightRequests,
      peakInFlight: stats.peakInFlightRequests,
      success: stats.successRequests,
      failed: stats.failedRequests,
      qps: round(stats.totalRequests / loadElapsedSeconds, 2),
      successQps: round(stats.successRequests / loadElapsedSeconds, 2),
      latencyMs: latencySummary(stats.latenciesMs),
      statusCounts: objectFromCounts(stats.statusCounts),
      errorCounts: objectFromCounts(stats.errorCounts)
    },
    upstream: {
      totalRequests: upstreamRuntime.totalRequests,
      activeRequests: upstreamRuntime.activeRequests,
      peakActiveRequests: upstreamRuntime.peakActiveRequests,
      streamRequests: upstreamRuntime.streamRequests,
      activeStreamRequests: upstreamRuntime.activeStreamRequests,
      peakActiveStreamRequests: upstreamRuntime.peakActiveStreamRequests,
      completedStreamRequests: upstreamRuntime.completedStreamRequests,
      abortedStreamRequests: upstreamRuntime.abortedStreamRequests
    },
    storage,
    postgres,
    redis,
    accountConcurrency
  }
}

async function sampleStorage(seeded: SeededGateway): Promise<StorageSnapshot> {
  const pool = await getPostgresPool()
  const traceLike = `${tracePrefix}-%`
  const [
    usageRows,
    catalogRows,
    auditRows,
    operationRows,
    publicRows,
    statsRows,
    stateRows
  ] = await Promise.all([
    pool.query('SELECT COUNT(*) AS total FROM juhe_usage.usage_records WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_usage.usage_record_shard_entries WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_dataset.audit_logs WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_dataset.operation_logs WHERE trace_id LIKE $1', [traceLike]),
    pool.query('SELECT COUNT(*) AS total FROM juhe_dataset.public_api_logs WHERE trace_id LIKE $1', [traceLike]),
    pool.query(`
      SELECT COUNT(*) AS total
      FROM juhe_stats.usage_stats_totals
      WHERE scope_id = ANY($1::text[])
    `, [[seeded.apiKeyId, seeded.groupId, ...seeded.accountIds]]),
    pool.query(`
      SELECT scope_type, scope_id, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
      FROM juhe_stats.stats_job_state
      WHERE job_name = 'usage_stats_aggregation'
      ORDER BY updated_at DESC
      LIMIT 1
    `)
  ])
  return {
    sampledAt: new Date().toISOString(),
    usageRecords: numberValue(usageRows.rows[0]?.total),
    usageCatalogEntries: numberValue(catalogRows.rows[0]?.total),
    auditLogs: numberValue(auditRows.rows[0]?.total),
    operationLogs: numberValue(operationRows.rows[0]?.total),
    publicApiLogs: numberValue(publicRows.rows[0]?.total),
    usageStatsTotalsRowsForFixture: numberValue(statsRows.rows[0]?.total),
    statsJobState: stateRows.rows[0] ? { ...stateRows.rows[0] } : undefined
  }
}

async function samplePostgres(): Promise<PostgresSample> {
  const pool = await getPostgresPool()
  const [activity, locks, lockWaitRows] = await Promise.all([pool.query(`
    SELECT
      count(*) FILTER (WHERE state = 'active') AS active,
      count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_transaction,
      count(*) FILTER (WHERE wait_event_type = 'Lock') AS lock_waiters,
      COALESCE(max(EXTRACT(EPOCH FROM (now() - xact_start))) FILTER (WHERE xact_start IS NOT NULL), 0) AS max_xact_age_seconds,
      COALESCE(max(EXTRACT(EPOCH FROM (now() - query_start))) FILTER (WHERE state = 'active' AND query_start IS NOT NULL), 0) AS max_active_query_seconds
    FROM pg_stat_activity
    WHERE datname = current_database()
      AND pid <> pg_backend_pid()
  `), pool.query(`
    SELECT count(*) AS not_granted_locks
    FROM pg_locks
    WHERE database = (SELECT oid FROM pg_database WHERE datname = current_database())
      AND granted = false
  `), pool.query(`
    SELECT
      pid,
      query_start,
      wait_event,
      GREATEST(EXTRACT(EPOCH FROM (clock_timestamp() - query_start)) * 1000, 0) AS wait_duration_ms,
      LEFT(regexp_replace(query, '\\s+', ' ', 'g'), 500) AS query
    FROM pg_stat_activity
    WHERE datname = current_database()
      AND pid <> pg_backend_pid()
      AND wait_event_type = 'Lock'
    ORDER BY query_start ASC
    LIMIT 100
  `)])
  const activityRow = activity.rows[0] ?? {}
  const locksRow = locks.rows[0] ?? {}
  const lockWaits = lockWaitRows.rows.map((row) => {
    const query = optionalText(row.query) ?? ''
    const waitEvent = optionalText(row.wait_event)
    return {
      pid: numberValue(row.pid),
      queryStart: row.query_start instanceof Date ? row.query_start.toISOString() : optionalText(row.query_start),
      waitEvent,
      waitDurationMs: round(numberValue(row.wait_duration_ms)),
      queryType: postgresLockWaitQueryType(query, waitEvent),
      query
    } satisfies PostgresLockWait
  })
  return {
    sampledAt: new Date().toISOString(),
    active: numberValue(activityRow.active),
    idleInTransaction: numberValue(activityRow.idle_in_transaction),
    lockWaiters: numberValue(activityRow.lock_waiters),
    notGrantedLocks: numberValue(locksRow.not_granted_locks),
    maxXactAgeSeconds: round(numberValue(activityRow.max_xact_age_seconds)),
    maxActiveQuerySeconds: round(numberValue(activityRow.max_active_query_seconds)),
    uniqueCoordinationLockWaiters: lockWaits.filter((wait) => wait.queryType === 'unique_coordination').length,
    accountLastUsedCoordinationLockWaiters: lockWaits.filter((wait) => wait.queryType === 'account_last_used_coordination').length,
    otherLockWaiters: lockWaits.filter((wait) => wait.queryType === 'other').length,
    lockWaits
  }
}

function postgresLockWaitQueryType(query: string, waitEvent: string | undefined): PostgresLockWait['queryType'] {
  const normalized = query.toLowerCase()
  if (waitEvent?.toLowerCase() !== 'transactionid') return 'other'
  if (normalized.includes('insert ') && normalized.includes('on conflict')) return 'unique_coordination'
  if (
    normalized.includes('update juhe_business.accounts')
    && normalized.includes('set last_used_at =')
    && normalized.includes('(last_used_at is null or last_used_at <')
  ) {
    return 'account_last_used_coordination'
  }
  return 'other'
}

async function sampleRedisStreams(): Promise<RedisStreamsSnapshot> {
  const url = runtimeConfig.redis.queueUrl
  if (!url) {
    const error = 'JUHE_AI_REDIS_QUEUE_URL 未配置'
    return {
      sampledAt: new Date().toISOString(),
      pendingCount: 0,
      backlogCount: 0,
      usageRecords: emptyRedisStreamSnapshot(error),
      auditLogs: emptyRedisStreamSnapshot(error),
      operationLogs: emptyRedisStreamSnapshot(error),
      publicApiLogs: emptyRedisStreamSnapshot(error),
      recordMaintenance: emptyRedisStreamSnapshot(error),
      error
    }
  }
  let client: RedisSampleClient | undefined
  try {
    client = await createRedisSampleClient(url)
    const [usageRecords, auditLogs, operationLogs, publicApiLogs, recordMaintenance] = await Promise.all([
      sampleRedisStream(client, usageRecordRedisStreamKey, usageRecordRedisStreamGroup),
      sampleRedisStream(client, auditLogRedisStreamKey, auditLogRedisStreamGroup),
      sampleRedisStream(client, operationLogRedisStreamKey, operationLogRedisStreamGroup),
      sampleRedisStream(client, publicApiLogRedisStreamKey, publicApiLogRedisStreamGroup),
      sampleRedisStream(client, recordMaintenanceRedisStreamKey, recordMaintenanceRedisStreamGroup)
    ])
    return {
      sampledAt: new Date().toISOString(),
      pendingCount: usageRecords.pendingCount + auditLogs.pendingCount + operationLogs.pendingCount + publicApiLogs.pendingCount + recordMaintenance.pendingCount,
      backlogCount: usageRecords.backlogCount + auditLogs.backlogCount + operationLogs.backlogCount + publicApiLogs.backlogCount + recordMaintenance.backlogCount,
      usageRecords,
      auditLogs,
      operationLogs,
      publicApiLogs,
      recordMaintenance,
      error: usageRecords.error ?? auditLogs.error ?? operationLogs.error ?? publicApiLogs.error ?? recordMaintenance.error
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return {
      sampledAt: new Date().toISOString(),
      pendingCount: 0,
      backlogCount: 0,
      usageRecords: emptyRedisStreamSnapshot(message),
      auditLogs: emptyRedisStreamSnapshot(message),
      operationLogs: emptyRedisStreamSnapshot(message),
      publicApiLogs: emptyRedisStreamSnapshot(message),
      recordMaintenance: emptyRedisStreamSnapshot(message),
      error: message
    }
  } finally {
    await client?.quit?.().catch(() => undefined)
    try {
      client?.destroy?.()
    } catch {
      // destroy() can throw after a clean quit().
    }
  }
}

async function sampleAccountConcurrency(seeded: SeededGateway): Promise<AccountConcurrencySnapshot> {
  const url = runtimeConfig.redis.stateUrl
  const empty = {
    sampledAt: new Date().toISOString(),
    total: 0,
    byAccount: {}
  }
  if (!url) {
    return { ...empty, error: 'JUHE_AI_REDIS_STATE_URL 未配置' }
  }
  let client: RedisSampleClient | undefined
  try {
    client = await createRedisSampleClient(url)
    const entries = await Promise.all(seeded.accountIds.map(async (accountId) => {
      const value = await sampleRedisAccountConcurrency(client!, accountId)
      return [accountId, value] as const
    }))
    const byAccount = Object.fromEntries(entries)
    return {
      sampledAt: new Date().toISOString(),
      total: entries.reduce((total, [, value]) => total + value, 0),
      byAccount
    }
  } catch (error) {
    return {
      ...empty,
      error: error instanceof Error ? error.message : String(error)
    }
  } finally {
    await client?.quit?.().catch(() => undefined)
    try {
      client?.destroy?.()
    } catch {
      // destroy() can throw after a clean quit().
    }
  }
}

async function sampleRedisAccountConcurrency(client: RedisSampleClient, accountId: string): Promise<number> {
  const now = Date.now()
  const result = await client.sendCommand([
    'EVAL',
    redisAccountConcurrencySampleScript,
    '2',
    redisNamespacedKey(`juhe-ai:account-concurrency-v2:${accountId}:total`),
    redisNamespacedKey(`juhe-ai:account-concurrency-v2:${accountId}:metadata`),
    String(now)
  ])
  return numberValue(result)
}

async function sampleRedisStream(client: RedisSampleClient, streamKey: string, groupName: string): Promise<RedisStreamSnapshot> {
  const [lengthResult, pendingRaw, groupsRaw] = await Promise.all([
    redisSampleCommand(client, ['XLEN', streamKey], `XLEN ${streamKey}`),
    redisSampleCommand(client, ['XPENDING', streamKey, groupName], `XPENDING ${streamKey} ${groupName}`),
    redisSampleCommand(client, ['XINFO', 'GROUPS', streamKey], `XINFO GROUPS ${streamKey}`)
  ])
  const pending = parsePendingSummary(pendingRaw)
  const group = parseStreamGroupInfo(groupsRaw, groupName)
  const lagCount = Math.max(0, group.lagCount)
  return {
    length: numberValue(lengthResult.value),
    pendingCount: pending.pendingCount,
    lagCount,
    backlogCount: pending.pendingCount + lagCount,
    minPendingId: pending.minPendingId,
    maxPendingId: pending.maxPendingId,
    consumers: pending.consumers,
    error: lengthResult.error ?? pending.error ?? group.error
  }
}

async function redisSampleCommand(
  client: RedisSampleClient,
  command: string[],
  label: string
): Promise<{ value?: unknown; error?: string }> {
  try {
    return { value: await client.sendCommand(command) }
  } catch (error) {
    return { error: `${label}: ${error instanceof Error ? error.message : String(error)}` }
  }
}

function emptyRedisStreamSnapshot(error?: string): RedisStreamSnapshot {
  return {
    length: 0,
    pendingCount: 0,
    lagCount: 0,
    backlogCount: 0,
    consumers: [],
    error
  }
}

async function createRedisSampleClient(url: string): Promise<RedisSampleClient> {
  const { createClient } = await import('redis')
  const client = createClient({
    url,
    socket: {
      connectTimeout: 3000,
      reconnectStrategy: false
    }
  }) as unknown as RedisSampleClient
  client.on('error', () => {})
  await withTimeout(client.connect(), 5000, 'Redis Stream 采样连接超时')
  return client
}

function parsePendingSummary(value: unknown): {
  pendingCount: number
  minPendingId?: string
  maxPendingId?: string
  consumers: Array<{ name: string; pending: number }>
  error?: string
} {
  if (value && typeof value === 'object' && !Array.isArray(value) && 'error' in value) {
    const error = (value as { error?: unknown }).error
    return {
      pendingCount: 0,
      consumers: [],
      error: error instanceof Error ? error.message : optionalText(error) ?? String(error)
    }
  }
  if (value && typeof value === 'object' && !Array.isArray(value) && 'value' in value) {
    return parsePendingSummary((value as { value?: unknown }).value)
  }
  if (Array.isArray(value)) {
    return {
      pendingCount: numberValue(value[0]),
      minPendingId: optionalText(value[1]),
      maxPendingId: optionalText(value[2]),
      consumers: Array.isArray(value[3])
        ? value[3].flatMap((item) => {
            if (!Array.isArray(item)) return []
            const name = optionalText(item[0])
            return name ? [{ name, pending: numberValue(item[1]) }] : []
          })
        : []
    }
  }
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    const consumers = Array.isArray(record.consumers)
      ? record.consumers.flatMap((item) => {
          if (!item || typeof item !== 'object') return []
          const consumer = item as Record<string, unknown>
          const name = optionalText(consumer.name)
          return name ? [{ name, pending: numberValue(consumer.pending) }] : []
        })
      : []
    return {
      pendingCount: numberValue(record.pending ?? record.pendingCount),
      minPendingId: optionalText(record.firstId ?? record.minPendingId),
      maxPendingId: optionalText(record.lastId ?? record.maxPendingId),
      consumers
    }
  }
  return { pendingCount: 0, consumers: [] }
}

function parseStreamGroupInfo(value: unknown, groupName: string): { lagCount: number; error?: string } {
  if (value && typeof value === 'object' && !Array.isArray(value) && 'error' in value) {
    const error = (value as { error?: unknown }).error
    return {
      lagCount: 0,
      error: error instanceof Error ? error.message : optionalText(error) ?? String(error)
    }
  }
  if (value && typeof value === 'object' && !Array.isArray(value) && 'value' in value) {
    return parseStreamGroupInfo((value as { value?: unknown }).value, groupName)
  }
  const groups = Array.isArray(value) ? value : []
  for (const group of groups) {
    const record = redisInfoGroupRecord(group)
    const name = optionalText(record.name)
    if (name !== groupName) {
      continue
    }
    return {
      lagCount: numberValue(record.lag ?? record.lagCount)
    }
  }
  return { lagCount: 0, error: `未找到 Redis Stream consumer group：${groupName}` }
}

function redisInfoGroupRecord(value: unknown): Record<string, unknown> {
  if (Array.isArray(value)) {
    const record: Record<string, unknown> = {}
    for (let index = 0; index < value.length; index += 2) {
      const key = optionalText(value[index])
      if (key) {
        record[key] = value[index + 1]
      }
    }
    return record
  }
  return value && typeof value === 'object' ? value as Record<string, unknown> : {}
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

async function resetPgStatStatements(): Promise<void> {
  const pool = await getPostgresPool()
  try {
    await pool.query('SELECT pg_stat_statements_reset()')
  } catch {
    // Some deployments expose pg_stat_statements for reads but do not grant reset.
  }
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
  input: GatewayLoadConfig
  startedAt: Date
  finishedAt: Date
  durationMs: number
  seeded: SeededGateway
  stats: LoadStats
  samples: MetricSnapshot[]
  deadlocksBefore: number
  deadlocksAfter: number
  redisBefore: RedisStreamsSnapshot
  redisAfter: RedisStreamsSnapshot
  storageBefore: StorageSnapshot
  storageAfter: StorageSnapshot
  postgresSamples: PostgresSample[]
  slowStatements: Array<Record<string, unknown>>
  upstreamRuntime: UpstreamRuntime
}): Record<string, unknown> & { pass: boolean; violations: string[] } {
  const loadDurationSeconds = Math.max(0.001, input.stats.loadDurationMs / 1000)
  const latency = latencySummary(input.stats.latenciesMs)
  const scenarioSummaries = buildScenarioSummaries(input.stats, input.input.scenarios)
  const promptSizeSummaries = buildPromptSizeSummaries(input.stats, input.input)
  const sseMetrics = scenarioSummaries.responses_stream?.stream
  const totalRequests = input.stats.totalRequests
  const errorRate = totalRequests > 0 ? round(input.stats.failedRequests / totalRequests, 4) : 0
  const maxLockWaiters = maxSample(input.postgresSamples, 'lockWaiters')
  const maxNotGrantedLocks = maxSample(input.postgresSamples, 'notGrantedLocks')
  const maxXactAgeSeconds = round(maxSample(input.postgresSamples, 'maxXactAgeSeconds'))
  const maxActiveQuerySeconds = round(maxSample(input.postgresSamples, 'maxActiveQuerySeconds'))
  const postgresLockAnalysis = analyzePostgresLockWaits(
    input.postgresSamples,
    input.input.maxAllowedKnownCoordinationWaitMs
  )
  const deadlocksDelta = Math.max(0, input.deadlocksAfter - input.deadlocksBefore)
  const usageRecordsDelta = input.storageAfter.usageRecords - input.storageBefore.usageRecords
  const auditLogsDelta = input.storageAfter.auditLogs - input.storageBefore.auditLogs
  const expectedUsageRecords = (['responses', 'chat', 'responses_stream'] as const)
    .reduce((total, scenario) => total + (scenarioSummaries[scenario]?.success ?? 0), 0)
  const auditReconciliation = expectedAuditLogCount(input.stats.successfulTraceIds)
  const redisDelta = redisStreamsDelta(input.redisBefore, input.redisAfter)
  const accountConcurrencySamples = input.samples.map((sample) => sample.accountConcurrency)
  const maxAccountConcurrencyTotal = Math.max(0, ...accountConcurrencySamples.map((sample) => sample.total))
  const accountConcurrencyCapacity = input.seeded.accountIds.length * input.input.accountConcurrencyLimit
  const violations: string[] = []

  if (errorRate > input.input.maxAllowedErrorRate) {
    violations.push(`错误率 ${errorRate} 超过阈值 ${input.input.maxAllowedErrorRate}`)
  }
  for (const scenario of input.input.scenarios) {
    const summary = scenarioSummaries[scenario]
    if (!summary || summary.count === 0) {
      violations.push(`配置场景 ${scenario} 没有正式 load 样本`)
      continue
    }
    if (summary.errors > 0) {
      violations.push(`配置场景 ${scenario} 存在 ${summary.errors} 个错误，正式验收要求零错误`)
    }
    if (scenario === 'chat' || scenario === 'responses') {
      if (summary.totalLatencyMs.p95 > input.input.maxAllowedNonStreamP95Ms) {
        violations.push(`${scenario} P95 ${summary.totalLatencyMs.p95}ms 超过普通请求阈值 ${input.input.maxAllowedNonStreamP95Ms}ms`)
      }
      if (
        input.input.maxAllowedNonStreamP99Ms !== undefined
        && summary.totalLatencyMs.p99 > input.input.maxAllowedNonStreamP99Ms
      ) {
        violations.push(`${scenario} P99 ${summary.totalLatencyMs.p99}ms 超过普通请求阈值 ${input.input.maxAllowedNonStreamP99Ms}ms`)
      }
      if (
        input.input.maxAllowedNonStreamMaxMs !== undefined
        && summary.totalLatencyMs.max > input.input.maxAllowedNonStreamMaxMs
      ) {
        violations.push(`${scenario} Max ${summary.totalLatencyMs.max}ms 超过普通请求阈值 ${input.input.maxAllowedNonStreamMaxMs}ms`)
      }
    }
  }
  if (input.input.scenarios.some((scenario) => scenario !== 'models')) {
    for (const [tier, summary] of Object.entries(promptSizeSummaries)) {
      if (!summary || summary.count === 0) {
        violations.push(`历史请求体档位 ${tier} 没有正式 load 样本`)
      } else if (summary.errors > 0) {
        violations.push(`历史请求体档位 ${tier} 存在 ${summary.errors} 个错误，正式验收要求零错误`)
      }
    }
  }
  if (sseMetrics && sseMetrics.ttfbSamples !== scenarioSummaries.responses_stream?.success) {
    violations.push(`SSE TTFB 样本不完整：ttfb=${sseMetrics.ttfbSamples}, success=${scenarioSummaries.responses_stream?.success ?? 0}`)
  }
  if (sseMetrics && sseMetrics.terminalSamples !== scenarioSummaries.responses_stream?.success) {
    violations.push(`SSE 终态样本不完整：terminal=${sseMetrics.terminalSamples}, success=${scenarioSummaries.responses_stream?.success ?? 0}`)
  }
  if (
    input.input.maxAllowedSseTtfbP99Ms !== undefined
    && sseMetrics
    && sseMetrics.ttfbSamples > 0
    && sseMetrics.ttfbMs.p99 > input.input.maxAllowedSseTtfbP99Ms
  ) {
    violations.push(`SSE TTFB P99 ${sseMetrics.ttfbMs.p99}ms 超过阈值 ${input.input.maxAllowedSseTtfbP99Ms}ms`)
  }
  if (
    input.input.maxAllowedSseTtfbMaxMs !== undefined
    && sseMetrics
    && sseMetrics.ttfbSamples > 0
    && sseMetrics.ttfbMs.max > input.input.maxAllowedSseTtfbMaxMs
  ) {
    violations.push(`SSE TTFB Max ${sseMetrics.ttfbMs.max}ms 超过阈值 ${input.input.maxAllowedSseTtfbMaxMs}ms`)
  }
  if (
    input.input.maxAllowedSseTotalP99Ms !== undefined
    && sseMetrics
    && sseMetrics.totalLatencyMs.p99 > input.input.maxAllowedSseTotalP99Ms
  ) {
    violations.push(`SSE 总时长 P99 ${sseMetrics.totalLatencyMs.p99}ms 超过慢流阈值 ${input.input.maxAllowedSseTotalP99Ms}ms`)
  }
  if (
    input.input.maxAllowedSseTotalMaxMs !== undefined
    && sseMetrics
    && sseMetrics.totalLatencyMs.max > input.input.maxAllowedSseTotalMaxMs
  ) {
    violations.push(`SSE 总时长 Max ${sseMetrics.totalLatencyMs.max}ms 超过慢流阈值 ${input.input.maxAllowedSseTotalMaxMs}ms`)
  }
  if (deadlocksDelta > input.input.maxAllowedDeadlocks) {
    violations.push(`PostgreSQL deadlocks 增量 ${deadlocksDelta} 超过阈值 ${input.input.maxAllowedDeadlocks}`)
  }
  if (postgresLockAnalysis.maxOtherWaiters > input.input.maxAllowedLockWaiters) {
    violations.push(`PostgreSQL 非 unique 协调锁等待过高：maxOtherWaiters=${postgresLockAnalysis.maxOtherWaiters}`)
  }
  if (postgresLockAnalysis.persistentOrSlowKnownCoordinationWaits.length > 0) {
    violations.push(`PostgreSQL 已知协调锁等待持续或超过阈值：count=${postgresLockAnalysis.persistentOrSlowKnownCoordinationWaits.length}, threshold=${input.input.maxAllowedKnownCoordinationWaitMs}ms`)
  }
  if (redisDelta.positiveBacklogDelta > input.input.maxAllowedRedisPending) {
    violations.push(`本轮新增 Redis Stream backlog=${redisDelta.positiveBacklogDelta} 超过阈值 ${input.input.maxAllowedRedisPending}（新增 pending=${redisDelta.positivePendingDelta}，当前总 backlog=${input.redisAfter.backlogCount}）`)
  }
  const redisSamplingErrors = collectRedisSamplingErrors(input.redisBefore, input.redisAfter, input.samples)
  if (redisSamplingErrors.length > 0) {
    violations.push(`Redis 采样失败：${redisSamplingErrors.slice(0, 5).join('；')}`)
  }
  for (const [name, stream] of redisStreamEntries(input.redisAfter)) {
    if (stream.length !== 0 || stream.pendingCount !== 0 || stream.lagCount !== 0 || stream.backlogCount !== 0) {
      violations.push(`settle 后 Redis Stream ${name} 未清零：length=${stream.length}, pending=${stream.pendingCount}, lag=${stream.lagCount}, backlog=${stream.backlogCount}`)
    }
  }
  if (usageRecordsDelta !== expectedUsageRecords) {
    violations.push(`使用记录未精确对账：actual=${usageRecordsDelta}, expected=${expectedUsageRecords}`)
  }
  if (auditLogsDelta !== auditReconciliation.expected) {
    violations.push(`审计日志未精确对账：actual=${auditLogsDelta}, expected=${auditReconciliation.expected}, mode=${auditReconciliation.mode}`)
  }
  const accountConcurrencyError = accountConcurrencySamples.find((sample) => sample.error)
  if (accountConcurrencyError) {
    violations.push(`Redis 账号并发槽采样失败：${accountConcurrencyError.error}`)
  }
  if (input.input.assertAccountConcurrency) {
    if (maxAccountConcurrencyTotal > accountConcurrencyCapacity) {
      violations.push(`Redis 账号并发槽峰值 ${maxAccountConcurrencyTotal} 超过配置总槽位 ${accountConcurrencyCapacity}`)
    }
    const activeStreamMismatch = input.samples.find((sample) => (
      !sample.accountConcurrency.error
      && sample.upstream.activeStreamRequests > 0
      && sample.accountConcurrency.total + 1 < sample.upstream.activeStreamRequests
    ))
    if (activeStreamMismatch) {
      violations.push(`账号并发读数低于上游活跃流：elapsed=${activeStreamMismatch.elapsedSeconds}s redisSlots=${activeStreamMismatch.accountConcurrency.total}, upstreamActiveStream=${activeStreamMismatch.upstream.activeStreamRequests}`)
    }
  }

  return {
    generatedAt: new Date().toISOString(),
    note: '真实 server + DB service + background workers + PostgreSQL + Redis/Redis Stream + 本地 mock 上游压测。cleanup=true 会删除本次夹具与 trace 明细，但已聚合的全局统计可能无法完全回滚，生产环境不要直接打开 stats-worker 观察压测。',
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    config: input.input,
    tracePrefix,
    startedAt: input.startedAt.toISOString(),
    finishedAt: input.finishedAt.toISOString(),
    durationMs: round(input.durationMs, 3),
    seeded: {
      apiKeyId: input.seeded.apiKeyId,
      groupId: input.seeded.groupId,
      accountIds: input.seeded.accountIds,
      accountCount: input.seeded.accountIds.length
    },
    requests: {
      started: input.stats.startedRequests,
      total: totalRequests,
      success: input.stats.successRequests,
      failed: input.stats.failedRequests,
      inFlight: input.stats.inFlightRequests,
      peakInFlight: input.stats.peakInFlightRequests,
      loadDurationMs: round(input.stats.loadDurationMs, 3),
      qps: round(totalRequests / loadDurationSeconds, 2),
      successQps: round(input.stats.successRequests / loadDurationSeconds, 2),
      errorRate,
      latencyMs: latency,
      statusCounts: objectFromCounts(input.stats.statusCounts),
      errorCounts: objectFromCounts(input.stats.errorCounts),
      statusSamples: Object.fromEntries(input.stats.statusSamples.entries()),
      responseBytes: input.stats.responseBytes,
      byScenario: scenarioSummaries,
      byPromptSize: promptSizeSummaries
    },
    storage: {
      before: input.storageBefore,
      after: input.storageAfter,
      delta: {
        usageRecords: usageRecordsDelta,
        usageCatalogEntries: input.storageAfter.usageCatalogEntries - input.storageBefore.usageCatalogEntries,
        auditLogs: auditLogsDelta,
        operationLogs: input.storageAfter.operationLogs - input.storageBefore.operationLogs,
        publicApiLogs: input.storageAfter.publicApiLogs - input.storageBefore.publicApiLogs,
        usageStatsTotalsRowsForFixture: input.storageAfter.usageStatsTotalsRowsForFixture - input.storageBefore.usageStatsTotalsRowsForFixture,
        expectedUsageRecords,
        expectedAuditLogs: auditReconciliation.expected,
        auditReconciliationMode: auditReconciliation.mode
      }
    },
    postgres: {
      deadlocksBefore: input.deadlocksBefore,
      deadlocksAfter: input.deadlocksAfter,
      deadlocksDelta,
      maxActive: maxSample(input.postgresSamples, 'active'),
      maxIdleInTransaction: maxSample(input.postgresSamples, 'idleInTransaction'),
      maxLockWaiters,
      maxNotGrantedLocks,
      maxXactAgeSeconds,
      maxActiveQuerySeconds,
      lockWaitAnalysis: postgresLockAnalysis,
      samples: input.postgresSamples,
      slowStatements: input.slowStatements
    },
    redis: {
      before: input.redisBefore,
      after: input.redisAfter,
      delta: redisDelta,
      accountConcurrency: {
        maxTotal: maxAccountConcurrencyTotal,
        capacity: accountConcurrencyCapacity,
        assertEnabled: input.input.assertAccountConcurrency
      }
    },
    upstream: {
      totalRequests: input.upstreamRuntime.totalRequests,
      activeRequests: input.upstreamRuntime.activeRequests,
      peakActiveRequests: input.upstreamRuntime.peakActiveRequests,
      streamRequests: input.upstreamRuntime.streamRequests,
      activeStreamRequests: input.upstreamRuntime.activeStreamRequests,
      peakActiveStreamRequests: input.upstreamRuntime.peakActiveStreamRequests,
      completedStreamRequests: input.upstreamRuntime.completedStreamRequests,
      abortedStreamRequests: input.upstreamRuntime.abortedStreamRequests,
      streamDurationMs: latencySummary(input.upstreamRuntime.streamDurationsMs),
      pathCounts: objectFromCounts(input.upstreamRuntime.pathCounts),
      connections: connectionStats(input.upstreamRuntime.connections)
    },
    childProcess: {
      stdoutTail: childOutput.stdout,
      stderrTail: childOutput.stderr
    },
    samples: input.samples,
    pass: violations.length === 0,
    violations
  }
}

function buildScenarioSummaries(
  stats: LoadStats,
  configuredScenarios: ScenarioName[]
): Partial<Record<ScenarioName, ScenarioSummary>> {
  const summaries: Partial<Record<ScenarioName, ScenarioSummary>> = {}
  for (const scenario of configuredScenarios) {
    const scenarioStats = stats.scenarioStats.get(scenario)
    const count = scenarioStats?.totalRequests ?? 0
    const totalLatencyMs = latencySummary(scenarioStats?.latenciesMs ?? [])
    const summary: ScenarioSummary = {
      count,
      success: scenarioStats?.successRequests ?? 0,
      errors: scenarioStats?.failedRequests ?? 0,
      errorRate: count > 0 ? round((scenarioStats?.failedRequests ?? 0) / count, 4) : 0,
      totalLatencyMs,
      statusCounts: objectFromCounts(scenarioStats?.statusCounts ?? new Map()),
      errorCounts: objectFromCounts(scenarioStats?.errorCounts ?? new Map()),
      responseBytes: scenarioStats?.responseBytes ?? 0
    }
    if (scenario === 'responses_stream') {
      summary.stream = {
        headersSamples: scenarioStats?.headersLatenciesMs.length ?? 0,
        ttfbSamples: scenarioStats?.firstByteLatenciesMs.length ?? 0,
        terminalSamples: scenarioStats?.terminalSamples ?? 0,
        incompleteTerminalSamples: Math.max(0, (scenarioStats?.successRequests ?? 0) - (scenarioStats?.terminalSamples ?? 0)),
        headersLatencyMs: latencySummary(scenarioStats?.headersLatenciesMs ?? []),
        ttfbMs: latencySummary(scenarioStats?.firstByteLatenciesMs ?? []),
        totalLatencyMs
      }
    }
    summaries[scenario] = summary
  }
  return summaries
}

function buildPromptSizeSummaries(
  stats: LoadStats,
  config: GatewayLoadConfig
): Partial<Record<PromptSizeTierName, PromptSizeSummary>> {
  const summaries: Partial<Record<PromptSizeTierName, PromptSizeSummary>> = {}
  for (const tier of promptSizeTierConfigs(config)) {
    const tierStats = stats.promptSizeStats.get(tier.tier)
    const count = tierStats?.count ?? 0
    summaries[tier.tier] = {
      configuredBytes: tier.bytes,
      configuredPermille: tier.permille,
      count,
      success: tierStats?.success ?? 0,
      errors: tierStats?.errors ?? 0,
      errorRate: count > 0 ? round((tierStats?.errors ?? 0) / count, 4) : 0,
      totalPromptBytes: tierStats?.totalPromptBytes ?? 0,
      totalLatencyMs: latencySummary(tierStats?.latenciesMs ?? []),
      errorCounts: objectFromCounts(tierStats?.errorCounts ?? new Map())
    }
  }
  return summaries
}

function promptSizeTierConfigs(config: GatewayLoadConfig): Array<{
  tier: PromptSizeTierName
  bytes: number
  permille: number
}> {
  if (config.promptSizeProfile === 'fixed') {
    return [{ tier: 'fixed', bytes: config.promptBytes, permille: 1000 }]
  }
  return config.historicalPromptSizeTiers.map((tier) => ({
    tier: tier.name,
    bytes: tier.bytes,
    permille: tier.permille
  }))
}

function expectedAuditLogCount(successfulTraceIds: string[]): { expected: number; mode: string } {
  const settings = readAuditLogSettings()
  if (!settings.enabled) return { expected: 0, mode: 'audit_disabled' }
  if (settings.successHotRetentionHours > 0) {
    return { expected: successfulTraceIds.length, mode: `success_hot_retention_${settings.successHotRetentionHours}h` }
  }
  const threshold = Math.round(settings.successSampleRate * 10_000)
  return {
    expected: successfulTraceIds.filter((traceId) => auditSampleBucket(traceId) < threshold).length,
    mode: `deterministic_success_sample_${settings.successSampleRate}`
  }
}

function auditSampleBucket(traceId: string): number {
  return createHash('sha256').update(traceId).digest().readUInt32BE(0) % 10_000
}

function printReport(report: Record<string, unknown> & { pass: boolean; violations: string[] }): void {
  const requests = report.requests as Record<string, unknown>
  const storage = report.storage as Record<string, unknown>
  const postgres = report.postgres as Record<string, unknown>
  const postgresLockWaitAnalysis = postgres.lockWaitAnalysis as PostgresLockAnalysis | undefined
  const redis = report.redis as Record<string, unknown>
  const redisAfter = redis.after as RedisStreamsSnapshot | undefined
  const redisDelta = redis.delta as RedisStreamsDeltaSnapshot | undefined
  const redisAccountConcurrency = redis.accountConcurrency as Record<string, unknown> | undefined
  const upstream = report.upstream as Record<string, unknown> | undefined
  const usageStream = redisAfter?.usageRecords
  const auditStream = redisAfter?.auditLogs
  const operationStream = redisAfter?.operationLogs
  const publicApiStream = redisAfter?.publicApiLogs
  const recordMaintenanceStream = redisAfter?.recordMaintenance
  const scenarioSummaries = requests.byScenario as Partial<Record<ScenarioName, ScenarioSummary>> | undefined
  const promptSizeSummaries = requests.byPromptSize as Partial<Record<PromptSizeTierName, PromptSizeSummary>> | undefined
  console.log('\n高性能网关压测汇总')
  console.log(`- pass=${report.pass}`)
  console.log(`- requests started=${requests.started} total=${requests.total} success=${requests.success} inFlight=${requests.inFlight} peakInFlight=${requests.peakInFlight} qps=${requests.successQps} p95=${(requests.latencyMs as LatencySummary).p95}ms errorRate=${requests.errorRate}`)
  for (const [scenario, summary] of Object.entries(scenarioSummaries ?? {})) {
    const streamMetrics = summary.stream
      ? ` headersP99=${summary.stream.headersLatencyMs.p99}ms ttfbP99=${summary.stream.ttfbMs.p99}ms ttfbMax=${summary.stream.ttfbMs.max}ms`
      : ''
    console.log(`- scenario=${scenario} count=${summary.count} success=${summary.success} errors=${summary.errors} p95=${summary.totalLatencyMs.p95}ms p99=${summary.totalLatencyMs.p99}ms max=${summary.totalLatencyMs.max}ms${streamMetrics}`)
  }
  for (const [tier, summary] of Object.entries(promptSizeSummaries ?? {})) {
    console.log(`- promptSize=${tier} configuredBytes=${summary.configuredBytes} count=${summary.count} totalPromptBytes=${summary.totalPromptBytes} errors=${summary.errors} p95=${summary.totalLatencyMs.p95}ms p99=${summary.totalLatencyMs.p99}ms max=${summary.totalLatencyMs.max}ms`)
  }
  console.log(`- upstream total=${upstream?.totalRequests ?? 0} peakActive=${upstream?.peakActiveRequests ?? 0} stream=${upstream?.streamRequests ?? 0} peakActiveStream=${upstream?.peakActiveStreamRequests ?? 0} completedStream=${upstream?.completedStreamRequests ?? 0} abortedStream=${upstream?.abortedStreamRequests ?? 0}`)
  console.log(`- storage delta=${JSON.stringify((storage.delta as Record<string, unknown>) ?? {})}`)
  console.log(`- postgres deadlocksDelta=${postgres.deadlocksDelta} maxLockWaiters=${postgres.maxLockWaiters} maxLockWait=${postgresLockWaitAnalysis?.maxWaitDurationMs ?? 0}ms lockQueryTypes=${JSON.stringify(postgresLockWaitAnalysis?.queryTypes ?? {})} maxXactAge=${postgres.maxXactAgeSeconds}s maxActiveQuery=${postgres.maxActiveQuerySeconds}s`)
  console.log(`- redis usageStream length=${usageStream?.length ?? 0} pending=${usageStream?.pendingCount ?? 0} lag=${usageStream?.lagCount ?? 0}; auditStream length=${auditStream?.length ?? 0} pending=${auditStream?.pendingCount ?? 0} lag=${auditStream?.lagCount ?? 0}; operationStream length=${operationStream?.length ?? 0} pending=${operationStream?.pendingCount ?? 0} lag=${operationStream?.lagCount ?? 0}; publicApiStream length=${publicApiStream?.length ?? 0} pending=${publicApiStream?.pendingCount ?? 0} lag=${publicApiStream?.lagCount ?? 0}; recordMaintenanceStream length=${recordMaintenanceStream?.length ?? 0} pending=${recordMaintenanceStream?.pendingCount ?? 0} lag=${recordMaintenanceStream?.lagCount ?? 0}; totalPending=${redisAfter?.pendingCount ?? 0} totalBacklog=${redisAfter?.backlogCount ?? 0}`)
  console.log(`- redis delta positivePending=${redisDelta?.positivePendingDelta ?? 0} positiveBacklog=${redisDelta?.positiveBacklogDelta ?? 0} netBacklogDelta=${redisDelta?.backlogDelta ?? 0}`)
  console.log(`- accountConcurrency maxRedisSlots=${redisAccountConcurrency?.maxTotal ?? 0}/${redisAccountConcurrency?.capacity ?? 0} assert=${redisAccountConcurrency?.assertEnabled === true}`)
  if (report.violations.length > 0) {
    console.log(`- violations=${report.violations.join('；')}`)
  }
}

function redisStreamsDelta(before: RedisStreamsSnapshot, after: RedisStreamsSnapshot): RedisStreamsDeltaSnapshot {
  const usageRecords = redisStreamDelta(before.usageRecords, after.usageRecords)
  const auditLogs = redisStreamDelta(before.auditLogs, after.auditLogs)
  const operationLogs = redisStreamDelta(before.operationLogs, after.operationLogs)
  const publicApiLogs = redisStreamDelta(before.publicApiLogs, after.publicApiLogs)
  const recordMaintenance = redisStreamDelta(before.recordMaintenance, after.recordMaintenance)
  const streams = [usageRecords, auditLogs, operationLogs, publicApiLogs, recordMaintenance]
  return {
    pendingDelta: after.pendingCount - before.pendingCount,
    backlogDelta: after.backlogCount - before.backlogCount,
    positivePendingDelta: streams.reduce((total, stream) => total + stream.positivePendingDelta, 0),
    positiveBacklogDelta: streams.reduce((total, stream) => total + stream.positiveBacklogDelta, 0),
    usageRecords,
    auditLogs,
    operationLogs,
    publicApiLogs,
    recordMaintenance
  }
}

function redisStreamDelta(before: RedisStreamSnapshot, after: RedisStreamSnapshot): RedisStreamDeltaSnapshot {
  const pendingDelta = after.pendingCount - before.pendingCount
  const backlogDelta = after.backlogCount - before.backlogCount
  return {
    pendingDelta,
    backlogDelta,
    positivePendingDelta: Math.max(0, pendingDelta),
    positiveBacklogDelta: Math.max(0, backlogDelta)
  }
}

function redisStreamEntries(snapshot: RedisStreamsSnapshot): Array<[string, RedisStreamSnapshot]> {
  return [
    ['usageRecords', snapshot.usageRecords],
    ['auditLogs', snapshot.auditLogs],
    ['operationLogs', snapshot.operationLogs],
    ['publicApiLogs', snapshot.publicApiLogs],
    ['recordMaintenance', snapshot.recordMaintenance]
  ]
}

function collectRedisSamplingErrors(
  before: RedisStreamsSnapshot,
  after: RedisStreamsSnapshot,
  samples: MetricSnapshot[]
): string[] {
  const errors = new Set<string>()
  for (const [label, snapshot] of [
    ['before', before],
    ...samples.map((sample, index) => [`sample-${index + 1}`, sample.redis] as const),
    ['after', after]
  ] as Array<readonly [string, RedisStreamsSnapshot]>) {
    if (snapshot.error) errors.add(`${label}: ${snapshot.error}`)
    for (const [name, stream] of redisStreamEntries(snapshot)) {
      if (stream.error) errors.add(`${label}/${name}: ${stream.error}`)
    }
  }
  return [...errors]
}

function analyzePostgresLockWaits(samples: PostgresSample[], knownCoordinationWaitThresholdMs: number): PostgresLockAnalysis {
  const observations = new Map<string, PostgresLockObservation>()
  const queryTypes: Record<string, number> = {}
  for (const sample of samples) {
    for (const wait of sample.lockWaits) {
      queryTypes[wait.queryType] = (queryTypes[wait.queryType] ?? 0) + 1
      const key = `${wait.pid}|${wait.queryStart ?? ''}|${wait.queryType}`
      const existing = observations.get(key)
      if (existing) {
        existing.samples += 1
        existing.maxWaitDurationMs = Math.max(existing.maxWaitDurationMs, wait.waitDurationMs)
      } else {
        observations.set(key, {
          ...wait,
          samples: 1,
          maxWaitDurationMs: wait.waitDurationMs
        })
      }
    }
  }
  const entries = [...observations.values()]
  const knownCoordinationWaits = entries.filter((entry) => entry.queryType !== 'other')
  const persistentOrSlowKnownCoordinationWaits = knownCoordinationWaits.filter((entry) => (
    entry.samples > 1 || entry.maxWaitDurationMs > knownCoordinationWaitThresholdMs
  ))
  return {
    maxWaitDurationMs: Math.max(0, ...entries.map((entry) => entry.maxWaitDurationMs)),
    maxKnownCoordinationWaitDurationMs: Math.max(0, ...knownCoordinationWaits.map((entry) => entry.maxWaitDurationMs)),
    maxOtherWaiters: maxSample(samples, 'otherLockWaiters'),
    queryTypes,
    transientKnownCoordinationWaits: knownCoordinationWaits.filter((entry) => !persistentOrSlowKnownCoordinationWaits.includes(entry)),
    persistentOrSlowKnownCoordinationWaits,
    observations: entries
  }
}

async function cleanupFixtureAndRecords(seeded: SeededGateway): Promise<void> {
  const pool = await getPostgresPool()
  const traceLike = `${tracePrefix}-%`
  const shardRows = await pool.query(`
    SELECT DISTINCT shard_key
    FROM juhe_usage.usage_record_shard_entries
    WHERE trace_id LIKE $1
      OR account_id = ANY($2::text[])
      OR api_key_id = $3
      OR group_id = $4
  `, [traceLike, seeded.accountIds, seeded.apiKeyId, seeded.groupId])
  const shardKeys = shardRows.rows.map((row) => row.shard_key).filter((value): value is string => typeof value === 'string' && value.length > 0)
  const auditIds = await pool.query(`
    SELECT id
    FROM juhe_dataset.audit_logs
    WHERE trace_id LIKE $1
      OR account_id = ANY($2::text[])
      OR api_key_id = $3
      OR group_id = $4
  `, [traceLike, seeded.accountIds, seeded.apiKeyId, seeded.groupId])
  const auditLogIds = auditIds.rows.map((row) => row.id).filter((value): value is string => typeof value === 'string' && value.length > 0)
  if (auditLogIds.length > 0) {
    const auditBlobRows = await pool.query(`
      SELECT DISTINCT blob_id
      FROM (
        SELECT headers_blob_id AS blob_id
        FROM juhe_dataset.audit_payload_refs
        WHERE audit_log_id = ANY($1::text[]) AND headers_blob_id IS NOT NULL
        UNION
        SELECT body_blob_id AS blob_id
        FROM juhe_dataset.audit_payload_refs
        WHERE audit_log_id = ANY($1::text[]) AND body_blob_id IS NOT NULL
      ) scoped_blobs
    `, [auditLogIds])
    const auditBlobIds = auditBlobRows.rows.map((row) => row.blob_id).filter(isNonEmptyString)
    await pool.query('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY($1::text[])', [auditLogIds])
    await pool.query('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY($1::text[])', [auditLogIds])
    await pool.query('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY($1::text[])', [auditLogIds])
    await cleanupAuditPayloadBlobsByIds(auditBlobIds)
    const remainingUnreferencedBlobs = await pool.query(`
      SELECT COUNT(*) AS total
      FROM juhe_dataset.audit_payload_blobs blobs
      WHERE blobs.id = ANY($1::text[])
        AND NOT EXISTS (
          SELECT 1
          FROM juhe_dataset.audit_payload_refs refs
          WHERE refs.headers_blob_id = blobs.id OR refs.body_blob_id = blobs.id
        )
    `, [auditBlobIds])
    assert.equal(numberValue(remainingUnreferencedBlobs.rows[0]?.total), 0, '本轮压测遗留了未清理的审计 payload blob')
  }
  await pool.query(`
    DELETE FROM juhe_usage.usage_record_shard_entries
    WHERE trace_id LIKE $1
      OR account_id = ANY($2::text[])
      OR api_key_id = $3
      OR group_id = $4
  `, [traceLike, seeded.accountIds, seeded.apiKeyId, seeded.groupId])
  await pool.query(`
    DELETE FROM juhe_usage.usage_records
    WHERE trace_id LIKE $1
      OR account_id = ANY($2::text[])
      OR api_key_id = $3
      OR group_id = $4
  `, [traceLike, seeded.accountIds, seeded.apiKeyId, seeded.groupId])
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
  await pool.query('DELETE FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id IN (SELECT id FROM juhe_dataset.operation_logs WHERE trace_id LIKE $1)', [traceLike])
  await pool.query('DELETE FROM juhe_dataset.operation_log_targets WHERE operation_log_id IN (SELECT id FROM juhe_dataset.operation_logs WHERE trace_id LIKE $1)', [traceLike])
  await pool.query('DELETE FROM juhe_dataset.operation_log_viewers WHERE operation_log_id IN (SELECT id FROM juhe_dataset.operation_logs WHERE trace_id LIKE $1)', [traceLike])
  await pool.query('DELETE FROM juhe_dataset.operation_logs WHERE trace_id LIKE $1', [traceLike])
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
  await sleep(500)
  const residual = await pool.query(`
    SELECT
      (SELECT COUNT(*) FROM juhe_business.accounts WHERE id = ANY($1::text[])) AS accounts,
      (SELECT COUNT(*) FROM juhe_business.groups WHERE id = $2) AS groups,
      (SELECT COUNT(*) FROM juhe_business.api_keys WHERE id = $3) AS api_keys,
      (SELECT COUNT(*) FROM juhe_business.route_strategies WHERE id = $4) AS route_strategies,
      (SELECT COUNT(*) FROM juhe_usage.usage_record_shard_entries WHERE account_id = ANY($1::text[]) OR api_key_id = $3 OR group_id = $2) AS usage_entries,
      (SELECT COUNT(*) FROM juhe_usage.usage_records WHERE account_id = ANY($1::text[]) OR api_key_id = $3 OR group_id = $2) AS usage_records,
      (SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE account_id = ANY($1::text[]) OR api_key_id = $3 OR group_id = $2) AS audit_logs
  `, [seeded.accountIds, seeded.groupId, seeded.apiKeyId, seeded.routeStrategyId])
  const remaining = residual.rows[0] ?? {}
  for (const [name, count] of Object.entries(remaining)) {
    assert.equal(numberValue(count), 0, `压测清理后仍残留 ${name}=${numberValue(count)}`)
  }
}

async function quiesceGatewayFixture(seeded: SeededGateway): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'disabled',
        schedulable = 0,
        next_health_check_at = NULL,
        updated_at = $2
    WHERE id = ANY($1::text[])
  `, [seeded.accountIds, new Date().toISOString()])
  await pool.query('UPDATE juhe_business.groups SET enabled = 0, updated_at = $2 WHERE id = $1', [seeded.groupId, new Date().toISOString()])
}

async function cleanupAuditPayloadBlobsByIds(blobIds: string[]): Promise<void> {
  const batchSize = 500
  for (let offset = 0; offset < blobIds.length; offset += batchSize) {
    const batch = blobIds.slice(offset, offset + batchSize)
    await cleanupUnreferencedAuditPayloadBlobsByIdsAsync(batch, batch.length)
  }
}

async function cleanupStaleFixtures(): Promise<void> {
  const pool = await getPostgresPool()
  const rows = await pool.query(`
    SELECT id, 'api_key' AS kind FROM juhe_business.api_keys WHERE name LIKE '压测网关Key-%'
    UNION ALL
    SELECT id, 'account' AS kind FROM juhe_business.accounts WHERE name LIKE '压测网关账户-%'
    UNION ALL
    SELECT id, 'group' AS kind FROM juhe_business.groups WHERE name LIKE '压测网关分组-%'
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
  assert.notEqual(process.env.NODE_ENV, 'production', '高性能网关压测禁止在 NODE_ENV=production 下运行')
  assert.equal(runtimeConfig.runtimeMode, 'performance', '高性能网关压测需要 JUHE_AI_RUNTIME_MODE=performance')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '高性能网关压测需要 JUHE_AI_DATABASE_DRIVER=postgres')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '高性能网关压测需要 JUHE_AI_CACHE_DRIVER=redis')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '高性能网关压测需要 JUHE_AI_RUNTIME_STATE_DRIVER=redis')
  assert.equal(runtimeConfig.queueDriver, 'redis_stream', '高性能网关压测需要 JUHE_AI_QUEUE_DRIVER=redis_stream')
  assert.ok(runtimeConfig.postgres.url, '高性能网关压测需要 JUHE_AI_POSTGRES_URL')
  assert.ok(runtimeConfig.redis.cacheUrl, '高性能网关压测需要 JUHE_AI_REDIS_CACHE_URL')
  assert.ok(runtimeConfig.redis.stateUrl, '高性能网关压测需要 JUHE_AI_REDIS_STATE_URL')
  assert.ok(runtimeConfig.redis.queueUrl, '高性能网关压测需要 JUHE_AI_REDIS_QUEUE_URL')
  assert.equal(process.env.JUHE_AI_GATEWAY_LOAD_ALLOW_SETTINGS_WRITE, '1', '高性能网关压测会写入临时系统设置，必须显式设置 JUHE_AI_GATEWAY_LOAD_ALLOW_SETTINGS_WRITE=1')
  assert.equal(process.env.JUHE_AI_GATEWAY_LOAD_ALLOW_PRIVATE_UPSTREAM, '1', '高性能网关压测使用本机 mock upstream，必须显式设置 JUHE_AI_GATEWAY_LOAD_ALLOW_PRIVATE_UPSTREAM=1')
  if (config.targetBaseUrl) {
    const target = new URL(config.targetBaseUrl)
    assert.ok(['127.0.0.1', 'localhost', '::1'].includes(target.hostname), '外部压测目标只允许回环地址，避免误压生产或公网服务')
  }
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
}

function loadConfig(): GatewayLoadConfig {
  const promptSizeProfile = parsePromptSizeProfile(envText('JUHE_AI_GATEWAY_LOAD_PROMPT_SIZE_PROFILE', 'fixed'))
  const historicalPromptSizeTiers = loadHistoricalPromptSizeTiers(promptSizeProfile)
  return {
    targetBaseUrl: optionalLoopbackBaseUrl('JUHE_AI_GATEWAY_LOAD_BASE_URL'),
    scenarios: scenarioList(envText('JUHE_AI_GATEWAY_LOAD_SCENARIOS', 'responses,chat')),
    durationSeconds: envInteger('JUHE_AI_GATEWAY_LOAD_DURATION_SECONDS', 30, 1, 3600),
    warmupSeconds: envInteger('JUHE_AI_GATEWAY_LOAD_WARMUP_SECONDS', 5, 0, 600),
    settleSeconds: envInteger('JUHE_AI_GATEWAY_LOAD_SETTLE_SECONDS', 10, 0, 600),
    concurrency: envInteger('JUHE_AI_GATEWAY_LOAD_CONCURRENCY', 64, 1, 2000),
    requestStartSpreadMs: envInteger('JUHE_AI_GATEWAY_LOAD_REQUEST_START_SPREAD_MS', 0, 0, 600_000),
    requestTimeoutMs: envInteger('JUHE_AI_GATEWAY_LOAD_REQUEST_TIMEOUT_MS', 30_000, 100, 600_000),
    sampleIntervalMs: envInteger('JUHE_AI_GATEWAY_LOAD_SAMPLE_INTERVAL_MS', 1000, 250, 300_000),
    upstreamLatencyMs: envInteger('JUHE_AI_GATEWAY_LOAD_UPSTREAM_LATENCY_MS', 50, 0, 600_000),
    upstreamStreamChunks: envInteger('JUHE_AI_GATEWAY_LOAD_STREAM_CHUNKS', 4, 1, 1000),
    upstreamStreamChunkIntervalMs: envInteger('JUHE_AI_GATEWAY_LOAD_STREAM_CHUNK_INTERVAL_MS', 10, 0, 600_000),
    upstreamStreamTotalMinMs: envInteger('JUHE_AI_GATEWAY_LOAD_STREAM_TOTAL_MIN_MS', 0, 0, 600_000),
    upstreamStreamTotalMaxMs: envInteger('JUHE_AI_GATEWAY_LOAD_STREAM_TOTAL_MAX_MS', 0, 0, 600_000),
    upstreamBodyBytes: envInteger('JUHE_AI_GATEWAY_LOAD_UPSTREAM_BODY_BYTES', 512, 0, 8 * 1024 * 1024),
    upstreamErrorRate: envFloat('JUHE_AI_GATEWAY_LOAD_UPSTREAM_ERROR_RATE', 0, 0, 1),
    accountCount: envInteger('JUHE_AI_GATEWAY_LOAD_ACCOUNT_COUNT', 32, 1, 1000),
    accountConcurrencyLimit: envInteger('JUHE_AI_GATEWAY_LOAD_ACCOUNT_CONCURRENCY', 10000, 1, 1000000),
    clientIpConcurrencyLimit: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_CLIENT_IP_CONCURRENCY', 0, 1_000_000),
    groupMaxQueueWaitMs: envInteger('JUHE_AI_GATEWAY_LOAD_GROUP_MAX_QUEUE_WAIT_MS', 30_000, 0, 3_600_000),
    model: envText('JUHE_AI_GATEWAY_LOAD_MODEL', 'gpt-5-mini'),
    promptBytes: envInteger('JUHE_AI_GATEWAY_LOAD_PROMPT_BYTES', 64, 1, 8 * 1024 * 1024),
    promptSizeProfile,
    historicalPromptSizeTiers,
    promptContentMode: parsePromptContentMode(envText('JUHE_AI_GATEWAY_LOAD_PROMPT_CONTENT_MODE', 'repetitive')),
    requestShape: requestShape(envText('JUHE_AI_GATEWAY_LOAD_REQUEST_SHAPE', 'simple')),
    enableStatsWorkerObservation: envBoolean('JUHE_AI_GATEWAY_LOAD_ENABLE_STATS_WORKER', false),
    cleanup: envBoolean('JUHE_AI_GATEWAY_LOAD_CLEANUP', true),
    assertAccountConcurrency: envBoolean('JUHE_AI_GATEWAY_LOAD_ASSERT_ACCOUNT_CONCURRENCY', false),
    maxAllowedErrorRate: envFloat('JUHE_AI_GATEWAY_LOAD_MAX_ERROR_RATE', 0.01, 0, 1),
    maxAllowedNonStreamP95Ms: envInteger(
      'JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P95_MS',
      envInteger('JUHE_AI_GATEWAY_LOAD_MAX_P95_MS', 3000, 1, 600_000),
      1,
      600_000
    ),
    maxAllowedNonStreamP99Ms: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_P99_MS', 1, 600_000),
    maxAllowedNonStreamMaxMs: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_NON_STREAM_MAX_MS', 1, 600_000),
    maxAllowedSseTtfbP99Ms: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_P99_MS', 1, 600_000),
    maxAllowedSseTtfbMaxMs: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_SSE_TTFB_MAX_MS', 1, 600_000),
    maxAllowedSseTotalP99Ms: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_SSE_TOTAL_P99_MS', 1, 600_000),
    maxAllowedSseTotalMaxMs: optionalEnvInteger('JUHE_AI_GATEWAY_LOAD_MAX_SSE_TOTAL_MAX_MS', 1, 600_000),
    maxAllowedDeadlocks: envInteger('JUHE_AI_GATEWAY_LOAD_MAX_DEADLOCKS', 0, 0, 1000),
    maxAllowedLockWaiters: envInteger('JUHE_AI_GATEWAY_LOAD_MAX_LOCK_WAITERS', 0, 0, 1000),
    maxAllowedKnownCoordinationWaitMs: envInteger(
      'JUHE_AI_GATEWAY_LOAD_MAX_KNOWN_COORDINATION_WAIT_MS',
      envInteger('JUHE_AI_GATEWAY_LOAD_MAX_UNIQUE_COORDINATION_WAIT_MS', 1500, 1, 600_000),
      1,
      600_000
    ),
    maxAllowedRedisPending: envInteger('JUHE_AI_GATEWAY_LOAD_MAX_REDIS_PENDING', 100, 0, 1_000_000),
    resetPgStatStatements: envBoolean('JUHE_AI_GATEWAY_LOAD_RESET_PG_STAT_STATEMENTS', true),
    reportPath: resolve(envText('JUHE_AI_GATEWAY_LOAD_REPORT_PATH', resolve(backendRoot, '..', 'reports', `performance-gateway-load-${runId}.json`)))
  }
}

function optionalLoopbackBaseUrl(name: string): string | undefined {
  const value = process.env[name]?.trim()
  if (!value) return undefined
  const parsed = new URL(value)
  assert.ok(['http:', 'https:'].includes(parsed.protocol), `${name} 只允许 http/https`)
  assert.ok(['127.0.0.1', 'localhost', '::1'].includes(parsed.hostname), `${name} 只允许回环地址`)
  parsed.pathname = parsed.pathname.replace(/\/$/, '')
  parsed.search = ''
  parsed.hash = ''
  return parsed.toString().replace(/\/$/, '')
}

function requestShape(value: string): RequestShape {
  assert.ok(value === 'simple' || value === 'historical_responses', 'JUHE_AI_GATEWAY_LOAD_REQUEST_SHAPE 必须为 simple 或 historical_responses')
  return value
}

function parsePromptSizeProfile(value: string): PromptSizeProfile {
  assert.ok(value === 'fixed' || value === 'historical', 'JUHE_AI_GATEWAY_LOAD_PROMPT_SIZE_PROFILE 必须为 fixed 或 historical')
  return value
}

function parsePromptContentMode(value: string): PromptContentMode {
  assert.ok(
    value === 'repetitive' || value === 'deterministic_entropy',
    'JUHE_AI_GATEWAY_LOAD_PROMPT_CONTENT_MODE 必须为 repetitive 或 deterministic_entropy'
  )
  return value
}

function loadHistoricalPromptSizeTiers(profile: PromptSizeProfile): PromptSizeTierConfig[] {
  const defaults: PromptSizeTierConfig[] = [
    { name: 'historical_5k', bytes: 5 * 1024, permille: 900 },
    { name: 'historical_500k', bytes: 500 * 1024, permille: 80 },
    { name: 'historical_2m', bytes: 2 * 1024 * 1024, permille: 15 },
    { name: 'historical_4m', bytes: 4 * 1024 * 1024, permille: 5 }
  ]
  if (profile === 'fixed') return defaults

  const envTiers = [
    ['5K', defaults[0]],
    ['500K', defaults[1]],
    ['2M', defaults[2]],
    ['4M', defaults[3]]
  ] as const
  const tiers = envTiers.map(([envTier, fallback]) => ({
    name: fallback.name,
    bytes: strictEnvInteger(`JUHE_AI_GATEWAY_LOAD_PROMPT_SIZE_${envTier}_BYTES`, fallback.bytes, 1, 8 * 1024 * 1024),
    permille: strictEnvInteger(`JUHE_AI_GATEWAY_LOAD_PROMPT_SIZE_${envTier}_PERMILLE`, fallback.permille, 0, 1000)
  }))
  const totalPermille = tiers.reduce((total, tier) => total + tier.permille, 0)
  assert.equal(totalPermille, 1000, `历史请求体四档 permille 合计必须为 1000，当前为 ${totalPermille}`)
  assert.ok(tiers.every((tier) => tier.permille > 0), '正式历史请求体分布要求四个 tier 的 permille 都大于 0')
  return tiers
}

function scenarioList(value: string): ScenarioName[] {
  const allowed = new Set<ScenarioName>(['models', 'responses', 'chat', 'responses_stream'])
  const parsed = value
    .split(',')
    .map((item) => item.trim())
    .filter((item): item is ScenarioName => allowed.has(item as ScenarioName))
  return parsed.length > 0 ? [...new Set(parsed)] : ['responses', 'chat']
}

function createLoadStats(): LoadStats {
  return {
    startedAtMs: performance.now(),
    loadDurationMs: 0,
    startedRequests: 0,
    inFlightRequests: 0,
    peakInFlightRequests: 0,
    latenciesMs: [],
    totalRequests: 0,
    successRequests: 0,
    failedRequests: 0,
    statusCounts: new Map(),
    errorCounts: new Map(),
    statusSamples: new Map(),
    responseBytes: 0,
    successfulTraceIds: [],
    scenarioStats: new Map(),
    promptSizeSequence: 0,
    promptSizeStats: new Map()
  }
}

function formalLoadElapsedSeconds(stats: LoadStats, currentElapsedSeconds: number): number {
  const formalDurationSeconds = stats.loadDurationMs > 0 ? stats.loadDurationMs / 1000 : currentElapsedSeconds
  return Math.max(0.001, Math.min(currentElapsedSeconds, formalDurationSeconds))
}

function selectPromptSize(stats: LoadStats, config: GatewayLoadConfig): SelectedPromptSize {
  const sequence = stats.promptSizeSequence
  stats.promptSizeSequence += 1
  if (config.promptSizeProfile === 'fixed') {
    return { tier: 'fixed', bytes: config.promptBytes }
  }

  // The coprime step covers every permille slot once; the offset also spreads rare tiers into short runs.
  const slot = (((sequence + 1) * 21) + 506) % 1000
  let upperBound = 0
  for (const tier of config.historicalPromptSizeTiers) {
    upperBound += tier.permille
    if (slot < upperBound) {
      return { tier: tier.name, bytes: tier.bytes }
    }
  }
  const fallback = config.historicalPromptSizeTiers[config.historicalPromptSizeTiers.length - 1]
  assert.ok(fallback, '历史请求体大小分布不能为空')
  return { tier: fallback.name, bytes: fallback.bytes }
}

function promptSizeLoadStats(stats: LoadStats, tier: PromptSizeTierName): PromptSizeLoadStats {
  const existing = stats.promptSizeStats.get(tier)
  if (existing) return existing
  const created: PromptSizeLoadStats = {
    count: 0,
    success: 0,
    errors: 0,
    totalPromptBytes: 0,
    latenciesMs: [],
    errorCounts: new Map()
  }
  stats.promptSizeStats.set(tier, created)
  return created
}

function recordPromptSizeResponse(stats: LoadStats, promptSize: SelectedPromptSize, response: FetchScenarioResult): void {
  const tierStats = promptSizeLoadStats(stats, promptSize.tier)
  tierStats.count += 1
  tierStats.totalPromptBytes += promptSize.bytes
  tierStats.latenciesMs.push(response.totalLatencyMs)
  if (response.ok) {
    tierStats.success += 1
  } else {
    tierStats.errors += 1
    increment(tierStats.errorCounts, `HTTP ${response.status}`)
  }
}

function recordPromptSizeError(
  stats: LoadStats,
  promptSize: SelectedPromptSize,
  latencyMs: number,
  error: string
): void {
  const tierStats = promptSizeLoadStats(stats, promptSize.tier)
  tierStats.count += 1
  tierStats.errors += 1
  tierStats.totalPromptBytes += promptSize.bytes
  tierStats.latenciesMs.push(latencyMs)
  increment(tierStats.errorCounts, error)
}

function scenarioLoadStats(stats: LoadStats, scenario: ScenarioName): ScenarioLoadStats {
  const existing = stats.scenarioStats.get(scenario)
  if (existing) return existing
  const created: ScenarioLoadStats = {
    totalRequests: 0,
    successRequests: 0,
    failedRequests: 0,
    latenciesMs: [],
    headersLatenciesMs: [],
    firstByteLatenciesMs: [],
    terminalSamples: 0,
    statusCounts: new Map(),
    errorCounts: new Map(),
    responseBytes: 0
  }
  stats.scenarioStats.set(scenario, created)
  return created
}

function recordScenarioResponse(stats: LoadStats, scenario: ScenarioName, response: FetchScenarioResult): void {
  const scenarioStats = scenarioLoadStats(stats, scenario)
  scenarioStats.totalRequests += 1
  scenarioStats.latenciesMs.push(response.totalLatencyMs)
  scenarioStats.responseBytes += response.responseBytes
  increment(scenarioStats.statusCounts, String(response.status))
  if (scenario === 'responses_stream') {
    scenarioStats.headersLatenciesMs.push(response.headersLatencyMs)
    if (response.firstByteLatencyMs !== undefined) {
      scenarioStats.firstByteLatenciesMs.push(response.firstByteLatencyMs)
    }
    if (response.streamTerminalSeen) {
      scenarioStats.terminalSamples += 1
    }
  }
  if (response.ok) {
    scenarioStats.successRequests += 1
  } else {
    scenarioStats.failedRequests += 1
    increment(scenarioStats.errorCounts, `HTTP ${response.status}`)
  }
}

function recordScenarioError(stats: LoadStats, scenario: ScenarioName, latencyMs: number, error: string): void {
  const scenarioStats = scenarioLoadStats(stats, scenario)
  scenarioStats.totalRequests += 1
  scenarioStats.failedRequests += 1
  scenarioStats.latenciesMs.push(latencyMs)
  increment(scenarioStats.errorCounts, error)
}

function beginLoadRequest(stats: LoadStats): void {
  stats.startedRequests += 1
  stats.inFlightRequests += 1
  stats.peakInFlightRequests = Math.max(stats.peakInFlightRequests, stats.inFlightRequests)
}

function finishLoadRequest(stats: LoadStats): void {
  stats.inFlightRequests = Math.max(0, stats.inFlightRequests - 1)
}

function initializePromptContent(config: GatewayLoadConfig): void {
  if (config.promptContentMode !== 'deterministic_entropy') return
  const maximumPromptBytes = config.promptSizeProfile === 'historical'
    ? Math.max(...config.historicalPromptSizeTiers.map((tier) => tier.bytes))
    : config.promptBytes
  const corpusBytes = Math.max(1024 * 1024, Math.min(16 * 1024 * 1024, maximumPromptBytes * 2))
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
  const corpus = Buffer.allocUnsafe(corpusBytes)
  let state = 0x9e3779b9
  for (let index = 0; index < corpus.length; index += 1) {
    state ^= state << 13
    state ^= state >>> 17
    state ^= state << 5
    state >>>= 0
    corpus[index] = alphabet.charCodeAt((state >>> 26) & 63)
  }
  deterministicEntropyCorpus = corpus.toString('ascii')
}

function promptText(bytes: number, requestId: string, contentMode: PromptContentMode): string {
  const prefix = `request ${requestId}: `
  if (contentMode === 'deterministic_entropy') {
    if (bytes <= prefix.length) return prefix.slice(0, bytes)
    assert.ok(deterministicEntropyCorpus, '确定性高熵 prompt corpus 未初始化')
    const contentBytes = bytes - prefix.length
    const maximumOffset = deterministicEntropyCorpus.length - contentBytes
    assert.ok(maximumOffset >= 0, `prompt bytes ${bytes} 超过高熵 corpus 容量`)
    const offset = stablePromptCorpusOffset(requestId, maximumOffset + 1)
    return prefix + deterministicEntropyCorpus.slice(offset, offset + contentBytes)
  }
  const target = Math.max(prefix.length, bytes)
  return (prefix + 'please only output OK. '.repeat(Math.ceil(target / 23))).slice(0, target)
}

function stablePromptCorpusOffset(requestId: string, range: number): number {
  let hash = 0x811c9dc5
  for (let index = 0; index < requestId.length; index += 1) {
    hash = Math.imul(hash ^ requestId.charCodeAt(index), 0x01000193)
  }
  return (hash >>> 0) % Math.max(1, range)
}

function responseText(bytes: number): string {
  if (bytes <= 0) return 'OK'
  return 'OK '.repeat(Math.ceil(bytes / 3)).slice(0, bytes)
}

function parseJsonBody(text: string): Record<string, unknown> {
  try {
    const value = JSON.parse(text) as unknown
    return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function latencySummary(values: number[]): LatencySummary {
  if (values.length === 0) {
    return { min: 0, avg: 0, p50: 0, p90: 0, p95: 0, p99: 0, max: 0 }
  }
  const sorted = [...values].sort((left, right) => left - right)
  const sum = sorted.reduce((total, value) => total + value, 0)
  return {
    min: round(sorted[0] ?? 0),
    avg: round(sum / sorted.length),
    p50: round(percentile(sorted, 0.50)),
    p90: round(percentile(sorted, 0.90)),
    p95: round(percentile(sorted, 0.95)),
    p99: round(percentile(sorted, 0.99)),
    max: round(sorted[sorted.length - 1] ?? 0)
  }
}

function percentile(sorted: number[], pct: number): number {
  if (sorted.length === 0) return 0
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * pct) - 1))
  return sorted[index] ?? 0
}

function formatLoadError(error: unknown): string {
  if (!(error instanceof Error)) {
    return String(error)
  }
  const cause = (error as Error & { cause?: unknown }).cause
  const causeCode = cause && typeof cause === 'object' ? (cause as Record<string, unknown>).code : undefined
  return [error.name || 'Error', error.message, typeof causeCode === 'string' ? `cause=${causeCode}` : undefined]
    .filter(Boolean)
    .join(': ')
    .slice(0, 240)
}

function rememberStatusSample(samples: Map<string, string>, status: number, text: string): void {
  const key = String(status)
  if (!samples.has(key)) {
    samples.set(key, text.slice(0, 500))
  }
}

function increment(map: Map<string, number>, key: string): void {
  map.set(key, (map.get(key) ?? 0) + 1)
}

function objectFromCounts(map: Map<string, number>): Record<string, number> {
  return Object.fromEntries([...map.entries()].sort(([left], [right]) => left.localeCompare(right)))
}

function maxSample<T, K extends keyof T>(samples: T[], key: K): number {
  return samples.reduce((max, sample) => Math.max(max, numberValue(sample[key])), 0)
}

function numberValue(value: unknown): number {
  const number = Number(value ?? 0)
  return Number.isFinite(number) ? number : 0
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function round(value: number, digits = 2): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

function envText(name: string, fallback: string): string {
  return process.env[name]?.trim() || fallback
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = Number(envText(name, String(fallback)))
  if (!Number.isFinite(value)) return fallback
  return Math.min(max, Math.max(min, Math.trunc(value)))
}

function optionalEnvInteger(name: string, min: number, max: number): number | undefined {
  const raw = process.env[name]?.trim()
  if (!raw) return undefined
  const value = Number(raw)
  if (!Number.isFinite(value)) return undefined
  return Math.min(max, Math.max(min, Math.trunc(value)))
}

function strictEnvInteger(name: string, fallback: number, min: number, max: number): number {
  const raw = process.env[name]?.trim()
  if (!raw) return fallback
  const value = Number(raw)
  assert.ok(Number.isInteger(value) && value >= min && value <= max, `${name} 必须是 ${min}-${max} 之间的整数`)
  return value
}

function envFloat(name: string, fallback: number, min: number, max: number): number {
  const value = Number(envText(name, String(fallback)))
  if (!Number.isFinite(value)) return fallback
  return Math.min(max, Math.max(min, value))
}

function envBoolean(name: string, fallback: boolean): boolean {
  const value = envText(name, '').toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  return fallback
}

function createConnectionTracker(): ConnectionTracker {
  return {
    acceptedSockets: 0,
    closedSockets: 0,
    peakActiveSockets: 0,
    activeSockets: new Set(),
    socketIds: new WeakMap(),
    requestsBySocketId: new Map()
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

async function waitForHealth(url: string, child?: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  let lastError: unknown
  while (Date.now() - startedAt < 30_000) {
    if (child && child.exitCode !== null) {
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
  const childDiagnostics = child ? `\nstdout=${childOutput.stdout}\nstderr=${childOutput.stderr}` : ''
  throw new Error(`等待健康检查超时：${lastError instanceof Error ? lastError.message : String(lastError)}${childDiagnostics}`)
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

function tailText(value: string, maxChars = 20_000): string {
  return value.length > maxChars ? value.slice(-maxChars) : value
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(message)), timeoutMs)
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}
