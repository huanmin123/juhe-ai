import { strict as assert } from 'node:assert'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

import {
  canonicalizeRealGoClientIpAllowlistApiBaseUrl,
  createRealGoClientIpAllowlistFixtureConfirmation,
  disposableRegistryHashSafetyNotice,
  formatRealGoClientIpAllowlistCleanupSummary,
  formatRealGoClientIpAllowlistSmokeSummary,
  loadRealGoClientIpAllowlistSmokeConfig,
  realGoClientIpAllowlistSmokeEnv,
  runRealGoClientIpAllowlistCleanupOnly,
  runRealGoClientIpAllowlistCleanupOnlyFromEnvironment,
  runRealGoClientIpAllowlistSmoke,
  runRealGoClientIpAllowlistSmokeFromEnvironment,
  type ClientIpAllowlistSmokeEnvironment,
  type RealGoClientIpAllowlistSmokeRuntime
} from '../smoke/plan0081-real-go-client-ip-allowlist-smoke'

interface RequestRecord {
  method?: string
  url?: string
  headers: IncomingMessage['headers']
  body?: unknown
  atMs: number
}

interface DisabledCountObservation {
  disabledCount: number
  atMs: number
}

type MockScenario =
  | 'success'
  | 'allowlist_commit_then_timeout'
  | 'cleanup_timeout_then_429'
  | 'first_unallowlist_polluted'
  | 'cleanup_polluted'
  | 'normal_finally_count_one'
  | 'allowlist_extra_key'
  | 'allowlist_non_utc_timestamp'
  | 'allowlist_missing_no_store'
  | 'allowlist_http_status'
  | 'main_and_cleanup_failure'

interface VirtualRuntimeFixture {
  runtime: RealGoClientIpAllowlistSmokeRuntime
  sleeps: number[]
}

const managementCookie = 'juhe_ai_session=client-ip-smoke-secret; scope=admin'
const mixedCaseDisposableIpHash = 'aB'.repeat(32)
const lowercaseDisposableIpHash = mixedCaseDisposableIpHash.toLowerCase()
const externalFixtureLeaseId = 'lease-regression-20260714'
const alternateFixtureLeaseId = 'lease-regression-20260714-alt'
const sensitivePolicyId = 'policy-sensitive-must-never-be-output'
const sensitiveErrorBody = 'sensitive-response-body-must-never-be-output'
const fixedTimestamp = '2026-07-14T06:30:00.123456789Z'
const actualTimeoutMs = 25
const delayedResponseMs = 90
const virtualClockStartMs = Date.UTC(2026, 6, 14, 6, 30, 0)
const ambiguityHorizonMs = 130_000
const delayedAllowlistCommitAfterMs = 125_000

const requestRecords: RequestRecord[] = []
const committedDisabledCounts: number[] = []
const disabledCountObservations: DisabledCountObservation[] = []
let scenario: MockScenario = 'success'
let hasActiveAllowlist = false
let allowlistRequestCount = 0
let cleanupRequestCount = 0
let virtualNowMs = virtualClockStartMs
let pendingAllowlistCommitAtMs: number | undefined
let delayedAllowlistCommittedAtMs: number | undefined
let allowlistHttpStatus = 503
let allowlistHttpSchedulesLateCommit = false

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
  await assertConfigurationSafety(baseUrl)
  await assertSuccessfulSmoke(baseUrl)
  await assertStrictAllowlistDto(baseUrl)
  await assertAllowlistHttpStatusClassification(baseUrl)
  await assertNoStoreIsRequired(baseUrl)
  await assertPrimaryAndCleanupFailuresAreCombined(baseUrl)
  await assertCommittedAllowlistTimeoutRunsCleanup(baseUrl)
  await assertCleanupTimeoutAndRateLimitRecover(baseUrl)
  await assertCountAnomaliesFail(baseUrl)
  await assertCleanupOnly(baseUrl)
} finally {
  await close(server)
}

console.log('PLAN-0081 real Go client IP allowlist smoke regression passed')

