import { createHash, randomUUID } from 'node:crypto'
import { isIP } from 'node:net'
import { pathToFileURL } from 'node:url'

export const realGoExternalSourceCreateSmokeEnv = {
  baseUrl: 'JUHE_REAL_GO_MANAGEMENT_BASE_URL',
  cookie: 'JUHE_REAL_GO_MANAGEMENT_COOKIE',
  timeoutMs: 'JUHE_REAL_GO_MANAGEMENT_TIMEOUT_MS',
  allowCreates: 'JUHE_REAL_GO_MANAGEMENT_ALLOW_EXTERNAL_INTEGRATION_SOURCE_CREATES',
  confirmation: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_CREATE_CONFIRMATION'
} as const

const externalIntegrationSourceCreateConfirmationPrefix = 'plan0081-external-source-create-v1:'
export const externalIntegrationSourceCreateNotesMarker = 'PLAN-0081 external source create smoke marker v1'

const managementApiPrefix = '/__aisys__/api'
const collectionPath = '/external-integration-sources'
const sourceNamePrefix = 'PLAN-0081 external source create smoke '
const smokeUserAgent = 'juhe-ai-plan0081-real-go-external-source-create-smoke/1.0'
const smokeHeaderValue = 'plan0081-real-go-external-source-create'
const defaultTimeoutMs = 15_000
const maximumTimeoutMs = 2_147_483_647
const cleanupMaximumAttempts = 3
const cleanupRetryDelayMs = 100

export type ExternalSourceCreateSmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoExternalSourceCreateSmokeConfig {
  baseUrl: string
  cookie: string
  timeoutMs?: number
  allowCreates: true
  confirmation: string
}

export interface RealGoExternalSourceCreateSmokeSummary {
  externalIntegrationSourceCreateChecked: true
}

export interface RealGoExternalSourceCreateSmokeRuntime {
  fetch?: typeof fetch
}

interface NormalizedConfig extends RealGoExternalSourceCreateSmokeConfig {
  timeoutMs: number
  fetch: typeof fetch
}

interface CreatedAuthorization {
  source: Record<string, unknown>
  token: Record<string, unknown>
}

interface CleanupTarget {
  sourceId: string
  tokenId: string
  sourceName: string
}

class SanitizedTransportError extends Error {
  constructor(label: string, kind: string) {
    super(`${label} request failed: ${kind}`)
    this.name = 'SanitizedTransportError'
  }
}

class SanitizedHttpStatusError extends Error {
  constructor(label: string, readonly status: number) {
    super(`${label} failed with HTTP ${status}`)
    this.name = 'SanitizedHttpStatusError'
  }
}

class CleanupTargetStillPresentError extends Error {
  constructor() {
    super('post-delete detail must return HTTP 404')
    this.name = 'CleanupTargetStillPresentError'
  }
}

export function loadRealGoExternalSourceCreateSmokeConfig(
  env: ExternalSourceCreateSmokeEnvironment = process.env
): RealGoExternalSourceCreateSmokeConfig {
  const rawBaseUrl = requiredEnvironmentValue(env, realGoExternalSourceCreateSmokeEnv.baseUrl)
  const cookie = requiredEnvironmentValue(env, realGoExternalSourceCreateSmokeEnv.cookie)
  const allowCreates = requiredEnvironmentValue(env, realGoExternalSourceCreateSmokeEnv.allowCreates)
  const confirmation = requiredEnvironmentValue(env, realGoExternalSourceCreateSmokeEnv.confirmation)

  expect(!/[\r\n]/.test(cookie), `${realGoExternalSourceCreateSmokeEnv.cookie} must be a single Cookie header line`)
  expect(
    allowCreates === '1',
    `${realGoExternalSourceCreateSmokeEnv.allowCreates} must equal 1`
  )
  const baseUrl = normalizeManagementApiBaseUrl(rawBaseUrl)
  expect(
    confirmation === externalIntegrationSourceCreateConfirmationForBaseUrl(baseUrl),
    `${realGoExternalSourceCreateSmokeEnv.confirmation} must match the normalized management target`
  )

  return {
    baseUrl,
    cookie,
    timeoutMs: optionalPositiveIntegerEnvironmentValue(env, realGoExternalSourceCreateSmokeEnv.timeoutMs),
    allowCreates: true,
    confirmation
  }
}

