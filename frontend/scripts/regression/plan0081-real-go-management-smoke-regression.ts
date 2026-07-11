import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import {
  formatRealGoManagementSmokeSummary,
  loadRealGoManagementSmokeConfig,
  realGoManagementSmokeEnv,
  runRealGoManagementSmoke,
  runRealGoManagementSmokeFromEnvironment,
  type SmokeEnvironment
} from '../smoke/plan0081-real-go-management-smoke'

interface RequestRecord {
  method?: string
  url?: string
  headers: IncomingMessage['headers']
  body?: unknown
}

type MockScenario =
  | 'normal'
  | 'patch_failure'
  | 'cleanup_404'
  | 'patch_and_cleanup_failure'

interface MockGroup {
  id: string
  ownerSystemAccountId: string
  name: string
  providerCode: string
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: 'personal' | 'high_concurrency'
  accessType: 'owner' | 'authorized'
  accountIds: string[]
  schedulingPolicy?: Record<string, unknown>
}

const cookie = 'juhe_ai_session=regression-secret; another_cookie=opaque-value'
const systemAccountId = 'sys_plan0081_target'
const selectedGroupId = 'grp_plan0081_owner_secondary'
const temporaryGroupId = 'grp_plan0081_temporary'
const requestRecords: RequestRecord[] = []
const groups = new Map<string, MockGroup>()
let scenario: MockScenario = 'normal'
let patchFailureDelivered = false

const server = createServer((req, res) => {
  void handleRequest(req, res).catch(() => {
    if (!res.headersSent) {
      res.statusCode = 500
      res.setHeader('Cache-Control', 'no-store')
      res.setHeader('Content-Type', 'application/json; charset=utf-8')
    }
    res.end(JSON.stringify({ message: 'mock handler failed' }))
  })
})
await listen(server)

try {
  const baseUrl = serverBaseUrl(server)

  await assertReadOnlySmoke(baseUrl)
  await assertSuccessfulMutationSmoke(baseUrl)
  await assertPatchFailureStillCleansUp(baseUrl)
  await assertCleanup404IsIdempotent(baseUrl)
  await assertPrimaryAndCleanupErrorsArePreserved(baseUrl)
  await assertInvalidConfiguration(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go management smoke regression passed')

async function assertReadOnlySmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.allowGroupMutations]: '0'
  })
  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))

  assert.deepEqual(summary, expectedSummary())
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 selectedGroupId=grp_plan0081_owner_secondary providers=2 modelOptions=2'
  )
  assert.deepEqual(requestPaths(), [
    groupsListPath(),
    groupDetailPath(selectedGroupId),
    providersPath(),
    modelOptionsPath()
  ])
  assert.equal(requestRecords.some((record) => ['POST', 'PATCH', 'DELETE'].includes(record.method ?? '')), false)
  assertNoCookieLeak(output)
  assertRequestHeaders()
}

async function assertSuccessfulMutationSmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = mutationEnvironment(baseUrl)
  const loadedConfig = loadRealGoManagementSmokeConfig(env)
  assert.equal(loadedConfig.allowGroupMutations, true)
  assert.equal(loadedConfig.providerCode, 'openai')
  assert.equal(loadedConfig.timeoutMs, 2_500)

  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
  assert.deepEqual(summary, expectedSummary())
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(groups.has(temporaryGroupId), false)
  assert.deepEqual(requestPaths(), [
    groupsListPath(),
    groupDetailPath(selectedGroupId),
    providersPath(),
    modelOptionsPath(),
    groupsCreatePath(),
    groupsListPath(),
    groupDetailPath(temporaryGroupId),
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId)
  ])

  const createRequest = requestRecords.find((record) => record.method === 'POST')
  assert(createRequest)
  assert.deepEqual(
    omitDynamicName(createRequest.body),
    {
      providerCode: 'openai',
      enabled: true,
      groupType: 'personal'
    }
  )
  assert.match(
    String(recordBody(createRequest).name),
    /^PLAN-0081 real Go management smoke [0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
  )
  const patchRequest = requestRecords.find((record) => record.method === 'PATCH')
  assert.deepEqual(patchRequest?.body, {
    name: `${String(recordBody(createRequest).name)} updated`,
    description: 'PLAN-0081 W5 group CRUD real Go smoke',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 7,
      maxQueueWaitMs: 45_000,
      clientIpConcurrencyLimit: 3,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 2
    }
  })
  assertNoCookieLeak(output)
  assertRequestHeaders()
}