async function assertConfigurationSafety(baseUrl: string): Promise<void> {
  resetMock('success')
  const validEnvironment = smokeEnvironment(baseUrl)
  const loaded = loadRealGoClientIpAllowlistSmokeConfig(validEnvironment)
  const canonicalBaseUrl = `${baseUrl}/__aisys__/api`
  assert.equal(loaded.baseUrl, canonicalBaseUrl)
  assert.equal(loaded.managementCookie, managementCookie)
  assert.equal(loaded.disposableIpHash, lowercaseDisposableIpHash)
  assert.equal(loaded.externalFixtureLeaseId, externalFixtureLeaseId)
  assert.equal(loaded.timeoutMs, 2_500)
  assert.equal(
    loaded.externalFixtureConfirmation,
    createRealGoClientIpAllowlistFixtureConfirmation(
      canonicalBaseUrl,
      lowercaseDisposableIpHash,
      externalFixtureLeaseId
    )
  )
  assert.equal(
    createRealGoClientIpAllowlistFixtureConfirmation(
      baseUrl,
      mixedCaseDisposableIpHash,
      externalFixtureLeaseId
    ),
    createRealGoClientIpAllowlistFixtureConfirmation(
      canonicalBaseUrl,
      lowercaseDisposableIpHash,
      externalFixtureLeaseId
    ),
    'fixture confirmation must bind canonical API base URL and lowercase hash'
  )

  for (const requiredName of [
    realGoClientIpAllowlistSmokeEnv.baseUrl,
    realGoClientIpAllowlistSmokeEnv.managementCookie,
    realGoClientIpAllowlistSmokeEnv.disposableIpHash,
    realGoClientIpAllowlistSmokeEnv.externalFixtureLeaseId,
    realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation
  ]) {
    const missing = { ...validEnvironment }
    delete missing[requiredName]
    assert.throws(
      () => loadRealGoClientIpAllowlistSmokeConfig(missing),
      new RegExp(`Missing required environment variable: ${requiredName}`)
    )
  }

  for (const invalidCookie of [`${managementCookie}\nsecond=value`, `${managementCookie}\rsecond=value`]) {
    assert.throws(
      () => loadRealGoClientIpAllowlistSmokeConfig({
        ...validEnvironment,
        [realGoClientIpAllowlistSmokeEnv.managementCookie]: invalidCookie
      }),
      /must be a single Cookie header line/
    )
  }
  for (const invalidHash of ['a'.repeat(63), `${'a'.repeat(63)}g`, 'not-a-disposable-hash']) {
    assert.throws(
      () => loadRealGoClientIpAllowlistSmokeConfig({
        ...validEnvironment,
        [realGoClientIpAllowlistSmokeEnv.disposableIpHash]: invalidHash
      }),
      /must be exactly 64 hexadecimal characters/
    )
  }

  const invalidBaseUrls: Array<[string, RegExp]> = [
    ['ftp://example.test', /must use HTTP or HTTPS/],
    ['/relative-management-api', /must be an absolute HTTP\(S\) URL/],
    ['https://smoke-user:smoke-password@example.test', /must not contain credentials/],
    [`${baseUrl}?owner=unexpected`, /must not contain query or fragment/],
    [`${baseUrl}#unexpected`, /must not contain query or fragment/],
    [`${baseUrl}\n`, /must be a single line/],
    ['http://example.test', /must use HTTPS unless the host is loopback/],
    ['http://192.0.2.10', /must use HTTPS unless the host is loopback/],
    ['http://0.0.0.0', /must use HTTPS unless the host is loopback/]
  ]
  for (const [invalidBaseUrl, expectedError] of invalidBaseUrls) {
    assert.throws(
      () => loadRealGoClientIpAllowlistSmokeConfig({
        ...validEnvironment,
        [realGoClientIpAllowlistSmokeEnv.baseUrl]: invalidBaseUrl
      }),
      expectedError
    )
  }

  for (const loopbackBaseUrl of [
    'http://localhost:3100',
    'http://127.23.45.67:3100/control',
    'http://[::1]:3100'
  ]) {
    assert.doesNotThrow(() => createRealGoClientIpAllowlistFixtureConfirmation(
      loopbackBaseUrl,
      mixedCaseDisposableIpHash,
      externalFixtureLeaseId
    ))
  }
  assert.doesNotThrow(() => createRealGoClientIpAllowlistFixtureConfirmation(
    'https://management.example.test/control',
    mixedCaseDisposableIpHash,
    externalFixtureLeaseId
  ))

  const boundConfirmation = validEnvironment[realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation]
  assert(boundConfirmation)
  for (const bindingOverride of [
    { [realGoClientIpAllowlistSmokeEnv.baseUrl]: `${baseUrl}/other-fixture` },
    { [realGoClientIpAllowlistSmokeEnv.disposableIpHash]: 'cd'.repeat(32) },
    { [realGoClientIpAllowlistSmokeEnv.externalFixtureLeaseId]: alternateFixtureLeaseId },
    { [realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation]: '1' }
  ]) {
    assert.throws(
      () => loadRealGoClientIpAllowlistSmokeConfig({
        ...validEnvironment,
        ...bindingOverride,
        ...(Object.hasOwn(bindingOverride, realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation)
          ? {}
          : { [realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation]: boundConfirmation })
      }),
      /confirmation.*does not match/i
    )
  }

  await assert.rejects(
    runRealGoClientIpAllowlistCleanupOnly(loaded, {
      cleanupWindowMs: 129_999,
      now: () => 0,
      sleep: async () => undefined
    }),
    /Cleanup window must be an integer from 130000/
  )
  assert.equal(requestRecords.length, 0, 'invalid configuration must not issue HTTP requests')
}