export function externalIntegrationSourceCreateConfirmationForBaseUrl(baseUrl: string): string {
  const normalized = normalizeManagementApiBaseUrl(baseUrl)
  const targetHash = createHash('sha256').update(normalized).digest('base64url').slice(0, 16)
  return `${externalIntegrationSourceCreateConfirmationPrefix}${targetHash}`
}

export async function runRealGoExternalSourceCreateSmoke(
  config: RealGoExternalSourceCreateSmokeConfig,
  runtime: RealGoExternalSourceCreateSmokeRuntime = {}
): Promise<RealGoExternalSourceCreateSmokeSummary> {
  const normalized = normalizeConfig(config, runtime)
  const sourceName = `${sourceNamePrefix}${randomUUID()}`
  let cleanupTarget: CleanupTarget | undefined
  let postOutcomeUncertain = false
  let primaryError: unknown
  let cleanupError: unknown

  try {
    try {
      postOutcomeUncertain = true
      const response = await request(normalized, collectionPath, 'POST', {
        name: sourceName,
        status: 'disabled',
        scopes: [],
        rateLimits: [],
        expiresAt: null,
        notes: externalIntegrationSourceCreateNotesMarker
      }, 'create')
      if (response.status !== 201) throw new SanitizedHttpStatusError('create', response.status)

      let data: unknown
      try {
        data = await response.json()
      } catch {
        postOutcomeUncertain = true
        throw new Error('create response must be valid JSON')
      }
      const created = unwrapData(data, 'create')
      if (
        isRecord(created)
        && isRecord(created.source)
        && isRecord(created.token)
        && isNonEmptyString(created.source.id)
        && isNonEmptyString(created.token.id)
      ) {
        cleanupTarget = { sourceId: created.source.id, tokenId: created.token.id, sourceName }
      } else {
        postOutcomeUncertain = true
      }
      assertSecretHeaders(response, 'create')
      const authorization = assertCreatedAuthorization(created, sourceName)
      const sourceId = authorization.source.id as string
      const tokenId = authorization.token.id as string
      cleanupTarget = { sourceId, tokenId, sourceName }
      const plaintextToken = authorization.token.token as string

      const detailResponse = await request(
        normalized,
        `${collectionPath}/${encodeURIComponent(sourceId)}`,
        'GET',
        undefined,
        'detail'
      )
      expectStatus(detailResponse, 200, 'detail')
      const detail = unwrapData(await parseJSON(detailResponse, 'detail'), 'detail')
      assertDetail(detail, sourceName, sourceId, tokenId, plaintextToken)

      const secretResponse = await request(
        normalized,
        `${collectionPath}/${encodeURIComponent(sourceId)}/tokens/${encodeURIComponent(tokenId)}/secret`,
        'GET',
        undefined,
        'token secret'
      )
      expectStatus(secretResponse, 200, 'token secret')
      assertSecretHeaders(secretResponse, 'token secret')
      const secret = unwrapData(await parseJSON(secretResponse, 'token secret'), 'token secret')
      expect(isRecord(secret) && Object.keys(secret).length === 1 && isNonEmptyString(secret.token), 'token secret DTO is invalid')
      expect(secret.token === plaintextToken, 'token secret does not match created token')
    } catch (error) {
      if (error instanceof SanitizedTransportError && !cleanupTarget) postOutcomeUncertain = true
      primaryError = error
    }
  } finally {
    try {
      let target = cleanupTarget
      if (!target && postOutcomeUncertain) {
        target = await recoverCleanupTarget(normalized, sourceName)
      }
      if (target) await deleteAndVerify(normalized, target)
    } catch (error) {
      cleanupError = error
    }
  }

  if (primaryError || cleanupError) throw combineErrors(primaryError, cleanupError)
  return { externalIntegrationSourceCreateChecked: true }
}

export async function runRealGoExternalSourceCreateSmokeFromEnvironment(
  env: ExternalSourceCreateSmokeEnvironment = process.env,
  writeSummary: (message: string) => void = console.log
): Promise<RealGoExternalSourceCreateSmokeSummary> {
  const summary = await runRealGoExternalSourceCreateSmoke(loadRealGoExternalSourceCreateSmokeConfig(env))
  writeSummary(formatRealGoExternalSourceCreateSmokeSummary(summary))
  return summary
}

