import { createHash } from 'node:crypto'
import { isIP } from 'node:net'
import { pathToFileURL } from 'node:url'

export const realGoSettingsWriteSmokeEnv = {
  baseUrl: 'JUHE_REAL_GO_SETTINGS_WRITE_BASE_URL',
  cookie: 'JUHE_REAL_GO_SETTINGS_WRITE_COOKIE',
  allow: 'JUHE_REAL_GO_SETTINGS_WRITE_ALLOW',
  confirmation: 'JUHE_REAL_GO_SETTINGS_WRITE_CONFIRMATION',
  timeoutMs: 'JUHE_REAL_GO_SETTINGS_WRITE_TIMEOUT_MS'
} as const

const apiPrefix = '/__aisys__/api'
const settingsPath = '/settings'
const confirmationPrefix = 'plan0081-settings-write-v1:'
const field = 'systemMetricsHourlyRetentionDays'
const minValue = 1
const maxValue = 30
const userAgent = 'juhe-ai-plan0081-real-go-settings-write-smoke/1.0'

export type RealGoSettingsWriteSmokeEnvironment = Readonly<Record<string, string | undefined>>
export interface RealGoSettingsWriteSmokeConfig {
  baseUrl: string
  cookie: string
  allow: true
  confirmation: string
  timeoutMs?: number
}
export interface RealGoSettingsWriteSmokeSummary {
  settingsWriteChecked: true
  settingsRestored: true
}
export interface RealGoSettingsWriteSmokeRuntime { fetch?: typeof fetch }

interface NormalizedConfig extends RealGoSettingsWriteSmokeConfig {
  timeoutMs: number
  fetch: typeof fetch
}

class SmokeError extends Error {}

export function normalizeManagementApiBaseUrl(value: string): string {
  let url: URL
  try { url = new URL(value) } catch { throw new SmokeError('base URL must be valid') }
  expect(url.protocol === 'http:' || url.protocol === 'https:', 'base URL protocol is invalid')
  expect(!url.username && !url.password && !url.search && !url.hash, 'base URL must not include userinfo, query, or fragment')
  if (url.protocol === 'http:') {
    const hostname = url.hostname.replace(/^\[|\]$/g, '')
    expect(isIP(hostname) > 0 && (hostname === '127.0.0.1' || hostname === '::1'), 'HTTP settings smoke requires loopback')
  }
  expect(url.pathname === '' || url.pathname === '/' || url.pathname === apiPrefix, 'base URL path must target the management API')
  url.pathname = apiPrefix
  url.search = ''
  url.hash = ''
  return url.toString().replace(/\/$/, '')
}

export function settingsWriteConfirmationForBaseUrl(baseUrl: string): string {
  const target = normalizeManagementApiBaseUrl(baseUrl)
  return `${confirmationPrefix}${createHash('sha256').update(target).digest('base64url').slice(0, 16)}`
}

export function loadRealGoSettingsWriteSmokeConfig(
  env: RealGoSettingsWriteSmokeEnvironment = process.env
): RealGoSettingsWriteSmokeConfig {
  const baseUrl = normalizeManagementApiBaseUrl(required(env, realGoSettingsWriteSmokeEnv.baseUrl))
  const cookie = required(env, realGoSettingsWriteSmokeEnv.cookie)
  expect(!/[\r\n]/.test(cookie), `${realGoSettingsWriteSmokeEnv.cookie} must be a single Cookie header line`)
  expect(env.NODE_ENV !== 'production', 'settings write smoke is disabled in production')
  expect(env[realGoSettingsWriteSmokeEnv.allow] === '1', `${realGoSettingsWriteSmokeEnv.allow} must equal 1`)
  const confirmation = required(env, realGoSettingsWriteSmokeEnv.confirmation)
  expect(confirmation === settingsWriteConfirmationForBaseUrl(baseUrl), 'settings write confirmation does not match target')
  return { baseUrl, cookie, allow: true, confirmation, timeoutMs: optionalTimeout(env[realGoSettingsWriteSmokeEnv.timeoutMs]) }
}

export async function runRealGoSettingsWriteSmoke(
  config: RealGoSettingsWriteSmokeConfig,
  runtime: RealGoSettingsWriteSmokeRuntime = {}
): Promise<RealGoSettingsWriteSmokeSummary> {
  const normalized = normalizeConfig(config, runtime)
  const initial = await getSettings(normalized)
  const original = initial[field]
  expect(typeof original === 'number' && Number.isInteger(original) && original >= minValue && original <= maxValue, 'retention setting is outside the supported range')
  const temporary = original === minValue ? original + 1 : original - 1
  let primaryError: unknown
  let cleanupError: unknown
  try {
    const updated = await patchSettings(normalized, temporary)
    assertSnapshot(updated, initial, temporary)
    assertSnapshot(await getSettings(normalized), initial, temporary)
  } catch (error) {
    primaryError = error
  } finally {
    try {
      const current = await getSettings(normalized)
      if (current[field] !== original) await patchSettings(normalized, original)
      assertSnapshot(await getSettings(normalized), initial, original)
    } catch (error) {
      cleanupError = error
    }
  }
  if (primaryError || cleanupError) throw combineErrors(primaryError, cleanupError)
  return { settingsWriteChecked: true, settingsRestored: true }
}