async function assertSuccessfulSmoke(baseUrl: string): Promise<void> {
  resetMock('success')
  const output: string[] = []
  const virtual = createVirtualRuntime()
  const summary = await runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message),
    virtual.runtime
  )

  assert.deepEqual(summary, {
    firstDisabledCount: 1,
    secondDisabledCount: 0,
    cleanupAttemptCount: 2
  })
  assert.deepEqual(output, [
    disposableRegistryHashSafetyNotice,
    formatRealGoClientIpAllowlistSmokeSummary(summary)
  ])
  assert.equal(hasActiveAllowlist, false)
  assert.equal(allowlistRequestCount, 1, 'allowlist must be attempted exactly once')
  assert.deepEqual(committedDisabledCounts, [1, 0, 0, 0])
  assert.deepEqual(requestRecords.map(requestSignature), [
    expectedRequestSignature('allowlist'),
    expectedRequestSignature('unallowlist'),
    expectedRequestSignature('unallowlist'),
    expectedRequestSignature('unallowlist'),
    expectedRequestSignature('unallowlist')
  ])
  assertRequestContract()
  const firstRunReasons = requestRecords.map(requestReason)
  assert.equal(new Set(firstRunReasons).size, requestRecords.length)
  assertNoSensitiveLeak(output, baseUrl, summary)

  resetMock('success')
  await runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    createVirtualRuntime().runtime
  )
  const secondRunReasons = requestRecords.map(requestReason)
  assert.equal(
    secondRunReasons.some((reason) => firstRunReasons.includes(reason)),
    false,
    'a repeated smoke run must not reuse a mutation-guard reason within 60 seconds'
  )
}

async function assertStrictAllowlistDto(baseUrl: string): Promise<void> {
  for (const [nextScenario, expectedError] of [
    ['allowlist_extra_key', /complete active allowlist DTO key set/],
    ['allowlist_non_utc_timestamp', /createdAt must be strict UTC RFC3339/]
  ] as const) {
    resetMock(nextScenario)
    const virtual = createVirtualRuntime()
    const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
      smokeEnvironment(baseUrl),
      () => undefined,
      virtual.runtime
    ))
    assert(failure instanceof Error)
    assert.match(failure.message, expectedError)
    assert.equal(allowlistRequestCount, 1)
    assert.equal(hasActiveAllowlist, false)
    assert.deepEqual(committedDisabledCounts, [1, 0, 0])
    assert(virtual.sleeps.every((delayMs) => delayMs < ambiguityHorizonMs))
    assertNoSensitiveLeak(errorMessages(failure), baseUrl)
  }
}

