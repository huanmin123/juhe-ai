import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import {
  externalIntegrationSourceCreateConfirmationForBaseUrl,
  formatRealGoExternalSourceCreateSmokeSummary,
  loadRealGoExternalSourceCreateSmokeConfig,
  realGoExternalSourceCreateSmokeEnv,
  runRealGoExternalSourceCreateSmoke,
  runRealGoExternalSourceCreateSmokeFromEnvironment,
  type ExternalSourceCreateSmokeEnvironment,
  type RealGoExternalSourceCreateSmokeRuntime
} from '../smoke/plan0081-real-go-external-source-create-smoke'

interface RequestRecord {
  method?: string
  pathname: string
  search: string
  headers: IncomingMessage['headers']
  body?: unknown
}

type Scenario =
  | 'success'
  | 'create_status'
  | 'create_header'
  | 'create_dto'
  | 'detail_status'
  | 'detail_dto'
  | 'detail_plaintext'
  | 'secret_status'
  | 'secret_mismatch'
  | 'delete_status'
  | 'delete_body'
  | 'delete_header'
  | 'still_present'
  | 'post_disconnect_committed'
  | 'post_disconnect_missing'
  | 'post_disconnect_multiple'
  | 'post_disconnect_marker_invalid'
  | 'post_disconnect_recovery_transient'
  | 'cleanup_detail_transient'
  | 'delete_disconnect_committed'
  | 'cleanup_failure'

const cookie = 'juhe_ai_session=external-create-cookie-secret'
const sensitiveBasePath = '/sensitive-management-base'
const sourceId = 'source-sensitive-id'
const tokenId = 'token/id sensitive'
const token = `juis_${'A'.repeat(43)}`
const marker = 'PLAN-0081 external source create smoke marker v1'
const sourceNamePrefix = 'PLAN-0081 external source create smoke '
const secretHeaders = { 'Cache-Control': 'no-store', Pragma: 'no-cache' }
const records: RequestRecord[] = []
let scenario: Scenario = 'success'
let sourcePresent = false
let currentName = ''
let postCount = 0
let listCount = 0
let deleteCount = 0
let detailGetCount = 0

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
  await assertGateFailsBeforeHTTP(baseUrl)
  await assertSuccessfulSequence(baseUrl)
  await assertFailures(baseUrl)
  await assertPostFailureCleanup(baseUrl)
  await assertAmbiguousPostRecovery(baseUrl)
  await assertBoundedCleanupRecovery(baseUrl)
  await assertCleanupFailureAggregation(baseUrl)
  await assertSensitiveValuesNeverLeak(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go external source create smoke regression passed')

async function assertGateFailsBeforeHTTP(baseUrl: string): Promise<void> {
  reset('success')
  assert.throws(() => loadRealGoExternalSourceCreateSmokeConfig({}), /Missing required environment variable/)
  assert.equal(records.length, 0)

  const valid = smokeEnvironment(baseUrl)
  for (const name of [
    realGoExternalSourceCreateSmokeEnv.baseUrl,
    realGoExternalSourceCreateSmokeEnv.cookie,
    realGoExternalSourceCreateSmokeEnv.allowCreates,
    realGoExternalSourceCreateSmokeEnv.confirmation
  ]) {
    const missing = { ...valid }
    delete missing[name]
    await assert.rejects(
      runRealGoExternalSourceCreateSmokeFromEnvironment(missing, () => undefined),
      /Missing required environment variable|must equal/
    )
    assert.equal(records.length, 0, `${name} must fail before HTTP`)
  }

  for (const override of [
    { [realGoExternalSourceCreateSmokeEnv.allowCreates]: '0' },
    { [realGoExternalSourceCreateSmokeEnv.allowCreates]: 'true' },
    { [realGoExternalSourceCreateSmokeEnv.confirmation]: 'wrong-confirmation' }
  ]) {
    await assert.rejects(
      runRealGoExternalSourceCreateSmokeFromEnvironment({ ...valid, ...override }, () => undefined),
      /must equal|must match/
    )
    assert.equal(records.length, 0, 'invalid destructive gate must fail before HTTP')
  }

  const wrongTarget = { ...valid }
  wrongTarget[realGoExternalSourceCreateSmokeEnv.baseUrl] = `${serverBaseUrl(server)}/different-target`
  await assert.rejects(
    runRealGoExternalSourceCreateSmokeFromEnvironment(wrongTarget, () => undefined),
    /must match the normalized management target/
  )
  assert.equal(records.length, 0, 'confirmation for another target must fail before HTTP')
}

async function assertSuccessfulSequence(baseUrl: string): Promise<void> {
  reset('success')
  const output: string[] = []
  const summary = await runRealGoExternalSourceCreateSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )

  assert.deepEqual(summary, { externalIntegrationSourceCreateChecked: true })
  assert.deepEqual(output, [formatRealGoExternalSourceCreateSmokeSummary(summary)])
  assert.equal(output[0], 'externalIntegrationSourceCreateChecked=true')
  assert.deepEqual(records.map(signature), [
    'POST /__aisys__/api/external-integration-sources',
    `GET /__aisys__/api/external-integration-sources?page=1&pageSize=100&keyword=${encodeURIComponent(currentName)}`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}/tokens/${encodeURIComponent(tokenId)}/secret`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`,
    `DELETE /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`
  ])
  assertRequestHeaders()
  const create = records[0]?.body as Record<string, unknown>
  assert.equal(typeof create.name, 'string')
  assert.match(String(create.name), /^PLAN-0081 external source create smoke [0-9a-f-]{36}$/)
  assert.equal(create.notes, marker)
  assert.equal(create.status, 'disabled')
  assert.equal(sourcePresent, false)
  assertNoLeaks(output.join('\n'), baseUrl)
}

