import { existsSync, mkdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import type { Socket } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, isAbsolute, join, resolve } from 'node:path'
import { monitorEventLoopDelay, performance } from 'node:perf_hooks'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { gatewayRawBodyHardLimit, gatewayRawBodyHardLimitBytes } from '../../modules/gateway/request/body.js'

type ScenarioName = 'models' | 'responses' | 'chat' | 'responses_stream'

interface PerfConfig {
  scenarios: ScenarioName[]
  concurrencyLevels: number[]
  durationSeconds: number
  warmupSeconds: number
  requestTimeoutMs: number
  upstreamLatencyMs: number
  upstreamStreamChunks: number
  upstreamStreamChunkIntervalMs: number
  upstreamBodyBytes: number
  upstreamErrorRate: number
  accountCount: number
  accountConcurrencyLimit: number
  model: string
  promptBytes: number
  p95TargetMs: number
  auditCaptureMode: 'default' | 'metadata_only'
  reportPath?: string
}

interface SeededGateway {
  apiKey: string
  apiKeyId: string
  groupId: string
  accountIds: string[]
}

interface ScenarioResult {
  scenario: ScenarioName
  concurrency: number
  durationSeconds: number
  warmupSeconds: number
  totalRequests: number
  successRequests: number
  failedRequests: number
  qps: number
  successQps: number
  errorRate: number
  latencyMs: {
    min: number
    avg: number
    p50: number
    p90: number
    p95: number
    p99: number
    max: number
  }
  eventLoopDelayMs: {
    mean: number
    p95: number
    p99: number
    max: number
  }
  cpu: {
    userMs: number
    systemMs: number
    totalMs: number
    perRequestMs: number
  }
  memory: {
    rssStartMb: number
    rssEndMb: number
    heapUsedStartMb: number
    heapUsedEndMb: number
  }
  statusCounts: Record<string, number>
  errorCounts: Record<string, number>
  statusSamples: Record<string, string>
  responseBytes: number
  usageRecordsDelta: number
  auditLogsDelta: number
  usageRecordBreakdownDelta: UsageRecordBreakdown
  auditLogBreakdownDelta: AuditLogBreakdown
  usageRecordQueueLength: number
  auditLogQueueLength: number
  upstreamRequestsDelta: number
  accountSideEffects?: unknown
}

interface UsageRecordBreakdown {
  total: number
  bySuccess: Record<string, number>
  byStatusCode: Record<string, number>
  byErrorCode: Record<string, number>
  byErrorMessage: Record<string, number>
  byEndpoint: Record<string, number>
}

interface AuditLogBreakdown {
  total: number
  byOutcome: Record<string, number>
  bySuccess: Record<string, number>
  byStatusCode: Record<string, number>
  bySampleReason: Record<string, number>
  byCaptureStatus: Record<string, number>
}

interface LoadStats {
  latenciesMs: number[]
  totalRequests: number
  successRequests: number
  failedRequests: number
  statusCounts: Map<string, number>
  errorCounts: Map<string, number>
  statusSamples: Map<string, string>
  responseBytes: number
}