export function formatRealGoExternalSourceCreateSmokeSummary(
  summary: RealGoExternalSourceCreateSmokeSummary
): string {
  expect(summary.externalIntegrationSourceCreateChecked === true, 'external integration source create was not checked')
  return 'externalIntegrationSourceCreateChecked=true'
}

async function recoverCleanupTarget(config: NormalizedConfig, sourceName: string): Promise<CleanupTarget | undefined> {
  const query = `?page=1&pageSize=100&keyword=${encodeURIComponent(sourceName)}`
  let lastError: unknown
  for (let attempt = 1; attempt <= cleanupMaximumAttempts; attempt += 1) {
    try {
      const response = await request(config, `${collectionPath}${query}`, 'GET', undefined, 'recovery list')
      expectStatus(response, 200, 'recovery list')
      const data = unwrapData(await parseJSON(response, 'recovery list'), 'recovery list')
      expect(isRecord(data) && Array.isArray(data.items), 'recovery list DTO is invalid')
      const exactNameCandidates = data.items.filter((item): item is Record<string, unknown> =>
        isRecord(item) && item.name === sourceName
      )
      if (exactNameCandidates.length === 0) {
        if (attempt < cleanupMaximumAttempts) {
          await cleanupRetryDelay()
          continue
        }
        return undefined
      }
      const candidate = exactNameCandidates[0]!
      expect(
        exactNameCandidates.length === 1
        && candidate.notes === externalIntegrationSourceCreateNotesMarker
        && candidate.isBuiltIn === false
        && candidate.status === 'disabled'
        && isNonEmptyString(candidate.id)
        && isRecord(candidate.primaryToken)
        && isNonEmptyString(candidate.primaryToken.id)
        && candidate.primaryToken.status === 'disabled',
        'recovery list exact-name candidates are unsafe'
      )
      return {
        sourceId: candidate.id as string,
        tokenId: candidate.primaryToken.id as string,
        sourceName
      }
    } catch (error) {
      lastError = error
      if (!isRetryableCleanupError(error) || attempt === cleanupMaximumAttempts) throw error
      await cleanupRetryDelay()
    }
  }
  throw lastError instanceof Error ? lastError : new Error('recovery list failed')
}

async function deleteAndVerify(config: NormalizedConfig, target: CleanupTarget): Promise<void> {
  const path = `${collectionPath}/${encodeURIComponent(target.sourceId)}`
  if (!(await cleanupTargetStillExists(config, path, target))) return

  let lastError: unknown
  for (let attempt = 1; attempt <= cleanupMaximumAttempts; attempt += 1) {
    try {
      const response = await request(config, path, 'DELETE', undefined, 'cleanup delete')
      if (response.status !== 204) throw new SanitizedHttpStatusError('cleanup delete', response.status)

      let contractError: unknown
      try {
        expect(response.headers.get('cache-control') === 'no-store', 'cleanup delete must return Cache-Control: no-store')
        const body = await response.text()
        expect(body.length === 0, 'cleanup delete must return an empty body')
      } catch (error) {
        contractError = error
      }
      await verifyCleanupTargetDeleted(config, path, target)
      if (contractError) throw contractError
      return
    } catch (error) {
      lastError = error
      if (!isRetryableCleanupError(error)) throw error
      if (!(await cleanupTargetStillExists(config, path, target))) return
      if (attempt === cleanupMaximumAttempts) throw error
      await cleanupRetryDelay()
    }
  }
  throw lastError instanceof Error ? lastError : new Error('cleanup delete failed')
}

async function cleanupTargetStillExists(
  config: NormalizedConfig,
  path: string,
  target: CleanupTarget
): Promise<boolean> {
  return retryCleanupRead(async () => {
    const response = await request(config, path, 'GET', undefined, 'cleanup detail')
    if (response.status === 404) return false
    expectStatus(response, 200, 'cleanup detail')
    const detail = unwrapData(await parseJSON(response, 'cleanup detail'), 'cleanup detail')
    assertDetail(detail, target.sourceName, target.sourceId, target.tokenId)
    return true
  })
}

async function verifyCleanupTargetDeleted(
  config: NormalizedConfig,
  path: string,
  target: CleanupTarget
): Promise<void> {
  let lastPresent = false
  for (let attempt = 1; attempt <= cleanupMaximumAttempts; attempt += 1) {
    lastPresent = await cleanupTargetStillExists(config, path, target)
    if (!lastPresent) return
    if (attempt < cleanupMaximumAttempts) await cleanupRetryDelay()
  }
  if (lastPresent) throw new CleanupTargetStillPresentError()
}

