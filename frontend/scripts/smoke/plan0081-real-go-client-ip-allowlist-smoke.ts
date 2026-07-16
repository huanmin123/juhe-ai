import { createHash, randomUUID } from 'node:crypto'
import { isIP } from 'node:net'
import { pathToFileURL } from 'node:url'

export const realGoClientIpAllowlistSmokeEnv = {
  baseUrl: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_BASE_URL',
  managementCookie: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_MANAGEMENT_COOKIE',
  disposableIpHash: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_DISPOSABLE_IP_HASH',
  externalFixtureLeaseId: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_EXTERNAL_FIXTURE_LEASE_ID',
  externalFixtureConfirmation: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_EXTERNAL_FIXTURE_CONFIRMATION',
  timeoutMs: 'JUHE_REAL_GO_CLIENT_IP_ALLOWLIST_TIMEOUT_MS'
} as const

export const disposableRegistryHashSafetyNotice =
  'EXTERNAL FIXTURE ONLY: this script cannot verify isolation. Run real smoke or cleanup-only only against a dedicated rebuildable external fixture lease; allowlist disables existing active policies for the bound lowercase registry hash.'

const managementApiPrefix = '/__aisys__/api'
const smokeUserAgent = 'juhe-ai-plan0081-real-go-client-ip-allowlist-smoke/2.0'
const smokeHeaderValue = 'plan0081-real-go-client-ip-allowlist'
const fixtureConfirmationVersion = 'plan0081-external-client-ip-allowlist-fixture-v1'
const fixtureConfirmationPrefix = 'plan0081-external-fixture-v1:'
const defaultTimeoutMs = 15_000
const minimumCleanupWindowMs = 130_000
const defaultCleanupWindowMs = 135_000
const allowlistMutationProcessingMaximumMs = 120_000
const allowlistCommitAmbiguityHorizonMs = allowlistMutationProcessingMaximumMs + 10_000
const minimumPostAmbiguityCleanupMs = 30_000
const maximumTimerMs = 2_147_483_647
const maximumCleanupAttempts = 1024
const cleanupRetryBaseDelayMs = 250
const cleanupRetryMaximumDelayMs = 10_000

const allowlistPolicyKeys = [
  'createdAt',
  'createdBySystemAccountId',
  'id',
  'ipHash',
  'policyType',
  'reason',
  'status',
  'updatedAt'
] as const

export type ClientIpAllowlistSmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoClientIpAllowlistSmokeConfig {
  baseUrl: string
  managementCookie: string
  disposableIpHash: string
  externalFixtureLeaseId: string
  externalFixtureConfirmation: string
  timeoutMs?: number
}

export interface RealGoClientIpAllowlistSmokeRuntime {
  now?: () => number
  sleep?: (delayMs: number) => Promise<void>
  cleanupWindowMs?: number
}

export interface RealGoClientIpAllowlistSmokeSummary {
  firstDisabledCount: number
  secondDisabledCount: number
  cleanupAttemptCount: number
}

export interface RealGoClientIpAllowlistCleanupSummary {
  cleanupAttemptCount: number
  zeroConfirmationCount: number
}

interface NormalizedRealGoClientIpAllowlistSmokeConfig extends RealGoClientIpAllowlistSmokeConfig {
  timeoutMs: number
}

interface ResolvedRealGoClientIpAllowlistSmokeRuntime {
  now: () => number
  sleep: (delayMs: number) => Promise<void>
  cleanupWindowMs: number
}

interface AllowlistPolicySummary {
  id: string
  ipHash: string
  policyType: 'allowlist'
  status: 'active'
  reason: string
  createdBySystemAccountId: string
  createdAt: string
  updatedAt: string
}

interface CleanupAttemptCountResult {
  kind: 'count'
  disabledCount: number
}

interface CleanupAttemptRetryResult {
  kind: 'retry'
  retryAfterMs?: number
}

type CleanupAttemptResult = CleanupAttemptCountResult | CleanupAttemptRetryResult
type ClientIpPolicyAction = 'allowlist' | 'unallowlist'

interface MainMutationReasons {
  allowlist: string
  firstUnallowlist: string
  secondUnallowlist: string
}

interface CleanupVerificationOptions {
  requireFirstObservedZero: boolean
  allowlistCommitUncertainSinceMs?: number
}

class SanitizedTransportError extends Error {
  readonly code = 'CLIENT_IP_ALLOWLIST_SMOKE_TRANSPORT_ERROR'

