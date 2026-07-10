import { pathToFileURL } from 'node:url'

export const realGoManagementSmokeEnv = {
  baseUrl: 'JUHE_REAL_GO_MANAGEMENT_BASE_URL',
  cookie: 'JUHE_REAL_GO_MANAGEMENT_COOKIE',
  groupId: 'JUHE_REAL_GO_MANAGEMENT_GROUP_ID',
  systemAccountId: 'JUHE_REAL_GO_MANAGEMENT_SYSTEM_ACCOUNT_ID'
} as const

const managementApiPrefix = '/__aisys__/api'
const smokeUserAgent = 'juhe-ai-plan0081-real-go-management-smoke/1.0'
const smokeHeaderValue = 'plan0081-real-go-management'
const defaultTimeoutMs = 15_000

export type SmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoManagementSmokeConfig {
  baseUrl: string
  cookie: string
  groupId?: string
  systemAccountId?: string
  timeoutMs?: number
}

export interface RealGoManagementSmokeSummary {
  groupCount: number
  selectedGroupId: string
  providerCount: number
  modelOptionCount: number
}

interface GroupListResult {
  items: unknown[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
  }
}

interface GroupRecord {
  id: string
  ownerSystemAccountId: string
  name: string
  providerCode: string
  enabled: boolean
  isDefault: boolean
  groupType: 'personal' | 'high_concurrency'
  accessType: 'owner' | 'authorized'
  accountStats: Record<string, unknown>
  permissions: Record<string, unknown>
}

interface GroupDetailRecord extends GroupRecord {
  accountIds: string[]
}

interface ProviderRecord {
  id: string
  code: string
  name: string
  enabled: boolean
  defaultProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  baseUrl: string
  defaultHealthCheckModel: string
  defaultSupportedModels: string[]
  accountTypes: string[]
  capabilities: string[]
  protocolProfiles: unknown[]
}

interface ModelOptionRecord {
  providerCode: string
  model: string
}

export function loadRealGoManagementSmokeConfig(
  env: SmokeEnvironment = process.env
): RealGoManagementSmokeConfig {
  const baseUrl = requiredEnvironmentValue(env, realGoManagementSmokeEnv.baseUrl)
  const cookie = requiredEnvironmentValue(env, realGoManagementSmokeEnv.cookie)
  expect(!/[\r\n]/.test(cookie), `${realGoManagementSmokeEnv.cookie} must be a single Cookie header line`)

  return {
    baseUrl: normalizeManagementApiBaseUrl(baseUrl),
    cookie,
    groupId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.groupId),
    systemAccountId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.systemAccountId)
  }
}

export async function runRealGoManagementSmoke(
  config: RealGoManagementSmokeConfig
): Promise<RealGoManagementSmokeSummary> {
  const normalizedConfig = {
    ...config,
    baseUrl: normalizeManagementApiBaseUrl(config.baseUrl),
    timeoutMs: config.timeoutMs ?? defaultTimeoutMs
  }
  expect(normalizedConfig.cookie.trim().length > 0, 'Cookie header must not be empty')
  expect(!/[\r\n]/.test(normalizedConfig.cookie), 'Cookie header must be a single line')
  expect(
    Number.isInteger(normalizedConfig.timeoutMs) && normalizedConfig.timeoutMs > 0,
    'Smoke timeout must be a positive integer'
  )

  const listUrl = endpointUrl(normalizedConfig.baseUrl, '/groups', {
    page: '1',
    pageSize: '500',
    systemAccountId: normalizedConfig.systemAccountId
  })
  const listData = await getEnvelopeData(listUrl, normalizedConfig, 'groups list')
  const groupList = assertGroupList(listData)
  const selectedGroup = selectOwnerNonDefaultGroup(groupList.items, normalizedConfig.groupId)

  const detailUrl = endpointUrl(
    normalizedConfig.baseUrl,
    `/groups/${encodeURIComponent(selectedGroup.id)}`,
    { systemAccountId: normalizedConfig.systemAccountId }
  )
  const detailData = await getEnvelopeData(detailUrl, normalizedConfig, 'group detail')
  const detail = assertGroupDetail(detailData)
  expect(detail.id === selectedGroup.id, 'Group detail id does not match the selected group')
  expect(detail.accessType === 'owner' && !detail.isDefault, 'Group detail must remain owner and non-default')
  expect(detail.ownerSystemAccountId === selectedGroup.ownerSystemAccountId, 'Group owner changed between list and detail')
  expect(detail.name === selectedGroup.name, 'Group name changed between list and detail')
  expect(detail.providerCode === selectedGroup.providerCode, 'Group provider changed between list and detail')

  const providersUrl = endpointUrl(normalizedConfig.baseUrl, '/providers/options', {
    systemAccountId: normalizedConfig.systemAccountId
  })
  const providersData = await getEnvelopeData(providersUrl, normalizedConfig, 'providers/options')
  const providers = assertProviders(providersData)

  const modelsUrl = endpointUrl(normalizedConfig.baseUrl, '/providers/models/options', {
    systemAccountId: normalizedConfig.systemAccountId
  })
  const modelsData = await getEnvelopeData(modelsUrl, normalizedConfig, 'providers/models/options')
  const modelOptions = assertModelOptions(modelsData)
  const providerCodes = new Set(providers.map((provider) => provider.code))
  for (const option of modelOptions) {
    expect(providerCodes.has(option.providerCode), 'Model option references an unknown provider')
  }

  return {
    groupCount: groupList.items.length,
    selectedGroupId: detail.id,
    providerCount: providers.length,
    modelOptionCount: modelOptions.length
  }
}

