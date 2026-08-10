import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { once } from 'node:events'

import type { AuditLogInput } from '../../storage/audit-log-types.js'

const root = resolve(import.meta.dirname, '../../../..')
const goRoot = join(root, 'backend-go')
const testRoot = await mkdtemp(join(tmpdir(), 'juhe-ai-f3-sidecar-e2e-'))
const port = await allocatePort()
const inputUrl = `http://127.0.0.1:${port}`
const businessSecret = 'f3-local-sidecar-e2e-business-secret'
const inputSecret = 'f3-local-sidecar-e2e-input-secret'
const databasePath = join(testRoot, 'f3-audit.sqlite3')
const blobDirectory = join(testRoot, 'blobs')
const hotSearchDirectory = join(testRoot, 'hot-search')
const binaryPath = join(testRoot, process.platform === 'win32' ? 'juhe-ai-audit-log-writer.exe' : 'juhe-ai-audit-log-writer')

const originalEnvironment = new Map<string, string | undefined>()
const environment = {
  JUHE_AI_SECRET: businessSecret,
  JUHE_AI_AUDIT_LOG_INPUT_URL: inputUrl,
  JUHE_AI_AUDIT_LOG_STORE: 'sqlite',
  JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-local-sidecar-e2e',
  JUHE_AI_AUDIT_LOG_DATABASE_PATH: databasePath,
  JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY: blobDirectory,
  JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY: hotSearchDirectory,
  JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH: join(testRoot, 'business-settings.sqlite3'),
  JUHE_AI_DATABASE_PATH: join(testRoot, 'business.sqlite3'),
  JUHE_AI_DATASET_DATABASE_PATH: join(testRoot, 'dataset.sqlite3'),
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(testRoot, 'usage-catalog.sqlite3'),
  JUHE_AI_STATS_DATABASE_PATH: join(testRoot, 'stats.sqlite3'),
  JUHE_AI_RUNTIME_LOG_DATABASE_PATH: join(testRoot, 'runtime-log.sqlite3'),
  JUHE_AI_TABLE_MONITOR_DATABASE_PATH: join(testRoot, 'table-monitor.sqlite3'),
  JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(testRoot, 'codex-shards'),
  JUHE_AI_USAGE_SHARD_ROOT: join(testRoot, 'usage-shards'),
  JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS: `127.0.0.1:${port}`,
  JUHE_AI_AUDIT_LOG_INPUT_SECRET: inputSecret,
  JUHE_AI_AUDIT_LOG_OWNER_LEASE: '5s',
  JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL: '1h',
  JUHE_AI_LOG_FILE_ENABLED: 'false',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  NODE_ENV: 'test'
}

for (const name of Object.keys(environment)) originalEnvironment.set(name, process.env[name])
Object.assign(process.env, environment)

let sidecar: ChildProcess | undefined
try {
  await mkdir(join(testRoot, 'codex-shards'), { recursive: true })
  await mkdir(join(testRoot, 'usage-shards'), { recursive: true })
  await buildWriter()
  sidecar = await startWriter()

  const { dispatchAuditLogToGo } = await import('../../modules/audit-logs/audit-log-go-input.service.js')
  const { createAuditLogF3QueryRepository } = await import('../../storage/audit-log-f3-query.repository.js')
  const repository = await createAuditLogF3QueryRepository({
    sqlitePath: databasePath,
    payloadBlobDirectory: blobDirectory,
    hotSearchDirectory
  })
  try {
    const id = 'f3-sidecar-e2e-stream'
    const traceId = 'trace-f3-sidecar-e2e'
    const timestamp = new Date().toISOString()
    dispatchAuditLogToGo(input({ id, traceId, timestamp, lifecycleStatus: 'in_progress' }))
    const placeholder = await waitFor(() => repository.getAuditLogDetail(id))
    assert.equal(placeholder.lifecycleStatus, 'in_progress', 'Go sidecar must persist the stream placeholder')

    dispatchAuditLogToGo(input({ id, traceId, timestamp, lifecycleStatus: 'finalized' }))
    const finalized = await waitFor(async () => {
      const detail = await repository.getAuditLogDetail(id)
      return detail?.lifecycleStatus === 'finalized' ? detail : undefined
    })
    assert.equal(finalized.attempts.length, 1, 'same-ID finalized input must persist one upstream attempt')
    assert.equal(finalized.payloads.length, 1, 'same-ID finalized input must persist one payload reference')
    const payload = await repository.getAuditLogPayload(id, finalized.payloads[0].id, { includeHeaders: true })
    assert.equal(payload?.bodyText, '{"message":"F3 sidecar end-to-end"}', 'Node reader must retrieve the Go-owned payload blob')
    assert.equal(payload?.headers?.['content-type'], 'application/json', 'payload headers must survive the Node to Go handoff')
    const list = await repository.listAuditLogs({ traceId, pageSize: 10 })
    assert.equal(list.items.filter((item) => item.id === id).length, 1, 'placeholder and finalized input must remain one audit record')
  } finally {
    await repository.close()
  }

  await stopWriter(sidecar)
  // A force-stopped Windows child cannot run Go's deferred lease release.
  // The minimum configured lease is 5s, so wait once for it to expire before
  // proving that the same stable instance ID can start again.
  await delay(5_500)
  sidecar = await startWriter()
  await stopWriter(sidecar)
  sidecar = undefined
  console.log('F3 Go sidecar E2E regression passed: Node input, Go SQLite persistence, stream finalization, Node read-only query, payload read, and sidecar restart.')
} finally {
  if (sidecar) await stopWriter(sidecar).catch(() => undefined)
  for (const [name, value] of originalEnvironment) {
    if (value === undefined) delete process.env[name]
    else process.env[name] = value
  }
  await cleanupTestRoot()
}