  constructor(label: string, transportKind: string) {
    super(`${label} request failed: ${transportKind}`)
    this.name = 'SanitizedTransportError'
  }
}
class SanitizedHttpStatusError extends Error {
  readonly code = 'CLIENT_IP_ALLOWLIST_SMOKE_HTTP_STATUS_ERROR'

  constructor(label: string, readonly status: number) {
    super(`${label} failed with HTTP ${status}`)
    this.name = 'SanitizedHttpStatusError'
  }
}
class CleanupProtocolError extends Error {}
class FixturePollutionError extends Error {}

export function canonicalizeRealGoClientIpAllowlistApiBaseUrl(rawValue: string): string {
  return normalizeManagementApiBaseUrl(rawValue)
}

export function createRealGoClientIpAllowlistFixtureConfirmation(
  baseUrl: string,
  disposableIpHash: string,
  externalFixtureLeaseId: string
): string {
  const canonicalFullApiBaseUrl = normalizeManagementApiBaseUrl(baseUrl)
  const lowercaseDisposableIpHash = normalizeDisposableIpHash(disposableIpHash)
  const normalizedLeaseId = normalizeExternalFixtureLeaseId(externalFixtureLeaseId)
  return buildFixtureConfirmation(canonicalFullApiBaseUrl, lowercaseDisposableIpHash, normalizedLeaseId)
}

export function loadRealGoClientIpAllowlistSmokeConfig(
  env: ClientIpAllowlistSmokeEnvironment = process.env
): RealGoClientIpAllowlistSmokeConfig {
  const rawBaseUrl = requiredEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.baseUrl)
  const rawManagementCookie = requiredEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.managementCookie)
  const rawDisposableIpHash = requiredEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.disposableIpHash)
  const rawLeaseId = requiredEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.externalFixtureLeaseId)
  const rawConfirmation = requiredEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation)

  expect(
    !/[\r\n]/.test(rawManagementCookie),
    `${realGoClientIpAllowlistSmokeEnv.managementCookie} must be a single Cookie header line`
  )

  const baseUrl = normalizeManagementApiBaseUrl(rawBaseUrl)
  const disposableIpHash = normalizeDisposableIpHash(rawDisposableIpHash)
  const externalFixtureLeaseId = normalizeExternalFixtureLeaseId(rawLeaseId)
  const externalFixtureConfirmation = rawConfirmation.trim()
  expect(
    externalFixtureConfirmation === buildFixtureConfirmation(baseUrl, disposableIpHash, externalFixtureLeaseId),
    `${realGoClientIpAllowlistSmokeEnv.externalFixtureConfirmation} does not match the canonical API base URL, lowercase disposable hash, and external fixture lease`
  )

  return {
    baseUrl,
    managementCookie: rawManagementCookie.trim(),
    disposableIpHash,
    externalFixtureLeaseId,
    externalFixtureConfirmation,
    timeoutMs: optionalPositiveIntegerEnvironmentValue(env, realGoClientIpAllowlistSmokeEnv.timeoutMs)
  }
}