export async function runRealGoSettingsWriteSmokeFromEnvironment(
  env: RealGoSettingsWriteSmokeEnvironment = process.env,
  writeSummary: (message: string) => void = console.log
): Promise<RealGoSettingsWriteSmokeSummary> {
  const summary = await runRealGoSettingsWriteSmoke(loadRealGoSettingsWriteSmokeConfig(env))
  writeSummary(formatRealGoSettingsWriteSmokeSummary(summary))
  return summary
}

export function formatRealGoSettingsWriteSmokeSummary(summary: RealGoSettingsWriteSmokeSummary): string {
  expect(summary.settingsWriteChecked === true && summary.settingsRestored === true, 'settings write smoke did not complete')
  return 'settingsWriteChecked=true settingsRestored=true'
}

async function getSettings(config: NormalizedConfig): Promise<Record<string, unknown>> {
  const response = await request(config, 'GET', undefined, 'settings GET')
  expect(response.status === 200, `settings GET failed with HTTP ${response.status}`)
  assertNoStore(response, 'settings GET')
  return parseSnapshot(await parseResponseJson(response))
}

async function patchSettings(config: NormalizedConfig, value: number): Promise<Record<string, unknown>> {
  const response = await request(config, 'PATCH', { [field]: value }, 'settings PATCH')
  expect(response.status === 200, `settings PATCH failed with HTTP ${response.status}`)
  assertNoStore(response, 'settings PATCH')
  return parseSnapshot(await parseResponseJson(response))
}

function assertSnapshot(actual: Record<string, unknown>, initial: Record<string, unknown>, expected: number): void {
  expect(actual[field] === expected, 'settings response has an unexpected retention value')
  const initialKeys = Object.keys(initial).sort()
  expect(JSON.stringify(Object.keys(actual).sort()) === JSON.stringify(initialKeys), 'settings response DTO keys changed')
  for (const key of initialKeys) if (key !== field) expect(JSON.stringify(actual[key]) === JSON.stringify(initial[key]), `settings response changed ${key}`)
}

async function request(config: NormalizedConfig, method: 'GET' | 'PATCH', body: unknown, label: string): Promise<Response> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), config.timeoutMs)
  try {
    return await config.fetch(`${config.baseUrl}${settingsPath}`, {
      method,
      redirect: 'manual',
      signal: controller.signal,
      headers: { Cookie: config.cookie, 'User-Agent': userAgent, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined
    })
  } catch { throw new SmokeError(`${label} request failed`) } finally { clearTimeout(timer) }
}

async function parseSnapshot(value: unknown): Promise<Record<string, unknown>> {
  expect(isRecord(value) && Object.keys(value).length === 1 && isRecord(value.data), 'settings response DTO is invalid')
  const data = value.data as Record<string, unknown>
  expect(Object.prototype.hasOwnProperty.call(data, field), 'settings response is missing the smoke field')
  return data
}
async function parseResponseJson(response: Response): Promise<unknown> {
  try { return await response.json() } catch { throw new SmokeError('settings response DTO is invalid') }
}

function assertNoStore(response: Response, label: string): void { expect(response.headers.get('cache-control') === 'no-store', `${label} must return Cache-Control: no-store`) }
function normalizeConfig(config: RealGoSettingsWriteSmokeConfig, runtime: RealGoSettingsWriteSmokeRuntime): NormalizedConfig {
  const baseUrl = normalizeManagementApiBaseUrl(config.baseUrl)
  expect(config.allow === true && config.confirmation === settingsWriteConfirmationForBaseUrl(baseUrl), 'settings write config is invalid')
  expect(!/[\r\n]/.test(config.cookie), 'settings write cookie is invalid')
  return { ...config, baseUrl, timeoutMs: config.timeoutMs ?? 15_000, fetch: runtime.fetch ?? fetch }
}
function required(env: RealGoSettingsWriteSmokeEnvironment, name: string): string { const value = env[name]; expect(typeof value === 'string' && value.length > 0, `Missing required environment variable: ${name}`); return value }
function optionalTimeout(value: string | undefined): number | undefined { if (value === undefined) return undefined; const parsed = Number(value); expect(Number.isInteger(parsed) && parsed > 0 && parsed <= 2_147_483_647, 'timeout must be a positive integer'); return parsed }
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
function expect(condition: unknown, message: string): asserts condition { if (!condition) throw new SmokeError(message) }
function combineErrors(primary: unknown, cleanup: unknown): Error { const errors = [primary, cleanup].filter(Boolean).map(error => error instanceof Error ? error.message : String(error)); return new Error(errors.join('; ')) }

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await runRealGoSettingsWriteSmokeFromEnvironment()
}
