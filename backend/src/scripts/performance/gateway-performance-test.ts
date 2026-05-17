import { existsSync, mkdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { dirname, isAbsolute, join, resolve } from 'node:path'
import { monitorEventLoopDelay, performance } from 'node:perf_hooks'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

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
  responseBytes: number
  usageRecordsDelta: number
  auditLogsDelta: number
  usageRecordQueueLength: number
  auditLogQueueLength: number
  upstreamRequestsDelta: number
}

interface LoadStats {
  latenciesMs: number[]
  totalRequests: number
  successRequests: number
  failedRequests: number
  statusCounts: Map<string, number>
  errorCounts: Map<string, number>
  responseBytes: number
}

interface UpstreamRuntime {
  totalRequests: number
  pathCounts: Map<string, number>
}

const tempRoot = resolve(tmpdir(), `juhe-ai-perf-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'perf.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'perf-records.sqlite3')
runtimeConfig.secret = 'juhe-ai-performance-test-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/gateway/openai-gateway-request-body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

async function main(): Promise<void> {
  const config = loadConfig()
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamRuntime: UpstreamRuntime = {
    totalRequests: 0,
    pathCounts: new Map()
  }

  try {
    upstreamServer = createMockOpenAIUpstream(config, upstreamRuntime)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const seeded = seedGatewayData(config, upstreamBaseUrl)
    gatewayCache.clearGatewayRuntimeCacheLocal()

    appServer = createGatewayServer()
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

    const summary = buildSummary(config, results, seeded, upstreamRuntime)
    console.log('\n性能综合测试汇总')
    console.log(JSON.stringify(summary, null, 2))
    if (config.reportPath) {
      mkdirSync(dirname(config.reportPath), { recursive: true })
      writeFileSync(config.reportPath, JSON.stringify(summary, null, 2), 'utf8')
      console.log(`\n性能测试报告已写入：${config.reportPath}`)
    }
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
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
    promptBytes: envInteger('JUHE_AI_PERF_PROMPT_BYTES', 64, 1, 2 * 1024 * 1024),
    p95TargetMs: envInteger('JUHE_AI_PERF_P95_TARGET_MS', 1000, 1, 600000),
    reportPath: optionalEnvText('JUHE_AI_PERF_REPORT_PATH')
  }
}

function seedGatewayData(config: PerfConfig, upstreamBaseUrl: string): SeededGateway {
  const access = { systemAccountId: 'sys_admin', role: 'user' as const }
  const group = repositories.createGroup({
    name: `性能压测分组-${Date.now()}`,
    providerCode: 'openai',
    enabled: true
  }, access)
  const accountIds: string[] = []
  for (let index = 0; index < config.accountCount; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `性能压测账户-${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-perf-${index + 1}`,
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: config.accountConcurrencyLimit,
      priority: index
    }, access)
    accountIds.push(account.id)
  }
  const apiKey = repositories.createApiKeyRecord({
    name: `性能压测 Key-${Date.now()}`,
    groupId: group.id,
    status: 'active'
  }, access)
  return {
    apiKey: apiKey.key,
    apiKeyId: apiKey.id,
    groupId: group.id,
    accountIds
  }
}

function createGatewayServer(): http.Server {
  const gatewayRawBodyLimit = '64mb'
  const app = express()
  app.use(requestContextMiddleware)
  app.use(cors({ credentials: true, origin: true }))
  app.get('/__aisys__/health', (_req, res) => {
    res.json({ status: 'ok', service: 'juhe-ai-performance-test' })
  })
  app.use(express.raw({ type: () => true, limit: gatewayRawBodyLimit }), handleGatewayRawBodyError, captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
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
  return http.createServer((req, res) => {
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
    auditLogQueue.flushAllAuditLogQueue()
  }

  const usageRecordsBefore = countRows('usage_records')
  const auditLogsBefore = countRows('audit_logs')
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
  auditLogQueue.flushAllAuditLogQueue()

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
    responseBytes: stats.responseBytes,
    usageRecordsDelta: countRows('usage_records') - usageRecordsBefore,
    auditLogsDelta: countRows('audit_logs') - auditLogsBefore,
    usageRecordQueueLength: usageRecordQueue.getUsageRecordQueueRuntime().queueLength,
    auditLogQueueLength: auditLogQueue.getAuditLogQueueRuntime().queueLength,
    upstreamRequestsDelta: input.upstreamRuntime.totalRequests - upstreamRequestsBefore
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
      const bytes = Buffer.byteLength(await response.text(), 'utf8')
      if (input.record) {
        const latencyMs = performance.now() - started
        stats.latenciesMs.push(latencyMs)
        stats.totalRequests += 1
        stats.responseBytes += bytes
        increment(stats.statusCounts, String(response.status))
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
        increment(stats.errorCounts, error instanceof Error ? error.name || error.message : String(error))
      }
    }
  }
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

function buildSummary(config: PerfConfig, results: ScenarioResult[], seeded: SeededGateway, upstreamRuntime: UpstreamRuntime): Record<string, unknown> {
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
      pathCounts: objectFromCounts(upstreamRuntime.pathCounts)
    },
    recordDatabase: {
      usageRecords: countRows('usage_records'),
      auditLogs: countRows('audit_logs'),
      bytes: fileBytes(runtimeConfig.recordDatabasePath),
      walBytes: fileBytes(`${runtimeConfig.recordDatabasePath}-wal`)
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
  const row = databaseModule.getRecordDatabase().prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function fileBytes(path: string): number {
  return existsSync(path) ? statSync(path).size : 0
}

function objectFromCounts(counts: Map<string, number>): Record<string, number> {
  return Object.fromEntries([...counts.entries()].sort(([left], [right]) => left.localeCompare(right)))
}

function increment(counts: Map<string, number>, key: string): void {
  counts.set(key, (counts.get(key) ?? 0) + 1)
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
  server.listen(0, '127.0.0.1')
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
    databaseModule.getDatabase().close()
  } catch {
  }
  try {
    databaseModule.getRecordDatabase().close()
  } catch {
  }
}

main().catch((error) => {
  console.error('\njuhe-ai 性能综合测试失败')
  console.error(error instanceof Error ? error.stack ?? error.message : error)
  process.exitCode = 1
})