export async function runRealGoClientIpAllowlistSmoke(
  config: RealGoClientIpAllowlistSmokeConfig,
  runtime: RealGoClientIpAllowlistSmokeRuntime = {}
): Promise<RealGoClientIpAllowlistSmokeSummary> {
  const normalizedConfig = normalizeConfig(config)
  const resolvedRuntime = normalizeRuntime(runtime)
  const reasons = createMainMutationReasons()
  let allowlistMutationAttempted = false
  let allowlistAttemptStartedAtMs: number | undefined
  let allowlistCommitUncertain = false
  let mainFlowCompleted = false
  let primaryError: unknown
  let cleanupError: unknown
  let firstDisabledCount: number | undefined
  let secondDisabledCount: number | undefined
  let cleanupAttemptCount: number | undefined

  try {
    try {
      allowlistMutationAttempted = true
      allowlistAttemptStartedAtMs = resolvedRuntime.now()
      let allowlistData: unknown
      try {
        allowlistData = await postPolicyMutation(
          normalizedConfig,
          'allowlist',
          reasons.allowlist,
          'allowlist'
        )
      } catch (error) {
        if (
          error instanceof SanitizedTransportError
          || isAllowlistCommitUncertainHttpError(error)
        ) {
          allowlistCommitUncertain = true
        }
        throw error
      }
      assertAllowlistPolicySummary(
        allowlistData,
        normalizedConfig.disposableIpHash,
        reasons.allowlist
      )

      const firstUnallowlistData = await postPolicyMutation(
        normalizedConfig,
        'unallowlist',
        reasons.firstUnallowlist,
        'first unallowlist'
      )
      firstDisabledCount = assertMainDisabledCount(firstUnallowlistData, 'first unallowlist', 1)

      const secondUnallowlistData = await postPolicyMutation(
        normalizedConfig,
        'unallowlist',
        reasons.secondUnallowlist,
        'second unallowlist'
      )
      secondDisabledCount = assertMainDisabledCount(secondUnallowlistData, 'second unallowlist', 0)
      mainFlowCompleted = true
    } catch (error) {
      primaryError = error
    }
  } finally {
    if (allowlistMutationAttempted) {
      try {
        const cleanup = await verifyCleanup(
          normalizedConfig,
          resolvedRuntime,
          {
            requireFirstObservedZero: mainFlowCompleted,
            allowlistCommitUncertainSinceMs: allowlistCommitUncertain
              ? allowlistAttemptStartedAtMs
              : undefined
          }
        )
        cleanupAttemptCount = cleanup.cleanupAttemptCount
      } catch (error) {
        cleanupError = error
      }
    }
  }

  throwSmokeErrors(primaryError, cleanupError)
  expect(firstDisabledCount === 1, 'Client IP allowlist smoke first disabled count was not produced')
  expect(secondDisabledCount === 0, 'Client IP allowlist smoke second disabled count was not produced')
  expect(cleanupAttemptCount !== undefined, 'Client IP allowlist smoke cleanup summary was not produced')
  return { firstDisabledCount, secondDisabledCount, cleanupAttemptCount }
}

export async function runRealGoClientIpAllowlistCleanupOnly(
  config: RealGoClientIpAllowlistSmokeConfig,
  runtime: RealGoClientIpAllowlistSmokeRuntime = {}
): Promise<RealGoClientIpAllowlistCleanupSummary> {
  return verifyCleanup(normalizeConfig(config), normalizeRuntime(runtime), {
    requireFirstObservedZero: false,
    allowlistCommitUncertainSinceMs: undefined
  })
}

export async function runRealGoClientIpAllowlistSmokeFromEnvironment(
  env: ClientIpAllowlistSmokeEnvironment = process.env,
  writeOutput: (message: string) => void = console.log,
  runtime: RealGoClientIpAllowlistSmokeRuntime = {}
): Promise<RealGoClientIpAllowlistSmokeSummary> {
  const config = loadRealGoClientIpAllowlistSmokeConfig(env)
  writeOutput(disposableRegistryHashSafetyNotice)
  const summary = await runRealGoClientIpAllowlistSmoke(config, runtime)
  writeOutput(formatRealGoClientIpAllowlistSmokeSummary(summary))
  return summary
}

export async function runRealGoClientIpAllowlistCleanupOnlyFromEnvironment(
  env: ClientIpAllowlistSmokeEnvironment = process.env,
  writeOutput: (message: string) => void = console.log,
  runtime: RealGoClientIpAllowlistSmokeRuntime = {}
): Promise<RealGoClientIpAllowlistCleanupSummary> {
  const config = loadRealGoClientIpAllowlistSmokeConfig(env)
  writeOutput(disposableRegistryHashSafetyNotice)
  const summary = await runRealGoClientIpAllowlistCleanupOnly(config, runtime)
  writeOutput(formatRealGoClientIpAllowlistCleanupSummary(summary))
  return summary
}

export function formatRealGoClientIpAllowlistSmokeSummary(
  summary: RealGoClientIpAllowlistSmokeSummary
): string {
  return [
    'PLAN-0081 real Go client IP allowlist smoke passed',
    `firstDisabledCount=${summary.firstDisabledCount}`,
    `secondDisabledCount=${summary.secondDisabledCount}`,
    `cleanupAttempts=${summary.cleanupAttemptCount}`
  ].join(' ')
}

export function formatRealGoClientIpAllowlistCleanupSummary(
  summary: RealGoClientIpAllowlistCleanupSummary
): string {
  return [
    'PLAN-0081 real Go client IP allowlist cleanup-only passed',
    `cleanupAttempts=${summary.cleanupAttemptCount}`,
    `zeroConfirmations=${summary.zeroConfirmationCount}`
  ].join(' ')
}