async function assertFailures(baseUrl: string): Promise<void> {
  const cases: Array<[Scenario, RegExp]> = [
    ['create_status', /create failed with HTTP 500/],
    ['create_header', /create must return Cache-Control: no-store/],
    ['create_dto', /create token DTO/],
    ['detail_status', /detail failed with HTTP 502/],
    ['detail_dto', /detail source DTO/],
    ['detail_plaintext', /detail must not contain plaintext token/],
    ['secret_status', /token secret failed with HTTP 503/],
    ['secret_mismatch', /token secret does not match created token/],
    ['delete_status', /cleanup delete failed with HTTP 500/],
    ['delete_body', /cleanup delete must return an empty body/],
    ['delete_header', /cleanup delete must return Cache-Control: no-store/]
  ]
  for (const [failureScenario, expected] of cases) {
    reset(failureScenario)
    await assert.rejects(
      run(baseUrl, failureScenario === 'delete_body' ? nonEmptyNoContentRuntime() : undefined),
      expected
    )
    if (failureScenario !== 'delete_status') {
      assert.equal(sourcePresent, false, `${failureScenario} must clean the source`)
    }
    if (failureScenario === 'create_status') assert.equal(postCount, 1, 'create failure cleanup must never retry POST')
    assertNoLeaks(await rejectionText(runWithFreshFailure(baseUrl, failureScenario)), baseUrl)
  }
}

async function assertPostFailureCleanup(baseUrl: string): Promise<void> {
  reset('detail_status')
  await assert.rejects(run(baseUrl), /detail failed/)
  assert.equal(deleteCount, 1)
  assert.equal(sourcePresent, false)
}