async function assertAllowlistHttpStatusClassification(baseUrl: string): Promise<void> {
  for (const status of [408, 500, 503, 504]) {
    resetMock('allowlist_http_status')
    allowlistHttpStatus = status
    allowlistHttpSchedulesLateCommit = true
    const virtual = createVirtualRuntime()
    const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
      smokeEnvironment(baseUrl),
      () => undefined,
      virtual.runtime
    ))

    assert(failure instanceof Error)
    assert.equal(failure.message, `allowlist failed with HTTP ${status}`)
    assert.equal(allowlistRequestCount, 1, `HTTP ${status} must not retry allowlist`)
    assert.equal(cleanupRequestCount, 5, `HTTP ${status} must use the ambiguity horizon`)
    assert.deepEqual(committedDisabledCounts, [0, 0, 1, 0, 0])
    assert.deepEqual(virtual.sleeps, [ambiguityHorizonMs, 250])
    assert.equal(delayedAllowlistCommittedAtMs, virtualClockStartMs + ambiguityHorizonMs)
    assert(disabledCountObservations[0].atMs < virtualClockStartMs + ambiguityHorizonMs)
    assert(disabledCountObservations[1].atMs < virtualClockStartMs + ambiguityHorizonMs)
    assert.equal(disabledCountObservations[2].disabledCount, 1)
    assert(disabledCountObservations[2].atMs >= virtualClockStartMs + ambiguityHorizonMs)
    assert.equal(hasActiveAllowlist, false)
    assertNoSensitiveLeak(errorMessages(failure), baseUrl)
  }

  for (const status of [400, 401, 403, 409, 429]) {
    resetMock('allowlist_http_status')
    allowlistHttpStatus = status
    allowlistHttpSchedulesLateCommit = false
    const virtual = createVirtualRuntime()
    const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
      smokeEnvironment(baseUrl),
      () => undefined,
      virtual.runtime
    ))

    assert(failure instanceof Error)
    assert.equal(failure.message, `allowlist failed with HTTP ${status}`)
    assert.equal(allowlistRequestCount, 1)
    assert.equal(cleanupRequestCount, 2, `HTTP ${status} must not use the ambiguity horizon`)
    assert.deepEqual(committedDisabledCounts, [0, 0])
    assert.deepEqual(virtual.sleeps, [])
    assert.equal(virtualNowMs, virtualClockStartMs)
    assertNoSensitiveLeak(errorMessages(failure), baseUrl)
  }
}

async function assertNoStoreIsRequired(baseUrl: string): Promise<void> {
  resetMock('allowlist_missing_no_store')
  const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    createVirtualRuntime().runtime
  ))
  assert(failure instanceof Error)
  assert.match(failure.message, /allowlist must return Cache-Control: no-store/)
  assert.equal(hasActiveAllowlist, false)
  assert.deepEqual(committedDisabledCounts, [1, 0, 0])
  assertNoSensitiveLeak(errorMessages(failure), baseUrl)
}

async function assertPrimaryAndCleanupFailuresAreCombined(baseUrl: string): Promise<void> {
  resetMock('main_and_cleanup_failure')
  const virtual = createVirtualRuntime()
  const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    virtual.runtime
  ))

  assert(failure instanceof AggregateError)
  assert.match(failure.message, /complete active allowlist DTO key set/)
  assert.match(failure.message, /cleanup failed: Cleanup could not confirm two consecutive disabledCount=0/)
  assert.equal(allowlistRequestCount, 1)
  assert.equal(cleanupRequestCount, 1)
  assert.deepEqual(virtual.sleeps, [130_000])
  assert.equal(hasActiveAllowlist, true, 'mock cleanup failure must retain state for the assertion')
  assertNoSensitiveLeak(errorMessages(failure), baseUrl)
}