async function verifyCleanup(
  config: NormalizedRealGoClientIpAllowlistSmokeConfig,
  runtime: ResolvedRealGoClientIpAllowlistSmokeRuntime,
  options: CleanupVerificationOptions
): Promise<RealGoClientIpAllowlistCleanupSummary> {
  const cleanupStartedAtMs = runtime.now()
  const ambiguityHorizonAtMs = options.allowlistCommitUncertainSinceMs === undefined
    ? undefined
    : options.allowlistCommitUncertainSinceMs + allowlistCommitAmbiguityHorizonMs
  const deadlineMs = Math.max(
    cleanupStartedAtMs + runtime.cleanupWindowMs,
    ambiguityHorizonAtMs === undefined
      ? 0
      : ambiguityHorizonAtMs + minimumPostAmbiguityCleanupMs
  )
  const cleanupRunId = randomUUID()
  let attemptCount = 0
  let consecutiveZeroCount = 0
  let firstObservedCount = true
  let persistentError: Error | undefined
  let postAmbiguityHorizonReset = ambiguityHorizonAtMs === undefined

  while (attemptCount < maximumCleanupAttempts && runtime.now() < deadlineMs) {
    if (
      ambiguityHorizonAtMs !== undefined
      && !postAmbiguityHorizonReset
      && runtime.now() >= ambiguityHorizonAtMs
    ) {
      consecutiveZeroCount = 0
      postAmbiguityHorizonReset = true
    }
    attemptCount += 1
    const remainingWindowMs = Math.max(1, Math.floor(deadlineMs - runtime.now()))
    const requestTimeoutMs = Math.min(config.timeoutMs, remainingWindowMs)
    const reason = createCleanupReason(cleanupRunId, attemptCount)

    let result: CleanupAttemptResult
    try {
      result = await attemptCleanupUnallowlist(
        config,
        reason,
        requestTimeoutMs,
        runtime.now()
      )
    } catch (error) {
      consecutiveZeroCount = 0
      if (error instanceof CleanupProtocolError) {
        persistentError = rememberPersistentCleanupError(persistentError, error)
      }
      if (!await waitForCleanupRetry(runtime, deadlineMs, cleanupRetryDelayMs(attemptCount))) {
        break
      }
      continue
    }

    if (result.kind === 'retry') {
      consecutiveZeroCount = 0
      const retryDelayMs = result.retryAfterMs ?? cleanupRetryDelayMs(attemptCount)
      if (!await waitForCleanupRetry(runtime, deadlineMs, retryDelayMs)) {
        break
      }
      continue
    }

    const disabledCount = result.disabledCount
    if (
      ambiguityHorizonAtMs !== undefined
      && !postAmbiguityHorizonReset
      && runtime.now() >= ambiguityHorizonAtMs
    ) {
      consecutiveZeroCount = 0
      postAmbiguityHorizonReset = true
    }
    if (firstObservedCount) {
      firstObservedCount = false
      if (options.requireFirstObservedZero && disabledCount !== 0) {
        persistentError = rememberPersistentCleanupError(
          persistentError,
          new CleanupProtocolError('Normal finally cleanup must return disabledCount=0')
        )
      }
    }
    if (disabledCount > 1) {
      persistentError = rememberPersistentCleanupError(
        persistentError,
        new FixturePollutionError('External fixture pollution detected: cleanup disabledCount exceeded 1')
      )
    }

    consecutiveZeroCount = disabledCount === 0 ? consecutiveZeroCount + 1 : 0
    if (consecutiveZeroCount >= 2) {
      if (!postAmbiguityHorizonReset && ambiguityHorizonAtMs !== undefined) {
        consecutiveZeroCount = 0
        if (!await waitForCleanupRetry(
          runtime,
          deadlineMs,
          Math.max(1, ambiguityHorizonAtMs - runtime.now())
        )) {
          break
        }
        continue
      }
      if (persistentError) {
        throw persistentError
      }
      return {
        cleanupAttemptCount: attemptCount,
        zeroConfirmationCount: 2
      }
    }

    if (disabledCount !== 0 && !await waitForCleanupRetry(runtime, deadlineMs, cleanupRetryBaseDelayMs)) {
      break
    }
  }

  const verificationError = new Error(
    'Cleanup could not confirm two consecutive disabledCount=0 responses within the bounded window'
  )
  if (persistentError) {
    throw new AggregateError(
      [persistentError, verificationError],
      `${persistentError.message}; ${verificationError.message}`
    )
  }
  throw verificationError
}

