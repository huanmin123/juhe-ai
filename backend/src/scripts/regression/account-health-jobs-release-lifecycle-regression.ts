import assert from 'node:assert/strict'
import { spawn, type ChildProcessByStdio } from 'node:child_process'
import { createCipheriv, createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'
import type { Readable } from 'node:stream'

import { publishAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync
const jobsBinary = resolveRequiredBinary(process.env.JUHE_AI_J1_RELEASE_JOBS_BINARY)
const root = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-release-lifecycle-'))
const inputDirectory = join(root, 'j1-input')
const jobsStorePath = join(root, 'j1-jobs.sqlite3')
const accountId = 'j1-release-lifecycle-account'
const signingKey = randomBytes(32).toString('base64url')
const credentialSecret = 'j1-release-lifecycle-credential-secret'
type JobsProcess = ChildProcessByStdio<null, Readable, Readable>
const jobsOutput = new WeakMap<JobsProcess, () => string>()
let upstreamServer: ReturnType<typeof createServer> | undefined
let first: JobsProcess | undefined
let second: JobsProcess | undefined

try {
  upstreamServer = createServer((request, response) => {
    assert.equal(request.method, 'POST')
    assert.equal(request.url, '/v1/chat/completions')
    assert.equal(request.headers.authorization, 'Bearer sk-j1-release-lifecycle')
    response.setHeader('content-type', 'application/json')
    response.end(JSON.stringify({ choices: [{ message: { content: 'juhe' } }] }))
  })
  const upstreamBaseURL = await listen(upstreamServer)
  publishFixtureInput(upstreamBaseURL)

  first = startJobs('first')
  const outcome = await waitForOutcome('first release owner must write an outcome', first)
  assert.equal(outcome.outcome, 'complete_success')
  assert.equal(outcome.account_id, accountId)
  const firstLease = await waitForLease('first release owner must acquire jobs lease', first)
  assert.match(firstLease.owner_id, /j1-release-lifecycle-first/u)

  await stopJobs(first, 'first release owner')
  first = undefined

  second = startJobs('second')
  const secondLease = await waitForLease('replacement release owner must wait stale lease then acquire J1 lease', second, /j1-release-lifecycle-second/u, 20_000)
  assert.match(secondLease.owner_id, /j1-release-lifecycle-second/u)
  assert(secondLease.fence_token > firstLease.fence_token, 'replacement owner must advance the lease fence')
  await stopJobs(second, 'replacement release owner')
  second = undefined
} finally {
  await stopIfRunning(first)
  await stopIfRunning(second)
  await closeServer(upstreamServer)
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-release-lifecycle-regression passed')

function resolveRequiredBinary(value: string | undefined): string {
  const binary = value?.trim()
  if (!binary) throw new Error('JUHE_AI_J1_RELEASE_JOBS_BINARY 必须指向已构建的 release juhe-ai-jobs 二进制')
  const resolved = resolve(binary)
  if (!existsSync(resolved)) throw new Error(`release jobs 二进制不存在：${resolved}`)
  return resolved
}

function publishFixtureInput(baseURL: string): void {
  mkdirSync(inputDirectory, { recursive: true })
  const now = new Date()
  publishAccountHealthJobsInput({
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
      health_model: 'gpt-j1-release-test',
      base_url: baseURL,
      key_set_fingerprint: 'j1-release-lifecycle-keyset',
      api_keys: [{
        index: 0,
        fingerprint: 'j1-release-lifecycle-key',
        credential: { kind: 'api_key', ciphertext: encryptV1(credentialSecret, JSON.stringify({ api_key: 'sk-j1-release-lifecycle' })) }
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

function startJobs(instance: 'first' | 'second'): JobsProcess {
  const outputRoot = join(root, instance)
  const environment: NodeJS.ProcessEnv = {
    ...process.env,
    JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS: '127.0.0.1:0',
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: `j1-release-lifecycle-${instance}`,
    JUHE_AI_RUNTIME_LOG_STORE: 'sqlite',
    JUHE_AI_RUNTIME_LOG_DATABASE_PATH: join(outputRoot, 'runtime-log.sqlite3'),
    JUHE_AI_RUNTIME_LOG_OWNER_LEASE: '15s',
    JUHE_AI_RUNTIME_LOG_POLL_INTERVAL: '1h',
    JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL: '1h',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: `j1-release-lifecycle-${instance}`,
    JUHE_AI_TABLE_MONITOR_STORE: 'sqlite',
    JUHE_AI_TABLE_MONITOR_DATABASE_PATH: join(outputRoot, 'table-monitor.sqlite3'),
    JUHE_AI_TABLE_MONITOR_INTERVAL: '1h',
    JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT: '1s',
    JUHE_AI_TABLE_MONITOR_OWNER_LEASE: '15s',
    JUHE_AI_DATABASE_PATH: join(outputRoot, 'business.sqlite3'),
    JUHE_AI_DATASET_DATABASE_PATH: join(outputRoot, 'dataset.sqlite3'),
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(outputRoot, 'usage.sqlite3'),
    JUHE_AI_STATS_DATABASE_PATH: join(outputRoot, 'stats.sqlite3'),
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(outputRoot, 'codex-shards'),
    JUHE_AI_LOG_DIR: join(outputRoot, 'logs'),
    JUHE_AI_LOG_FILE_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID: `j1-release-lifecycle-${instance}`,
    JUHE_AI_ACCOUNT_HEALTH_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH: jobsStorePath,
    JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: inputDirectory,
    JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: signingKey,
    JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET: credentialSecret,
    JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL: '5s',
    JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE: '15s',
    JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT: '2s',
    JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES: '4096',
    JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY: '1'
  }
  mkdirSync(join(outputRoot, 'logs'), { recursive: true })
  mkdirSync(join(outputRoot, 'codex-shards'), { recursive: true })
  for (const databasePath of [
    environment.JUHE_AI_DATABASE_PATH,
    environment.JUHE_AI_DATASET_DATABASE_PATH,
    environment.JUHE_AI_USAGE_CATALOG_DATABASE_PATH,
    environment.JUHE_AI_STATS_DATABASE_PATH
  ]) {
    const database = new Constructor(databasePath!)
    database.close()
  }
  const businessDatabase = new Constructor(environment.JUHE_AI_DATABASE_PATH!)
  try {
    businessDatabase.exec(`
      CREATE TABLE system_settings (
        system_account_id TEXT NOT NULL,
        key TEXT NOT NULL,
        value_json TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        PRIMARY KEY (system_account_id, key)
      );
    `)
  } finally {
    businessDatabase.close()
  }
  const child = spawn(jobsBinary, [], { cwd: outputRoot, env: environment, stdio: ['ignore', 'pipe', 'pipe'] })
  jobsOutput.set(child, captureOutput(child))
  return child
}

async function waitForOutcome(label: string, child: JobsProcess): Promise<{ account_id: string, outcome: string }> {
  return await waitFor(label, () => {
    if (!existsSync(jobsStorePath)) return undefined
    const database = new Constructor(jobsStorePath)
    try {
      const row = database.prepare('SELECT payload FROM account_health_outcomes WHERE account_id = ?').get(accountId) as { payload: string } | undefined
      return row ? JSON.parse(row.payload) as { account_id: string, outcome: string } : undefined
    } finally {
      database.close()
    }
  }, () => jobsOutput.get(child)?.() ?? '')
}

async function waitForLease(label: string, child: JobsProcess, expectedOwner?: RegExp, timeoutMs?: number): Promise<{ owner_id: string, fence_token: number }> {
  return await waitFor(label, () => {
    if (!existsSync(jobsStorePath)) return undefined
    const database = new Constructor(jobsStorePath)
    try {
      const lease = database.prepare(`
        SELECT owner_id, fence_token FROM account_health_owner_leases WHERE lease_key = 'account-health-owner'
      `).get() as { owner_id: string, fence_token: number } | undefined
      return lease && (!expectedOwner || expectedOwner.test(lease.owner_id)) ? lease : undefined
    } finally {
      database.close()
    }
  }, () => jobsOutput.get(child)?.() ?? '', timeoutMs)
}

async function waitFor<T>(label: string, read: () => T | undefined, detail?: () => string, timeoutMs = 15_000): Promise<T> {
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
  throw new Error(`${label} 超时${lastError instanceof Error ? `：${lastError.message}` : ''}${detail ? `\n${detail()}` : ''}`)
}

async function stopJobs(child: JobsProcess, label: string): Promise<void> {
  const output = jobsOutput.get(child) ?? (() => '')
  assert.equal(child.kill('SIGINT'), true, `${label} 未能发送 SIGINT`)
  const exit = await waitForExit(child, 10_000)
  assert(exit.code === 0 || exit.signal === 'SIGINT', `${label} 未正常退出：${output()}`)
}

async function stopIfRunning(child: JobsProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null) return
  child.kill('SIGKILL')
  await waitForExit(child, 5_000).catch(() => undefined)
}

function captureOutput(child: JobsProcess): () => string {
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8').on('data', (chunk: string) => { stdout += chunk })
  child.stderr.setEncoding('utf8').on('data', (chunk: string) => { stderr += chunk })
  return () => `${stdout}\n${stderr}`
}

async function waitForExit(child: JobsProcess, timeoutMs: number): Promise<{ code: number | null, signal: NodeJS.Signals | null }> {
  return await new Promise((resolveExit, rejectExit) => {
    const timer = setTimeout(() => rejectExit(new Error(`jobs process did not exit within ${timeoutMs}ms`)), timeoutMs)
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
