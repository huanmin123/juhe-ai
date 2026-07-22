import { strict as assert } from 'node:assert'
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import type { Socket } from 'node:net'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

type LoadScenario = 'chat' | 'responses' | 'responses_stream' | 'models'

interface WorkerLoadConfig {
  durationSeconds: number
  warmupSeconds: number
  settleSeconds: number
  concurrency: number
  sampleIntervalSeconds: number
  requestTimeoutMs: number
  upstreamLatencyMs: number
  upstreamStreamChunks: number
  upstreamStreamChunkIntervalMs: number
  upstreamBodyBytes: number
  upstreamErrorRate: number
  accountCount: number
  cooldownAccountCount: number
  accountConcurrencyLimit: number
  model: string
  promptBytes: number
  reportPath: string
  keepTemp: boolean
}

interface SeededGateway {
  apiKey: string
  apiKeyId: string
  groupId: string
  accountIds: string[]
  cooldownAccountIds: string[]
}

interface LoadStats {
  startedAtMs: number
  latenciesMs: number[]
  totalRequests: number
  successRequests: number
  failedRequests: number
  statusCounts: Map<string, number>
  errorCounts: Map<string, number>
  responseBytes: number
}

interface MetricSnapshot {
  elapsedSeconds: number
  request: {
    total: number
    success: number
    failed: number
    qps: number
    successQps: number
    latencyMs: LatencySummary
    statusCounts: Record<string, number>
    errorCounts: Record<string, number>
  }
  storage: StorageSnapshot
  process: {
    children: ProcessSnapshot[]
    eventLoopByRole: Record<string, ProcessEventLoopSummary>
  }
  sqlite: {
    readErrorCount: number
    lockSignalCount: number
    lockSignals: string[]
  }
}

interface StorageSnapshot {
  usageRecords: number
  usageShardFiles: number
  usageCatalogEntries: number
  auditLogs: number
  usageStatsMinuteRows: number
  usageStatsMinuteRequests: number
  usageStatsTotalsRequests: number
  statsUsageShardStates: number
  statsUsageMaxLagSeconds: number
  statsUsageLastSuccessAt?: string
  systemMetricSamples: number
  processEventLoopSamples: number
  databaseBytes: Record<string, number>
  walBytes: Record<string, number>
}

interface ProcessEventLoopSummary {
  samples: number
  maxLagMs: number
  avgLagMs: number
  maxRssMb: number
  latestSampledAt?: string
}

interface ProcessSnapshot {
  pid: number
  ppid: number
  cpuPercent: number
  rssMb: number
  command: string
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
}

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const runId = `${Date.now()}-${Math.random().toString(16).slice(2)}`
const tempRoot = resolve(tmpdir(), `juhe-ai-worker-real-load-${runId}`)
const dataRoot = resolve(tempRoot, 'data')
const logRoot = resolve(tempRoot, 'logs')
const reportRoot = resolve(backendRoot, 'tmp')
const databasePath = resolve(dataRoot, 'business.sqlite3')
const datasetDatabasePath = resolve(dataRoot, 'dataset.sqlite3')
const usageCatalogDatabasePath = resolve(dataRoot, 'usage-catalog.sqlite3')
const statsDatabasePath = resolve(dataRoot, 'stats.sqlite3')
const usageShardRoot = resolve(dataRoot, 'usage-shards')
const codexContextRoot = resolve(dataRoot, 'codex-context')
const config = loadConfig()

mkdirSync(dataRoot, { recursive: true })
mkdirSync(logRoot, { recursive: true })
mkdirSync(reportRoot, { recursive: true })

runtimeConfig.databasePath = databasePath
runtimeConfig.datasetDatabasePath = datasetDatabasePath
runtimeConfig.usageCatalogDatabasePath = usageCatalogDatabasePath
runtimeConfig.statsDatabasePath = statsDatabasePath
runtimeConfig.usageShardRoot = usageShardRoot
runtimeConfig.codexContextRoot = codexContextRoot
runtimeConfig.codexContextStateShardRoot = resolve(codexContextRoot, 'state-shards')
runtimeConfig.secret = 'worker-real-load-test-secret'
runtimeConfig.processRole = 'server'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
logger.level = 'silent'

const childOutput = { stdout: '', stderr: '' }