export async function runRealGoManagementSmokeFromEnvironment(
  env: SmokeEnvironment = process.env,
  writeSummary: (message: string) => void = console.log
): Promise<RealGoManagementSmokeSummary> {
  const summary = await runRealGoManagementSmoke(loadRealGoManagementSmokeConfig(env))
  writeSummary(formatRealGoManagementSmokeSummary(summary))
  return summary
}

export function formatRealGoManagementSmokeSummary(summary: RealGoManagementSmokeSummary): string {
  return [
    'PLAN-0081 real Go management smoke passed',
    `groups=${summary.groupCount}`,
    `selectedGroupId=${summary.selectedGroupId}`,
    `providers=${summary.providerCount}`,
    `modelOptions=${summary.modelOptionCount}`
  ].join(' ')
}

function normalizeManagementApiBaseUrl(rawValue: string): string {
  let url: URL
  try {
    url = new URL(rawValue.trim())
  } catch {
    throw new Error(`${realGoManagementSmokeEnv.baseUrl} must be an absolute HTTP(S) URL`)
  }
  expect(url.protocol === 'http:' || url.protocol === 'https:', `${realGoManagementSmokeEnv.baseUrl} must use HTTP or HTTPS`)
  expect(!url.username && !url.password, `${realGoManagementSmokeEnv.baseUrl} must not contain credentials`)
  expect(!url.search && !url.hash, `${realGoManagementSmokeEnv.baseUrl} must not contain query or fragment`)

  const pathname = url.pathname.replace(/\/+$/, '')
  url.pathname = pathname.endsWith(managementApiPrefix)
    ? pathname
    : `${pathname}${managementApiPrefix}`.replace(/\/{2,}/g, '/')
  return url.toString().replace(/\/+$/, '')
}

function endpointUrl(
  baseUrl: string,
  path: string,
  params: Record<string, string | undefined>
): URL {
  const url = new URL(`${baseUrl}${path}`)
  for (const [key, value] of Object.entries(params)) {
    if (value) {
      url.searchParams.set(key, value)
    }
  }
  return url
}

async function getEnvelopeData(
  url: URL,
  config: RealGoManagementSmokeConfig & { timeoutMs: number },
  label: string
): Promise<unknown> {
  let response: Response
  try {
    response = await fetch(url, {
      method: 'GET',
      headers: {
        accept: 'application/json',
        cookie: config.cookie,
        'user-agent': smokeUserAgent,
        'x-juhe-ai-smoke': smokeHeaderValue
      },
      redirect: 'error',
      signal: AbortSignal.timeout(config.timeoutMs)
    })
  } catch (error) {
    const errorName = error instanceof Error && error.name ? error.name : 'transport error'
    throw new Error(`${label} request failed: ${errorName}`)
  }

  if (!response.ok) {
    await response.body?.cancel()
    throw new Error(`${label} failed with HTTP ${response.status}`)
  }
  expect(response.headers.get('cache-control') === 'no-store', `${label} must return Cache-Control: no-store`)

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

function assertGroupList(value: unknown): GroupListResult {
  expect(isRecord(value), 'groups list data must be an object')
  expect(Array.isArray(value.items), 'groups list items must be an array')
  expect(isNonNegativeInteger(value.total), 'groups list total must be a non-negative integer')
  expect(typeof value.hasMore === 'boolean', 'groups list hasMore must be boolean')
  expect(value.page === 1, 'groups list page must be 1')
  expect(value.pageSize === 500, 'groups list pageSize must be 500')
  expect(isRecord(value.runtimeSnapshot), 'groups list runtimeSnapshot must be an object')
  expect(
    typeof value.runtimeSnapshot.accountConcurrencyAvailable === 'boolean',
    'groups list runtimeSnapshot.accountConcurrencyAvailable must be boolean'
  )
  expect(value.total >= value.items.length, 'groups list total must cover returned items')
  return value as unknown as GroupListResult
}

function selectOwnerNonDefaultGroup(items: unknown[], requestedGroupId?: string): GroupRecord {
  const groups = items.map((item, index) => assertGroup(item, `groups list item ${index}`))
  if (requestedGroupId) {
    const requested = groups.find((group) => group.id === requestedGroupId)
    expect(requested, `Configured group ${requestedGroupId} was not returned by groups list`)
    expect(requested.accessType === 'owner' && !requested.isDefault, 'Configured group must be owner and non-default')
    return requested
  }

  const selected = groups.find((group) => group.accessType === 'owner' && !group.isDefault)
  expect(selected, 'No owner non-default group was returned; set up one before running this smoke')
  return selected
}

function assertGroupDetail(value: unknown): GroupDetailRecord {
  const group = assertGroup(value, 'group detail')
  expect(isRecord(value) && Array.isArray(value.accountIds), 'group detail accountIds must be an array')
  assertStringArray(value.accountIds, 'group detail accountIds')
  return value as unknown as GroupDetailRecord
}

function assertGroup(value: unknown, label: string): GroupRecord {
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.ownerSystemAccountId), `${label}.ownerSystemAccountId must be a non-empty string`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(isNonEmptyString(value.providerCode), `${label}.providerCode must be a non-empty string`)
  expect(typeof value.enabled === 'boolean', `${label}.enabled must be boolean`)
  expect(typeof value.isDefault === 'boolean', `${label}.isDefault must be boolean`)
  expect(
    value.groupType === 'personal' || value.groupType === 'high_concurrency',
    `${label}.groupType must be personal or high_concurrency`
  )
  expect(value.accessType === 'owner' || value.accessType === 'authorized', `${label}.accessType is invalid`)
  expect(isRecord(value.accountStats), `${label}.accountStats must be an object`)
  expect(isRecord(value.permissions), `${label}.permissions must be an object`)
  return value as unknown as GroupRecord
}

