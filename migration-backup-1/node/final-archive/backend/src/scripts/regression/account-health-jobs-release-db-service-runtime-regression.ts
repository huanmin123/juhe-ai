import assert from 'node:assert/strict'
import { spawn, type ChildProcessByStdio } from 'node:child_process'
import { createCipheriv, createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import type { DatabaseSync } from 'node:sqlite'
import type { Readable } from 'node:stream'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync
const releaseRoot = resolveRequiredReleaseRoot(process.env.JUHE_AI_J1_RELEASE_ROOT)
const dbServicePath = resolveRequiredFile(join(releaseRoot, 'backend', 'dist', 'db-service.js'), 'release DB-service 入口')
const jobsBinary = resolveRequiredFile(
  process.env.JUHE_AI_J1_RELEASE_JOBS_BINARY?.trim()
    ? resolve(process.env.JUHE_AI_J1_RELEASE_JOBS_BINARY)
    : join(releaseRoot, 'backend-go', process.platform === 'win32' ? 'juhe-ai-jobs.exe' : 'juhe-ai-jobs'),
  'release jobs 二进制'
)
const releaseSchema = await import(pathToFileURL(resolveRequiredFile(join(releaseRoot, 'backend', 'dist', 'storage', 'schema.js'), 'release schema 模块')).href) as {
  applyBusinessSchema(database: DatabaseSync): void
  seedDefaults(database: DatabaseSync): void
}
const releaseInputProtocol = await import(pathToFileURL(resolveRequiredFile(join(releaseRoot, 'backend', 'dist', 'modules', 'background', 'account-health-jobs-input.protocol.js'), 'release J1 input 协议模块')).href) as {
  publishAccountHealthJobsInput(input: { root: string; accountId: string; signingKey: string; payload: Record<string, unknown> }): void
}

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-release-db-service-'))
const accountId = 'j1-release-db-service-account'
const businessPath = join(root, 'business.sqlite3')
const datasetPath = join(root, 'dataset.sqlite3')
const usageCatalogPath = join(root, 'usage.sqlite3')
const statsPath = join(root, 'stats.sqlite3')
const runtimeLogPath = join(root, 'runtime-log.sqlite3')
const tableMonitorPath = join(root, 'table-monitor.sqlite3')
const jobsStorePath = join(root, 'jobs.sqlite3')
const inputDirectory = join(root, 'input')
const signingKey = randomBytes(32).toString('base64url')
const credentialSecret = 'j1-release-db-service-credential-secret'
const commonEnvironment: NodeJS.ProcessEnv = {
  ...process.env,
  NODE_ENV: 'production',
  JUHE_AI_DISABLE_BASE_ENV: 'true',
  JUHE_AI_RUNTIME_MODE: 'standalone',
  JUHE_AI_PROCESS_ROLE: 'db-service',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_CACHE_DRIVER: 'memory',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
  JUHE_AI_QUEUE_DRIVER: 'memory',
  JUHE_AI_SECRET: 'j1-release-db-service-runtime-secret',
  JUHE_AI_ALLOWED_ORIGINS: 'http://127.0.0.1:39100',
  JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
  JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  JUHE_AI_LOG_FILE_ENABLED: 'false',
  JUHE_AI_LOG_DIR: join(root, 'logs'),
  JUHE_AI_DATABASE_PATH: businessPath,
  JUHE_AI_DATASET_DATABASE_PATH: datasetPath,
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogPath,
  JUHE_AI_STATS_DATABASE_PATH: statsPath,
  JUHE_AI_RUNTIME_LOG_DATABASE_PATH: runtimeLogPath,
  JUHE_AI_TABLE_MONITOR_DATABASE_PATH: tableMonitorPath,
  JUHE_AI_USAGE_SHARD_ROOT: join(root, 'usage-shards'),
  JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(root, 'codex-shards'),
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: inputDirectory,
  JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: signingKey,
  JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET: credentialSecret,
  JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_ENABLED: 'true',
  JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_POLL_MS: '100',
  JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED: 'true',
  JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH: jobsStorePath,
  JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_POLL_MS: '100'
}

type ManagedProcess = ChildProcessByStdio<null, Readable, Readable>
const outputByProcess = new WeakMap<ManagedProcess, () => string>()
let dbService: ManagedProcess | undefined
let jobs: ManagedProcess | undefined
let upstream: ReturnType<typeof createServer> | undefined

try {
  createBusinessFixture()
  upstream = createUpstreamFixture()
  const upstreamBaseURL = await listen(upstream)
  publishFixtureInput(upstreamBaseURL)

  dbService = startProcess(process.execPath, [dbServicePath], releaseRoot, commonEnvironment)
  jobs = startJobsProcess()

  const receipt = await waitFor('release DB-service must project the Go outcome', () => readReceipt())
  assert.equal(receipt.disposition, 'applied')
  const account = readAccountHealth()
  assert.equal(account.last_health_check_at, receipt.observed_at)
  assert.equal(account.last_health_success_at, receipt.observed_at)
  assert.equal(account.health_check_failure_count, 0)
  const cursor = readCursor()
  assert.equal(cursor.outcome_id, receipt.outcome_id)
  assert.equal(cursor.observed_at, receipt.observed_at)

  await stopProcess(jobs, 'release jobs')
  jobs = undefined
  await stopProcess(dbService, 'release DB-service')
  dbService = undefined
} finally {
  await stopIfRunning(jobs)
  await stopIfRunning(dbService)
  await closeServer(upstream)
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-release-db-service-runtime-regression passed')

function resolveRequiredReleaseRoot(value: string | undefined): string {
  const root = resolve(value?.trim() || join(import.meta.dirname, '../../../../release/juhe-ai-release'))
  if (!existsSync(root)) throw new Error(`release 根目录不存在：${root}`)
  if (!existsSync(join(root, 'node_modules'))) {
    throw new Error(`release 根目录缺少生产依赖：${root}。请先在该 release 根目录运行 pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...`)
  }
  return root
}

function resolveRequiredFile(path: string, label: string): string {
  if (!existsSync(path)) throw new Error(`${label}不存在：${path}`)
  return path
}

function createBusinessFixture(): void {
  mkdirSync(join(root, 'logs'), { recursive: true })
  mkdirSync(join(root, 'codex-shards'), { recursive: true })
  for (const path of [datasetPath, usageCatalogPath, statsPath]) {
    const database = new Constructor(path)
    database.close()
  }
  const database = new Constructor(businessPath)
  try {
    releaseSchema.applyBusinessSchema(database)
    releaseSchema.seedDefaults(database)
    database.prepare(`
      INSERT INTO accounts (
        id, config_revision, dispatch_revision, system_account_id,
        provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, schedulable,
        health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, 2, 3, 'sys_admin', 'openai', 'profile_openai_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 'fixture', 1, 'gpt-j1-release-db-service', 'chat_json', ?, ?)
    `).run(accountId, 'J1 release DB-service fixture', '2026-08-17T00:00:00.000Z', '2026-08-17T00:00:00.000Z')
    database.prepare(`
      INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at)
      VALUES (?, 1, '2026-08-17T00:00:00.000Z')
    `).run(accountId)
  } finally {
    database.close()
  }
}

function createUpstreamFixture(): ReturnType<typeof createServer> {
  return createServer((request, response) => {
    assert.equal(request.method, 'POST')
    assert.equal(request.url, '/v1/chat/completions')
    assert.equal(request.headers.authorization, 'Bearer sk-j1-release-db-service')
    response.setHeader('content-type', 'application/json')
    response.end(JSON.stringify({ choices: [{ message: { content: 'juhe' } }] }))
  })
}

function publishFixtureInput(baseURL: string): void {
  const now = new Date()
  releaseInputProtocol.publishAccountHealthJobsInput({
    root: inputDirectory,
    accountId,
    signingKey,
    payload: {
      account_id: accountId,
      input_version: 1,
      config_revision: 2,
      dispatch_revision: 3,
      provider: 'openai',
      type: 'api_key',
      endpoint_mode: 'chat_json',
      health_model: 'gpt-j1-release-db-service',
      base_url: baseURL,
      key_set_fingerprint: 'j1-release-db-service-keyset',
      api_keys: [{
        index: 0,
        fingerprint: 'j1-release-db-service-key',
        credential: { kind: 'api_key', ciphertext: encryptV1(credentialSecret, JSON.stringify({ api_key: 'sk-j1-release-db-service' })) }
      }],
      issued_at: now.toISOString(),
      expires_at: new Date(now.getTime() + 60 * 60 * 1000).toISOString(),
      tls_policy_version: 'j1-direct-upstream-v1',
      allow_insecure_base_url: true,
      eligibility: { account_status: 'active', schedulable: true, bound_group: true, authorization_eligible: true },
      schedule: {
        health_interval_ms: 60 * 60 * 1000,
        health_jitter_ms: 0,
        failure_threshold: 1,
        failure_retry_ms: 60 * 1000,
        cooldown_neutral_base_ms: 30 * 1000,
        cooldown_neutral_max_ms: 15 * 60 * 1000,
        cooldown_failure_backoff_ms: 60 * 1000
      }
    }
  })
}

function startJobsProcess(): ManagedProcess {
  const environment: NodeJS.ProcessEnv = {
    ...commonEnvironment,
    JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS: '127.0.0.1:0',
    JUHE_AI_LOG_FILE_ENABLED: 'true',
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'j1-release-db-service-runtime-log',
    JUHE_AI_RUNTIME_LOG_STORE: 'sqlite',
    JUHE_AI_RUNTIME_LOG_OWNER_LEASE: '15s',
    JUHE_AI_RUNTIME_LOG_POLL_INTERVAL: '1h',
    JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL: '1h',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'j1-release-db-service-table-monitor',
    JUHE_AI_TABLE_MONITOR_STORE: 'sqlite',
    JUHE_AI_TABLE_MONITOR_INTERVAL: '1h',
    JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT: '1s',
    JUHE_AI_TABLE_MONITOR_OWNER_LEASE: '15s',
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID: 'j1-release-db-service-jobs',
    JUHE_AI_ACCOUNT_HEALTH_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH: jobsStorePath,
    JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL: '5s',
    JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE: '15s',
    JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT: '2s',
    JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES: '4096',
    JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY: '1'
  }
  return startProcess(jobsBinary, [], root, environment)
}

function startProcess(command: string, args: string[], cwd: string, environment: NodeJS.ProcessEnv): ManagedProcess {
  const child = spawn(command, args, { cwd, env: environment, stdio: ['ignore', 'pipe', 'pipe'] })
  outputByProcess.set(child, captureOutput(child))
  return child
}

function readReceipt(): { outcome_id: string; observed_at: string; disposition: string } | undefined {
  const database = openReadOnlyBusinessDatabase()
  try {
    return database.prepare(`
      SELECT receipt.outcome_id, outcome.observed_at, receipt.disposition
      FROM account_health_projection_receipts AS receipt
      JOIN (
        SELECT outcome_id, observed_at FROM account_health_projection_cursors
        WHERE consumer_key = 'juhe-ai-account-health-jobs-projector-v1'
      ) AS outcome ON outcome.outcome_id = receipt.outcome_id
      WHERE receipt.account_id = ?
    `).get(accountId) as { outcome_id: string; observed_at: string; disposition: string } | undefined
  } finally {
    database.close()
  }
}

function readAccountHealth(): { last_health_check_at: string; last_health_success_at: string; health_check_failure_count: number } {
  const database = openReadOnlyBusinessDatabase()
  try {
    const row = database.prepare(`
      SELECT last_health_check_at, last_health_success_at, health_check_failure_count
      FROM accounts WHERE id = ?
    `).get(accountId) as { last_health_check_at: string; last_health_success_at: string; health_check_failure_count: number } | undefined
    assert(row, 'release DB-service must retain the projected account')
    return row
  } finally {
    database.close()
  }
}

function readCursor(): { outcome_id: string; observed_at: string } {
  const database = openReadOnlyBusinessDatabase()
  try {
    const row = database.prepare(`
      SELECT outcome_id, observed_at FROM account_health_projection_cursors
      WHERE consumer_key = 'juhe-ai-account-health-jobs-projector-v1'
    `).get() as { outcome_id: string; observed_at: string } | undefined
    assert(row, 'release DB-service must persist its projector cursor')
    return row
  } finally {
    database.close()
  }
}

function openReadOnlyBusinessDatabase(): DatabaseSync {
  const database = new Constructor(businessPath)
  database.exec('PRAGMA query_only=ON')
  return database
}

async function waitFor<T>(label: string, read: () => T | undefined, timeoutMs = 20_000): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let delay = 25
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const value = read()
      if (value !== undefined) return value
    } catch (error) {
      lastError = error
    }
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, delay))
    delay = Math.min(delay * 2, 400)
  }
  const details = [dbService, jobs].flatMap((child) => child ? [outputByProcess.get(child)?.() ?? ''] : []).join('\n')
  throw new Error(`${label} 超时${lastError instanceof Error ? `：${lastError.message}` : ''}\n${details}`)
}