async function attemptCleanupUnallowlist(
  config: NormalizedRealGoClientIpAllowlistSmokeConfig,
  reason: string,
  timeoutMs: number,
  nowMs: number
): Promise<CleanupAttemptResult> {
  const response = await sendRequest(
    policyEndpointUrl(config, 'unallowlist'),
    config,
    { reason },
    'cleanup unallowlist',
    timeoutMs
  )

  if (response.headers.get('cache-control') !== 'no-store') {
    await discardResponseBody(response)
    throw new CleanupProtocolError('Cleanup response must return Cache-Control: no-store')
  }
  if (response.status === 429) {
    const retryAfterMs = parseRetryAfterMs(response.headers.get('retry-after'), nowMs)
    await discardResponseBody(response)
    return { kind: 'retry', retryAfterMs }
  }
  if (response.status === 408 || response.status === 409 || response.status >= 500) {
    await discardResponseBody(response)
    return { kind: 'retry' }
  }
  if (response.status !== 200) {
    await discardResponseBody(response)
    throw new CleanupProtocolError(`Cleanup unallowlist failed with HTTP ${response.status}`)
  }

  try {
    const data = await parseEnvelopeData(response, 'cleanup unallowlist')
    return { kind: 'count', disabledCount: assertDisabledCount(data, 'cleanup unallowlist') }
  } catch {
    throw new CleanupProtocolError('Cleanup response DTO was invalid')
  }
}

function normalizeConfig(
  config: RealGoClientIpAllowlistSmokeConfig
): NormalizedRealGoClientIpAllowlistSmokeConfig {
  expect(
    typeof config.managementCookie === 'string' && config.managementCookie.trim().length > 0,
    'Management Cookie header must not be empty'
  )
  expect(!/[\r\n]/.test(config.managementCookie), 'Management Cookie header must be a single line')

  const baseUrl = normalizeManagementApiBaseUrl(config.baseUrl)
  const disposableIpHash = normalizeDisposableIpHash(config.disposableIpHash)
  const externalFixtureLeaseId = normalizeExternalFixtureLeaseId(config.externalFixtureLeaseId)
  expect(
    typeof config.externalFixtureConfirmation === 'string'
      && config.externalFixtureConfirmation.trim()
        === buildFixtureConfirmation(baseUrl, disposableIpHash, externalFixtureLeaseId),
    'External fixture confirmation does not match the canonical API base URL, lowercase disposable hash, and lease'
  )

  const timeoutMs = config.timeoutMs ?? defaultTimeoutMs
  expect(
    Number.isSafeInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= maximumTimerMs,
    `Client IP allowlist smoke timeout must be a positive integer no greater than ${maximumTimerMs}`
  )

  return {
    baseUrl,
    managementCookie: config.managementCookie.trim(),
    disposableIpHash,
    externalFixtureLeaseId,
    externalFixtureConfirmation: config.externalFixtureConfirmation.trim(),
    timeoutMs
  }
}

function normalizeRuntime(
  runtime: RealGoClientIpAllowlistSmokeRuntime
): ResolvedRealGoClientIpAllowlistSmokeRuntime {
  const now = runtime.now ?? Date.now
  const sleep = runtime.sleep ?? defaultSleep
  const cleanupWindowMs = runtime.cleanupWindowMs ?? defaultCleanupWindowMs
  expect(typeof now === 'function', 'Smoke clock must be a function')
  expect(typeof sleep === 'function', 'Smoke sleep must be a function')
  expect(
    Number.isSafeInteger(cleanupWindowMs)
      && cleanupWindowMs >= minimumCleanupWindowMs
      && cleanupWindowMs <= maximumTimerMs,
    `Cleanup window must be an integer from ${minimumCleanupWindowMs} to ${maximumTimerMs} milliseconds`
  )
  expect(Number.isFinite(now()), 'Smoke clock must return a finite timestamp')
  return { now, sleep, cleanupWindowMs }
}

function buildFixtureConfirmation(
  canonicalFullApiBaseUrl: string,
  lowercaseDisposableIpHash: string,
  externalFixtureLeaseId: string
): string {
  const digest = createHash('sha256')
    .update([
      fixtureConfirmationVersion,
      canonicalFullApiBaseUrl,
      lowercaseDisposableIpHash,
      externalFixtureLeaseId
    ].join('\n'))
    .digest('hex')
  return `${fixtureConfirmationPrefix}${digest}`
}