interface UpstreamRuntime {
  totalRequests: number
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

interface ConnectionStats {
  acceptedSockets: number
  closedSockets: number
  activeSockets: number
  peakActiveSockets: number
  socketsWithRequests: number
  reusedSockets: number
  maxRequestsPerSocket: number
  avgRequestsPerSocket: number
}

const tempRoot = resolve(tmpdir(), `juhe-ai-perf-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const perfListenBacklog = 8192
runtimeConfig.databasePath = join(tempRoot, 'perf.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'juhe-ai-performance-test-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { handleOpenAIGatewayRequest },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  mockdataFixtures,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue,
  usageRecordShards,
  gatewayAccountSideEffects
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../maintenance/mockdata/fixtures.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js')
])

usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)

async function main(): Promise<void> {
  const config = loadConfig()
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamRuntime: UpstreamRuntime = {
    totalRequests: 0,
    pathCounts: new Map(),
    connections: createConnectionTracker()
  }
  const gatewayConnections = createConnectionTracker()

  try {
    upstreamServer = createMockOpenAIUpstream(config, upstreamRuntime)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const seeded = seedGatewayData(config, upstreamBaseUrl)
    prewarmStorageDatabases()
    prewarmUsageRecordShards()
    gatewayCache.clearGatewayRuntimeCacheLocal()

    appServer = createGatewayServer(gatewayConnections, config)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverPort(appServer)}`

    const results: ScenarioResult[] = []
    printHeader(config, baseUrl, upstreamBaseUrl, seeded)
    for (const scenario of config.scenarios) {
      for (const concurrency of config.concurrencyLevels) {
        const result = await runScenario({
          config,
          scenario,
          concurrency,
          baseUrl,
          apiKey: seeded.apiKey,
          upstreamRuntime
        })
        results.push(result)
        printScenarioResult(result)
      }
    }

    const summary = buildSummary(config, results, seeded, upstreamRuntime, gatewayConnections)
    console.log('\n性能综合测试汇总')
    console.log(JSON.stringify(summary, null, 2))
    if (config.reportPath) {
      mkdirSync(dirname(config.reportPath), { recursive: true })
      writeFileSync(config.reportPath, JSON.stringify(summary, null, 2), 'utf8')
      console.log(`\n性能测试报告已写入：${config.reportPath}`)
    }
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await auditLogQueue.flushAllAuditLogQueueAsync()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    closeDatabases()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function loadConfig(): PerfConfig {
  return {
    scenarios: scenarioList(envText('JUHE_AI_PERF_SCENARIOS', 'models,responses,chat')),
    concurrencyLevels: positiveIntegerList(envText('JUHE_AI_PERF_CONCURRENCY', '10,25,50'), [10, 25, 50]),
    durationSeconds: envInteger('JUHE_AI_PERF_DURATION_SECONDS', 6, 1, 3600),
    warmupSeconds: envInteger('JUHE_AI_PERF_WARMUP_SECONDS', 1, 0, 600),
    requestTimeoutMs: envInteger('JUHE_AI_PERF_REQUEST_TIMEOUT_MS', 5000, 100, 600000),
    upstreamLatencyMs: envInteger('JUHE_AI_PERF_UPSTREAM_LATENCY_MS', 20, 0, 600000),
    upstreamStreamChunks: envInteger('JUHE_AI_PERF_UPSTREAM_STREAM_CHUNKS', 4, 1, 1000),
    upstreamStreamChunkIntervalMs: envInteger('JUHE_AI_PERF_UPSTREAM_STREAM_CHUNK_INTERVAL_MS', 10, 0, 600000),
    upstreamBodyBytes: envInteger('JUHE_AI_PERF_UPSTREAM_BODY_BYTES', 512, 0, 2 * 1024 * 1024),
    upstreamErrorRate: envFloat('JUHE_AI_PERF_UPSTREAM_ERROR_RATE', 0, 0, 1),
    accountCount: envInteger('JUHE_AI_PERF_ACCOUNT_COUNT', 8, 1, 1000),
    accountConcurrencyLimit: envInteger('JUHE_AI_PERF_ACCOUNT_CONCURRENCY', 10000, 1, 1000000),
    model: envText('JUHE_AI_PERF_MODEL', 'gpt-5.4-mini'),
    promptBytes: envInteger('JUHE_AI_PERF_PROMPT_BYTES', 64, 1, Math.max(1, gatewayRawBodyHardLimitBytes - 64 * 1024)),
    p95TargetMs: envInteger('JUHE_AI_PERF_P95_TARGET_MS', 1000, 1, 600000),
    auditCaptureMode: auditCaptureMode(envText('JUHE_AI_PERF_AUDIT_CAPTURE_MODE', 'default')),
    reportPath: optionalEnvText('JUHE_AI_PERF_REPORT_PATH')
  }
}

function seedGatewayData(config: PerfConfig, upstreamBaseUrl: string): SeededGateway {
  const fixture = mockdataFixtures.createMockGatewayFixture({
    label: '性能压测',
    upstreamBaseUrl,
    accountCount: config.accountCount,
    accountConcurrencyLimit: config.accountConcurrencyLimit,
    clientCompatibility: 'openai_standard'
  })
  if (!fixture.apiKey) throw new Error('Mockdata 压测夹具未生成本地网关 Key')
  return {
    apiKey: fixture.apiKey.key,
    apiKeyId: fixture.apiKey.id,
    groupId: fixture.group.id,
    accountIds: fixture.accounts.map((account) => account.id)
  }
}

function prewarmUsageRecordShards(): void {
  const createdAt = new Date().toISOString()
  const targetShardCount = usageRecordShards.usageRecordShardCount()
  const seenShardKeys = new Set<string>()
  for (let index = 0; seenShardKeys.size < targetShardCount && index < targetShardCount * 100; index += 1) {
    const id = usageRecordShards.generateUsageRecordId(createdAt, `perf-prewarm-${index}`)
    const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
    if (seenShardKeys.has(location.shardKey)) {
      continue
    }
    usageRecordShards.getUsageRecordShardDatabase(location)
    seenShardKeys.add(location.shardKey)
  }
  if (seenShardKeys.size < targetShardCount) {
    console.warn(`usage shard 预热未覆盖全部分片：${seenShardKeys.size}/${targetShardCount}`)
  }
}

function prewarmStorageDatabases(): void {
  databaseModule.getBusinessDatabase().prepare('SELECT 1').get()
  databaseModule.getDatasetDatabase().prepare('SELECT 1').get()
  databaseModule.getUsageCatalogDatabase().prepare('SELECT 1').get()
  databaseModule.getStatsDatabase().prepare('SELECT 1').get()
}

function createGatewayServer(connectionTracker: ConnectionTracker, config: PerfConfig): http.Server {
  const gatewayRawBodyLimit = gatewayRawBodyHardLimit
  const app = express()
  app.use((req, _res, next) => {
    recordSocketRequest(connectionTracker, req.socket)
    next()
  })
  app.use(requestContextMiddleware)
  app.use(cors({ credentials: true, origin: true }))
  app.get('/__aisys__/health', (_req, res) => {
    res.json({ status: 'ok', service: 'juhe-ai-performance-test' })
  })
  app.use(express.raw({ type: () => true, limit: gatewayRawBodyLimit }), handleGatewayRawBodyError, captureGatewayRawBody, (req: Request, res: ExpressResponse, next: NextFunction) => {
    handleOpenAIGatewayRequest(req, res, { auditCaptureMode: config.auditCaptureMode })
      .catch((error: unknown) => next(error))
  })
  app.use(handleGatewayPerfUnhandledError)
  const server = http.createServer(app)
  attachConnectionTracker(server, connectionTracker)
  return server
}

function handleGatewayPerfUnhandledError(
  error: Error,
  _req: Request,
  res: ExpressResponse,
  _next: NextFunction
): void {
  if (res.headersSent) {
    res.end()
    return
  }
  res.status(500).json({
    error: {
      message: error.message,
      type: 'perf_gateway_error'
    }
  })
}

function handleGatewayRawBodyError(error: Error & { status?: number; statusCode?: number }, _req: Request, res: ExpressResponse, next: NextFunction): void {
  if (res.headersSent) {
    next(error)
    return
  }
  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400
  res.status(statusCode >= 400 && statusCode < 600 ? statusCode : 400).json({
    error: {
      message: statusCode === 413 ? '请求体过大' : '网关请求体无效',
      type: statusCode === 413 ? 'request_too_large' : 'invalid_request_error'
    }
  })
}

function createMockOpenAIUpstream(config: PerfConfig, runtime: UpstreamRuntime): http.Server {
  const server = http.createServer((req, res) => {
    recordSocketRequest(runtime.connections, req.socket)
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    runtime.totalRequests += 1
    increment(runtime.pathCounts, `${req.method ?? 'GET'} ${url.pathname}`)

    const bodyChunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      bodyChunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
      const bodyText = Buffer.concat(bodyChunks).toString('utf8')
      const shouldError = config.upstreamErrorRate > 0 && Math.random() < config.upstreamErrorRate
      setTimeout(() => {
        if (shouldError) {
          sendUpstreamError(res)
          return
        }
        if (url.pathname === '/v1/models') {
          sendModels(res)
          return
        }
        if (url.pathname === '/v1/chat/completions') {
          sendChatCompletion(res, config)
          return
        }
        if (url.pathname === '/v1/responses') {
          const stream = parseJsonBody(bodyText).stream === true
          if (stream) {
            sendResponseStream(res, config)
          } else {
            sendResponseJson(res, config)
          }
          return
        }
        sendResponseJson(res, config)
      }, config.upstreamLatencyMs)
    })
  })
  attachConnectionTracker(server, runtime.connections)
  return server
}