async function retryCleanupRead<T>(operation: () => Promise<T>): Promise<T> {
  let lastError: unknown
  for (let attempt = 1; attempt <= cleanupMaximumAttempts; attempt += 1) {
    try {
      return await operation()
    } catch (error) {
      lastError = error
      if (!isRetryableCleanupError(error) || attempt === cleanupMaximumAttempts) throw error
      await cleanupRetryDelay()
    }
  }
  throw lastError instanceof Error ? lastError : new Error('cleanup read failed')
}

function isRetryableCleanupError(error: unknown): boolean {
  if (error instanceof SanitizedTransportError || error instanceof CleanupTargetStillPresentError) return true
  return error instanceof SanitizedHttpStatusError
    && (error.status === 408 || error.status === 409 || error.status === 425 || error.status === 429 || error.status >= 500)
}

function cleanupRetryDelay(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, cleanupRetryDelayMs))
}

function assertCreatedAuthorization(value: unknown, sourceName: string): CreatedAuthorization {
  expect(isRecord(value), 'create DTO is invalid')
  expect(hasExactKeys(value, ['source', 'token']), 'create DTO is invalid')
  expect(isRecord(value.source), 'create source DTO is invalid')
  expect(isRecord(value.token), 'create token DTO is invalid')
  assertSource(value.source, sourceName, false)
  expect(value.source.notes === externalIntegrationSourceCreateNotesMarker, 'create source DTO notes mismatch')
  expect(value.source.tokenCount === 1 && value.source.activeTokenCount === 0, 'create source DTO token counts are invalid')
  expect(isRecord(value.source.primaryToken), 'create source DTO primaryToken is invalid')
  expect(value.source.primaryToken.id === value.token.id, 'create source DTO primaryToken id mismatch')
  expect(value.source.primaryToken.status === 'disabled', 'create source DTO primaryToken status mismatch')

  expect(
    hasOnlyKeys(value.token, ['id', 'name', 'token', 'tokenPrefix', 'tokenSuffix', 'scopes', 'expiresAt']),
    'create token DTO is invalid'
  )
  for (const field of ['id', 'name', 'token', 'tokenPrefix', 'tokenSuffix'] as const) {
    expect(isNonEmptyString(value.token[field]), 'create token DTO is invalid')
  }
  expect(Array.isArray(value.token.scopes) && value.token.scopes.length === 0, 'create token DTO is invalid')
  expect(!Object.hasOwn(value.token, 'expiresAt'), 'create token DTO expiresAt mismatch')
  const token = value.token.token as string
  expect(/^juis_[A-Za-z0-9_-]{43}$/.test(token) && token.length === 48, 'create token must use exact juis_ format')
  expect(value.token.tokenPrefix === token.slice(0, 8), 'create token DTO prefix mismatch')
  expect(value.token.tokenSuffix === token.slice(-8), 'create token DTO suffix mismatch')
  return value as unknown as CreatedAuthorization
}

function assertDetail(
  value: unknown,
  sourceName: string,
  sourceId: string,
  tokenId: string,
  plaintextToken?: string
): void {
  expect(isRecord(value), 'detail source DTO is invalid')
  assertSource(value, sourceName, true)
  expect(value.id === sourceId, 'detail source DTO id mismatch')
  expect(value.notes === externalIntegrationSourceCreateNotesMarker, 'detail source DTO notes mismatch')
  expect(value.tokenCount === 1 && value.activeTokenCount === 0, 'detail source DTO token counts are invalid')
  expect(Array.isArray(value.tokens) && value.tokens.length === 1, 'detail source DTO must contain exactly one token')
  if (plaintextToken !== undefined) expect(!containsString(value, plaintextToken), 'detail must not contain plaintext token')
  const token = value.tokens[0]
  expect(isRecord(token), 'detail token DTO is invalid')
  expect(token.id === tokenId, 'detail token DTO id mismatch')
  expect(
    hasOnlyKeys(token, [
      'id', 'name', 'tokenPrefix', 'tokenSuffix', 'status', 'scopes', 'expiresAt', 'lastUsedAt',
      'createdAt', 'updatedAt', 'revokedAt', 'isBuiltIn'
    ]),
    'detail token DTO is invalid'
  )
  expect(token.status === 'disabled' && token.isBuiltIn === false, 'detail token DTO is invalid')
  expect(Array.isArray(token.scopes) && token.scopes.length === 0, 'detail token DTO is invalid')
  expect(!Object.hasOwn(token, 'expiresAt'), 'detail token DTO expiresAt mismatch')
  for (const field of ['id', 'name', 'tokenPrefix', 'tokenSuffix', 'createdAt', 'updatedAt'] as const) {
    expect(isNonEmptyString(token[field]), 'detail token DTO is invalid')
  }
}