async function main(): Promise<void> {
  const upstreamRuntime: UpstreamRuntime = {
    totalRequests: 0,
    pathCounts: new Map(),
    connections: createConnectionTracker()
  }
  let upstreamServer: http.Server | undefined
  let backendProcess: ChildProcess | undefined
  const samples: MetricSnapshot[] = []
  const loadStats = createLoadStats()

  try {
    upstreamServer = createMockOpenAIUpstream(config, upstreamRuntime)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const seeded = await seedData(upstreamBaseUrl)

    const port = await freePort()
    backendProcess = startBackendServer(port)
    const baseUrl = `http://127.0.0.1:${port}`
    await waitForHealth(`${baseUrl}/__aisys__/health`, backendProcess)
    await waitForHealth(`${baseUrl}/__aisys__/api/health`, backendProcess)
    await waitForChildProcessTopology(backendProcess, 3, 1)

    console.log('真实 worker 压测启动')
    console.log(`- 后端：${baseUrl}`)
    console.log(`- 模拟上游：${upstreamBaseUrl}`)
    console.log(`- duration=${config.durationSeconds}s warmup=${config.warmupSeconds}s settle=${config.settleSeconds}s concurrency=${config.concurrency} sample=${config.sampleIntervalSeconds}s`)
    console.log(`- accounts=${seeded.accountIds.length} cooldownAccounts=${seeded.cooldownAccountIds.length} upstreamLatency=${config.upstreamLatencyMs}ms`)

    if (config.warmupSeconds > 0) {
      await runLoadPhase({
        baseUrl,
        apiKey: seeded.apiKey,
        durationSeconds: config.warmupSeconds,
        config,
        stats: createLoadStats(),
        record: false
      })
    }

    loadStats.startedAtMs = performance.now()
    const sampler = sampleLoop({
      samples,
      stats: loadStats,
      child: backendProcess,
      durationSeconds: config.durationSeconds,
      intervalSeconds: config.sampleIntervalSeconds
    })
    await runLoadPhase({
      baseUrl,
      apiKey: seeded.apiKey,
      durationSeconds: config.durationSeconds,
      config,
      stats: loadStats,
      record: true
    })
    await sampler
    if (config.settleSeconds > 0) {
      console.log(`压测请求结束，等待 ${config.settleSeconds}s 观察 worker 消化情况`)
      await sleep(config.settleSeconds * 1000)
    }
    samples.push(collectSnapshot(loadStats, backendProcess))

    await stopProcessTree(backendProcess)
    backendProcess = undefined
    await closeServer(upstreamServer)
    upstreamServer = undefined

    const lockSignals = collectLockSignals()
    const summary = buildSummary({
      seeded,
      samples,
      stats: loadStats,
      upstreamRuntime,
      lockSignals
    })
    writeFileSync(config.reportPath, JSON.stringify(summary, null, 2), 'utf8')
    printSummary(summary)
    console.log(`压测报告已写入：${config.reportPath}`)
    if (config.keepTemp) {
      console.log(`临时目录已保留：${tempRoot}`)
    }
  } finally {
    await stopProcessTree(backendProcess)
    await closeServer(upstreamServer)
    if (!config.keepTemp) {
      await removeTempRootWithRetry(tempRoot)
    }
  }
}

async function seedData(upstreamBaseUrl: string): Promise<SeededGateway> {
  const [
    databaseModule,
    schema,
    repositories,
    fixtures
  ] = await Promise.all([
    import('../../storage/database.js'),
    import('../../storage/schema.js'),
    import('../../storage/repositories.js'),
    import('../maintenance/mockdata/fixtures.js')
  ])
  const businessDatabase = databaseModule.getBusinessDatabase()
  schema.applyBusinessSchema(businessDatabase)
  schema.seedDefaults(businessDatabase)
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1_000_000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1_000_000,
    systemApiRateLimitIpWritePerMinute: 1_000_000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1_000_000,
    systemApiRateLimitUserReadPerMinute: 1_000_000,
    systemApiRateLimitUserWritePerMinute: 1_000_000,
    statsAggregationIntervalSeconds: 5,
    statsAggregationBatchSize: 1000,
    statsAggregationMaxBatchesPerRun: 10,
    groupAccountStatsRefreshIntervalSeconds: 5,
    systemMetricsSampleIntervalSeconds: 5,
    accountQualityRefreshIntervalSeconds: 60,
    accountQualityWindowMinutes: 5,
    accountHealthCheckIntervalHours: 1,
    accountHealthCheckJitterMinutes: 0,
    accountHealthCheckBatchSize: 100,
    cooldownAccountRetestIntervalSeconds: 1,
    cooldownAccountRetestBatchSize: 100,
    cooldownAccountRetestMaxBackoffHours: 12,
    temporaryUnschedulableRetryAttempts: 0
  })
  const fixture = fixtures.createMockGatewayFixture({
    label: '真实Worker压测',
    upstreamBaseUrl,
    accountCount: config.accountCount,
    accountConcurrencyLimit: config.accountConcurrencyLimit
  })
  assert(fixture.apiKey, '真实 worker 压测需要生成 API Key')
  const cooldownAccountIds = fixture.accounts.slice(0, Math.min(config.cooldownAccountCount, fixture.accounts.length - 1)).map((account) => account.id)
  markAccountsDueForOpsJobs(businessDatabase, fixture.accounts.map((account) => account.id), cooldownAccountIds)
  databaseModule.closeStorageDatabases()
  return {
    apiKey: fixture.apiKey.key,
    apiKeyId: fixture.apiKey.id,
    groupId: fixture.group.id,
    accountIds: fixture.accounts.map((account) => account.id),
    cooldownAccountIds
  }
}

function markAccountsDueForOpsJobs(database: DatabaseSync, accountIds: string[], cooldownAccountIds: string[]): void {
  const now = new Date().toISOString()
  const old = new Date(Date.now() - 2 * 3600_000).toISOString()
  const markHealthDue = database.prepare(`
    UPDATE accounts
    SET
        last_health_check_at = NULL,
        next_health_check_at = NULL,
        last_health_success_at = NULL,
        health_check_failure_count = 0,
        updated_at = ?
    WHERE id = ?
  `)
  for (const accountId of accountIds) {
    markHealthDue.run(now, accountId)
  }
  const markCooldown = database.prepare(`
    UPDATE accounts
    SET status = 'temporary_unavailable',
        schedulable = 1,
        cooldown_until = ?,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = ?,
        cooldown_retest_last_at = NULL,
        last_error_code = 'worker_load_seed',
        last_error_message = '真实 worker 压测种子冷却账号',
        updated_at = ?
    WHERE id = ?
  `)
  for (const accountId of cooldownAccountIds) {
    markCooldown.run(old, old, now, accountId)
  }
}