function createMainMutationReasons(): MainMutationReasons {
  const prefix = `PLAN-0081 external fixture client IP allowlist smoke ${randomUUID()}`
  return {
    allowlist: `${prefix}: allowlist`,
    firstUnallowlist: `${prefix}: first unallowlist`,
    secondUnallowlist: `${prefix}: second unallowlist idempotence check`
  }
}

function createCleanupReason(cleanupRunId: string, attemptCount: number): string {
  return `PLAN-0081 external fixture client IP allowlist smoke ${cleanupRunId}: cleanup attempt ${attemptCount} ${randomUUID()}`
}

async function postPolicyMutation(
  config: NormalizedRealGoClientIpAllowlistSmokeConfig,
  action: ClientIpPolicyAction,
  reason: string,
  label: string
): Promise<unknown> {
  const response = await sendRequest(
    policyEndpointUrl(config, action),
    config,
    { reason },
    label,
    config.timeoutMs
  )
  if (action === 'allowlist' && response.status !== 200) {
    const status = response.status
    await discardResponseBody(response)
    throw new SanitizedHttpStatusError(label, status)
  }
  if (response.headers.get('cache-control') !== 'no-store') {
    await discardResponseBody(response)
    throw new Error(`${label} must return Cache-Control: no-store`)
  }
  if (response.status !== 200) {
    const status = response.status
    await discardResponseBody(response)
    throw new SanitizedHttpStatusError(label, status)
  }
  return parseEnvelopeData(response, label)
}

function policyEndpointUrl(
  config: NormalizedRealGoClientIpAllowlistSmokeConfig,
  action: ClientIpPolicyAction
): URL {
  return new URL(
    `${config.baseUrl}/ip-stats/${encodeURIComponent(config.disposableIpHash)}/${action}`
  )
}

async function sendRequest(
  url: URL,
  config: NormalizedRealGoClientIpAllowlistSmokeConfig,
  body: { reason: string },
  label: string,
  timeoutMs: number
): Promise<Response> {
  try {
    return await fetch(url, {
      method: 'POST',
      headers: {
        accept: 'application/json',
        cookie: config.managementCookie,
        'content-type': 'application/json',
        'user-agent': smokeUserAgent,
        'x-juhe-ai-smoke': smokeHeaderValue
      },
      body: JSON.stringify(body),
      redirect: 'error',
      signal: AbortSignal.timeout(timeoutMs)
    })
  } catch (error) {
    throw new SanitizedTransportError(label, sanitizedTransportKind(error))
  }
}

function sanitizedTransportKind(error: unknown): string {
  if (
    error instanceof Error
    && (error.name === 'TimeoutError' || error.name === 'AbortError' || error.name === 'TypeError')
  ) {
    return error.name
  }
  return 'TransportError'
}

function isAllowlistCommitUncertainHttpError(error: unknown): boolean {
  return error instanceof SanitizedHttpStatusError
    && (error.status === 408 || (error.status >= 500 && error.status <= 599))
}

async function parseEnvelopeData(response: Response, label: string): Promise<unknown> {
  let payload: unknown
  try {
    payload = await response.json()
  } catch {
    throw new Error(`${label} returned invalid JSON`)
  }
  expect(isRecord(payload), `${label} envelope must be an object`)
  expect(
    Object.keys(payload).length === 1 && Object.hasOwn(payload, 'data'),
    `${label} envelope must contain only the data field`
  )
  return payload.data
}

function assertAllowlistPolicySummary(
  value: unknown,
  expectedDisposableIpHash: string,
  expectedReason: string
): AllowlistPolicySummary {
  expect(isRecord(value), 'allowlist data must be an object')
  expect(
    hasExactKeys(value, allowlistPolicyKeys),
    'allowlist data must contain exactly the complete active allowlist DTO key set'
  )
  expect(isNonEmptyString(value.id), 'allowlist data.id must be a non-empty string')
  expect(value.ipHash === expectedDisposableIpHash, 'allowlist data.ipHash must match the lowercase disposable hash')
  expect(value.policyType === 'allowlist', 'allowlist data.policyType must be allowlist')
  expect(value.status === 'active', 'allowlist data.status must be active')
  expect(value.reason === expectedReason, 'allowlist data.reason must match the mutation reason')
  expect(
    isNonEmptyString(value.createdBySystemAccountId),
    'allowlist data.createdBySystemAccountId must be a non-empty string'
  )
  expect(isStrictUtcRfc3339(value.createdAt), 'allowlist data.createdAt must be strict UTC RFC3339')
  expect(isStrictUtcRfc3339(value.updatedAt), 'allowlist data.updatedAt must be strict UTC RFC3339')
  return value as unknown as AllowlistPolicySummary
}