async function runScenario(input: {
  config: PerfConfig
  scenario: ScenarioName
  concurrency: number
  baseUrl: string
  apiKey: string
  upstreamRuntime: UpstreamRuntime
}): Promise<ScenarioResult> {
  if (input.config.warmupSeconds > 0) {
    await runLoadPhase({
      ...input,
      durationSeconds: input.config.warmupSeconds,
      record: false
    })
    usageRecordQueue.flushAllUsageRecordQueue()
    await auditLogQueue.flushAllAuditLogQueueAsync()
  }

  const usageRecordsBefore = countRows('usage_records')
  const auditLogsBefore = countRows('audit_logs')
  const usageRecordBreakdownBefore = usageRecordBreakdown(usageRecordsBefore)
  const auditLogBreakdownBefore = auditLogBreakdown(auditLogsBefore)
  const upstreamRequestsBefore = input.upstreamRuntime.totalRequests
  const memoryStart = process.memoryUsage()
  const cpuStart = process.cpuUsage()
  const loopDelay = monitorEventLoopDelay({ resolution: 10 })
  loopDelay.enable()
  const startedAt = performance.now()

  const stats = await runLoadPhase({
    ...input,
    durationSeconds: input.config.durationSeconds,
    record: true
  })

  const elapsedSeconds = Math.max(0.001, (performance.now() - startedAt) / 1000)
  loopDelay.disable()
  const cpu = process.cpuUsage(cpuStart)
  const memoryEnd = process.memoryUsage()
  usageRecordQueue.flushAllUsageRecordQueue()
  await auditLogQueue.flushAllAuditLogQueueAsync()
  const usageRecordsAfter = countRows('usage_records')
  const auditLogsAfter = countRows('audit_logs')
  const usageRecordBreakdownAfter = usageRecordBreakdown(usageRecordsAfter)
  const auditLogBreakdownAfter = auditLogBreakdown(auditLogsAfter)

  const totalRequests = stats.totalRequests
  const latency = latencySummary(stats.latenciesMs)
  return {
    scenario: input.scenario,
    concurrency: input.concurrency,
    durationSeconds: round(elapsedSeconds, 3),
    warmupSeconds: input.config.warmupSeconds,
    totalRequests,
    successRequests: stats.successRequests,
    failedRequests: stats.failedRequests,
    qps: round(totalRequests / elapsedSeconds, 2),
    successQps: round(stats.successRequests / elapsedSeconds, 2),
    errorRate: totalRequests > 0 ? round(stats.failedRequests / totalRequests, 4) : 0,
    latencyMs: latency,
    eventLoopDelayMs: {
      mean: round(nsToMs(loopDelay.mean), 3),
      p95: round(nsToMs(loopDelay.percentile(95)), 3),
      p99: round(nsToMs(loopDelay.percentile(99)), 3),
      max: round(nsToMs(loopDelay.max), 3)
    },
    cpu: {
      userMs: round(cpu.user / 1000, 3),
      systemMs: round(cpu.system / 1000, 3),
      totalMs: round((cpu.user + cpu.system) / 1000, 3),
      perRequestMs: totalRequests > 0 ? round((cpu.user + cpu.system) / 1000 / totalRequests, 4) : 0
    },
    memory: {
      rssStartMb: bytesToMb(memoryStart.rss),
      rssEndMb: bytesToMb(memoryEnd.rss),
      heapUsedStartMb: bytesToMb(memoryStart.heapUsed),
      heapUsedEndMb: bytesToMb(memoryEnd.heapUsed)
    },
    statusCounts: objectFromCounts(stats.statusCounts),
    errorCounts: objectFromCounts(stats.errorCounts),
    statusSamples: Object.fromEntries(stats.statusSamples.entries()),
    responseBytes: stats.responseBytes,
    usageRecordsDelta: usageRecordsAfter - usageRecordsBefore,
    auditLogsDelta: auditLogsAfter - auditLogsBefore,
    usageRecordBreakdownDelta: subtractUsageRecordBreakdown(usageRecordBreakdownAfter, usageRecordBreakdownBefore),
    auditLogBreakdownDelta: subtractAuditLogBreakdown(auditLogBreakdownAfter, auditLogBreakdownBefore),
    usageRecordQueueLength: usageRecordQueue.getUsageRecordQueueRuntime().queueLength,
    auditLogQueueLength: auditLogQueue.getAuditLogQueueRuntime().queueLength,
    upstreamRequestsDelta: input.upstreamRuntime.totalRequests - upstreamRequestsBefore,
    accountSideEffects: gatewayAccountSideEffects.getGatewayAccountSideEffectState()
  }
}