function input(options: { id: string, traceId: string, timestamp: string, lifecycleStatus: 'in_progress' | 'finalized' }): AuditLogInput {
  const finalized = options.lifecycleStatus === 'finalized'
  return {
    id: options.id,
    lifecycleStatus: options.lifecycleStatus,
    traceId: options.traceId,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/chat/completions',
    stream: true,
    auditOutcome: finalized ? 'upstream_failed' : 'gateway_succeeded',
    success: false,
    finalStatusCode: finalized ? 502 : undefined,
    errorPhase: finalized ? 'upstream' : undefined,
    errorCode: finalized ? 'sidecar_e2e' : undefined,
    errorMessage: finalized ? 'F3 sidecar end-to-end' : undefined,
    sampleBucket: 0,
    sampleReason: 'full_capture',
    startedAt: options.timestamp,
    endedAt: options.timestamp,
    createdAt: options.timestamp,
    attempts: finalized ? [{
      id: 'f3-sidecar-e2e-attempt',
      attemptIndex: 0,
      upstreamMethod: 'POST',
      upstreamUrl: 'https://upstream.invalid/v1/chat/completions',
      upstreamStatusCode: 502,
      success: false,
      errorPhase: 'upstream',
      errorCode: 'sidecar_e2e',
      errorMessage: 'F3 sidecar end-to-end',
      startedAt: options.timestamp,
      endedAt: options.timestamp
    }] : [],
    payloads: finalized ? [{
      id: 'f3-sidecar-e2e-payload',
      partType: 'gateway_error',
      sequenceIndex: 0,
      contentType: 'application/json',
      headers: { 'content-type': 'application/json' },
      body: '{"message":"F3 sidecar end-to-end"}',
      captureStatus: 'complete',
      createdAt: options.timestamp
    }] : []
  }
}

async function buildWriter(): Promise<void> {
  const result = spawn(process.env.JUHE_AI_GO_BINARY ?? 'go', ['build', '-o', binaryPath, './cmd/juhe-ai-audit-log-writer'], {
    cwd: goRoot,
    env: process.env,
    stdio: 'pipe',
    windowsHide: true
  })
  const failure = await processResult(result)
  assert.equal(failure.code, 0, `F3 Go writer build failed: ${failure.output}`)
}

async function startWriter(): Promise<ChildProcess> {
  const child = spawn(binaryPath, [], { cwd: goRoot, env: process.env, stdio: 'pipe', windowsHide: true })
  const ready = waitForReady(child)
  await ready
  return child
}

async function waitForReady(child: ChildProcess): Promise<void> {
  let output = ''
  child.stdout?.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
  child.stderr?.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
  const deadline = Date.now() + 20_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`F3 Go sidecar exited before ready: ${output}`)
    try {
      const response = await fetch(`${inputUrl}/__aiinternal__/health`)
      if (response.status === 204) return
    } catch {
      // The listener has not bound yet; retry with bounded backoff below.
    }
    await delay(50)
  }
  throw new Error(`F3 Go sidecar did not become ready: ${output}`)
}

async function stopWriter(child: ChildProcess): Promise<void> {
  if (child.exitCode === null) child.kill()
  await Promise.race([
    once(child, 'exit'),
    delay(5_000).then(() => { throw new Error('F3 Go sidecar did not stop within 5s') })
  ])
}

async function allocatePort(): Promise<number> {
  const server = createServer()
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string', 'failed to allocate a loopback port')
  await new Promise<void>((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()))
  return address.port
}

async function waitFor<T>(operation: () => Promise<T | undefined>, timeoutMs = 5_000): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const result = await operation()
      if (result !== undefined) return result
    } catch (error) {
      lastError = error
    }
    await delay(30)
  }
  throw new Error(`F3 Go sidecar result did not become visible before timeout${lastError ? `: ${String(lastError)}` : ''}`)
}

async function processResult(child: ChildProcess): Promise<{ code: number | null, output: string }> {
  let output = ''
  child.stdout?.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
  child.stderr?.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
  const [code] = await once(child, 'exit') as [number | null]
  return { code, output }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}

async function cleanupTestRoot(): Promise<void> {
  let lastError: unknown
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try {
      await rm(testRoot, { recursive: true, force: true, maxRetries: 1, retryDelay: 100 })
      return
    } catch (error) {
      lastError = error
      await delay(200)
    }
  }
  throw lastError
}
