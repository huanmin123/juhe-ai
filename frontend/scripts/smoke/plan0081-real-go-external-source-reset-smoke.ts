import { createHash } from 'node:crypto'
import { isIP } from 'node:net'
import { pathToFileURL } from 'node:url'

export const realGoExternalSourceResetSmokeEnv = {
  baseUrl: 'JUHE_REAL_GO_MANAGEMENT_BASE_URL',
  cookie: 'JUHE_REAL_GO_MANAGEMENT_COOKIE',
  timeoutMs: 'JUHE_REAL_GO_MANAGEMENT_TIMEOUT_MS',
  enabled: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_SOURCE_RESET_ENABLED',
  confirmation: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_SOURCE_RESET_CONFIRMATION'
} as const

export const builtInExternalSourceResetFixture = {
  sourceId: 'extsrc_builtin_test',
  tokenId: 'exttok_builtin_test'
} as const

const confirmationVersion = 'plan0081-external-source-reset-v1'
const managementApiPrefix = '/__aisys__/api'
const collectionPath = '/external-integration-sources'
const resetPath = `${collectionPath}/built-in-test-token/reset`
const smokeUserAgent = 'juhe-ai-plan0081-real-go-external-source-reset-smoke/1.0'
const smokeHeaderValue = 'plan0081-real-go-external-source-reset'
const defaultTimeoutMs = 15_000
const maximumTimeoutMs = 2_147_483_647

export type ExternalSourceResetSmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoExternalSourceResetSmokeConfig {
  baseUrl: string
  cookie: string
  timeoutMs?: number
  enabled: true
  confirmation: string
}

export interface RealGoExternalSourceResetSmokeSummary {
  externalIntegrationSourceResetChecked: true
  confirmationHash: string
}

export interface RealGoExternalSourceResetSmokeRuntime {
  fetch?: typeof fetch
}

interface NormalizedConfig extends RealGoExternalSourceResetSmokeConfig {
  timeoutMs: number
  fetch: typeof fetch
}

interface TokenPreview {
  tokenPrefix: string
  tokenSuffix: string
}

interface FixtureSnapshot extends TokenPreview {
  plaintextToken: string
}