function startBackendServer(port: number): ChildProcess {
  const child = spawn(process.execPath, ['--import', 'tsx', 'src/server.ts'], {
    cwd: backendRoot,
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_HOST: '127.0.0.1',
      JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_DATABASE_PATH: databasePath,
      JUHE_AI_DATASET_DATABASE_PATH: datasetDatabasePath,
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath,
      JUHE_AI_STATS_DATABASE_PATH: statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: usageShardRoot,
      JUHE_AI_CODEX_CONTEXT_ROOT: codexContextRoot,
      JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: resolve(codexContextRoot, 'state-shards'),
      JUHE_AI_SECRET: 'worker-real-load-test-secret',
      JUHE_AI_LOG_LEVEL: 'warn',
      JUHE_AI_LOG_DIR: logRoot,
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'true',
      JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true',
      JUHE_AI_USAGE_SHARD_COUNT: '16'
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
  durationSeconds: number
  config: WorkerLoadConfig
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
    config: WorkerLoadConfig
    stats: LoadStats
    record: boolean
  }
): Promise<void> {
  let sequence = 0
  while (performance.now() < endAt) {
    sequence += 1
    const started = performance.now()
    const scenario = scenarioForRequest(workerIndex, sequence)
    try {
      const response = await fetchWithTimeout(input.baseUrl, input.apiKey, scenario, input.config, `${workerIndex}-${sequence}`)
      const responseText = await response.text()
      if (input.record) {
        const latencyMs = performance.now() - started
        input.stats.latenciesMs.push(latencyMs)
        input.stats.totalRequests += 1
        input.stats.responseBytes += Buffer.byteLength(responseText, 'utf8')
        increment(input.stats.statusCounts, String(response.status))
        if (response.ok) {
          input.stats.successRequests += 1
        } else {
          input.stats.failedRequests += 1
          increment(input.stats.errorCounts, `HTTP ${response.status}`)
          if (isLockText(responseText)) {
            increment(input.stats.errorCounts, 'sqlite_lock_response')
          }
        }
      }
    } catch (error) {
      if (input.record) {
        input.stats.latenciesMs.push(performance.now() - started)
        input.stats.totalRequests += 1
        input.stats.failedRequests += 1
        increment(input.stats.errorCounts, formatLoadError(error))
      }
    }
  }
}

async function fetchWithTimeout(
  baseUrl: string,
  apiKey: string,
  scenario: LoadScenario,
  config: WorkerLoadConfig,
  requestId: string
): Promise<Response> {
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

function scenarioForRequest(workerIndex: number, sequence: number): LoadScenario {
  const value = (workerIndex * 997 + sequence) % 10
  if (value <= 1) return 'responses_stream'
  if (value <= 5) return 'responses'
  return 'chat'
}

function buildScenarioRequest(scenario: LoadScenario, config: WorkerLoadConfig, requestId: string): {
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

function createMockOpenAIUpstream(loadConfig: WorkerLoadConfig, runtime: UpstreamRuntime): http.Server {
  const server = http.createServer((req, res) => {
    recordSocket(runtime.connections, req.socket)
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    runtime.totalRequests += 1
    increment(runtime.pathCounts, `${req.method ?? 'GET'} ${url.pathname}`)
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const shouldError = loadConfig.upstreamErrorRate > 0 && Math.random() < loadConfig.upstreamErrorRate
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
          sendChatCompletion(res, loadConfig)
          return
        }
        if (url.pathname === '/v1/responses') {
          const body = parseJsonBody(bodyText)
          if (body.stream === true) {
            sendResponseStream(res, loadConfig)
          } else {
            sendResponseJson(res, loadConfig)
          }
          return
        }
        sendResponseJson(res, loadConfig)
      }, loadConfig.upstreamLatencyMs)
    })
  })
  server.on('connection', (socket) => {
    runtime.connections.acceptedSockets += 1
    runtime.connections.activeSockets.add(socket)
    runtime.connections.peakActiveSockets = Math.max(runtime.connections.peakActiveSockets, runtime.connections.activeSockets.size)
    socket.once('close', () => {
      runtime.connections.closedSockets += 1
      runtime.connections.activeSockets.delete(socket)
    })
  })
  return server
}

function sendModels(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    object: 'list',
    data: [{ id: 'gpt-5.4-mini', object: 'model', created: 0, owned_by: 'openai' }]
  }))
}

function sendChatCompletion(res: http.ServerResponse, loadConfig: WorkerLoadConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'chatcmpl_worker_load',
    object: 'chat.completion',
    created: Math.trunc(Date.now() / 1000),
    model: loadConfig.model,
    choices: [{
      index: 0,
      message: { role: 'assistant', content: responseText(loadConfig.upstreamBodyBytes) },
      finish_reason: 'stop'
    }],
    usage: { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 }
  }))
}

function sendResponseJson(res: http.ServerResponse, loadConfig: WorkerLoadConfig): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'resp_worker_load',
    object: 'response',
    status: 'completed',
    model: loadConfig.model,
    output: [{
      type: 'message',
      role: 'assistant',
      content: [{ type: 'output_text', text: responseText(loadConfig.upstreamBodyBytes) }]
    }],
    usage: {
      input_tokens: 12,
      output_tokens: 8,
      input_tokens_details: { cached_tokens: 0 }
    }
  }))
}