async function assertPatchFailureStillCleansUp(baseUrl: string): Promise<void> {
  resetMock('patch_failure')
  const output: string[] = []
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), (message) => output.push(message))
  } catch (error) {
    failure = error
  }

  assert(failure instanceof Error)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assert.equal(failure.message.includes(cookie), false)
  assert.deepEqual(output, [])
  assert.equal(groups.has(temporaryGroupId), false, 'finally cleanup must remove the PATCH-mutated group')
  assert.deepEqual(requestPaths().slice(-3), [
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId)
  ])
  assertRequestHeaders()
}

async function assertCleanup404IsIdempotent(baseUrl: string): Promise<void> {
  resetMock('cleanup_404')
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), () => undefined)
  } catch (error) {
    failure = error
  }

  assert(failure instanceof Error)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assert.doesNotMatch(failure.message, /cleanup failed/)
  assert.equal(groups.has(temporaryGroupId), false)
  assert.deepEqual(requestPaths().slice(-3), [
    groupPatchPath(temporaryGroupId),
    groupDetailPath(temporaryGroupId),
    groupDeletePath(temporaryGroupId)
  ])
  assert.equal(requestRecords.at(-1)?.method, 'DELETE')
  assertRequestHeaders()
}

async function assertPrimaryAndCleanupErrorsArePreserved(baseUrl: string): Promise<void> {
  resetMock('patch_and_cleanup_failure')
  let failure: unknown
  try {
    await runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl), () => undefined)
  } catch (error) {
    failure = error
  }

  assert(failure instanceof AggregateError)
  assert.match(failure.message, /temporary group PATCH failed with HTTP 503/)
  assert.match(failure.message, /cleanup failed: temporary group cleanup check failed with HTTP 502/)
  assert.equal(failure.message.includes(cookie), false)
  assert.deepEqual(
    failure.errors.map((error) => error instanceof Error ? error.message : String(error)),
    [
      'temporary group PATCH failed with HTTP 503',
      'temporary group cleanup check failed with HTTP 502'
    ]
  )
  assertRequestHeaders()
}