async function assertCommittedAllowlistTimeoutRunsCleanup(baseUrl: string): Promise<void> {
  resetMock('allowlist_commit_then_timeout')
  const output: string[] = []
  const virtual = createVirtualRuntime()
  const failure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl, {
      [realGoClientIpAllowlistSmokeEnv.timeoutMs]: String(actualTimeoutMs)
    }),
    (message) => output.push(message),
    virtual.runtime
  ))

  assert(failure instanceof Error)
  assert.match(failure.message, /allowlist request failed: (?:TimeoutError|AbortError)/)
  assert.equal(allowlistRequestCount, 1, 'an uncertain allowlist result must never be retried')
  assert.equal(hasActiveAllowlist, false, 'post-horizon cleanup must remove the delayed allowlist commit')
  assert.deepEqual(committedDisabledCounts, [0, 0, 1, 0, 0])
  assert.equal(cleanupRequestCount, 6)
  assert.deepEqual(virtual.sleeps, [ambiguityHorizonMs, 29_000, 250])
  assert.equal(delayedAllowlistCommittedAtMs, virtualClockStartMs + ambiguityHorizonMs)
  assert(disabledCountObservations[0].atMs < virtualClockStartMs + ambiguityHorizonMs)
  assert(disabledCountObservations[1].atMs < virtualClockStartMs + ambiguityHorizonMs)
  assert.equal(disabledCountObservations[2].disabledCount, 1)
  assert.equal(
    disabledCountObservations[2].atMs,
    virtualClockStartMs + ambiguityHorizonMs + 29_000
  )
  assert.deepEqual(requestRecords.map((record) => actionFromRecord(record)), [
    'allowlist',
    'unallowlist',
    'unallowlist',
    'unallowlist',
    'unallowlist',
    'unallowlist',
    'unallowlist'
  ])
  assert.equal(new Set(requestRecords.map(requestReason)).size, requestRecords.length)
  assertNoSensitiveLeak([...output, ...errorMessages(failure)], baseUrl)
}

async function assertCleanupTimeoutAndRateLimitRecover(baseUrl: string): Promise<void> {
  resetMock('cleanup_timeout_then_429')
  const output: string[] = []
  const virtual = createVirtualRuntime()
  const summary = await runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl, {
      [realGoClientIpAllowlistSmokeEnv.timeoutMs]: String(actualTimeoutMs)
    }),
    (message) => output.push(message),
    virtual.runtime
  )

  assert.deepEqual(summary, {
    firstDisabledCount: 1,
    secondDisabledCount: 0,
    cleanupAttemptCount: 4
  })
  assert.equal(cleanupRequestCount, 4)
  assert.equal(hasActiveAllowlist, false)
  assert.deepEqual(committedDisabledCounts, [1, 0, 0, 0, 0])
  assert(virtual.sleeps.includes(250), 'cleanup timeout must use bounded retry backoff')
  assert(virtual.sleeps.includes(2_000), 'cleanup 429 must honor Retry-After')
  assert.equal(new Set(requestRecords.map(requestReason)).size, requestRecords.length)
  assertRequestContract()
  assertNoSensitiveLeak(output, baseUrl, summary)
}

async function assertCountAnomaliesFail(baseUrl: string): Promise<void> {
  resetMock('first_unallowlist_polluted')
  const mainFailure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    createVirtualRuntime().runtime
  ))
  assert(mainFailure instanceof Error)
  assert.match(mainFailure.message, /External fixture pollution detected: first unallowlist disabledCount exceeded 1/)
  assert.equal(hasActiveAllowlist, false)
  assertNoSensitiveLeak(errorMessages(mainFailure), baseUrl)

  resetMock('normal_finally_count_one')
  const normalFinallyFailure = await captureFailure(runRealGoClientIpAllowlistSmokeFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    createVirtualRuntime().runtime
  ))
  assert(normalFinallyFailure instanceof Error)
  assert.match(normalFinallyFailure.message, /Normal finally cleanup must return disabledCount=0/)
  assert.deepEqual(committedDisabledCounts, [1, 0, 1, 0, 0])
  assert.equal(hasActiveAllowlist, false)
  assertNoSensitiveLeak(errorMessages(normalFinallyFailure), baseUrl)

  resetMock('cleanup_polluted')
  const cleanupPollutionFailure = await captureFailure(runRealGoClientIpAllowlistCleanupOnlyFromEnvironment(
    smokeEnvironment(baseUrl),
    () => undefined,
    createVirtualRuntime().runtime
  ))
  assert(cleanupPollutionFailure instanceof Error)
  assert.match(cleanupPollutionFailure.message, /External fixture pollution detected: cleanup disabledCount exceeded 1/)
  assert.deepEqual(committedDisabledCounts, [2, 0, 0])
  assert.equal(hasActiveAllowlist, false)
  assertNoSensitiveLeak(errorMessages(cleanupPollutionFailure), baseUrl)
}