async function stopProcess(child: ManagedProcess | undefined, label: string): Promise<void> {
  assert(child, `${label} process must exist`)
  const output = outputByProcess.get(child) ?? (() => '')
  assert.equal(child.kill('SIGINT'), true, `${label} 未能发送 SIGINT`)
  const exit = await waitForExit(child, 10_000)
  assert(exit.code === 0 || exit.signal === 'SIGINT', `${label} 未正常退出：${output()}`)
}

async function stopIfRunning(child: ManagedProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null) return
  child.kill('SIGKILL')
  await waitForExit(child, 5_000).catch(() => undefined)
}

function captureOutput(child: ManagedProcess): () => string {
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8').on('data', (chunk: string) => { stdout += chunk })
  child.stderr.setEncoding('utf8').on('data', (chunk: string) => { stderr += chunk })
  return () => `${stdout}\n${stderr}`
}

async function waitForExit(child: ManagedProcess, timeoutMs: number): Promise<{ code: number | null; signal: NodeJS.Signals | null }> {
  return await new Promise((resolveExit, rejectExit) => {
    const timer = setTimeout(() => rejectExit(new Error('process did not exit within timeout')), timeoutMs)
    child.once('close', (code, signal) => {
      clearTimeout(timer)
      resolveExit({ code, signal })
    })
  })
}

function encryptV1(secret: string, value: string): string {
  const key = createHash('sha256').update(secret, 'utf8').digest()
  const iv = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', key, iv)
  const ciphertext = Buffer.concat([cipher.update(value, 'utf8'), cipher.final()])
  return `v1:${iv.toString('base64url')}:${cipher.getAuthTag().toString('base64url')}:${ciphertext.toString('base64url')}`
}

async function listen(server: ReturnType<typeof createServer>): Promise<string> {
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
  const address = server.address()
  assert(address && typeof address !== 'string')
  return `http://127.0.0.1:${address.port}`
}

async function closeServer(server: ReturnType<typeof createServer> | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()))
}
