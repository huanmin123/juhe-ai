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
  | 'ip_stats_not_ready'
  | 'ip_stats_empty'
  | 'ip_stats_detail_not_ready'
  | 'ip_stats_detail_empty'
  | 'ip_stats_failure'
  | 'ip_stats_timeout'
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
const missingGroupId = 'grp_plan0081_missing_sensitive'
const missingProviderCode = 'missing-provider-sensitive'
const sensitiveClientIPHash = 'a'.repeat(64)
const explicitClientIPHash = 'd'.repeat(64)
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

  await assertLogBoundaryRedaction(baseUrl)
  await assertReadOnlySmoke(baseUrl)
  await assertClientIPRangeNotReadySmoke(baseUrl)
  await assertClientIPRangeEmptySmoke(baseUrl)
  await assertStrictClientIPDetailRequiresTarget(baseUrl)
  await assertStrictClientIPDetailResponseRequirements(baseUrl)
  await assertExplicitClientIPHashSmoke(baseUrl)
  await assertSuccessfulMutationSmoke(baseUrl)
  await assertPatchFailureStillCleansUp(baseUrl)
  await assertCleanup404IsIdempotent(baseUrl)
  await assertPrimaryAndCleanupErrorsArePreserved(baseUrl)
  await assertInvalidConfiguration(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go management smoke regression passed')

async function assertLogBoundaryRedaction(baseUrl: string): Promise<void> {
  const messages: string[] = []

  resetMock('normal')
  await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.allowGroupMutations]: '0'
    }),
    (message) => messages.push(message)
  )

  resetMock('normal')
  const groupFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.groupId]: missingGroupId
    }), () => undefined)
  )
  assert.match(groupFailureMessage, /Configured group was not returned by groups list/)
  messages.push(groupFailureMessage)

  resetMock('normal')
  const providerFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(mutationEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.providerCode]: missingProviderCode
    }), () => undefined)
  )
  assert.match(providerFailureMessage, /Mutation provider was not returned by providers\/options/)
  messages.push(providerFailureMessage)

  resetMock('ip_stats_failure')
  const clientIPFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl), () => undefined)
  )
  assert.equal(clientIPFailureMessage, 'client IP stats list failed with HTTP 503')
  messages.push(clientIPFailureMessage)

  resetMock('ip_stats_timeout')
  const clientIPTimeoutMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.timeoutMs]: '250'
    }), () => undefined)
  )
  assert.match(clientIPTimeoutMessage, /^client IP stats list request failed: (TimeoutError|AbortError)$/)
  messages.push(clientIPTimeoutMessage)

  const credentialedBaseUrl = 'https://smoke-user:smoke-password@example.test/private'
  const baseUrlFailureMessage = await captureFailureMessage(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(credentialedBaseUrl), () => undefined)
  )
  assert.match(baseUrlFailureMessage, /must not contain credentials/)
  messages.push(baseUrlFailureMessage)

  assertNoEnvironmentIdentifierLeak(messages, baseUrl, [
    missingGroupId,
    missingProviderCode,
    'smoke-user',
    'smoke-password',
    credentialedBaseUrl
  ])
}

async function assertReadOnlySmoke(baseUrl: string): Promise<void> {
  resetMock('normal')
  const output: string[] = []
  const env = smokeEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.allowGroupMutations]: '0',
    [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
  })
  const loadedConfig = loadRealGoManagementSmokeConfig(env)
  assert.equal(loadedConfig.requireClientIpDetail, true)
  assert.equal(loadedConfig.clientIpHash, undefined)
  const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))

  assert.deepEqual(summary, expectedSummary())
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 clientIpItems=3 clientIpRangeReady=true clientIpDetailChecked=true'
  )
  assert.deepEqual(requestPaths(), [
    groupsListPath(),
    groupDetailPath(selectedGroupId),
    providersPath(),
    modelOptionsPath(),
    clientIPStatsPath(),
    clientIPStatsDetailPath()
  ])
  assert.equal(requestRecords.some((record) => ['POST', 'PATCH', 'DELETE'].includes(record.method ?? '')), false)
  assertNoCookieLeak(output)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertStrictClientIPDetailRequiresTarget(baseUrl: string): Promise<void> {
  for (const requestScenario of ['ip_stats_not_ready', 'ip_stats_empty'] as const) {
    resetMock(requestScenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
      }), (message) => output.push(message))
    )

    assert.equal(
      failureMessage,
      `client IP detail is required but no verifiable target is available; set ${realGoManagementSmokeEnv.clientIpHash} to a known 64-character hexadecimal hash`
    )
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      groupsListPath(),
      groupDetailPath(selectedGroupId),
      providersPath(),
      modelOptionsPath(),
      clientIPStatsPath()
    ])
    assertNoEnvironmentIdentifierLeak([failureMessage], baseUrl)
    assertRequestHeaders()
  }
}