async function assertCleanupOnly(baseUrl: string): Promise<void> {
  resetMock('success', true)
  const output: string[] = []
  const virtual = createVirtualRuntime()
  const summary = await runRealGoClientIpAllowlistCleanupOnlyFromEnvironment(
    smokeEnvironment(baseUrl),
    (message) => output.push(message),
    virtual.runtime
  )

  assert.deepEqual(summary, {
    cleanupAttemptCount: 3,
    zeroConfirmationCount: 2
  })
  assert.deepEqual(output, [
    disposableRegistryHashSafetyNotice,
    formatRealGoClientIpAllowlistCleanupSummary(summary)
  ])
  assert.equal(allowlistRequestCount, 0, 'cleanup-only must never call allowlist')
  assert.equal(hasActiveAllowlist, false)
  assert.deepEqual(committedDisabledCounts, [1, 0, 0])
  assert(requestRecords.every((record) => actionFromRecord(record) === 'unallowlist'))
  assert.equal(new Set(requestRecords.map(requestReason)).size, requestRecords.length)
  assert.deepEqual(virtual.sleeps, [250])
  assert(virtual.sleeps.every((delayMs) => delayMs < ambiguityHorizonMs))
  assertRequestContract()
  assertNoSensitiveLeak(output, baseUrl, summary)
}

function resetMock(nextScenario: MockScenario, activeAllowlist = false): void {
  scenario = nextScenario
  hasActiveAllowlist = activeAllowlist
  allowlistRequestCount = 0
  cleanupRequestCount = 0
  requestRecords.length = 0
  committedDisabledCounts.length = 0
  disabledCountObservations.length = 0
  virtualNowMs = virtualClockStartMs
  pendingAllowlistCommitAtMs = undefined
  delayedAllowlistCommittedAtMs = undefined
  allowlistHttpStatus = 503
  allowlistHttpSchedulesLateCommit = false
}

function smokeEnvironment(
  baseUrl: string,
  overrides: ClientIpAllowlistSmokeEnvironment = {}
): Record<string, string | undefined> {
  const configuredBaseUrl = overrides[realGoClientIpAllowlistSmokeEnv.baseUrl] ?? baseUrl
  const configuredHash = overrides[realGoClientIpAllowlistSmokeEnv.disposableIpHash]
    ?? mixedCaseDisposableIpHash
  const configuredLease = overrides[realGoClientIpAllowlistSmokeEnv.externalFixtureLeaseId]
    ?? externalFixtureLeaseId
  const configuredConfirmation = Object.hasOwn(
    overrides,
    realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation
  )
    ? overrides[realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation]
    : createRealGoClientIpAllowlistFixtureConfirmation(
        configuredBaseUrl,
        configuredHash,
        configuredLease
      )

  return {
    [realGoClientIpAllowlistSmokeEnv.baseUrl]: configuredBaseUrl,
    [realGoClientIpAllowlistSmokeEnv.managementCookie]: managementCookie,
    [realGoClientIpAllowlistSmokeEnv.disposableIpHash]: configuredHash,
    [realGoClientIpAllowlistSmokeEnv.externalFixtureLeaseId]: configuredLease,
    [realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation]: configuredConfirmation,
    [realGoClientIpAllowlistSmokeEnv.timeoutMs]: '2500',
    ...overrides
  }
}

function createVirtualRuntime(): VirtualRuntimeFixture {
  const sleeps: number[] = []
  return {
    runtime: {
      now: () => virtualNowMs,
      sleep: async (delayMs) => {
        sleeps.push(delayMs)
        virtualNowMs += delayMs
        applyPendingAllowlistCommitIfDue()
      },
      cleanupWindowMs: 130_000
    },
    sleeps
  }
}

async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const body = await readRequestBody(req)
  requestRecords.push({
    method: req.method,
    url: req.url,
    headers: { ...req.headers },
    body,
    atMs: virtualNowMs
  })

  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  const match = /^\/__aisys__\/api\/ip-stats\/([^/]+)\/(allowlist|unallowlist)$/.exec(url.pathname)
  res.setHeader('Content-Type', 'application/json; charset=utf-8')
  if (!(
    match?.[2] === 'allowlist'
    && (scenario === 'allowlist_missing_no_store' || scenario === 'allowlist_http_status')
  )) {
    res.setHeader('Cache-Control', 'no-store')
  }

  if (req.method !== 'POST' || !match?.[1] || !match[2]) {
    res.statusCode = 404
    res.end(JSON.stringify({ message: 'not found' }))
    return
  }

  const receivedIpHash = decodeURIComponent(match[1])
  const action = match[2]
  const reason = String(requestBody(body).reason ?? '')
  if (action === 'allowlist') {
    await handleAllowlistRequest(res, receivedIpHash, reason)
    return
  }
  await handleUnallowlistRequest(res, reason)
}