async function runLoadPhase(input: {
  config: PerfConfig
  scenario: ScenarioName
  concurrency: number
  durationSeconds: number
  baseUrl: string
  apiKey: string
  record: boolean
}): Promise<LoadStats> {
  const stats: LoadStats = {
    latenciesMs: [],
    totalRequests: 0,
    successRequests: 0,
    failedRequests: 0,
    statusCounts: new Map(),
    errorCounts: new Map(),
    statusSamples: new Map(),
    responseBytes: 0
  }
  const endAt = performance.now() + input.durationSeconds * 1000
  const workers = Array.from({ length: input.concurrency }, (_, workerIndex) => loadWorker(workerIndex, endAt, input, stats))
  await Promise.all(workers)
  return stats
}

async function loadWorker(
  workerIndex: number,
  endAt: number,
  input: {
    config: PerfConfig
    scenario: ScenarioName
    baseUrl: string
    apiKey: string
    record: boolean
  },
  stats: LoadStats
): Promise<void> {
  let sequence = 0
  while (performance.now() < endAt) {
    sequence += 1
    const started = performance.now()
    try {
      const response: globalThis.Response = await fetchWithTimeout(input.baseUrl, input.apiKey, input.scenario, input.config, `${workerIndex}-${sequence}`)
      const responseText = await response.text()
      const bytes = Buffer.byteLength(responseText, 'utf8')
      if (input.record) {
        const latencyMs = performance.now() - started
        stats.latenciesMs.push(latencyMs)
        stats.totalRequests += 1
        stats.responseBytes += bytes
        increment(stats.statusCounts, String(response.status))
        rememberStatusSample(stats.statusSamples, response.status, responseText)
        if (response.ok) {
          stats.successRequests += 1
        } else {
          stats.failedRequests += 1
          increment(stats.errorCounts, `HTTP ${response.status}`)
        }
      }
    } catch (error) {
      if (input.record) {
        const latencyMs = performance.now() - started
        stats.latenciesMs.push(latencyMs)
        stats.totalRequests += 1
        stats.failedRequests += 1
        increment(stats.errorCounts, formatLoadError(error))
      }
    }
  }
}