function assertMainDisabledCount(value: unknown, label: string, expectedCount: 0 | 1): number {
  const disabledCount = assertDisabledCount(value, label)
  if (disabledCount > 1) {
    throw new FixturePollutionError(`External fixture pollution detected: ${label} disabledCount exceeded 1`)
  }
  expect(disabledCount === expectedCount, `${label} data.disabledCount must be exactly ${expectedCount}`)
  return disabledCount
}

function assertDisabledCount(value: unknown, label: string): number {
  expect(isRecord(value), `${label} data must be an object`)
  expect(
    Object.keys(value).length === 1 && Object.hasOwn(value, 'disabledCount'),
    `${label} data must contain only disabledCount`
  )
  expect(
    Number.isSafeInteger(value.disabledCount) && Number(value.disabledCount) >= 0,
    `${label} data.disabledCount must be a non-negative integer`
  )
  return Number(value.disabledCount)
}

function normalizeManagementApiBaseUrl(rawValue: string): string {
  expect(typeof rawValue === 'string' && rawValue.trim().length > 0, 'Management Base URL must not be empty')
  expect(!/[\r\n]/.test(rawValue), 'Management Base URL must be a single line')

  const trimmedValue = rawValue.trim()
  let url: URL
  try {
    url = new URL(trimmedValue)
  } catch {
    throw new Error(`${realGoClientIpAllowlistSmokeEnv.baseUrl} must be an absolute HTTP(S) URL`)
  }
  expect(
    url.protocol === 'http:' || url.protocol === 'https:',
    `${realGoClientIpAllowlistSmokeEnv.baseUrl} must use HTTP or HTTPS`
  )
  expect(
    !url.username && !url.password,
    `${realGoClientIpAllowlistSmokeEnv.baseUrl} must not contain credentials`
  )
  expect(
    !/[?#]/.test(trimmedValue) && !url.search && !url.hash,
    `${realGoClientIpAllowlistSmokeEnv.baseUrl} must not contain query or fragment`
  )
  expect(
    url.protocol === 'https:' || isLoopbackHostname(url.hostname),
    `${realGoClientIpAllowlistSmokeEnv.baseUrl} must use HTTPS unless the host is loopback`
  )

  const pathname = url.pathname.replace(/\/+$/, '')
  url.pathname = pathname.endsWith(managementApiPrefix)
    ? pathname
    : `${pathname}${managementApiPrefix}`.replace(/\/{2,}/g, '/')
  return url.toString().replace(/\/+$/, '')
}

function isLoopbackHostname(rawHostname: string): boolean {
  const hostname = rawHostname.toLowerCase().replace(/^\[|\]$/g, '')
  if (hostname === 'localhost' || hostname === '::1') {
    return true
  }
  return isIP(hostname) === 4 && hostname.split('.')[0] === '127'
}

function normalizeDisposableIpHash(rawValue: string): string {
  expect(typeof rawValue === 'string', 'Disposable client IP hash must be a string')
  const value = rawValue.trim()
  expect(/^[0-9a-fA-F]{64}$/.test(value), 'Disposable client IP hash must be exactly 64 hexadecimal characters')
  return value.toLowerCase()
}

function normalizeExternalFixtureLeaseId(rawValue: string): string {
  expect(typeof rawValue === 'string', 'External fixture lease ID must be a string')
  const value = rawValue.trim()
  expect(
    /^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$/.test(value),
    'External fixture lease ID must be 8-128 safe identifier characters'
  )
  return value
}

function requiredEnvironmentValue(env: ClientIpAllowlistSmokeEnvironment, name: string): string {
  const value = env[name]
  if (typeof value !== 'string' || value.trim().length === 0) {
    throw new Error(`Missing required environment variable: ${name}`)
  }
  return value
}

function optionalPositiveIntegerEnvironmentValue(
  env: ClientIpAllowlistSmokeEnvironment,
  name: string
): number | undefined {
  const rawValue = env[name]
  if (rawValue === undefined || rawValue.trim() === '') {
    return undefined
  }
  const value = rawValue.trim()
  expect(/^[1-9]\d*$/.test(value), `${name} must be a positive integer`)
  const parsed = Number(value)
  expect(Number.isSafeInteger(parsed) && parsed <= maximumTimerMs, `${name} must not exceed ${maximumTimerMs}`)
  return parsed
}

function parseRetryAfterMs(rawValue: string | null, nowMs: number): number | undefined {
  if (!rawValue) {
    return undefined
  }
  const value = rawValue.trim()
  if (/^\d+$/.test(value)) {
    const seconds = Number(value)
    return Number.isSafeInteger(seconds) && seconds > 0
      ? Math.min(seconds * 1000, maximumTimerMs)
      : undefined
  }
  const retryAtMs = Date.parse(value)
  if (!Number.isFinite(retryAtMs) || retryAtMs <= nowMs) {
    return undefined
  }
  return Math.min(Math.ceil(retryAtMs - nowMs), maximumTimerMs)
}

function cleanupRetryDelayMs(attemptCount: number): number {
  return Math.min(
    cleanupRetryBaseDelayMs * (2 ** Math.min(attemptCount - 1, 10)),
    cleanupRetryMaximumDelayMs
  )
}

async function waitForCleanupRetry(
  runtime: ResolvedRealGoClientIpAllowlistSmokeRuntime,
  deadlineMs: number,
  requestedDelayMs: number
): Promise<boolean> {
  const remainingMs = Math.floor(deadlineMs - runtime.now())
  if (remainingMs <= 0) {
    return false
  }
  const delayMs = Math.max(1, Math.min(Math.ceil(requestedDelayMs), remainingMs))
  try {
    await runtime.sleep(delayMs)
  } catch {
    throw new Error('Cleanup retry sleep failed')
  }
  return runtime.now() < deadlineMs
}

function rememberPersistentCleanupError(current: Error | undefined, next: Error): Error {
  if (!current || next instanceof FixturePollutionError) {
    return next
  }
  return current
}

function hasExactKeys(value: Record<string, unknown>, expectedKeys: readonly string[]): boolean {
  const actualKeys = Object.keys(value).sort()
  return actualKeys.length === expectedKeys.length
    && actualKeys.every((key, index) => key === expectedKeys[index])
}

function isStrictUtcRfc3339(value: unknown): value is string {
  if (typeof value !== 'string') {
    return false
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value)
  if (!match) {
    return false
  }
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number)
  if (
    year < 1
    || month < 1 || month > 12
    || hour > 23
    || minute > 59
    || second > 59
  ) {
    return false
  }
  const maximumDay = new Date(Date.UTC(year, month, 0)).getUTCDate()
  return day >= 1 && day <= maximumDay && Number.isFinite(Date.parse(value))
}