function sendResponseStream(res: http.ServerResponse, loadConfig: WorkerLoadConfig): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  let index = 0
  const writeNext = (): void => {
    if (index < loadConfig.upstreamStreamChunks) {
      const event = {
        type: 'response.output_text.delta',
        delta: responseText(Math.max(1, Math.ceil(loadConfig.upstreamBodyBytes / loadConfig.upstreamStreamChunks)))
      }
      res.write(`event: response.output_text.delta\ndata: ${JSON.stringify(event)}\n\n`)
      index += 1
      setTimeout(writeNext, loadConfig.upstreamStreamChunkIntervalMs)
      return
    }
    res.write(`event: response.completed\ndata: ${JSON.stringify({
      type: 'response.completed',
      response: { status: 'completed', usage: { input_tokens: 12, output_tokens: 8 } }
    })}\n\n`)
    res.end()
  }
  writeNext()
}

function sendUpstreamError(res: http.ServerResponse): void {
  res.writeHead(500, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    error: { type: 'server_error', code: 'mock_upstream_error', message: '模拟上游错误' }
  }))
}

async function sampleLoop(input: {
  samples: MetricSnapshot[]
  stats: LoadStats
  child: ChildProcess
  durationSeconds: number
  intervalSeconds: number
}): Promise<void> {
  const startedAt = performance.now()
  let nextSampleAt = startedAt + input.intervalSeconds * 1000
  while (performance.now() - startedAt < input.durationSeconds * 1000) {
    const waitMs = Math.max(0, nextSampleAt - performance.now())
    await sleep(waitMs)
    const snapshot = collectSnapshot(input.stats, input.child)
    input.samples.push(snapshot)
    printSample(snapshot)
    nextSampleAt += input.intervalSeconds * 1000
  }
}

function collectSnapshot(stats: LoadStats, child: ChildProcess): MetricSnapshot {
  const elapsedSeconds = Math.max(0.001, (performance.now() - stats.startedAtMs) / 1000)
  const storage = collectStorageSnapshot()
  const lockSignals = collectLockSignals(20)
  return {
    elapsedSeconds: round(elapsedSeconds, 3),
    request: {
      total: stats.totalRequests,
      success: stats.successRequests,
      failed: stats.failedRequests,
      qps: round(stats.totalRequests / elapsedSeconds, 2),
      successQps: round(stats.successRequests / elapsedSeconds, 2),
      latencyMs: latencySummary(stats.latenciesMs),
      statusCounts: objectFromCounts(stats.statusCounts),
      errorCounts: objectFromCounts(stats.errorCounts)
    },
    storage,
    process: {
      children: listRelatedProcesses(child.pid),
      eventLoopByRole: collectProcessEventLoopSummary()
    },
    sqlite: {
      readErrorCount: sqliteReadErrors.length,
      lockSignalCount: lockSignals.length,
      lockSignals: lockSignals.slice(0, 10)
    }
  }
}

const sqliteReadErrors: string[] = []

function collectStorageSnapshot(): StorageSnapshot {
  return {
    usageRecords: countUsageRecords(),
    usageShardFiles: countUsageShardFiles(),
    usageCatalogEntries: countRowsSafe(usageCatalogDatabasePath, 'usage_record_shard_entries'),
    auditLogs: countRowsSafe(datasetDatabasePath, 'audit_logs'),
    usageStatsMinuteRows: countRowsSafe(statsDatabasePath, 'usage_stats_minute'),
    usageStatsMinuteRequests: sumColumnSafe(statsDatabasePath, 'usage_stats_minute', 'request_count'),
    usageStatsTotalsRequests: sumColumnSafe(statsDatabasePath, 'usage_stats_totals', 'request_count'),
    statsUsageShardStates: countStatsUsageShardStates(),
    statsUsageMaxLagSeconds: maxStatsUsageLagSeconds(),
    statsUsageLastSuccessAt: latestStatsUsageSuccessAt(),
    systemMetricSamples: countRowsSafe(statsDatabasePath, 'system_metrics_samples'),
    processEventLoopSamples: countRowsSafe(statsDatabasePath, 'process_event_loop_samples'),
    databaseBytes: {
      business: fileBytes(databasePath),
      dataset: fileBytes(datasetDatabasePath),
      usageCatalog: fileBytes(usageCatalogDatabasePath),
      stats: fileBytes(statsDatabasePath),
      usageShards: sumUsageShardFileBytes('')
    },
    walBytes: {
      business: fileBytes(`${databasePath}-wal`),
      dataset: fileBytes(`${datasetDatabasePath}-wal`),
      usageCatalog: fileBytes(`${usageCatalogDatabasePath}-wal`),
      stats: fileBytes(`${statsDatabasePath}-wal`),
      usageShards: sumUsageShardFileBytes('-wal')
    }
  }
}