interface ResetResult extends TokenPreview {
  plaintextToken: string
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

export function loadRealGoExternalSourceResetSmokeConfig(
  env: ExternalSourceResetSmokeEnvironment = process.env
): RealGoExternalSourceResetSmokeConfig {
  const baseUrl = requiredEnvironmentValue(env, realGoExternalSourceResetSmokeEnv.baseUrl)
  const cookie = requiredEnvironmentValue(env, realGoExternalSourceResetSmokeEnv.cookie)
  const enabled = requiredEnvironmentValue(env, realGoExternalSourceResetSmokeEnv.enabled)
  const confirmation = requiredEnvironmentValue(env, realGoExternalSourceResetSmokeEnv.confirmation)

  expect(!/[\r\n]/.test(cookie), `${realGoExternalSourceResetSmokeEnv.cookie} must be a single Cookie header line`)
  expect(enabled === '1', `${realGoExternalSourceResetSmokeEnv.enabled} must equal 1`)
  expect(/^[0-9a-f]{64}$/.test(confirmation), `${realGoExternalSourceResetSmokeEnv.confirmation} must be a lowercase SHA-256 hash`)

  return {
    baseUrl: normalizeManagementApiBaseUrl(baseUrl),
    cookie,
    timeoutMs: optionalPositiveIntegerEnvironmentValue(env, realGoExternalSourceResetSmokeEnv.timeoutMs),
    enabled: true,
    confirmation
  }
}

export function externalSourceResetConfirmationHash(
  baseUrl: string,
  oldTokenPrefix: string,
  oldTokenSuffix: string
): string {
  const normalizedBaseUrl = normalizeManagementApiBaseUrl(baseUrl)
  assertTokenPreview({ tokenPrefix: oldTokenPrefix, tokenSuffix: oldTokenSuffix }, 'confirmation preview')
  return createHash('sha256')
    .update([
      confirmationVersion,
      normalizedBaseUrl,
      builtInExternalSourceResetFixture.sourceId,
      builtInExternalSourceResetFixture.tokenId,
      oldTokenPrefix,
      oldTokenSuffix
    ].join('\n'))
    .digest('hex')
}

export async function runRealGoExternalSourceResetSmoke(
  config: RealGoExternalSourceResetSmokeConfig,
  runtime: RealGoExternalSourceResetSmokeRuntime = {}
): Promise<RealGoExternalSourceResetSmokeSummary> {
  const normalized = normalizeConfig(config, runtime)
  const oldSnapshot = await readFixtureSnapshot(normalized, 'old')
  const confirmationHash = externalSourceResetConfirmationHash(
    normalized.baseUrl,
    oldSnapshot.tokenPrefix,
    oldSnapshot.tokenSuffix
  )
  expect(
    normalized.confirmation === confirmationHash,
    `${realGoExternalSourceResetSmokeEnv.confirmation} does not match the current isolated fixture`
  )

  // Rotation is intentionally irreversible; this smoke is only for an isolated fixture.
  const resetResponse = await request(normalized, resetPath, 'POST', {}, 'reset')
  expectStatus(resetResponse, 200, 'reset')
  assertSecretHeaders(resetResponse, 'reset')
  const resetResult = assertResetResult(unwrapData(await parseJSON(resetResponse, 'reset'), 'reset'))

  const newSnapshot = await readFixtureSnapshot(normalized, 'new')
  expect(newSnapshot.plaintextToken === resetResult.plaintextToken, 'new token secret does not match reset token')
  expect(newSnapshot.tokenPrefix === resetResult.tokenPrefix, 'new token prefix does not match reset preview')
  expect(newSnapshot.tokenSuffix === resetResult.tokenSuffix, 'new token suffix does not match reset preview')
  expect(newSnapshot.plaintextToken !== oldSnapshot.plaintextToken, 'reset must replace the old token')

  return {
    externalIntegrationSourceResetChecked: true,
    confirmationHash
  }
}

export async function runRealGoExternalSourceResetSmokeFromEnvironment(
  env: ExternalSourceResetSmokeEnvironment = process.env,
  writeSummary: (message: string) => void = console.log
): Promise<RealGoExternalSourceResetSmokeSummary> {
  const summary = await runRealGoExternalSourceResetSmoke(loadRealGoExternalSourceResetSmokeConfig(env))
  writeSummary(formatRealGoExternalSourceResetSmokeSummary(summary))
  return summary
}

export function formatRealGoExternalSourceResetSmokeSummary(
  summary: RealGoExternalSourceResetSmokeSummary
): string {
  expect(summary.externalIntegrationSourceResetChecked === true, 'external integration source reset was not checked')
  expect(/^[0-9a-f]{64}$/.test(summary.confirmationHash), 'confirmation hash is invalid')
  return `externalIntegrationSourceResetChecked=true confirmationHash=${summary.confirmationHash}`
}

async function readFixtureSnapshot(config: NormalizedConfig, phase: 'old' | 'new'): Promise<FixtureSnapshot> {
  const detailResponse = await request(
    config,
    `${collectionPath}/${builtInExternalSourceResetFixture.sourceId}`,
    'GET',
    undefined,
    `${phase} detail`
  )
  expectStatus(detailResponse, 200, `${phase} detail`)
  const preview = assertFixtureDetail(unwrapData(await parseJSON(detailResponse, `${phase} detail`), `${phase} detail`), phase)

  const secretResponse = await request(
    config,
    `${collectionPath}/${builtInExternalSourceResetFixture.sourceId}/tokens/${builtInExternalSourceResetFixture.tokenId}/secret`,
    'GET',
    undefined,
    `${phase} token secret`
  )
  expectStatus(secretResponse, 200, `${phase} token secret`)
  assertSecretHeaders(secretResponse, `${phase} token secret`)
  const secret = unwrapData(await parseJSON(secretResponse, `${phase} token secret`), `${phase} token secret`)
  expect(isRecord(secret) && hasExactKeys(secret, ['token']), `${phase} token secret DTO is invalid`)
  expect(isValidPlaintextToken(secret.token), `${phase} token secret format is invalid`)
  expect(secret.token.startsWith(preview.tokenPrefix), `${phase} token secret does not match preview prefix`)
  expect(secret.token.endsWith(preview.tokenSuffix), `${phase} token secret does not match preview suffix`)

  return { ...preview, plaintextToken: secret.token }
}

function assertFixtureDetail(value: unknown, phase: 'old' | 'new'): TokenPreview {
  expect(isRecord(value), `${phase} detail source DTO is invalid`)
  expect(value.id === builtInExternalSourceResetFixture.sourceId, `${phase} detail source ID mismatch`)
  expect(value.isBuiltIn === true, `${phase} detail source must be built in`)
  expect(Array.isArray(value.tokens), `${phase} detail tokens DTO is invalid`)
  const token = value.tokens.find((item) => isRecord(item) && item.id === builtInExternalSourceResetFixture.tokenId)
  expect(isRecord(token), `${phase} detail fixed token is missing`)
  expect(token.isBuiltIn === true, `${phase} detail token must be built in`)
  assertTokenPreview(token, `${phase} detail token`)
  return { tokenPrefix: token.tokenPrefix, tokenSuffix: token.tokenSuffix }
}

function assertResetResult(value: unknown): ResetResult {
  expect(isRecord(value) && hasExactKeys(value, ['source', 'token']), 'reset DTO is invalid')
  expect(isRecord(value.source), 'reset source DTO is invalid')
  expect(value.source.id === builtInExternalSourceResetFixture.sourceId, 'reset source ID mismatch')
  expect(value.source.isBuiltIn === true, 'reset source must be built in')
  expect(Array.isArray(value.source.tokens), 'reset source tokens DTO is invalid')
  const sourceToken = value.source.tokens.find((item) =>
    isRecord(item) && item.id === builtInExternalSourceResetFixture.tokenId
  )
  expect(isRecord(sourceToken), 'reset source fixed token is missing')
  expect(sourceToken.isBuiltIn === true, 'reset source token must be built in')
  assertTokenPreview(sourceToken, 'reset source token')

  expect(isRecord(value.token), 'reset token DTO is invalid')
  expect(
    hasOnlyKeys(value.token, ['id', 'name', 'token', 'tokenPrefix', 'tokenSuffix', 'scopes', 'expiresAt']),
    'reset token DTO is invalid'
  )
  expect(value.token.id === builtInExternalSourceResetFixture.tokenId, 'reset token ID mismatch')
  expect(isValidPlaintextToken(value.token.token), 'reset token format is invalid')
  assertTokenPreview(value.token, 'reset token')
  expect(value.token.tokenPrefix === value.token.token.slice(0, 8), 'reset token prefix does not match plaintext')
  expect(value.token.tokenSuffix === value.token.token.slice(-8), 'reset token suffix does not match plaintext')
  expect(sourceToken.tokenPrefix === value.token.tokenPrefix, 'reset source and token prefixes differ')
  expect(sourceToken.tokenSuffix === value.token.tokenSuffix, 'reset source and token suffixes differ')

  return {
    plaintextToken: value.token.token,
    tokenPrefix: value.token.tokenPrefix,
    tokenSuffix: value.token.tokenSuffix
  }
}

async function request(
  config: NormalizedConfig,
  path: string,
  method: 'GET' | 'POST',
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
  config: RealGoExternalSourceResetSmokeConfig,
  runtime: RealGoExternalSourceResetSmokeRuntime
): NormalizedConfig {
  expect(config.enabled === true, `${realGoExternalSourceResetSmokeEnv.enabled} must equal 1`)
  expect(/^[0-9a-f]{64}$/.test(config.confirmation), 'confirmation must be a lowercase SHA-256 hash')
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
  expect(!/[\r\n]/.test(rawValue), `${realGoExternalSourceResetSmokeEnv.baseUrl} must be a single line`)
  let url: URL
  try {
    url = new URL(rawValue.trim())
  } catch {
    throw new Error(`${realGoExternalSourceResetSmokeEnv.baseUrl} must be an absolute HTTP(S) URL`)
  }
  expect(url.protocol === 'http:' || url.protocol === 'https:', `${realGoExternalSourceResetSmokeEnv.baseUrl} must use HTTP or HTTPS`)
  expect(!url.username && !url.password, `${realGoExternalSourceResetSmokeEnv.baseUrl} must not contain credentials`)
  expect(!url.search && !url.hash, `${realGoExternalSourceResetSmokeEnv.baseUrl} must not contain query or fragment`)
  expect(url.protocol === 'https:' || isLoopbackHost(url.hostname), `${realGoExternalSourceResetSmokeEnv.baseUrl} must use HTTPS unless the host is loopback`)
  const pathname = url.pathname.replace(/\/+$/, '')
  url.pathname = pathname.endsWith(managementApiPrefix) ? pathname : `${pathname}${managementApiPrefix}`
  return url.toString().replace(/\/$/, '')
}

function isLoopbackHost(hostname: string): boolean {
  const normalized = hostname.replace(/^\[|\]$/g, '').toLowerCase()
  return normalized === 'localhost' || normalized === '::1' || (isIP(normalized) === 4 && normalized.startsWith('127.'))
}

function requiredEnvironmentValue(env: ExternalSourceResetSmokeEnvironment, name: string): string {
  const value = env[name]
  expect(typeof value === 'string' && value.trim().length > 0, `Missing required environment variable: ${name}`)
  return value.trim()
}

function optionalPositiveIntegerEnvironmentValue(
  env: ExternalSourceResetSmokeEnvironment,
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

function assertTokenPreview(value: Record<string, unknown>, label: string): asserts value is Record<string, unknown> & TokenPreview {
  expect(typeof value.tokenPrefix === 'string' && /^juis_[A-Za-z0-9_-]{3}$/.test(value.tokenPrefix), `${label} prefix is invalid`)
  expect(typeof value.tokenSuffix === 'string' && /^[A-Za-z0-9_-]{8}$/.test(value.tokenSuffix), `${label} suffix is invalid`)
}

function isValidPlaintextToken(value: unknown): value is string {
  return typeof value === 'string' && value.length === 48 && /^juis_[A-Za-z0-9_-]{43}$/.test(value)
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

function transportKind(error: unknown): string {
  return error instanceof Error && error.name === 'AbortError' ? 'timeout' : 'transport error'
}

function expect(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}

const isDirectExecution = process.argv[1]
  ? import.meta.url === pathToFileURL(process.argv[1]).href
  : false

if (isDirectExecution) {
  try {
    await runRealGoExternalSourceResetSmokeFromEnvironment()
  } catch (error) {
    console.error(error instanceof Error ? error.message : 'External source reset smoke failed')
    process.exitCode = 1
  }
}