function assertSource(value: Record<string, unknown>, sourceName: string, detail: boolean): void {
  expect(
    hasOnlyKeys(value, [
      'id', 'name', 'status', 'scopes', 'rateLimits', 'expiresAt', 'notes', 'lastUsedAt', 'createdAt',
      'updatedAt', 'tokenCount', 'activeTokenCount', 'primaryToken', 'tokens', 'isBuiltIn'
    ]),
    `${detail ? 'detail' : 'create'} source DTO is invalid`
  )
  expect(isNonEmptyString(value.id), `${detail ? 'detail' : 'create'} source DTO is invalid`)
  expect(value.name === sourceName, `${detail ? 'detail' : 'create'} source DTO name mismatch`)
  expect(value.status === 'disabled' && value.isBuiltIn === false, `${detail ? 'detail' : 'create'} source DTO is invalid`)
  expect(
    Array.isArray(value.scopes) && value.scopes.length === 0
    && Array.isArray(value.rateLimits) && value.rateLimits.length === 0,
    `${detail ? 'detail' : 'create'} source DTO is invalid`
  )
  expect(!Object.hasOwn(value, 'expiresAt'), `${detail ? 'detail' : 'create'} source DTO expiresAt mismatch`)
  expect(value.notes === externalIntegrationSourceCreateNotesMarker, `${detail ? 'detail' : 'create'} source DTO notes mismatch`)
  expect(isNonEmptyString(value.createdAt) && isNonEmptyString(value.updatedAt), `${detail ? 'detail' : 'create'} source DTO is invalid`)
}

async function request(
  config: NormalizedConfig,
  path: string,
  method: 'GET' | 'POST' | 'DELETE',
  body: unknown,
  label: string
): Promise<Response> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), config.timeoutMs)
  try {
    return await config.fetch(`${config.baseUrl}${path}`, {
      method,
      headers: {
        accept: 'application/json',
        cookie: config.cookie,
        'user-agent': smokeUserAgent,
        'x-juhe-ai-smoke': smokeHeaderValue,
        ...(method === 'POST' ? { 'content-type': 'application/json' } : {})
      },
      body: method === 'POST' ? JSON.stringify(body) : undefined,
      redirect: 'error',
      signal: controller.signal
    })
  } catch (error) {
    throw new SanitizedTransportError(label, transportKind(error))
  } finally {
    clearTimeout(timeout)
  }
}

function normalizeConfig(
  config: RealGoExternalSourceCreateSmokeConfig,
  runtime: RealGoExternalSourceCreateSmokeRuntime
): NormalizedConfig {
  expect(config.allowCreates === true, `${realGoExternalSourceCreateSmokeEnv.allowCreates} must equal 1`)
  expect(
    config.confirmation === externalIntegrationSourceCreateConfirmationForBaseUrl(config.baseUrl),
    `${realGoExternalSourceCreateSmokeEnv.confirmation} must match the normalized management target`
  )
  expect(!/[\r\n]/.test(config.cookie) && config.cookie.trim().length > 0, 'management cookie is invalid')
  const timeoutMs = config.timeoutMs ?? defaultTimeoutMs
  expect(
    Number.isInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= maximumTimeoutMs,
    'timeoutMs must be a positive integer'
  )
  return {
    ...config,
    baseUrl: normalizeManagementApiBaseUrl(config.baseUrl),
    cookie: config.cookie.trim(),
    timeoutMs,
    fetch: runtime.fetch ?? globalThis.fetch
  }
}