function formatLoadError(error: unknown): string {
  if (!(error instanceof Error)) {
    return String(error)
  }
  const parts = [
    error.name || 'Error',
    error.message
  ].filter(Boolean)
  const cause = (error as Error & { cause?: unknown }).cause
  const causeCode = objectStringProperty(cause, 'code')
  if (causeCode) {
    parts.push(`cause=${causeCode}`)
  }
  return parts.join(': ').slice(0, 240)
}

function objectStringProperty(value: unknown, key: string): string | undefined {
  if (typeof value !== 'object' || value === null) {
    return undefined
  }
  const property = (value as Record<string, unknown>)[key]
  return typeof property === 'string' && property.trim() ? property.trim() : undefined
}

async function fetchWithTimeout(
  baseUrl: string,
  apiKey: string,
  scenario: ScenarioName,
  config: PerfConfig,
  requestId: string
): Promise<globalThis.Response> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(new Error('请求超时')), config.requestTimeoutMs)
  try {
    const request = buildScenarioRequest(scenario, config, requestId)
    return await fetch(`${baseUrl}${request.path}`, {
      method: request.method,
      headers: {
        authorization: `Bearer ${apiKey}`,
        ...request.headers
      },
      body: request.body,
      signal: controller.signal
    })
  } finally {
    clearTimeout(timeout)
  }
}

function buildScenarioRequest(scenario: ScenarioName, config: PerfConfig, requestId: string): {
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
        model: config.model,
        messages: [{ role: 'user', content: promptText(config.promptBytes, requestId) }],
        max_tokens: 16,
        stream: false
      })
    }
  }
  return {
    method: 'POST',
    path: '/v1/responses',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      model: config.model,
      input: promptText(config.promptBytes, requestId),
      max_output_tokens: 16,
      stream: scenario === 'responses_stream'
    })
  }
}

function sendModels(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    object: 'list',
    data: [
      { id: 'gpt-5.4-mini', object: 'model', created: 0, owned_by: 'openai' }
    ]
  }))
}

function sendChatCompletion(res: http.ServerResponse, config: PerfConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'chatcmpl_perf',
    object: 'chat.completion',
    created: Math.trunc(Date.now() / 1000),
    model: config.model,
    choices: [
      {
        index: 0,
        message: {
          role: 'assistant',
          content: responseText(config.upstreamBodyBytes)
        },
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 12,
      completion_tokens: 8,
      total_tokens: 20
    }
  }))
}

function sendResponseJson(res: http.ServerResponse, config: PerfConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'resp_perf',
    object: 'response',
    status: 'completed',
    model: config.model,
    output: [
      {
        type: 'message',
        role: 'assistant',
        content: [{ type: 'output_text', text: responseText(config.upstreamBodyBytes) }]
      }
    ],
    usage: {
      input_tokens: 12,
      output_tokens: 8,
      input_tokens_details: {
        cached_tokens: 0
      }
    }
  }))
}