async function handleAllowlistRequest(
  res: ServerResponse,
  receivedIpHash: string,
  reason: string
): Promise<void> {
  allowlistRequestCount += 1
  if (scenario === 'allowlist_commit_then_timeout') {
    hasActiveAllowlist = false
    pendingAllowlistCommitAtMs = virtualNowMs + delayedAllowlistCommitAfterMs
  } else if (scenario === 'allowlist_http_status') {
    hasActiveAllowlist = false
    if (allowlistHttpSchedulesLateCommit) {
      pendingAllowlistCommitAtMs = virtualNowMs + delayedAllowlistCommitAfterMs
    }
  } else {
    hasActiveAllowlist = true
  }
  const policy: Record<string, unknown> = {
    id: sensitivePolicyId,
    ipHash: receivedIpHash,
    policyType: 'allowlist',
    status: 'active',
    reason,
    createdBySystemAccountId: 'sys_smoke_admin',
    createdAt: scenario === 'allowlist_non_utc_timestamp'
      ? '2026-07-14T06:30:00.123456789+00:00'
      : fixedTimestamp,
    updatedAt: fixedTimestamp
  }
  if (scenario === 'allowlist_extra_key' || scenario === 'main_and_cleanup_failure') {
    policy.unexpected = sensitiveErrorBody
  }
  if (scenario === 'allowlist_http_status') {
    res.statusCode = allowlistHttpStatus
    res.end(JSON.stringify({ message: sensitiveErrorBody }))
    return
  }
  if (scenario === 'allowlist_commit_then_timeout') {
    await delay(delayedResponseMs)
    if (res.destroyed) {
      return
    }
  }
  sendEnvelope(res, policy)
}

async function handleUnallowlistRequest(res: ServerResponse, reason: string): Promise<void> {
  applyPendingAllowlistCommitIfDue()
  const cleanupRequest = reason.includes(': cleanup attempt ')
  if (cleanupRequest) {
    cleanupRequestCount += 1
  }

  if (
    cleanupRequest
    && scenario === 'allowlist_commit_then_timeout'
    && cleanupRequestCount === 3
  ) {
    res.statusCode = 429
    res.setHeader('Retry-After', '29')
    res.end(JSON.stringify({ message: sensitiveErrorBody }))
    return
  }

  if (cleanupRequest && scenario === 'cleanup_timeout_then_429') {
    if (cleanupRequestCount === 1) {
      const disabledCount = disableActiveAllowlist()
      recordDisabledCount(disabledCount)
      await delay(delayedResponseMs)
      if (!res.destroyed) {
        sendEnvelope(res, { disabledCount })
      }
      return
    }
    if (cleanupRequestCount === 2) {
      res.statusCode = 429
      res.setHeader('Retry-After', '2')
      res.end(JSON.stringify({ message: sensitiveErrorBody }))
      return
    }
  }

  if (cleanupRequest && scenario === 'main_and_cleanup_failure') {
    res.statusCode = 429
    res.setHeader('Retry-After', '130')
    res.end(JSON.stringify({ message: sensitiveErrorBody }))
    return
  }

  let disabledCount: number
  if (scenario === 'first_unallowlist_polluted' && reason.includes(': first unallowlist')) {
    hasActiveAllowlist = false
    disabledCount = 2
  } else if (scenario === 'cleanup_polluted' && cleanupRequestCount === 1) {
    hasActiveAllowlist = false
    disabledCount = 2
  } else if (scenario === 'normal_finally_count_one' && cleanupRequestCount === 1) {
    hasActiveAllowlist = false
    disabledCount = 1
  } else {
    disabledCount = disableActiveAllowlist()
  }
  recordDisabledCount(disabledCount)
  sendEnvelope(res, { disabledCount })
}

function applyPendingAllowlistCommitIfDue(): void {
  if (pendingAllowlistCommitAtMs === undefined || virtualNowMs < pendingAllowlistCommitAtMs) {
    return
  }
  hasActiveAllowlist = true
  delayedAllowlistCommittedAtMs = virtualNowMs
  pendingAllowlistCommitAtMs = undefined
}