async function assertStrictClientIPDetailResponseRequirements(baseUrl: string): Promise<void> {
  const cases = [
    {
      scenario: 'ip_stats_detail_not_ready',
      expectedMessage: 'client IP stats detail is required but rangeReady is false'
    },
    {
      scenario: 'ip_stats_detail_empty',
      expectedMessage: 'client IP stats detail is required but items is empty'
    }
  ] as const

  for (const testCase of cases) {
    resetMock(testCase.scenario)
    const output: string[] = []
    const failureMessage = await captureFailureMessage(
      runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
        [realGoManagementSmokeEnv.requireClientIpDetail]: '1'
      }), (message) => output.push(message))
    )

    assert.equal(failureMessage, testCase.expectedMessage)
    assert.deepEqual(output, [])
    assert.deepEqual(requestPaths(), [
      groupsListPath(),
      groupDetailPath(selectedGroupId),
      providersPath(),
      modelOptionsPath(),
      clientIPStatsPath(),
      clientIPStatsDetailPath()
    ])
    assertNoEnvironmentIdentifierLeak([failureMessage], baseUrl)
    assertRequestHeaders()
  }
}

async function assertExplicitClientIPHashSmoke(baseUrl: string): Promise<void> {
  const cases = [
    { scenario: 'ip_stats_empty', expectedItemCount: 0 },
    { scenario: 'ip_stats_detail_not_ready', expectedItemCount: 3 },
    { scenario: 'ip_stats_detail_empty', expectedItemCount: 3 }
  ] as const

  for (const testCase of cases) {
    resetMock(testCase.scenario)
    const output: string[] = []
    const env = smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.clientIpHash]: explicitClientIPHash.toUpperCase()
    })
    const loadedConfig = loadRealGoManagementSmokeConfig(env)
    assert.equal(loadedConfig.clientIpHash, explicitClientIPHash)
    assert.equal(loadedConfig.requireClientIpDetail, false)

    const summary = await runRealGoManagementSmokeFromEnvironment(env, (message) => output.push(message))
    assert.deepEqual(summary, expectedSummary(true, testCase.expectedItemCount, true))
    assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
    assert.deepEqual(requestPaths(), [
      groupsListPath(),
      groupDetailPath(selectedGroupId),
      providersPath(),
      modelOptionsPath(),
      clientIPStatsPath(),
      clientIPStatsDetailPath(explicitClientIPHash)
    ])
    assertNoEnvironmentIdentifierLeak(output, baseUrl)
    assertRequestHeaders()
  }
}

async function assertClientIPRangeNotReadySmoke(baseUrl: string): Promise<void> {
  resetMock('ip_stats_not_ready')
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )

  assert.deepEqual(summary, expectedSummary(false))
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 clientIpItems=0 clientIpRangeReady=false clientIpDetailChecked=false'
  )
  assert.deepEqual(requestPaths(), [
    groupsListPath(),
    groupDetailPath(selectedGroupId),
    providersPath(),
    modelOptionsPath(),
    clientIPStatsPath()
  ])
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
  assertRequestHeaders()
}