async function discardResponseBody(response: Response): Promise<void> {
  try {
    await response.body?.cancel()
  } catch {
    // Response disposal is best effort; request errors remain sanitized.
  }
}

function defaultSleep(delayMs: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, delayMs))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function expect(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function throwSmokeErrors(primaryError: unknown, cleanupError: unknown): void {
  if (primaryError && cleanupError) {
    const primary = safeError(primaryError, 'Client IP allowlist smoke failed')
    const cleanup = safeError(cleanupError, 'Client IP allowlist cleanup failed')
    throw new AggregateError(
      [primary, cleanup],
      `${primary.message}; cleanup failed: ${cleanup.message}`
    )
  }
  if (primaryError) {
    throw safeError(primaryError, 'Client IP allowlist smoke failed')
  }
  if (cleanupError) {
    throw safeError(cleanupError, 'Client IP allowlist cleanup failed')
  }
}

function safeError(error: unknown, fallbackMessage: string): Error {
  return error instanceof Error ? error : new Error(fallbackMessage)
}

function isMainModule(): boolean {
  const entryPath = process.argv[1]
  return Boolean(entryPath && import.meta.url === pathToFileURL(entryPath).href)
}

async function runCommandLine(args: readonly string[]): Promise<void> {
  if (args.length === 0) {
    await runRealGoClientIpAllowlistSmokeFromEnvironment()
    return
  }
  if (args.length === 1 && args[0] === '--cleanup-only') {
    await runRealGoClientIpAllowlistCleanupOnlyFromEnvironment()
    return
  }
  throw new Error('Client IP allowlist smoke accepts only the optional --cleanup-only argument')
}

if (isMainModule()) {
  try {
    await runCommandLine(process.argv.slice(2))
  } catch (error) {
    console.error(error instanceof Error ? error.message : 'Client IP allowlist smoke failed')
    process.exitCode = 1
  }
}