function recordDisabledCount(disabledCount: number): void {
  committedDisabledCounts.push(disabledCount)
  disabledCountObservations.push({ disabledCount, atMs: virtualNowMs })
}

function disableActiveAllowlist(): number {
  const disabledCount = hasActiveAllowlist ? 1 : 0
  hasActiveAllowlist = false
  return disabledCount
}

function assertRequestContract(): void {
  for (const record of requestRecords) {
    assert.equal(record.method, 'POST')
    assert.equal(record.headers.cookie, managementCookie)
    assert.equal(record.headers.accept, 'application/json')
    assert.equal(record.headers['content-type'], 'application/json')
    assert.equal(
      record.headers['user-agent'],
      'juhe-ai-plan0081-real-go-client-ip-allowlist-smoke/2.0'
    )
    assert.equal(record.headers['x-juhe-ai-smoke'], 'plan0081-real-go-client-ip-allowlist')
    assert.deepEqual(Object.keys(requestBody(record.body)), ['reason'])
    assert.match(requestReason(record), /^PLAN-0081 external fixture client IP allowlist smoke [0-9a-f-]+: /)
    const url = new URL(record.url ?? '/', 'http://127.0.0.1')
    assert.equal(url.search, '')
    assert(url.pathname.includes(`/ip-stats/${encodeURIComponent(lowercaseDisposableIpHash)}/`))
    assert.equal(url.pathname.includes(mixedCaseDisposableIpHash), false)
  }
}

function assertNoSensitiveLeak(values: unknown[], baseUrl: string, extraValue?: unknown): void {
  const messages = [...values.map(String), ...(extraValue === undefined ? [] : [JSON.stringify(extraValue)])]
  const confirmation = createRealGoClientIpAllowlistFixtureConfirmation(
    baseUrl,
    mixedCaseDisposableIpHash,
    externalFixtureLeaseId
  )
  const requestBodies = requestRecords.map((record) => JSON.stringify(record.body))
  const forbiddenValues = [
    managementCookie,
    baseUrl,
    canonicalizeRealGoClientIpAllowlistApiBaseUrl(baseUrl),
    mixedCaseDisposableIpHash,
    lowercaseDisposableIpHash,
    externalFixtureLeaseId,
    confirmation,
    sensitivePolicyId,
    sensitiveErrorBody,
    ...requestBodies
  ]
  for (const forbiddenValue of forbiddenValues) {
    assert.equal(
      messages.some((message) => message.includes(forbiddenValue)),
      false,
      'errors and output must not expose fixture configuration, policy IDs, or bodies'
    )
  }
}

function expectedRequestSignature(action: 'allowlist' | 'unallowlist'): string {
  return `POST /__aisys__/api/ip-stats/${encodeURIComponent(lowercaseDisposableIpHash)}/${action}`
}

function requestSignature(record: RequestRecord): string {
  return `${record.method} ${record.url}`
}

function actionFromRecord(record: RequestRecord): 'allowlist' | 'unallowlist' {
  const pathname = new URL(record.url ?? '/', 'http://127.0.0.1').pathname
  if (pathname.endsWith('/allowlist')) {
    return 'allowlist'
  }
  assert(pathname.endsWith('/unallowlist'))
  return 'unallowlist'
}

function requestReason(record: RequestRecord): string {
  const reason = requestBody(record.body).reason
  assert(typeof reason === 'string')
  return reason
}

function requestBody(value: unknown): Record<string, unknown> {
  assert(value && typeof value === 'object' && !Array.isArray(value))
  return value as Record<string, unknown>
}

function sendEnvelope(res: ServerResponse, data: unknown): void {
  res.end(JSON.stringify({ data }))
}

async function readRequestBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  assert(chunks.length > 0, 'mutation request body must not be empty')
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as unknown
}

function errorMessages(error: unknown): string[] {
  if (error instanceof AggregateError) {
    return [error.message, ...error.errors.flatMap(errorMessages)]
  }
  return [error instanceof Error ? error.message : String(error)]
}

async function captureFailure(operation: Promise<unknown>): Promise<unknown> {
  try {
    await operation
  } catch (error) {
    return error
  }
  assert.fail('expected operation to fail')
}

function delay(delayMs: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, delayMs))
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