function normalizeManagementApiBaseUrl(rawValue: string): string {
  expect(!/[\r\n]/.test(rawValue), `${realGoExternalSourceCreateSmokeEnv.baseUrl} must be a single line`)
  let url: URL
  try {
    url = new URL(rawValue.trim())
  } catch {
    throw new Error(`${realGoExternalSourceCreateSmokeEnv.baseUrl} must be an absolute HTTP(S) URL`)
  }
  expect(url.protocol === 'http:' || url.protocol === 'https:', `${realGoExternalSourceCreateSmokeEnv.baseUrl} must use HTTP or HTTPS`)
  expect(!url.username && !url.password, `${realGoExternalSourceCreateSmokeEnv.baseUrl} must not contain credentials`)
  expect(!url.search && !url.hash, `${realGoExternalSourceCreateSmokeEnv.baseUrl} must not contain query or fragment`)
  expect(url.protocol === 'https:' || isLoopbackHost(url.hostname), `${realGoExternalSourceCreateSmokeEnv.baseUrl} must use HTTPS unless the host is loopback`)
  const pathname = url.pathname.replace(/\/+$/, '')
  url.pathname = pathname.endsWith(managementApiPrefix) ? pathname : `${pathname}${managementApiPrefix}`
  return url.toString().replace(/\/$/, '')
}

function isLoopbackHost(hostname: string): boolean {
  const normalized = hostname.replace(/^\[|\]$/g, '').toLowerCase()
  return normalized === 'localhost' || normalized === '::1' || (isIP(normalized) === 4 && normalized.startsWith('127.'))
}

function requiredEnvironmentValue(env: ExternalSourceCreateSmokeEnvironment, name: string): string {
  const value = env[name]
  expect(typeof value === 'string' && value.trim().length > 0, `Missing required environment variable: ${name}`)
  return value.trim()
}

function optionalPositiveIntegerEnvironmentValue(
  env: ExternalSourceCreateSmokeEnvironment,
  name: string
): number | undefined {
  const value = env[name]
  if (value === undefined || value.trim() === '') return undefined
  const parsed = Number(value)
  expect(Number.isInteger(parsed) && parsed > 0 && parsed <= maximumTimeoutMs, `${name} must be a positive integer`)
  return parsed
}

function expectStatus(response: Response, status: number, label: string): void {
  if (response.status !== status) throw new SanitizedHttpStatusError(label, response.status)
}

function assertSecretHeaders(response: Response, label: string): void {
  expect(response.headers.get('cache-control') === 'no-store', `${label} must return Cache-Control: no-store`)
  expect(response.headers.get('pragma') === 'no-cache', `${label} must return Pragma: no-cache`)
}

async function parseJSON(response: Response, label: string): Promise<unknown> {
  try {
    return await response.json()
  } catch {
    throw new Error(`${label} response must be valid JSON`)
  }
}

function unwrapData(value: unknown, label: string): unknown {
  expect(isRecord(value) && hasExactKeys(value, ['data']), `${label} response envelope is invalid`)
  return value.data
}

function containsString(value: unknown, target: string): boolean {
  if (typeof value === 'string') return value === target
  if (Array.isArray(value)) return value.some((item) => containsString(item, target))
  if (isRecord(value)) return Object.values(value).some((item) => containsString(item, target))
  return false
}

function hasExactKeys(value: Record<string, unknown>, keys: string[]): boolean {
  const actual = Object.keys(value).sort()
  const expected = [...keys].sort()
  return actual.length === expected.length && actual.every((key, index) => key === expected[index])
}

function hasOnlyKeys(value: Record<string, unknown>, keys: string[]): boolean {
  const allowed = new Set(keys)
  return Object.keys(value).every((key) => allowed.has(key))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}

function transportKind(error: unknown): string {
  return error instanceof Error && error.name === 'AbortError' ? 'timeout' : 'transport error'
}

function combineErrors(primary: unknown, cleanup: unknown): Error {
  const messages: string[] = []
  if (primary) messages.push(`main: ${safeErrorMessage(primary)}`)
  if (cleanup) messages.push(`cleanup: ${safeErrorMessage(cleanup)}`)
  const errors = [primary, cleanup].filter((error): error is NonNullable<unknown> => error !== undefined && error !== null)
    .map((error) => error instanceof Error ? error : new Error('unknown error'))
  if (errors.length > 1) return new AggregateError(errors, messages.join('; '))
  return new Error(messages.join('; '), { cause: errors[0] })
}

function safeErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'unknown error'
}

function expect(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}

const isDirectExecution = process.argv[1]
  ? import.meta.url === pathToFileURL(process.argv[1]).href
  : false

if (isDirectExecution) {
  await runRealGoExternalSourceCreateSmokeFromEnvironment()
}
