import { strict as assert } from 'node:assert'
import { execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import {
  externalSourceResetConfirmationHash,
  loadRealGoExternalSourceResetSmokeConfig,
  realGoExternalSourceResetSmokeEnv
} from '../smoke/plan0081-real-go-external-source-reset-smoke'

interface RequestRecord {
  method?: string
  pathname: string
  headers: IncomingMessage['headers']
  body?: unknown
}

interface ChildResult {
  stdout: string
  stderr: string
  exitCode: number
}

const execFileAsync = promisify(execFile)
const repositoryRoot = fileURLToPath(new URL('../../../', import.meta.url))
const tsxCliPath = fileURLToPath(new URL('../../../backend/node_modules/tsx/dist/cli.mjs', import.meta.url))
const smokePath = fileURLToPath(new URL('../smoke/plan0081-real-go-external-source-reset-smoke.ts', import.meta.url))

const cookie = 'juhe_ai_session=external-reset-cookie-secret; scope=admin'
const sensitiveBasePath = '/sensitive-reset-management-base'
const sourceId = 'extsrc_builtin_test'
const tokenId = 'exttok_builtin_test'
const oldToken = `juis_${'A'.repeat(43)}`
const newToken = `juis_${'B'.repeat(43)}`
const oldPrefix = oldToken.slice(0, 8)
const oldSuffix = oldToken.slice(-8)
const newPrefix = newToken.slice(0, 8)
const newSuffix = newToken.slice(-8)
const records: RequestRecord[] = []
let postCalls = 0
let resetApplied = false

assert.equal(oldToken.length, 48)
assert.equal(newToken.length, 48)
assert.notEqual(oldToken, newToken)

const server = createServer((req, res) => {
  void handleRequest(req, res).catch(() => {
    if (!res.headersSent) {
      res.statusCode = 500
      res.setHeader('Cache-Control', 'no-store')
    }
    res.end()
  })
})
await listen(server)

try {
  const baseUrl = `${serverBaseUrl(server)}${sensitiveBasePath}`
  await assertConfigurationGates(baseUrl)
  assert.throws(
    () => loadRealGoExternalSourceResetSmokeConfig({
      ...smokeEnvironment('https://management.example.test')
    }),
    /loopback/i,
    'remote HTTPS must fail during configuration parsing'
  )
  await assertReadOnlyConfirmationSequence(baseUrl)
  await assertSuccessfulChildSequence(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go external source reset smoke regression passed')

async function assertConfigurationGates(baseUrl: string): Promise<void> {
  const valid = smokeEnvironment(baseUrl)
  const preHttpCases: Array<[string, NodeJS.ProcessEnv]> = [
    ['missing enabled', without(valid, realGoExternalSourceResetSmokeEnv.enabled)],
    ['non-loopback HTTP', {
      ...valid,
      [realGoExternalSourceResetSmokeEnv.baseUrl]: 'http://192.0.2.10'
    }],
    ['Cookie CRLF', {
      ...valid,
      [realGoExternalSourceResetSmokeEnv.cookie]: `${cookie}\r\nsecond=value`
    }]
  ]

  for (const [label, environment] of preHttpCases) {
    resetRequests()
    const result = await runSmokeChild(environment)
    assert.notEqual(result.exitCode, 0, `${label} must fail`)
    assert.equal(postCalls, 0, `${label} must fail before POST`)
    assert.equal(records.length, 0, `${label} must fail before HTTP`)
  }

  resetRequests()
  const wrongConfirmation = await runSmokeChild({
    ...valid,
    [realGoExternalSourceResetSmokeEnv.confirmation]: '0'.repeat(64)
  })
  assert.notEqual(wrongConfirmation.exitCode, 0, 'wrong confirmation must fail')
  assert.equal(postCalls, 0, 'wrong confirmation must fail before POST')
  assert.deepEqual(records.map(requestSignature), [
    `GET /__aisys__/api/external-integration-sources/${sourceId}`,
    `GET /__aisys__/api/external-integration-sources/${sourceId}/tokens/${tokenId}/secret`
  ])
}

async function assertReadOnlyConfirmationSequence(baseUrl: string): Promise<void> {
  resetRequests()
  const environment = without(
    without(smokeEnvironment(baseUrl), realGoExternalSourceResetSmokeEnv.enabled),
    realGoExternalSourceResetSmokeEnv.confirmation
  )
  const result = await runSmokeChild(environment, ['--print-confirmation'])
  assert.equal(result.exitCode, 0, result.stderr || result.stdout)
  assert.equal(result.stderr, '')
  assert.equal(
    result.stdout.trim(),
    `confirmationHash=${externalSourceResetConfirmationHash(baseUrl, oldPrefix, oldSuffix)}`
  )
  assert.equal(postCalls, 0, 'confirmation mode must never POST')
  assert.deepEqual(records.map(requestSignature), [
    `GET /__aisys__/api/external-integration-sources/${sourceId}`,
    `GET /__aisys__/api/external-integration-sources/${sourceId}/tokens/${tokenId}/secret`
  ])
}

async function assertSuccessfulChildSequence(baseUrl: string): Promise<void> {
  resetRequests()
  const result = await runSmokeChild(smokeEnvironment(baseUrl))
  assert.equal(result.exitCode, 0, result.stderr || result.stdout)
  assert.equal(result.stderr, '')
  assert.match(result.stdout, /external.*reset.*=true/i)
  assert.deepEqual(records.map(requestSignature), [
    `GET /__aisys__/api/external-integration-sources/${sourceId}`,
    `GET /__aisys__/api/external-integration-sources/${sourceId}/tokens/${tokenId}/secret`,
    'POST /__aisys__/api/external-integration-sources/built-in-test-token/reset',
    `GET /__aisys__/api/external-integration-sources/${sourceId}`,
    `GET /__aisys__/api/external-integration-sources/${sourceId}/tokens/${tokenId}/secret`
  ])
  assert.equal(postCalls, 1)
  assert.deepEqual(records[2]?.body, {})

  for (const record of records) {
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers.cookie, cookie)
  }
  assert.equal(records[2]?.headers['content-type'], 'application/json')

  const rawResetResponse = JSON.stringify(resetEnvelope())
  assertNoSensitiveOutput(`${result.stdout}\n${result.stderr}`, baseUrl, rawResetResponse)
}

async function runSmokeChild(environment: NodeJS.ProcessEnv, args: string[] = []): Promise<ChildResult> {
  try {
    const result = await execFileAsync(process.execPath, [
      tsxCliPath,
      '--tsconfig',
      fileURLToPath(new URL('../../../frontend/tsconfig.json', import.meta.url)),
      smokePath,
      ...args
    ], {
      cwd: repositoryRoot,
      env: environment,
      encoding: 'utf8',
      maxBuffer: 1024 * 1024,
      windowsHide: true
    })
    return { stdout: result.stdout, stderr: result.stderr, exitCode: 0 }
  } catch (error) {
    const failure = error as { stdout?: string; stderr?: string; code?: number }
    return {
      stdout: failure.stdout ?? '',
      stderr: failure.stderr ?? '',
      exitCode: typeof failure.code === 'number' ? failure.code : 1
    }
  }
}

function smokeEnvironment(baseUrl: string): NodeJS.ProcessEnv {
  const environment: NodeJS.ProcessEnv = {
    ...process.env,
    [realGoExternalSourceResetSmokeEnv.baseUrl]: baseUrl,
    [realGoExternalSourceResetSmokeEnv.cookie]: cookie,
    [realGoExternalSourceResetSmokeEnv.enabled]: '1',
    [realGoExternalSourceResetSmokeEnv.confirmation]: externalSourceResetConfirmationHash(
      baseUrl,
      oldPrefix,
      oldSuffix
    )
  }
  environment[realGoExternalSourceResetSmokeEnv.timeoutMs] = '1000'
  return environment
}

async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? '/', 'http://mock.test')
  const body = await readBody(req)
  records.push({ method: req.method, pathname: url.pathname, headers: req.headers, body })
  const apiBase = `${sensitiveBasePath}/__aisys__/api/external-integration-sources`
  const detailPath = `${apiBase}/${sourceId}`
  const secretPath = `${detailPath}/tokens/${tokenId}/secret`

  if (req.method === 'GET' && url.pathname === detailPath) {
    return sendJSON(res, 200, { data: sourceDetail() })
  }
  if (req.method === 'GET' && url.pathname === secretPath) {
    return sendJSON(res, 200, { data: { token: resetApplied ? newToken : oldToken } })
  }
  if (req.method === 'POST' && url.pathname === `${apiBase}/built-in-test-token/reset`) {
    postCalls += 1
    resetApplied = true
    return sendJSON(res, 200, resetEnvelope(), {
      'Cache-Control': 'no-store',
      Pragma: 'no-cache'
    })
  }
  return sendJSON(res, 404, { message: 'unexpected mock path' })
}