function sendResponseStream(res: http.ServerResponse, config: PerfConfig): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  let index = 0
  const writeNext = () => {
    if (index < config.upstreamStreamChunks) {
      const event = {
        type: 'response.output_text.delta',
        delta: responseText(Math.max(1, Math.ceil(config.upstreamBodyBytes / config.upstreamStreamChunks)))
      }
      res.write(`event: response.output_text.delta\ndata: ${JSON.stringify(event)}\n\n`)
      index += 1
      setTimeout(writeNext, config.upstreamStreamChunkIntervalMs)
      return
    }
    const completed = {
      type: 'response.completed',
      response: {
        status: 'completed',
        usage: {
          input_tokens: 12,
          output_tokens: 8
        }
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

function buildSummary(
  config: PerfConfig,
  results: ScenarioResult[],
  seeded: SeededGateway,
  upstreamRuntime: UpstreamRuntime,
  gatewayConnections: ConnectionTracker
): Record<string, unknown> {
  const stable = results
    .filter((item) => item.errorRate <= 0.01 && item.latencyMs.p95 <= config.p95TargetMs)
    .sort((left, right) => right.successQps - left.successQps)[0]
  return {
    generatedAt: new Date().toISOString(),
    note: '本结果来自本机模拟上游和临时 SQLite 数据库，用于容量参考；真实部署容量需要在目标机器和目标网络下复测。',
    config,
    seeded: {
      apiKeyId: seeded.apiKeyId,
      groupId: seeded.groupId,
      accountCount: seeded.accountIds.length
    },
    estimatedStableCapacity: stable
      ? {
          scenario: stable.scenario,
          concurrency: stable.concurrency,
          successQps: stable.successQps,
          p95LatencyMs: stable.latencyMs.p95,
          p99LatencyMs: stable.latencyMs.p99,
          errorRate: stable.errorRate,
          condition: `errorRate <= 1% 且 p95 <= ${config.p95TargetMs}ms`
        }
      : undefined,
    results,
    upstream: {
      totalRequests: upstreamRuntime.totalRequests,
      pathCounts: objectFromCounts(upstreamRuntime.pathCounts),
      connections: connectionStats(upstreamRuntime.connections)
    },
    gateway: {
      connections: connectionStats(gatewayConnections)
    },
    datasetDatabase: {
      usageRecords: countRows('usage_records'),
      auditLogs: countRows('audit_logs'),
      bytes: fileBytes(runtimeConfig.datasetDatabasePath),
      walBytes: fileBytes(`${runtimeConfig.datasetDatabasePath}-wal`)
    },
    usageCatalogDatabase: {
      entries: countUsageCatalogRows('usage_record_shard_entries'),
      shards: countUsageCatalogRows('usage_record_shards'),
      bytes: fileBytes(databaseModule.usageCatalogDatabasePath()),
      walBytes: fileBytes(`${databaseModule.usageCatalogDatabasePath()}-wal`)
    },
    statsDatabase: {
      bytes: fileBytes(runtimeConfig.statsDatabasePath),
      walBytes: fileBytes(`${runtimeConfig.statsDatabasePath}-wal`)
    }
  }
}

function printHeader(config: PerfConfig, baseUrl: string, upstreamBaseUrl: string, seeded: SeededGateway): void {
  console.log('juhe-ai 性能综合测试启动')
  console.log(`- 网关地址：${baseUrl}`)
  console.log(`- 模拟上游：${upstreamBaseUrl}`)
  console.log(`- 场景：${config.scenarios.join(', ')}`)
  console.log(`- 并发档位：${config.concurrencyLevels.join(', ')}`)
  console.log(`- 单档时长：${config.durationSeconds}s，预热：${config.warmupSeconds}s`)
  console.log(`- 模拟上游延迟：${config.upstreamLatencyMs}ms，响应体：${config.upstreamBodyBytes} bytes`)
  console.log(`- 审计捕获模式：${config.auditCaptureMode}`)
  console.log(`- 临时账户：${seeded.accountIds.length} 个，单账号并发上限：${config.accountConcurrencyLimit}`)
}

function printScenarioResult(result: ScenarioResult): void {
  console.log([
    `场景=${result.scenario}`,
    `并发=${result.concurrency}`,
    `QPS=${result.qps}`,
    `成功QPS=${result.successQps}`,
    `错误率=${(result.errorRate * 100).toFixed(2)}%`,
    `p50=${result.latencyMs.p50}ms`,
    `p95=${result.latencyMs.p95}ms`,
    `p99=${result.latencyMs.p99}ms`,
    `事件循环p99=${result.eventLoopDelayMs.p99}ms`,
    `请求数=${result.totalRequests}`
  ].join(' | '))
}

function latencySummary(values: number[]): ScenarioResult['latencyMs'] {
  if (values.length === 0) {
    return { min: 0, avg: 0, p50: 0, p90: 0, p95: 0, p99: 0, max: 0 }
  }
  const sorted = [...values].sort((left, right) => left - right)
  const sum = sorted.reduce((total, value) => total + value, 0)
  return {
    min: round(sorted[0], 3),
    avg: round(sum / sorted.length, 3),
    p50: round(percentile(sorted, 50), 3),
    p90: round(percentile(sorted, 90), 3),
    p95: round(percentile(sorted, 95), 3),
    p99: round(percentile(sorted, 99), 3),
    max: round(sorted[sorted.length - 1], 3)
  }
}

function percentile(sortedValues: number[], percentileValue: number): number {
  if (sortedValues.length === 0) return 0
  const index = Math.min(sortedValues.length - 1, Math.max(0, Math.ceil((percentileValue / 100) * sortedValues.length) - 1))
  return sortedValues[index]
}

function parseJsonBody(text: string): Record<string, unknown> {
  try {
    const value = JSON.parse(text) as unknown
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function promptText(bytes: number, requestId: string): string {
  const prefix = `请求 ${requestId}：`
  const target = Math.max(prefix.length, bytes)
  return (prefix + '请只输出 OK。'.repeat(Math.ceil(target / 8))).slice(0, target)
}

function responseText(bytes: number): string {
  if (bytes <= 0) return 'OK'
  return 'OK '.repeat(Math.ceil(bytes / 3)).slice(0, bytes)
}

function countRows(tableName: string): number {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(tableName)) {
    throw new Error(`非法表名：${tableName}`)
  }
  if (tableName === 'usage_records') {
    return usageRecordShards.listUsageRecordShardLocations()
      .reduce((total, location) => {
        const row = usageRecordShards.getUsageRecordShardDatabase(location).prepare('SELECT COUNT(*) AS total FROM usage_records').get() as { total?: number } | undefined
        return total + Number(row?.total ?? 0)
      }, 0)
  }
  const row = databaseModule.getDatasetDatabase().prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function countUsageCatalogRows(tableName: string): number {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(tableName)) {
    throw new Error(`非法表名：${tableName}`)
  }
  const row = databaseModule.getUsageCatalogDatabase().prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function usageRecordBreakdown(total = countRows('usage_records')): UsageRecordBreakdown {
  return {
    total,
    bySuccess: countUsageRecordsGrouped("CASE WHEN success = 1 THEN 'success' ELSE 'failure' END"),
    byStatusCode: countUsageRecordsGrouped("COALESCE(CAST(status_code AS TEXT), 'null')"),
    byErrorCode: countUsageRecordsGrouped("COALESCE(error_code, 'null')"),
    byErrorMessage: countUsageRecordsGrouped("COALESCE(error_message, 'null')"),
    byEndpoint: countUsageRecordsGrouped("COALESCE(endpoint, 'null')")
  }
}

function auditLogBreakdown(total = countRows('audit_logs')): AuditLogBreakdown {
  return {
    total,
    byOutcome: countDatasetGrouped('audit_logs', "COALESCE(audit_outcome, 'null')"),
    bySuccess: countDatasetGrouped('audit_logs', "CASE WHEN success = 1 THEN 'success' ELSE 'failure' END"),
    byStatusCode: countDatasetGrouped('audit_logs', "COALESCE(CAST(final_status_code AS TEXT), 'null')"),
    bySampleReason: countDatasetGrouped('audit_logs', "COALESCE(sample_reason, 'null')"),
    byCaptureStatus: countDatasetGrouped('audit_logs', "COALESCE(capture_status, 'null')")
  }
}

function subtractUsageRecordBreakdown(after: UsageRecordBreakdown, before: UsageRecordBreakdown): UsageRecordBreakdown {
  return {
    total: after.total - before.total,
    bySuccess: subtractCountRecords(after.bySuccess, before.bySuccess),
    byStatusCode: subtractCountRecords(after.byStatusCode, before.byStatusCode),
    byErrorCode: subtractCountRecords(after.byErrorCode, before.byErrorCode),
    byErrorMessage: subtractCountRecords(after.byErrorMessage, before.byErrorMessage),
    byEndpoint: subtractCountRecords(after.byEndpoint, before.byEndpoint)
  }
}

function subtractAuditLogBreakdown(after: AuditLogBreakdown, before: AuditLogBreakdown): AuditLogBreakdown {
  return {
    total: after.total - before.total,
    byOutcome: subtractCountRecords(after.byOutcome, before.byOutcome),
    bySuccess: subtractCountRecords(after.bySuccess, before.bySuccess),
    byStatusCode: subtractCountRecords(after.byStatusCode, before.byStatusCode),
    bySampleReason: subtractCountRecords(after.bySampleReason, before.bySampleReason),
    byCaptureStatus: subtractCountRecords(after.byCaptureStatus, before.byCaptureStatus)
  }
}

function countDatasetGrouped(tableName: string, expression: string): Record<string, number> {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(tableName)) {
    throw new Error(`非法表名：${tableName}`)
  }
  const rows = databaseModule.getDatasetDatabase()
    .prepare(`SELECT ${expression} AS key, COUNT(*) AS total FROM ${tableName} GROUP BY ${expression}`)
    .all() as Array<{ key?: string | number | null; total?: number }>
  return countRowsToRecord(rows)
}

function countUsageRecordsGrouped(expression: string): Record<string, number> {
  const counts = new Map<string, number>()
  for (const location of usageRecordShards.listUsageRecordShardLocations()) {
    const rows = usageRecordShards.getUsageRecordShardDatabase(location)
      .prepare(`SELECT ${expression} AS key, COUNT(*) AS total FROM usage_records GROUP BY ${expression}`)
      .all() as Array<{ key?: string | number | null; total?: number }>
    for (const row of rows) {
      addCount(counts, countKey(row.key), Number(row.total ?? 0))
    }
  }
  return objectFromCounts(counts)
}

function countRowsToRecord(rows: Array<{ key?: string | number | null; total?: number }>): Record<string, number> {
  const counts = new Map<string, number>()
  for (const row of rows) {
    addCount(counts, countKey(row.key), Number(row.total ?? 0))
  }
  return objectFromCounts(counts)
}

function subtractCountRecords(after: Record<string, number>, before: Record<string, number>): Record<string, number> {
  const output = new Map<string, number>()
  const keys = new Set([...Object.keys(after), ...Object.keys(before)])
  for (const key of keys) {
    const delta = (after[key] ?? 0) - (before[key] ?? 0)
    if (delta !== 0) {
      output.set(key, delta)
    }
  }
  return objectFromCounts(output)
}

function addCount(counts: Map<string, number>, key: string, count: number): void {
  counts.set(key, (counts.get(key) ?? 0) + count)
}

function countKey(value: string | number | null | undefined): string {
  return value === null || value === undefined ? 'null' : String(value)
}

function fileBytes(path: string): number {
  return existsSync(path) ? statSync(path).size : 0
}

function objectFromCounts(counts: Map<string, number>): Record<string, number> {
  return Object.fromEntries([...counts.entries()].sort(([left], [right]) => left.localeCompare(right)))
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
  server.on('connection', (socket) => {
    tracker.acceptedSockets += 1
    tracker.socketIds.set(socket, tracker.acceptedSockets)
    tracker.activeSockets.add(socket)
    tracker.peakActiveSockets = Math.max(tracker.peakActiveSockets, tracker.activeSockets.size)
    socket.once('close', () => {
      tracker.closedSockets += 1
      tracker.activeSockets.delete(socket)
    })
  })
}

function recordSocketRequest(tracker: ConnectionTracker, socket: Socket): void {
  const socketId = tracker.socketIds.get(socket)
  if (!socketId) {
    return
  }
  tracker.requestsBySocketId.set(socketId, (tracker.requestsBySocketId.get(socketId) ?? 0) + 1)
}

function connectionStats(tracker: ConnectionTracker): ConnectionStats {
  const requestCounts = [...tracker.requestsBySocketId.values()]
  const totalRequests = requestCounts.reduce((total, value) => total + value, 0)
  return {
    acceptedSockets: tracker.acceptedSockets,
    closedSockets: tracker.closedSockets,
    activeSockets: tracker.activeSockets.size,
    peakActiveSockets: tracker.peakActiveSockets,
    socketsWithRequests: requestCounts.length,
    reusedSockets: requestCounts.filter((count) => count > 1).length,
    maxRequestsPerSocket: requestCounts.length > 0 ? Math.max(...requestCounts) : 0,
    avgRequestsPerSocket: requestCounts.length > 0 ? round(totalRequests / requestCounts.length, 3) : 0
  }
}

function increment(counts: Map<string, number>, key: string): void {
  counts.set(key, (counts.get(key) ?? 0) + 1)
}

function rememberStatusSample(samples: Map<string, string>, status: number, bodyText: string): void {
  const key = String(status)
  if (samples.has(key)) {
    return
  }
  samples.set(key, bodyText.slice(0, 500))
}

function positiveIntegerList(value: string, fallback: number[]): number[] {
  const parsed = value
    .split(',')
    .map((item) => Number(item.trim()))
    .filter((item) => Number.isInteger(item) && item > 0)
    .map((item) => Math.trunc(item))
  return parsed.length > 0 ? [...new Set(parsed)] : fallback
}

function scenarioList(value: string): ScenarioName[] {
  const allowed = new Set<ScenarioName>(['models', 'responses', 'chat', 'responses_stream'])
  const parsed = value
    .split(',')
    .map((item) => item.trim())
    .filter((item): item is ScenarioName => allowed.has(item as ScenarioName))
  return parsed.length > 0 ? [...new Set(parsed)] : ['responses']
}

function auditCaptureMode(value: string): PerfConfig['auditCaptureMode'] {
  if (value === 'default' || value === 'metadata_only') {
    return value
  }
  throw new Error(`非法审计捕获模式：${value}，可选值为 default 或 metadata_only`)
}

function envText(name: string, fallback: string): string {
  const value = process.env[name]?.trim()
  return value ? value : fallback
}

function optionalEnvText(name: string): string | undefined {
  const value = process.env[name]?.trim()
  if (!value) return undefined
  if (isAbsolute(value)) return value
  const invocationCwd = process.env.INIT_CWD?.trim() || process.cwd()
  return resolve(invocationCwd, value)
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const number = Number(envText(name, String(fallback)))
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function envFloat(name: string, fallback: number, min: number, max: number): number {
  const number = Number(envText(name, String(fallback)))
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(number, min), max)
}

function round(value: number, digits: number): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

function nsToMs(value: number): number {
  return Number.isFinite(value) ? value / 1_000_000 : 0
}

function bytesToMb(value: number): number {
  return round(value / 1024 / 1024, 2)
}

function listen(server: http.Server): Promise<void> {
  server.listen({ port: 0, host: '127.0.0.1', backlog: perfListenBacklog })
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
    server.closeIdleConnections?.()
  })
}

function closeDatabases(): void {
  try {
    databaseModule.getBusinessDatabase().close()
  } catch {
  }
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
}

main().catch((error) => {
  console.error('\njuhe-ai 性能综合测试失败')
  console.error(error instanceof Error ? error.stack ?? error.message : error)
  process.exitCode = 1
})