async function assertAmbiguousPostRecovery(baseUrl: string): Promise<void> {
  reset('post_disconnect_committed')
  await assert.rejects(run(baseUrl), /create request failed/)
  assert.equal(listCount, 1, 'ambiguous POST must perform one bounded list diagnostic')
  assert.equal(deleteCount, 1, 'single exact marker match must be cleaned')
  assert.equal(sourcePresent, false)
  assert.deepEqual(records.map(signature), [
    'POST /__aisys__/api/external-integration-sources',
    `GET /__aisys__/api/external-integration-sources?page=1&pageSize=100&keyword=${encodeURIComponent(currentName)}`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`,
    `DELETE /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`,
    `GET /__aisys__/api/external-integration-sources/${encodeURIComponent(sourceId)}`
  ])

  reset('post_disconnect_missing')
  await assert.rejects(run(baseUrl), /create request failed/)
  assert.equal(listCount, 3, 'missing ambiguous POST target must use the bounded visibility window')
  assert.equal(deleteCount, 0)

  for (const recoveryScenario of ['post_disconnect_multiple', 'post_disconnect_marker_invalid'] as const) {
    reset(recoveryScenario)
    const text = await rejectionText(run(baseUrl))
    assert.match(text, /cleanup: recovery list exact-name candidates are unsafe/)
    assert.equal(deleteCount, 0)
    assertNoLeaks(text, baseUrl)
  }
}

async function assertBoundedCleanupRecovery(baseUrl: string): Promise<void> {
  reset('post_disconnect_recovery_transient')
  await assert.rejects(run(baseUrl), /create request failed/)
  assert.equal(listCount, 2, 'recovery list must retry one transient failure')
  assert.equal(deleteCount, 1)
  assert.equal(sourcePresent, false)

  for (const cleanupScenario of ['cleanup_detail_transient', 'delete_disconnect_committed', 'still_present'] as const) {
    reset(cleanupScenario)
    const summary = await run(baseUrl)
    assert.deepEqual(summary, { externalIntegrationSourceCreateChecked: true })
    assert.equal(sourcePresent, false)
    if (cleanupScenario === 'still_present') {
      assert.equal(deleteCount, 2, '204 with a still-present target must retry DELETE after safe revalidation')
    }
    if (cleanupScenario === 'delete_disconnect_committed') {
      assert.equal(deleteCount, 1, 'uncertain committed delete must be verified instead of blindly repeated')
    }
  }
}

async function assertCleanupFailureAggregation(baseUrl: string): Promise<void> {
  reset('cleanup_failure')
  const error = await rejectionError(run(baseUrl))
  assert.ok(error instanceof AggregateError)
  assert.equal(error.errors.length, 2)
  const text = error.message
  assert.match(text, /detail failed with HTTP 502/)
  assert.match(text, /cleanup delete failed with HTTP 500/)
  assertNoLeaks(text, baseUrl)
}

async function assertSensitiveValuesNeverLeak(baseUrl: string): Promise<void> {
  for (const failureScenario of ['secret_mismatch', 'cleanup_failure', 'post_disconnect_committed'] as const) {
    reset(failureScenario)
    assertNoLeaks(await rejectionText(run(baseUrl)), baseUrl)
  }
}

function run(
  baseUrl: string,
  runtime?: RealGoExternalSourceCreateSmokeRuntime
): Promise<{ externalIntegrationSourceCreateChecked: true }> {
  return runRealGoExternalSourceCreateSmoke(
    loadRealGoExternalSourceCreateSmokeConfig(smokeEnvironment(baseUrl)),
    runtime
  )
}

function nonEmptyNoContentRuntime(): RealGoExternalSourceCreateSmokeRuntime {
  return {
    fetch: (async (input, init) => {
      const response = await fetch(input, init)
      if (init?.method !== 'DELETE' || response.status !== 204) return response
      return {
        status: 204,
        headers: response.headers,
        text: async () => 'not-empty'
      } as Response
    }) as typeof fetch
  }
}

async function runWithFreshFailure(baseUrl: string, failureScenario: Scenario): Promise<unknown> {
  reset(failureScenario)
  return run(baseUrl)
}

function smokeEnvironment(baseUrl: string): ExternalSourceCreateSmokeEnvironment {
  return {
    [realGoExternalSourceCreateSmokeEnv.baseUrl]: baseUrl,
    [realGoExternalSourceCreateSmokeEnv.cookie]: cookie,
    [realGoExternalSourceCreateSmokeEnv.timeoutMs]: '1000',
    [realGoExternalSourceCreateSmokeEnv.allowCreates]: '1',
    [realGoExternalSourceCreateSmokeEnv.confirmation]: externalIntegrationSourceCreateConfirmationForBaseUrl(baseUrl)
  }
}

function reset(next: Scenario): void {
  scenario = next
  records.length = 0
  sourcePresent = false
  currentName = ''
  postCount = 0
  listCount = 0
  deleteCount = 0
  detailGetCount = 0
}

async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? '/', 'http://mock.test')
  const body = await readBody(req)
  records.push({ method: req.method, pathname: url.pathname, search: url.search, headers: req.headers, body })
  const collection = `${sensitiveBasePath}/__aisys__/api/external-integration-sources`

  if (req.method === 'POST' && url.pathname === collection) {
    postCount += 1
    const payload = body as Record<string, unknown>
    currentName = String(payload.name ?? '')
    sourcePresent = scenario !== 'post_disconnect_missing'
    if (scenario.startsWith('post_disconnect_')) {
      req.socket.destroy()
      return
    }
    if (scenario === 'create_status') return sendJSON(res, 500, { message: 'sensitive server body' })
    return sendJSON(res, 201, createEnvelope(), scenario === 'create_header' ? { 'Cache-Control': 'private' } : secretHeaders)
  }

  if (req.method === 'GET' && url.pathname === collection) {
    listCount += 1
    if (scenario === 'post_disconnect_recovery_transient' && listCount === 1) {
      return sendJSON(res, 503, { message: token })
    }
    let items = sourcePresent ? [sourceSummary(false)] : []
    if (scenario === 'post_disconnect_multiple') {
      items = [sourceSummary(false), { ...sourceSummary(false), id: 'other-source-id' }]
    }
    if (scenario === 'post_disconnect_marker_invalid' && items[0]) {
      items[0] = { ...items[0], notes: 'wrong marker' }
    }
    return sendJSON(res, 200, { data: { items, page: 1, pageSize: 100, pageUpperBound: items.length, hasMore: false } })
  }

  const detailPath = `${collection}/${encodeURIComponent(sourceId)}`
  if (req.method === 'GET' && url.pathname === detailPath) {
    if (!sourcePresent) return sendJSON(res, 404, { message: 'missing' })
    detailGetCount += 1
    if (scenario === 'cleanup_detail_transient' && detailGetCount === 2) {
      return sendJSON(res, 503, { message: token })
    }
    if ((scenario === 'detail_status' || scenario === 'cleanup_failure') && detailGetCount === 1) {
      return sendJSON(res, 502, { message: token })
    }
    return sendJSON(res, 200, { data: sourceSummary(true) })
  }

  const secretPath = `${detailPath}/tokens/${encodeURIComponent(tokenId)}/secret`
  if (req.method === 'GET' && url.pathname === secretPath) {
    if (scenario === 'secret_status') return sendJSON(res, 503, { message: token })
    return sendJSON(res, 200, { data: { token: scenario === 'secret_mismatch' ? 'juis_other_secret' : token } }, secretHeaders)
  }

  if (req.method === 'DELETE' && url.pathname === detailPath) {
    deleteCount += 1
    if (scenario === 'delete_disconnect_committed') {
      sourcePresent = false
      req.socket.destroy()
      return
    }
    if (scenario === 'cleanup_failure' || scenario === 'delete_status') return sendJSON(res, 500, { message: token })
    sourcePresent = scenario === 'still_present' && deleteCount === 1
    res.statusCode = scenario === 'delete_status' ? 500 : 204
    res.setHeader('Cache-Control', scenario === 'delete_header' ? 'private' : 'no-store')
    res.setHeader('Pragma', 'no-cache')
    res.end(scenario === 'delete_body' ? 'not-empty' : undefined)
    return
  }

  sendJSON(res, 404, { message: 'unexpected' })
}

function createEnvelope(): Record<string, unknown> {
  if (scenario === 'create_dto') return { data: { token: { token } } }
  return { data: { token: createdToken() } }
}

function sourceSummary(detail: boolean): Record<string, unknown> {
  const summary: Record<string, unknown> = {
    id: sourceId,
    name: currentName,
    status: 'disabled',
    scopes: [],
    rateLimits: [],
    notes: marker,
    createdAt: '2026-07-16T00:00:00.000Z',
    updatedAt: '2026-07-16T00:00:00.000Z',
    tokenCount: 1,
    activeTokenCount: 0,
    isBuiltIn: false
  }
  if (detail) summary.tokens = [tokenSummary()]
  else summary.primaryToken = {
    id: tokenId,
    tokenPrefix: token.slice(0, 8),
    tokenSuffix: token.slice(-8)
  }
  if (scenario === 'detail_dto' && detail && detailGetCount === 1) summary.tokens = []
  if (scenario === 'detail_plaintext' && detail && detailGetCount === 1) {
    (summary.tokens as Array<Record<string, unknown>>)[0]!.token = token
  }
  return summary
}

function createdToken(): Record<string, unknown> {
  return {
    id: tokenId,
    name: `${currentName} Token`,
    token,
    tokenPrefix: token.slice(0, 8),
    tokenSuffix: token.slice(-8),
    scopes: []
  }
}

function tokenSummary(): Record<string, unknown> {
  const { token: _plaintext, ...summary } = createdToken()
  return {
    ...summary,
    status: 'disabled',
    createdAt: '2026-07-16T00:00:00.000Z',
    updatedAt: '2026-07-16T00:00:00.000Z',
    isBuiltIn: false
  }
}

function assertRequestHeaders(): void {
  for (const record of records) {
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers.cookie, cookie)
    assert.equal(record.headers['user-agent'], 'juhe-ai-plan0081-real-go-external-source-create-smoke/1.0')
    assert.equal(record.headers['x-juhe-ai-smoke'], 'plan0081-real-go-external-source-create')
    if (record.method === 'POST') assert.equal(record.headers['content-type'], 'application/json')
  }
}

function assertNoLeaks(text: string, baseUrl: string): void {
  for (const sensitive of [cookie, baseUrl, sourceId, token]) {
    assert.equal(text.includes(sensitive), false, `output leaked sensitive value: ${sensitive}`)
  }
}

function signature(record: RequestRecord): string {
  const pathname = record.pathname.replace(sensitiveBasePath, '')
  return `${record.method} ${pathname}${record.search}`
}

async function rejectionText(value: Promise<unknown>): Promise<string> {
  return (await rejectionError(value)).message
}

async function rejectionError(value: Promise<unknown>): Promise<Error> {
  try {
    await value
    assert.fail('expected rejection')
  } catch (error) {
    return error instanceof Error ? error : new Error(String(error))
  }
}

async function readBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  for await (const chunk of req) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  if (chunks.length === 0) return undefined
  return JSON.parse(Buffer.concat(chunks).toString('utf8'))
}

function sendJSON(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = { 'Cache-Control': 'no-store' }): void {
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