function sourceDetail(): Record<string, unknown> {
  return {
    id: sourceId,
    name: '内置测试来源',
    status: 'enabled',
    scopes: [],
    rateLimits: [{ windowSeconds: 60, maxRequests: 10 }],
    notes: '系统内置测试 Token，只返回 mock 数据；可停用或重置，不支持编辑或删除。',
    createdAt: '2026-07-19T00:00:00.000Z',
    updatedAt: resetApplied ? '2026-07-19T00:00:01.000Z' : '2026-07-19T00:00:00.000Z',
    tokenCount: 1,
    activeTokenCount: 1,
    isBuiltIn: true,
    tokens: [tokenSummary()]
  }
}

function tokenSummary(): Record<string, unknown> {
  const token = resetApplied ? newToken : oldToken
  return {
    id: tokenId,
    name: '内置测试 Token',
    tokenPrefix: token.slice(0, 8),
    tokenSuffix: token.slice(-8),
    status: 'active',
    createdAt: '2026-07-19T00:00:00.000Z',
    updatedAt: resetApplied ? '2026-07-19T00:00:01.000Z' : '2026-07-19T00:00:00.000Z',
    isBuiltIn: true
  }
}

function resetEnvelope(): Record<string, unknown> {
  return {
    data: {
      token: {
        id: tokenId,
        name: '内置测试 Token',
        token: newToken,
        tokenPrefix: newPrefix,
        tokenSuffix: newSuffix,
        scopes: []
      }
    }
  }
}