async function assertInvalidConfiguration(baseUrl: string): Promise<void> {
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({}, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.baseUrl}`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment({
      [realGoManagementSmokeEnv.baseUrl]: baseUrl
    }, () => undefined),
    new RegExp(`Missing required environment variable: ${realGoManagementSmokeEnv.cookie}`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.allowGroupMutations]: 'true'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.allowGroupMutations} must be 0 or 1`)
  )
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.timeoutMs]: '0'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.timeoutMs} must be a positive integer`)
  )
  await assert.rejects(
    runRealGoManagementSmoke({
      baseUrl,
      cookie,
      timeoutMs: 0
    }),
    /Smoke timeout must be a positive integer/
  )

  resetMock('normal')
  const unsupportedProviderEnv = mutationEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.providerCode]: 'missing-provider'
  })
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(unsupportedProviderEnv, () => undefined),
    /Mutation provider missing-provider was not returned by providers\/options/
  )
  assert.equal(requestRecords.some((record) => record.method === 'POST'), false)
  assertRequestHeaders()
}

function resetMock(nextScenario: MockScenario): void {
  scenario = nextScenario
  patchFailureDelivered = false
  requestRecords.length = 0
  groups.clear()
  groups.set('grp_plan0081_default', groupFixture('grp_plan0081_default', '默认分组', true, 'owner'))
  groups.set('grp_plan0081_authorized', groupFixture('grp_plan0081_authorized', '授权分组', false, 'authorized'))
  groups.set(selectedGroupId, groupFixture(selectedGroupId, '真实 Go 管理 smoke 分组', false, 'owner'))
}

function smokeEnvironment(baseUrl: string, overrides: SmokeEnvironment = {}): SmokeEnvironment {
  return {
    [realGoManagementSmokeEnv.baseUrl]: baseUrl,
    [realGoManagementSmokeEnv.cookie]: cookie,
    [realGoManagementSmokeEnv.systemAccountId]: systemAccountId,
    ...overrides
  }
}

function mutationEnvironment(baseUrl: string, overrides: SmokeEnvironment = {}): SmokeEnvironment {
  return smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.allowGroupMutations]: '1',
    [realGoManagementSmokeEnv.providerCode]: 'openai',
    [realGoManagementSmokeEnv.timeoutMs]: '2500',
    ...overrides
  })
}

async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const body = await readRequestBody(req)
  requestRecords.push({
    method: req.method,
    url: req.url,
    headers: { ...req.headers },
    body
  })

  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Content-Type', 'application/json; charset=utf-8')

  if (req.method === 'GET' && url.pathname === '/__aisys__/api/groups') {
    sendEnvelope(res, groupListFixture())
    return
  }
  if (req.method === 'POST' && url.pathname === '/__aisys__/api/groups') {
    const input = recordBody({ body })
    const name = String(input.name ?? '')
    const providerCode = String(input.providerCode ?? '')
    const created = groupFixture(temporaryGroupId, name, false, 'owner', providerCode)
    groups.set(created.id, created)
    res.statusCode = 201
    sendEnvelope(res, groupResponse(created))
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/providers/options') {
    sendEnvelope(res, [
      providerFixture('gpt', 'GPT'),
      providerFixture('openai', 'OpenAI')
    ])
    return
  }
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/providers/models/options') {
    sendEnvelope(res, [
      {
        providerCode: 'gpt',
        model: 'gpt-5.6-sol',
        supportedApiProtocols: ['responses'],
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['low', 'high']
      },
      {
        providerCode: 'openai',
        model: 'gpt-4.1',
        supportedApiProtocols: ['chat_completions']
      }
    ])
    return
  }

  const groupId = groupIdFromPath(url.pathname)
  if (groupId) {
    await handleGroupDetailRequest(req, res, groupId, body)
    return
  }

  res.statusCode = 404
  res.end(JSON.stringify({ message: 'not found' }))
}

async function handleGroupDetailRequest(
  req: IncomingMessage,
  res: ServerResponse,
  groupId: string,
  body: unknown
): Promise<void> {
  const group = groups.get(groupId)

  if (
    req.method === 'GET'
    && groupId === temporaryGroupId
    && scenario === 'patch_and_cleanup_failure'
    && patchFailureDelivered
  ) {
    res.statusCode = 502
    res.end(JSON.stringify({ message: 'cleanup lookup unavailable' }))
    return
  }

  if (req.method === 'GET') {
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    sendEnvelope(res, groupResponse(group))
    return
  }

  if (req.method === 'PATCH') {
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    const input = recordBody({ body })
    if (typeof input.name === 'string') {
      group.name = input.name
    }
    if (typeof input.description === 'string') {
      group.description = input.description
    }
    if (input.groupType === 'high_concurrency') {
      group.groupType = 'high_concurrency'
    }
    if (input.schedulingPolicy && typeof input.schedulingPolicy === 'object' && !Array.isArray(input.schedulingPolicy)) {
      group.schedulingPolicy = input.schedulingPolicy as Record<string, unknown>
    }
    if (scenario !== 'normal') {
      patchFailureDelivered = true
      res.statusCode = 503
      res.end(JSON.stringify({ message: 'patch response unavailable' }))
      return
    }
    sendEnvelope(res, groupResponse(group))
    return
  }

  if (req.method === 'DELETE') {
    if (scenario === 'cleanup_404') {
      groups.delete(groupId)
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'already deleted' }))
      return
    }
    if (!group) {
      res.statusCode = 404
      res.end(JSON.stringify({ message: 'not found' }))
      return
    }
    groups.delete(groupId)
    res.statusCode = 204
    res.end()
    return
  }

  res.statusCode = 405
  res.end(JSON.stringify({ message: 'method not allowed' }))
}

function groupListFixture(): Record<string, unknown> {
  return {
    items: [...groups.values()].map(groupListItem),
    total: groups.size,
    hasMore: false,
    page: 1,
    pageSize: 500,
    runtimeSnapshot: {
      accountConcurrencyAvailable: true
    }
  }
}

function groupFixture(
  id: string,
  name: string,
  isDefault: boolean,
  accessType: 'owner' | 'authorized',
  providerCode = 'gpt'
): MockGroup {
  return {
    id,
    ownerSystemAccountId: accessType === 'owner' ? systemAccountId : 'sys_plan0081_owner',
    name,
    providerCode,
    enabled: true,
    isDefault,
    groupType: 'personal',
    accessType,
    accountIds: accessType === 'owner' ? ['acct_plan0081_one'] : []
  }
}

function groupListItem(group: MockGroup): Record<string, unknown> {
  const { accountIds, ...item } = groupResponse(group)
  return {
    ...item,
    accountCount: accountIds.length
  }
}

function groupResponse(group: MockGroup): Record<string, unknown> {
  return {
    ...group,
    ownerSystemAccountName: 'Owner',
    systemAccountId: group.ownerSystemAccountId,
    accountStats: {
      total: group.accountIds.length,
      available: group.accountIds.length
    },
    authorizationLimits: {},
    permissions: {
      canManageAccounts: group.accessType === 'owner'
    },
    schedulingPolicy: group.groupType === 'high_concurrency'
      ? {
          mode: 'balanced_fast',
          defaultSoftConcurrency: 5,
          maxQueueWaitMs: 60_000,
          clientIpConcurrencyLimit: 0,
          clientIpConcurrencyOverflowMode: 'reject',
          imageLaneMaxConcurrency: 0,
          ...group.schedulingPolicy
        }
      : undefined
  }
}

function providerFixture(code: string, name: string): Record<string, unknown> {
  const profileId = `profile_${code}_openai_v1`
  return {
    id: `provider_${code}`,
    code,
    name,
    enabled: true,
    defaultProtocolProfileId: profileId,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.example.test/v1',
    defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
    defaultSupportedModels: code === 'gpt' ? ['gpt-5.6-sol'] : ['gpt-4.1'],
    accountTypes: ['api_key'],
    capabilities: ['chat_completions', 'responses'],
    protocolProfiles: [{
      id: profileId,
      providerCode: code,
      name: `${name} OpenAI v1`,
      enabled: true,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      baseUrl: 'https://api.example.test/v1',
      defaultHealthCheckModel: code === 'gpt' ? 'gpt-5.6-sol' : 'gpt-4.1',
      accountTypes: ['api_key'],
      capabilities: ['chat_completions', 'responses'],
      endpointFamilies: []
    }]
  }
}

function sendEnvelope(res: ServerResponse, data: unknown): void {
  res.end(JSON.stringify({ data }))
}

function groupIdFromPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/groups\/([^/]+)$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

async function readRequestBody(req: IncomingMessage): Promise<unknown> {
  if (req.method === 'GET' || req.method === 'DELETE') {
    return undefined
  }
  const chunks: Buffer[] = []
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  if (!chunks.length) {
    return undefined
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as unknown
}

function recordBody(record: Pick<RequestRecord, 'body'>): Record<string, unknown> {
  assert(record.body && typeof record.body === 'object' && !Array.isArray(record.body))
  return record.body as Record<string, unknown>
}

function omitDynamicName(value: unknown): Record<string, unknown> {
  const { name: _name, ...rest } = recordBody({ body: value })
  return rest
}

function expectedSummary(): Record<string, unknown> {
  return {
    groupCount: 3,
    selectedGroupId,
    providerCount: 2,
    modelOptionCount: 2
  }
}

function requestPaths(): string[] {
  return requestRecords.map((record) => `${record.method} ${record.url}`)
}

function groupsListPath(): string {
  return `GET /__aisys__/api/groups?page=1&pageSize=500&systemAccountId=${systemAccountId}`
}

function groupsCreatePath(): string {
  return `POST /__aisys__/api/groups?systemAccountId=${systemAccountId}`
}

function groupDetailPath(groupId: string): string {
  return `GET /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function groupPatchPath(groupId: string): string {
  return `PATCH /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function groupDeletePath(groupId: string): string {
  return `DELETE /__aisys__/api/groups/${groupId}?systemAccountId=${systemAccountId}`
}

function providersPath(): string {
  return `GET /__aisys__/api/providers/options?systemAccountId=${systemAccountId}`
}

function modelOptionsPath(): string {
  return `GET /__aisys__/api/providers/models/options?systemAccountId=${systemAccountId}`
}

function assertRequestHeaders(): void {
  for (const record of requestRecords) {
    assert.equal(record.headers.cookie, cookie)
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers['user-agent'], 'juhe-ai-plan0081-real-go-management-smoke/1.0')
    assert.equal(record.headers['x-juhe-ai-smoke'], 'plan0081-real-go-management')
    if (record.body !== undefined) {
      assert.equal(record.headers['content-type'], 'application/json')
    }
  }
}

function assertNoCookieLeak(messages: string[]): void {
  assert.equal(messages.some((line) => line.includes(cookie)), false, 'output must not expose the Cookie header')
}

function listen(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.once('error', rejectPromise)
    target.listen(0, '127.0.0.1', () => {
      target.off('error', rejectPromise)
      resolvePromise()
    })
  })
}

function close(target: Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    target.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

function serverBaseUrl(target: Server): string {
  const address = target.address()
  assert(address && typeof address !== 'string')
  return `http://127.0.0.1:${(address as AddressInfo).port}`
}