function collectProcessEventLoopSummary(): Record<string, ProcessEventLoopSummary> {
  return withReadOnlyDatabase(statsDatabasePath, (database) => {
    const rows = database.prepare(`
      SELECT process_role AS role,
        COUNT(*) AS samples,
        MAX(event_loop_lag_ms) AS max_lag_ms,
        AVG(event_loop_lag_ms) AS avg_lag_ms,
        MAX(process_rss_bytes) AS max_rss_bytes,
        MAX(sampled_at) AS latest_sampled_at
      FROM process_event_loop_samples
      GROUP BY process_role
      ORDER BY process_role
    `).all() as Array<Record<string, unknown>>
    return Object.fromEntries(rows.map((row) => [
      String(row.role),
      {
        samples: Number(row.samples ?? 0),
        maxLagMs: round(Number(row.max_lag_ms ?? 0), 3),
        avgLagMs: round(Number(row.avg_lag_ms ?? 0), 3),
        maxRssMb: bytesToMb(Number(row.max_rss_bytes ?? 0)),
        latestSampledAt: optionalText(row.latest_sampled_at)
      }
    ]))
  }, {})
}

function countUsageRecords(): number {
  let total = 0
  for (const filePath of usageShardDatabaseFiles()) {
    total += countRowsSafe(filePath, 'usage_records')
  }
  return total
}

function countUsageShardFiles(): number {
  return usageShardDatabaseFiles().length
}

function usageShardDatabaseFiles(): string[] {
  return listFilesRecursive(usageShardRoot).filter((filePath) => filePath.endsWith('.sqlite3'))
}

function countRowsSafe(databasePathInput: string, tableName: string): number {
  if (!existsSync(databasePathInput)) return 0
  return withReadOnlyDatabase(databasePathInput, (database) => {
    const row = database.prepare(`SELECT COUNT(*) AS total FROM ${safeIdentifier(tableName)}`).get() as { total?: number } | undefined
    return Number(row?.total ?? 0)
  }, 0)
}

function sumColumnSafe(databasePathInput: string, tableName: string, columnName: string): number {
  if (!existsSync(databasePathInput)) return 0
  return withReadOnlyDatabase(databasePathInput, (database) => {
    const row = database.prepare(`SELECT COALESCE(SUM(${safeIdentifier(columnName)}), 0) AS total FROM ${safeIdentifier(tableName)}`).get() as { total?: number } | undefined
    return Number(row?.total ?? 0)
  }, 0)
}

function countStatsUsageShardStates(): number {
  return withReadOnlyDatabase(statsDatabasePath, (database) => {
    const row = database.prepare(`
      SELECT COUNT(*) AS total
      FROM stats_job_state
      WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation'
    `).get() as { total?: number } | undefined
    return Number(row?.total ?? 0)
  }, 0)
}

function maxStatsUsageLagSeconds(): number {
  return withReadOnlyDatabase(statsDatabasePath, (database) => {
    const row = database.prepare(`
      SELECT COALESCE(MAX(lag_seconds), 0) AS max_lag
      FROM stats_job_state
      WHERE job_name = 'usage_stats_aggregation'
    `).get() as { max_lag?: number } | undefined
    return Number(row?.max_lag ?? 0)
  }, 0)
}

function latestStatsUsageSuccessAt(): string | undefined {
  return withReadOnlyDatabase(statsDatabasePath, (database) => {
    const row = database.prepare(`
      SELECT MAX(last_success_at) AS last_success_at
      FROM stats_job_state
      WHERE job_name = 'usage_stats_aggregation'
    `).get() as { last_success_at?: string | null } | undefined
    return row?.last_success_at ?? undefined
  }, undefined)
}

function withReadOnlyDatabase<T>(databasePathInput: string, callback: (database: DatabaseSync) => T, fallback: T): T {
  let database: DatabaseSync | undefined
  try {
    database = new DatabaseSync(databasePathInput, { readOnly: true })
    database.exec('PRAGMA busy_timeout = 100')
    return callback(database)
  } catch (error) {
    const message = formatLoadError(error)
    if (!/no such table|no such column/i.test(message)) {
      sqliteReadErrors.push(message)
    }
    return fallback
  } finally {
    try {
      database?.close()
    } catch {
    }
  }
}

function sumUsageShardFileBytes(suffix: '' | '-wal'): number {
  return usageShardDatabaseFiles().reduce((total, filePath) => total + fileBytes(`${filePath}${suffix}`), 0)
}

function collectLockSignals(limit = Number.POSITIVE_INFINITY): string[] {
  const sources = [
    childOutput.stdout,
    childOutput.stderr,
    ...listFilesRecursive(logRoot)
      .filter((filePath) => /\.(log|txt)$/i.test(filePath))
      .map((filePath) => safeReadFile(filePath))
  ]
  const signals: string[] = []
  for (const source of sources) {
    for (const line of source.split('\n')) {
      if (isLockText(line)) {
        signals.push(line.slice(0, 500))
        if (signals.length >= limit) return signals
      }
    }
  }
  for (const error of sqliteReadErrors) {
    if (isLockText(error)) {
      signals.push(error.slice(0, 500))
      if (signals.length >= limit) return signals
    }
  }
  return signals
}

function isLockText(text: string): boolean {
  return /SQLITE_BUSY|SQLITE_LOCKED|database is locked|database table is locked|\bbusy\b/i.test(text)
}