function assertProviders(value: unknown): ProviderRecord[] {
  expect(Array.isArray(value) && value.length > 0, 'providers/options data must be a non-empty array')
  return value.map((item, index) => {
    const label = `providers/options item ${index}`
    expect(isRecord(item), `${label} must be an object`)
    for (const field of [
      'id',
      'code',
      'name',
      'defaultProtocolProfileId',
      'protocolCode',
      'protocolVersion',
      'baseUrl',
      'defaultHealthCheckModel'
    ]) {
      expect(isNonEmptyString(item[field]), `${label}.${field} must be a non-empty string`)
    }
    expect(typeof item.enabled === 'boolean', `${label}.enabled must be boolean`)
    assertStringArray(item.defaultSupportedModels, `${label}.defaultSupportedModels`)
    assertStringArray(item.accountTypes, `${label}.accountTypes`)
    assertStringArray(item.capabilities, `${label}.capabilities`)
    expect(Array.isArray(item.protocolProfiles) && item.protocolProfiles.length > 0, `${label}.protocolProfiles must be non-empty`)
    expect(
      item.protocolProfiles.some((profile) => isRecord(profile) && profile.id === item.defaultProtocolProfileId),
      `${label}.defaultProtocolProfileId must reference a protocol profile`
    )
    return item as unknown as ProviderRecord
  })
}

function assertModelOptions(value: unknown): ModelOptionRecord[] {
  expect(Array.isArray(value) && value.length > 0, 'providers/models/options data must be a non-empty array')
  return value.map((item, index) => {
    const label = `providers/models/options item ${index}`
    expect(isRecord(item), `${label} must be an object`)
    expect(isNonEmptyString(item.providerCode), `${label}.providerCode must be a non-empty string`)
    expect(isNonEmptyString(item.model), `${label}.model must be a non-empty string`)
    for (const field of ['supportedApiProtocols', 'supportedServiceTiers', 'supportedReasoningEfforts']) {
      if (Object.hasOwn(item, field)) {
        assertStringArray(item[field], `${label}.${field}`)
      }
    }
    if (Object.hasOwn(item, 'defaultReasoningEffort')) {
      expect(isNonEmptyString(item.defaultReasoningEffort), `${label}.defaultReasoningEffort must be a non-empty string`)
    }
    return item as unknown as ModelOptionRecord
  })
}

function requiredEnvironmentValue(env: SmokeEnvironment, name: string): string {
  const value = env[name]?.trim()
  if (!value) {
    throw new Error(`Missing required environment variable: ${name}`)
  }
  return value
}

function optionalEnvironmentValue(env: SmokeEnvironment, name: string): string | undefined {
  return env[name]?.trim() || undefined
}

function assertStringArray(value: unknown, label: string): asserts value is string[] {
  expect(Array.isArray(value), `${label} must be an array`)
  expect(value.every((item) => typeof item === 'string'), `${label} must contain only strings`)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function isNonNegativeInteger(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 0
}

function expect(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function isMainModule(): boolean {
  const entryPath = process.argv[1]
  return Boolean(entryPath && import.meta.url === pathToFileURL(entryPath).href)
}

if (isMainModule()) {
  try {
    await runRealGoManagementSmokeFromEnvironment()
  } catch (error) {
    console.error(error instanceof Error ? error.message : 'PLAN-0081 real Go management smoke failed')
    process.exitCode = 1
  }
}