function assertNoSensitiveOutput(output: string, baseUrl: string, rawResponse: string): void {
  for (const sensitive of [
    baseUrl,
    cookie,
    oldToken,
    newToken,
    oldPrefix,
    oldSuffix,
    newPrefix,
    newSuffix,
    rawResponse
  ]) {
    assert.equal(output.includes(sensitive), false, `child output leaked sensitive value hash=${shortHash(sensitive)}`)
  }
}

function shortHash(value: string): string {
  return createHash('sha256').update(value).digest('hex').slice(0, 12)
}

function resetRequests(): void {
  records.length = 0
  postCalls = 0
  resetApplied = false
}

function requestSignature(record: RequestRecord): string {
  return `${record.method} ${record.pathname.replace(sensitiveBasePath, '')}`
}

function without(environment: NodeJS.ProcessEnv, name: string): NodeJS.ProcessEnv {
  const copy = { ...environment }
  delete copy[name]
  return copy
}

async function readBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  for await (const chunk of req) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  if (chunks.length === 0) return undefined
  return JSON.parse(Buffer.concat(chunks).toString('utf8'))
}

function sendJSON(
  res: ServerResponse,
  status: number,
  body: unknown,
  headers: Record<string, string> = { 'Cache-Control': 'no-store', Pragma: 'no-cache' }
): void {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json; charset=utf-8')
  for (const [name, value] of Object.entries(headers)) res.setHeader(name, value)
  res.end(JSON.stringify(body))
}

function listen(target: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    target.once('error', reject)
    target.listen(0, '127.0.0.1', () => {
      target.off('error', reject)
      resolve()
    })
  })
}

function close(target: Server): Promise<void> {
  return new Promise((resolve, reject) => target.close((error) => error ? reject(error) : resolve()))
}

function serverBaseUrl(target: Server): string {
  const address = target.address() as AddressInfo
  return `http://127.0.0.1:${address.port}`
}