function printSample(snapshot: MetricSnapshot): void {
  const eventLoop = Object.entries(snapshot.process.eventLoopByRole)
    .map(([role, value]) => `${role}:${value.maxLagMs}ms/${value.maxRssMb}MB`)
    .join(' ')
  console.log([
    `[${snapshot.elapsedSeconds.toFixed(0)}s/${config.durationSeconds}s]`,
    `req=${snapshot.request.total}`,
    `qps=${snapshot.request.qps}`,
    `ok=${snapshot.request.success}`,
    `err=${snapshot.request.failed}`,
    `p95=${snapshot.request.latencyMs.p95}ms`,
    `usage=${snapshot.storage.usageRecords}`,
    `audit=${snapshot.storage.auditLogs}`,
    `statsReq=${snapshot.storage.usageStatsMinuteRequests}`,
    `statsLag=${snapshot.storage.statsUsageMaxLagSeconds}s`,
    `sqliteReadErr=${snapshot.sqlite.readErrorCount}`,
    `lockSignals=${snapshot.sqlite.lockSignalCount}`,
    `loop=${eventLoop || 'none'}`
  ].join(' '))
}

function buildSummary(input: {
  seeded: SeededGateway
  samples: MetricSnapshot[]
  stats: LoadStats
  upstreamRuntime: UpstreamRuntime
  lockSignals: string[]
}): Record<string, unknown> {
  const finalSnapshot = input.samples[input.samples.length - 1] ?? collectSnapshot(input.stats, { pid: undefined } as ChildProcess)
  const elapsedSeconds = Math.max(0.001, (performance.now() - input.stats.startedAtMs) / 1000)
  const usageDigestionGap = Math.max(0, input.stats.successRequests - finalSnapshot.storage.usageRecords)
  return {
    generatedAt: new Date().toISOString(),
    note: '真实多进程压测：临时 server + DB service + ingest-worker + stats-worker + ops-worker + 本地 mock 上游。',
    config,
    tempRoot: config.keepTemp ? tempRoot : undefined,
    seeded: {
      apiKeyId: input.seeded.apiKeyId,
      groupId: input.seeded.groupId,
      accountCount: input.seeded.accountIds.length,
      cooldownAccountCount: input.seeded.cooldownAccountIds.length
    },
    request: {
      elapsedSeconds: round(elapsedSeconds, 3),
      total: input.stats.totalRequests,
      success: input.stats.successRequests,
      failed: input.stats.failedRequests,
      qps: round(input.stats.totalRequests / elapsedSeconds, 2),
      successQps: round(input.stats.successRequests / elapsedSeconds, 2),
      latencyMs: latencySummary(input.stats.latenciesMs),
      statusCounts: objectFromCounts(input.stats.statusCounts),
      errorCounts: objectFromCounts(input.stats.errorCounts),
      responseBytes: input.stats.responseBytes
    },
    digestion: {
      usageRecords: finalSnapshot.storage.usageRecords,
      usageCatalogEntries: finalSnapshot.storage.usageCatalogEntries,
      auditLogs: finalSnapshot.storage.auditLogs,
      usageStatsMinuteScopeRequests: finalSnapshot.storage.usageStatsMinuteRequests,
      usageStatsTotalsScopeRequests: finalSnapshot.storage.usageStatsTotalsRequests,
      statsUsageShardStates: finalSnapshot.storage.statsUsageShardStates,
      usageDigestionGap,
      usageDigestionRatio: input.stats.successRequests > 0 ? round(finalSnapshot.storage.usageRecords / input.stats.successRequests, 4) : 0,
      statsUsageMaxLagSeconds: finalSnapshot.storage.statsUsageMaxLagSeconds,
      statsUsageLastSuccessAt: finalSnapshot.storage.statsUsageLastSuccessAt,
      note: 'usageStats*Requests 是按 system/account/group/apiKey 等统计 scope 展开的聚合请求数，不与 usageRecords 一一对应；统计消化以 shard cursor 状态和 lag 为准。'
    },
    sqlite: {
      readErrorCount: sqliteReadErrors.length,
      lockSignalCount: input.lockSignals.length,
      lockSignals: input.lockSignals.slice(0, 20),
      databaseBytes: finalSnapshot.storage.databaseBytes,
      walBytes: finalSnapshot.storage.walBytes
    },
    process: {
      latestChildren: finalSnapshot.process.children,
      eventLoopByRole: finalSnapshot.process.eventLoopByRole
    },
    upstream: {
      totalRequests: input.upstreamRuntime.totalRequests,
      pathCounts: objectFromCounts(input.upstreamRuntime.pathCounts),
      connections: {
        acceptedSockets: input.upstreamRuntime.connections.acceptedSockets,
        closedSockets: input.upstreamRuntime.connections.closedSockets,
        activeSockets: input.upstreamRuntime.connections.activeSockets.size,
        peakActiveSockets: input.upstreamRuntime.connections.peakActiveSockets
      }
    },
    samples: input.samples
  }
}

function printSummary(summary: Record<string, unknown>): void {
  const request = summary.request as Record<string, unknown>
  const digestion = summary.digestion as Record<string, unknown>
  const sqlite = summary.sqlite as Record<string, unknown>
  console.log('\n真实 worker 压测汇总')
  console.log(`- 请求：total=${request.total} success=${request.success} failed=${request.failed} qps=${request.qps} p95=${(request.latencyMs as LatencySummary).p95}ms p99=${(request.latencyMs as LatencySummary).p99}ms`)
  console.log(`- 消化：usage=${digestion.usageRecords} usageCatalog=${digestion.usageCatalogEntries} audit=${digestion.auditLogs} statsScopeReq=${digestion.usageStatsMinuteScopeRequests} usageGap=${digestion.usageDigestionGap} statsShardStates=${digestion.statsUsageShardStates} statsLag=${digestion.statsUsageMaxLagSeconds}s`)
  console.log(`- SQLite：readErrors=${sqlite.readErrorCount} lockSignals=${sqlite.lockSignalCount}`)
}