async function assertClientIPRangeEmptySmoke(baseUrl: string): Promise<void> {
  resetMock('ip_stats_empty')
  const output: string[] = []
  const summary = await runRealGoManagementSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message)
  )

  assert.deepEqual(summary, expectedSummary(true, 0))
  assert.deepEqual(output, [formatRealGoManagementSmokeSummary(summary)])
  assert.equal(
    output[0],
    'PLAN-0081 real Go management smoke passed groups=3 providers=2 modelOptions=2 clientIpItems=0 clientIpRangeReady=true clientIpDetailChecked=false'
  )
  assert.deepEqual(requestPaths(), [
    groupsListPath(),
    groupDetailPath(selectedGroupId),
    providersPath(),
    modelOptionsPath(),
    clientIPStatsPath()
  ])
  assert.equal(requestRecords.every((record) => record.method === 'GET'), true)
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
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
    clientIPStatsPath(),
    clientIPStatsDetailPath(),
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
  assertNoEnvironmentIdentifierLeak(output, baseUrl)
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
  assertNoEnvironmentIdentifierLeak([failure.message], baseUrl)
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
  assertNoEnvironmentIdentifierLeak([failure.message], baseUrl)
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
  assertNoEnvironmentIdentifierLeak(
    [
      failure.message,
      ...failure.errors.map((error) => error instanceof Error ? error.message : String(error))
    ],
    baseUrl
  )
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
      [realGoManagementSmokeEnv.requireClientIpDetail]: 'true'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.requireClientIpDetail} must be 0 or 1`)
  )
  resetMock('normal')
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(smokeEnvironment(baseUrl, {
      [realGoManagementSmokeEnv.clientIpHash]: 'not-a-64-character-hexadecimal-hash'
    }), () => undefined),
    new RegExp(`${realGoManagementSmokeEnv.clientIpHash} must be a 64-character hexadecimal hash`)
  )
  assert.deepEqual(requestRecords, [])
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
  await assert.rejects(
    runRealGoManagementSmoke({
      baseUrl,
      clientIpHash: 'g'.repeat(64),
      cookie
    }),
    /Smoke client IP hash must be a 64-character hexadecimal hash/
  )

  resetMock('normal')
  const unsupportedProviderEnv = mutationEnvironment(baseUrl, {
    [realGoManagementSmokeEnv.providerCode]: missingProviderCode
  })
  await assert.rejects(
    runRealGoManagementSmokeFromEnvironment(unsupportedProviderEnv, () => undefined),
    /Mutation provider was not returned by providers\/options/
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
  if (req.method === 'GET' && url.pathname === '/__aisys__/api/ip-stats') {
    await handleClientIPStatsRequest(res, scenario)
    return
  }
  const clientIPHash = clientIPHashFromDetailPath(url.pathname)
  if (req.method === 'GET' && clientIPHash) {
    sendEnvelope(res, clientIPStatsDetailFixture(clientIPHash, scenario))
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

async function handleClientIPStatsRequest(res: ServerResponse, requestScenario: MockScenario): Promise<void> {
  if (requestScenario === 'ip_stats_timeout') {
    await delay(1_000)
    if (res.destroyed) {
      return
    }
  }
  if (requestScenario === 'ip_stats_failure') {
    res.statusCode = 503
    res.end(JSON.stringify({
      message: `client IP window unavailable for ${sensitiveClientIPHash}; cookie=${cookie}`
    }))
    return
  }
  const rangeReady = requestScenario !== 'ip_stats_not_ready'
  sendEnvelope(res, clientIPStatsListFixture(rangeReady, rangeReady && requestScenario !== 'ip_stats_empty'))
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

function expectedSummary(
  clientIpRangeReady = true,
  clientIpItemCount = clientIpRangeReady ? 3 : 0,
  clientIpDetailChecked = clientIpRangeReady && clientIpItemCount > 0
): Record<string, unknown> {
  return {
    groupCount: 3,
    selectedGroupId,
    providerCount: 2,
    modelOptionCount: 2,
    clientIpItemCount,
    clientIpRangeReady,
    clientIpDetailChecked
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

function clientIPStatsPath(): string {
  return 'GET /__aisys__/api/ip-stats?page=1&pageSize=20&sortField=requestCount&sortOrder=desc'
}

function clientIPStatsDetailPath(ipHash = sensitiveClientIPHash): string {
  return `GET /__aisys__/api/ip-stats/${encodeURIComponent(ipHash)}/detail?startDate=2026-07-14&endDate=2026-07-14&page=1&pageSize=20&sortOrder=asc`
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

function assertNoEnvironmentIdentifierLeak(
  messages: string[],
  baseUrl: string,
  additionalIdentifiers: readonly string[] = []
): void {
  const identifiers = [
    cookie,
    baseUrl,
    systemAccountId,
    selectedGroupId,
    temporaryGroupId,
    sensitiveClientIPHash,
    explicitClientIPHash,
    'gpt',
    'openai',
    ...additionalIdentifiers
  ]
  for (const identifier of identifiers) {
    assert.equal(
      messages.some((message) => message.includes(identifier)),
      false,
      `output must not expose environment identifier: ${identifier}`
    )
  }
}

function clientIPStatsListFixture(rangeReady: boolean, includeItems = rangeReady): Record<string, unknown> {
  const items = includeItems ? clientIPStatsItemsFixture() : []
  return {
    items,
    pageUpperBound: items.length,
    hasMore: false,
    page: 1,
    pageSize: 20,
    range: {
      startDate: '2026-07-14',
      endDate: '2026-07-14',
      days: 1,
      maxDays: 31
    },
    rangeReady
  }
}

function clientIPStatsDetailFixture(ipHash: string, requestScenario: MockScenario): Record<string, unknown> {
  const rangeReady = requestScenario !== 'ip_stats_detail_not_ready'
  const includeItems = rangeReady && requestScenario !== 'ip_stats_detail_empty'
  const items = includeItems
    ? [
        {
          accountId: 'acct_plan0081_low_usage',
          accountName: '低用量账号',
          accountOwnerSystemAccountId: systemAccountId,
          accountOwnerSystemAccountName: 'PLAN-0081 系统账号',
          rangeUsage: clientIPUsageFixture({
            requestCount: 2,
            successCount: 2,
            errorCount: 0,
            inputTokens: 200,
            outputTokens: 50,
            activeDays: 1,
            averageDurationMs: 180.5,
            averageFirstTokenMs: 35.25,
            maxDurationMs: 260,
            lastUsedAt: '2026-07-14T07:30:00.000Z',
            lastErrorAt: '2026-07-14T06:30:00.000Z'
          })
        },
        {
          accountId: 'acct_plan0081_high_usage',
          rangeUsage: clientIPUsageFixture({
            requestCount: 8,
            successCount: 6,
            errorCount: 2,
            inputTokens: 1_000,
            outputTokens: 250,
            activeDays: 1
          })
        }
      ]
    : []
  return {
    ipHash,
    aggregateIpKey: '203.0.113.0/24',
    lastSeenAt: '2026-07-14T08:30:00.000Z',
    items,
    pageUpperBound: items.length,
    hasMore: false,
    page: 1,
    pageSize: 20,
    range: {
      startDate: '2026-07-14',
      endDate: '2026-07-14',
      days: 1,
      maxDays: 31
    },
    rangeReady
  }
}

function clientIPStatsItemsFixture(): Record<string, unknown>[] {
  return [
    {
      ipHash: sensitiveClientIPHash,
      aggregateIpKey: '203.0.113.0/24',
      lastSeenAt: '2026-07-14T08:30:00.000Z',
      status: 'blacklisted',
      rangeUsage: clientIPUsageFixture({
        requestCount: 10,
        successCount: 8,
        errorCount: 2,
        inputTokens: 1_200,
        outputTokens: 300,
        activeDays: 1,
        averageDurationMs: 425.5,
        averageFirstTokenMs: 80.25,
        maxDurationMs: 900,
        lastUsedAt: '2026-07-14T08:25:00.000Z',
        lastErrorAt: '2026-07-14T07:00:00.000Z'
      })
    },
    {
      ipHash: 'b'.repeat(64),
      aggregateIpKey: '198.51.100.0/24',
      status: 'allowlisted',
      rangeUsage: clientIPUsageFixture({
        requestCount: 0,
        successCount: 0,
        errorCount: 0,
        inputTokens: 0,
        outputTokens: 0,
        activeDays: 0
      })
    },
    {
      ipHash: 'c'.repeat(64),
      aggregateIpKey: '192.0.2.0/24',
      lastSeenAt: '',
      status: 'normal',
      rangeUsage: clientIPUsageFixture({
        requestCount: 4,
        successCount: 3,
        errorCount: 1,
        inputTokens: 80,
        outputTokens: 20,
        activeDays: 1
      })
    }
  ]
}

function clientIPHashFromDetailPath(pathname: string): string | undefined {
  const match = /^\/__aisys__\/api\/ip-stats\/([^/]+)\/detail$/.exec(pathname)
  return match?.[1] ? decodeURIComponent(match[1]) : undefined
}

function clientIPUsageFixture(overrides: Record<string, unknown>): Record<string, unknown> {
  const requestCount = Number(overrides.requestCount ?? 0)
  const errorCount = Number(overrides.errorCount ?? 0)
  const inputTokens = Number(overrides.inputTokens ?? 0)
  const outputTokens = Number(overrides.outputTokens ?? 0)
  return {
    requestCount,
    successCount: 0,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    inputTokens,
    outputTokens,
    cacheReadTokens: 5,
    cacheReadCost: 0.001,
    cacheWriteTokens: 3,
    cacheWrite1hTokens: 2,
    cacheWriteCost: 0.002,
    thinkingTokens: 1,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: inputTokens + outputTokens,
    totalCost: 0.03,
    activeDays: 0,
    ...overrides
  }
}

async function captureFailureMessage(operation: Promise<unknown>): Promise<string> {
  try {
    await operation
  } catch (error) {
    assert(error instanceof Error)
    return error.message
  }
  assert.fail('expected operation to fail')
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds))
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