function createLoadStats(): LoadStats {
  return {
    startedAtMs: performance.now(),
    latenciesMs: [],
    totalRequests: 0,
    successRequests: 0,
    failedRequests: 0,
    statusCounts: new Map(),
    errorCounts: new Map(),
    responseBytes: 0
  }
}

function latencySummary(values: number[]): LatencySummary {
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

function listRelatedProcesses(parentPid: number | undefined): ProcessSnapshot[] {
  if (!parentPid) return []
  const all = listProcessTable()
  const related = new Set<number>([parentPid])
  let changed = true
  while (changed) {
    changed = false
    for (const processInfo of all) {
      if (related.has(processInfo.ppid) && !related.has(processInfo.pid)) {
        related.add(processInfo.pid)
        changed = true
      }
    }
  }
  return all.filter((processInfo) => related.has(processInfo.pid))
}

function listProcessTable(): ProcessSnapshot[] {
  if (process.platform === 'win32') {
    const output = execFileSync('pwsh', [
      '-NoProfile',
      '-Command',
      'Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,CommandLine | ConvertTo-Json -Compress'
    ], { encoding: 'utf8' })
    const parsed = JSON.parse(output || '[]') as unknown
    const rows = Array.isArray(parsed) ? parsed : [parsed]
    return rows.flatMap((row) => {
      if (!row || typeof row !== 'object') return []
      const record = row as Record<string, unknown>
      const pid = Number(record.ProcessId)
      const ppid = Number(record.ParentProcessId)
      if (!Number.isFinite(pid) || !Number.isFinite(ppid)) return []
      return [{
        pid,
        ppid,
        cpuPercent: 0,
        rssMb: 0,
        command: typeof record.CommandLine === 'string' ? record.CommandLine : ''
      }]
    })
  }
  const output = execFileSync('ps', ['-eo', 'pid=,ppid=,pcpu=,rss=,command='], { encoding: 'utf8' })
  const rows: ProcessSnapshot[] = []
  for (const line of output.split('\n')) {
    const match = line.match(/^\s*(\d+)\s+(\d+)\s+([\d.]+)\s+(\d+)\s+(.*)$/)
    if (!match) continue
    rows.push({
      pid: Number(match[1]),
      ppid: Number(match[2]),
      cpuPercent: Number(match[3]),
      rssMb: round(Number(match[4]) / 1024, 2),
      command: match[5]
    })
  }
  return rows
}

async function waitForChildProcessTopology(child: ChildProcess, expectedWorkerCount: number, expectedDbServiceCount: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 30_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出：exitCode=${child.exitCode}\nstdout=${childOutput.stdout}\nstderr=${childOutput.stderr}`)
    }
    const children = listRelatedProcesses(child.pid).filter((item) => item.pid !== child.pid)
    const workerChildren = children.filter((item) => /(?:^|\b)worker\.(?:js|ts)\b/i.test(item.command))
    const dbServiceChildren = children.filter((item) => /(?:^|\b)db-service\.(?:js|ts)\b/i.test(item.command))
    if (workerChildren.length >= expectedWorkerCount && dbServiceChildren.length >= expectedDbServiceCount) {
      return
    }
    await sleep(250)
  }
  throw new Error(`等待 worker 拓扑超时\nstdout=${childOutput.stdout}\nstderr=${childOutput.stderr}`)
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
      lastError = new Error(`${url} HTTP ${response.status}`)
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

async function removeTempRootWithRetry(root: string): Promise<void> {
  const maxAttempts = process.platform === 'win32' ? 10 : 1
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    try {
      rmSync(root, { recursive: true, force: true })
      return
    } catch (error) {
      if (attempt >= maxAttempts || !isRetryableTempCleanupError(error)) {
        throw error
      }
      await sleep(Math.min(1000, attempt * 100))
    }
  }
}

function isRetryableTempCleanupError(error: unknown): boolean {
  if (!error || typeof error !== 'object' || !('code' in error)) return false
  const code = String(error.code)
  return code === 'EBUSY' || code === 'EPERM' || code === 'ENOTEMPTY'
}

function loadConfig(): WorkerLoadConfig {
  return {
    durationSeconds: envInteger('JUHE_AI_WORKER_LOAD_DURATION_SECONDS', 600, 1, 3600),
    warmupSeconds: envInteger('JUHE_AI_WORKER_LOAD_WARMUP_SECONDS', 10, 0, 600),
    settleSeconds: envInteger('JUHE_AI_WORKER_LOAD_SETTLE_SECONDS', 20, 0, 600),
    concurrency: envInteger('JUHE_AI_WORKER_LOAD_CONCURRENCY', 10, 1, 1000),
    sampleIntervalSeconds: envInteger('JUHE_AI_WORKER_LOAD_SAMPLE_INTERVAL_SECONDS', 10, 1, 300),
    requestTimeoutMs: envInteger('JUHE_AI_WORKER_LOAD_REQUEST_TIMEOUT_MS', 15_000, 100, 600_000),
    upstreamLatencyMs: envInteger('JUHE_AI_WORKER_LOAD_UPSTREAM_LATENCY_MS', 80, 0, 600_000),
    upstreamStreamChunks: envInteger('JUHE_AI_WORKER_LOAD_STREAM_CHUNKS', 4, 1, 1000),
    upstreamStreamChunkIntervalMs: envInteger('JUHE_AI_WORKER_LOAD_STREAM_CHUNK_INTERVAL_MS', 20, 0, 600_000),
    upstreamBodyBytes: envInteger('JUHE_AI_WORKER_LOAD_UPSTREAM_BODY_BYTES', 512, 0, 2 * 1024 * 1024),
    upstreamErrorRate: envFloat('JUHE_AI_WORKER_LOAD_UPSTREAM_ERROR_RATE', 0, 0, 1),
    accountCount: envInteger('JUHE_AI_WORKER_LOAD_ACCOUNT_COUNT', 24, 2, 1000),
    cooldownAccountCount: envInteger('JUHE_AI_WORKER_LOAD_COOLDOWN_ACCOUNT_COUNT', 4, 0, 100),
    accountConcurrencyLimit: envInteger('JUHE_AI_WORKER_LOAD_ACCOUNT_CONCURRENCY', 10000, 1, 1000000),
    model: envText('JUHE_AI_WORKER_LOAD_MODEL', 'gpt-5.4-mini'),
    promptBytes: envInteger('JUHE_AI_WORKER_LOAD_PROMPT_BYTES', 64, 1, 1024 * 1024),
    reportPath: resolve(envText('JUHE_AI_WORKER_LOAD_REPORT_PATH', resolve(reportRoot, `worker-real-load-${runId}.json`))),
    keepTemp: envBoolean('JUHE_AI_WORKER_LOAD_KEEP_TEMP', false)
  }
}

function envText(name: string, fallback: string): string {
  return process.env[name]?.trim() || fallback
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = Number(envText(name, String(fallback)))
  if (!Number.isFinite(value)) return fallback
  return Math.min(max, Math.max(min, Math.trunc(value)))
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

function promptText(bytes: number, requestId: string): string {
  const prefix = `请求 ${requestId}：`
  const target = Math.max(prefix.length, bytes)
  return (prefix + '请只输出 OK。'.repeat(Math.ceil(target / 8))).slice(0, target)
}

function responseText(bytes: number): string {
  if (bytes <= 0) return 'OK'
  return 'OK '.repeat(Math.ceil(bytes / 3)).slice(0, bytes)
}

function parseJsonBody(text: string): Record<string, unknown> {
  try {
    const value = JSON.parse(text) as unknown
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function createConnectionTracker(): ConnectionTracker {
  return {
    acceptedSockets: 0,
    closedSockets: 0,
    peakActiveSockets: 0,
    activeSockets: new Set()
  }
}

function recordSocket(tracker: ConnectionTracker, socket: Socket): void {
  tracker.peakActiveSockets = Math.max(tracker.peakActiveSockets, tracker.activeSockets.size)
  if (socket.destroyed) {
    return
  }
}

function increment(map: Map<string, number>, key: string): void {
  map.set(key, (map.get(key) ?? 0) + 1)
}

function objectFromCounts(map: Map<string, number>): Record<string, number> {
  return Object.fromEntries([...map.entries()].sort(([left], [right]) => left.localeCompare(right)))
}

function safeIdentifier(value: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(value)) {
    throw new Error(`非法 SQL 标识符：${value}`)
  }
  return value
}

function listFilesRecursive(root: string): string[] {
  if (!existsSync(root)) return []
  const output: string[] = []
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const filePath = resolve(root, entry.name)
    if (entry.isDirectory()) {
      output.push(...listFilesRecursive(filePath))
    } else if (entry.isFile()) {
      output.push(filePath)
    }
  }
  return output
}

function safeReadFile(filePath: string): string {
  try {
    return readFileSync(filePath, 'utf8')
  } catch {
    return ''
  }
}

function fileBytes(filePath: string): number {
  try {
    return statSync(filePath).size
  } catch {
    return 0
  }
}

function bytesToMb(value: number): number {
  return round(value / 1024 / 1024, 2)
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function formatLoadError(error: unknown): string {
  if (!(error instanceof Error)) {
    return String(error)
  }
  const cause = (error as Error & { cause?: unknown }).cause
  const causeCode = typeof cause === 'object' && cause !== null && typeof (cause as Record<string, unknown>).code === 'string'
    ? String((cause as Record<string, unknown>).code)
    : undefined
  return [error.name || 'Error', error.message, causeCode ? `cause=${causeCode}` : undefined].filter(Boolean).join(': ').slice(0, 240)
}

function round(value: number, digits = 2): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (typeof address === 'object' && address && typeof address.port === 'number') {
    return address.port
  }
  throw new Error('无法读取服务端口')
}

function closeServer(server: http.Server | undefined): Promise<void> {
  return new Promise((resolvePromise) => {
    if (!server || !server.listening) {
      resolvePromise()
      return
    }
    server.close(() => resolvePromise())
  })
}

function freePort(): Promise<number> {
  return new Promise((resolvePromise, rejectPromise) => {
    const server = net.createServer()
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      const port = typeof address === 'object' && address ? address.port : undefined
      server.close(() => {
        if (typeof port === 'number') {
          resolvePromise(port)
        } else {
          rejectPromise(new Error('无法分配临时端口'))
        }
      })
    })
  })
}

function tailText(text: string): string {
  return text.length > 20_000 ? text.slice(text.length - 20_000) : text
}

await main()
