import { randomUUID } from 'node:crypto'
import { pathToFileURL } from 'node:url'

export const realGoManagementSmokeEnv = {
  allowGroupMutations: 'JUHE_REAL_GO_MANAGEMENT_ALLOW_GROUP_MUTATIONS',
  baseUrl: 'JUHE_REAL_GO_MANAGEMENT_BASE_URL',
  cookie: 'JUHE_REAL_GO_MANAGEMENT_COOKIE',
  groupId: 'JUHE_REAL_GO_MANAGEMENT_GROUP_ID',
  providerCode: 'JUHE_REAL_GO_MANAGEMENT_GROUP_PROVIDER_CODE',
  systemAccountId: 'JUHE_REAL_GO_MANAGEMENT_SYSTEM_ACCOUNT_ID',
  timeoutMs: 'JUHE_REAL_GO_MANAGEMENT_TIMEOUT_MS'
} as const

const managementApiPrefix = '/__aisys__/api'
const smokeUserAgent = 'juhe-ai-plan0081-real-go-management-smoke/1.0'
const smokeHeaderValue = 'plan0081-real-go-management'
const temporaryGroupNamePrefix = 'PLAN-0081 real Go management smoke '
const temporaryGroupDescription = 'PLAN-0081 W5 group CRUD real Go smoke'
const defaultTimeoutMs = 15_000
const maximumTimeoutMs = 2_147_483_647
const mutationSchedulingPolicy = {
  defaultSoftConcurrency: 7,
  maxQueueWaitMs: 45_000,
  clientIpConcurrencyLimit: 3,
  clientIpConcurrencyOverflowMode: 'queue',
  imageLaneMaxConcurrency: 2
} as const

export type SmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoManagementSmokeConfig {
  allowGroupMutations?: boolean
  baseUrl: string
  cookie: string
  groupId?: string
  providerCode?: string
  systemAccountId?: string
  timeoutMs?: number
}

export interface RealGoManagementSmokeSummary {
  groupCount: number
  selectedGroupId: string
  providerCount: number
  modelOptionCount: number
}

interface NormalizedRealGoManagementSmokeConfig extends RealGoManagementSmokeConfig {
  allowGroupMutations: boolean
  timeoutMs: number
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
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: 'personal' | 'high_concurrency'
  accessType: 'owner' | 'authorized'
  accountStats: Record<string, unknown>
  permissions: Record<string, unknown>
  schedulingPolicy?: Record<string, unknown>
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

interface TemporaryGroupIdentity {
  id: string
  name: string
  providerCode: string
  ownerSystemAccountId?: string
  cleanupNames: string[]
}

interface ReadOnlySmokeResult {
  selectedGroup: GroupRecord
  providers: ProviderRecord[]
  summary: RealGoManagementSmokeSummary
}

export function loadRealGoManagementSmokeConfig(
  env: SmokeEnvironment = process.env
): RealGoManagementSmokeConfig {
  const baseUrl = requiredEnvironmentValue(env, realGoManagementSmokeEnv.baseUrl)
  const cookie = requiredEnvironmentValue(env, realGoManagementSmokeEnv.cookie)
  expect(!/[\r\n]/.test(cookie), `${realGoManagementSmokeEnv.cookie} must be a single Cookie header line`)

  return {
    allowGroupMutations: optionalBinaryFlag(env, realGoManagementSmokeEnv.allowGroupMutations),
    baseUrl: normalizeManagementApiBaseUrl(baseUrl),
    cookie,
    groupId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.groupId),
    providerCode: optionalEnvironmentValue(env, realGoManagementSmokeEnv.providerCode),
    systemAccountId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.systemAccountId),
    timeoutMs: optionalPositiveIntegerEnvironmentValue(env, realGoManagementSmokeEnv.timeoutMs)
  }
}

export async function runRealGoManagementSmoke(
  config: RealGoManagementSmokeConfig
): Promise<RealGoManagementSmokeSummary> {
  const normalizedConfig = normalizeConfig(config)
  let createdGroup: TemporaryGroupIdentity | undefined
  let primaryError: unknown
  let summary: RealGoManagementSmokeSummary | undefined

  try {
    const readOnlyResult = await runReadOnlySmoke(normalizedConfig)
    summary = readOnlyResult.summary
    if (normalizedConfig.allowGroupMutations) {
      await runGroupMutationSmoke(normalizedConfig, readOnlyResult, (identity) => {
        createdGroup = identity
      })
    }
  } catch (error) {
    primaryError = error
  }

  let cleanupError: unknown
  if (createdGroup) {
    try {
      await cleanupTemporaryGroup(normalizedConfig, createdGroup)
    } catch (error) {
      cleanupError = error
    }
  }

  throwSmokeErrors(primaryError, cleanupError)
  expect(summary, 'Smoke summary was not produced')
  return summary
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

async function runReadOnlySmoke(
  config: NormalizedRealGoManagementSmokeConfig
): Promise<ReadOnlySmokeResult> {
  const listData = await getEnvelopeData(groupsListUrl(config), config, 'groups list')
  const groupList = assertGroupList(listData)
  const selectedGroup = selectOwnerNonDefaultGroup(groupList.items, config.groupId)

  const detailData = await getEnvelopeData(groupDetailUrl(config, selectedGroup.id), config, 'group detail')
  const detail = assertGroupDetail(detailData)
  assertTemporaryGroupIdentity(detail, selectedGroup, 'group detail')

  const providersUrl = endpointUrl(config.baseUrl, '/providers/options', {
    systemAccountId: config.systemAccountId
  })
  const providersData = await getEnvelopeData(providersUrl, config, 'providers/options')
  const providers = assertProviders(providersData)

  const modelsUrl = endpointUrl(config.baseUrl, '/providers/models/options', {
    systemAccountId: config.systemAccountId
  })
  const modelsData = await getEnvelopeData(modelsUrl, config, 'providers/models/options')
  const modelOptions = assertModelOptions(modelsData)
  const providerCodes = new Set(providers.map((provider) => provider.code))
  for (const option of modelOptions) {
    expect(providerCodes.has(option.providerCode), 'Model option references an unknown provider')
  }

  return {
    selectedGroup,
    providers,
    summary: {
      groupCount: groupList.items.length,
      selectedGroupId: detail.id,
      providerCount: providers.length,
      modelOptionCount: modelOptions.length
    }
  }
}

async function runGroupMutationSmoke(
  config: NormalizedRealGoManagementSmokeConfig,
  readOnlyResult: ReadOnlySmokeResult,
  registerCreatedGroup: (identity: TemporaryGroupIdentity) => void
): Promise<void> {
  const provider = selectMutationProvider(config.providerCode, readOnlyResult.selectedGroup, readOnlyResult.providers)
  const expectedName = `${temporaryGroupNamePrefix}${randomUUID()}`
  const createData = await requestEnvelopeData(
    endpointUrl(config.baseUrl, '/groups', { systemAccountId: config.systemAccountId }),
    config,
    'temporary group create',
    {
      method: 'POST',
      body: {
        name: expectedName,
        providerCode: provider.code,
        enabled: true,
        groupType: 'personal'
      },
      expectedStatus: 201
    }
  )
  const createdGroup = assertCreatedGroup(createData, expectedName, provider.code)
  const identity: TemporaryGroupIdentity = {
    id: createdGroup.id,
    name: expectedName,
    providerCode: provider.code,
    ownerSystemAccountId: createdGroup.ownerSystemAccountId,
    cleanupNames: [expectedName]
  }
  registerCreatedGroup(identity)

  const listData = await getEnvelopeData(groupsListUrl(config), config, 'groups list after create')
  const groupList = assertGroupList(listData)
  const listedGroupValue = groupList.items.find((item) => isRecord(item) && item.id === identity.id)
  expect(listedGroupValue, 'Temporary group was not returned by groups list')
  const listedGroup = assertGroup(listedGroupValue, 'temporary group list item')
  assertTemporaryGroupIdentity(listedGroup, identity, 'temporary group list item')
  expect(listedGroup.groupType === 'personal', 'Temporary group list item must be personal before PATCH')
  identity.ownerSystemAccountId = listedGroup.ownerSystemAccountId

  const detailData = await getEnvelopeData(groupDetailUrl(config, identity.id), config, 'temporary group detail')
  const detail = assertGroupDetail(detailData)
  assertTemporaryGroupIdentity(detail, identity, 'temporary group detail')
  expect(detail.groupType === 'personal', 'Temporary group detail must be personal before PATCH')

  const patchedName = `${expectedName} updated`
  identity.cleanupNames = [expectedName, patchedName]
  const patchData = await requestEnvelopeData(
    groupDetailUrl(config, identity.id),
    config,
    'temporary group PATCH',
    {
      method: 'PATCH',
      body: {
        name: patchedName,
        description: temporaryGroupDescription,
        groupType: 'high_concurrency',
        schedulingPolicy: mutationSchedulingPolicy
      },
      expectedStatus: 200
    }
  )
  const patchedGroup = assertGroupDetail(patchData)
  assertPatchedTemporaryGroup(patchedGroup, identity, patchedName, 'temporary group PATCH response')
  identity.name = patchedName
  identity.cleanupNames = [patchedName]

  const patchedDetailData = await getEnvelopeData(
    groupDetailUrl(config, identity.id),
    config,
    'temporary group detail after PATCH'
  )
  const patchedDetail = assertGroupDetail(patchedDetailData)
  assertPatchedTemporaryGroup(patchedDetail, identity, patchedName, 'temporary group detail after PATCH')

  await expectResponseStatus(
    groupDetailUrl(config, identity.id),
    config,
    'temporary group DELETE',
    { method: 'DELETE', expectedStatuses: [204] }
  )
  await expectResponseStatus(
    groupDetailUrl(config, identity.id),
    config,
    'temporary group GET after DELETE',
    { method: 'GET', expectedStatuses: [404] }
  )

}

async function cleanupTemporaryGroup(
  config: NormalizedRealGoManagementSmokeConfig,
  identity: TemporaryGroupIdentity
): Promise<void> {
  const detailUrl = groupDetailUrl(config, identity.id)
  const response = await sendRequest(detailUrl, config, 'temporary group cleanup check', { method: 'GET' })
  if (response.status === 404) {
    await response.body?.cancel()
    assertNoStore(response, 'temporary group cleanup check')
    return
  }
  if (response.status !== 200) {
    await response.body?.cancel()
    throw new Error(`temporary group cleanup check failed with HTTP ${response.status}`)
  }

  const detailData = await parseEnvelopeData(response, 'temporary group cleanup check')
  const detail = assertGroupDetail(detailData)
  assertTemporaryGroupIdentity(detail, identity, 'temporary group cleanup check', identity.cleanupNames)

  await expectResponseStatus(
    detailUrl,
    config,
    'temporary group cleanup DELETE',
    { method: 'DELETE', expectedStatuses: [204, 404] }
  )
}

function normalizeConfig(config: RealGoManagementSmokeConfig): NormalizedRealGoManagementSmokeConfig {
  expect(typeof config.cookie === 'string' && config.cookie.trim().length > 0, 'Cookie header must not be empty')
  expect(!/[\r\n]/.test(config.cookie), 'Cookie header must be a single line')
  expect(
    config.allowGroupMutations === undefined || typeof config.allowGroupMutations === 'boolean',
    'Smoke mutation flag must be boolean'
  )
  const timeoutMs = config.timeoutMs ?? defaultTimeoutMs
  expect(
    Number.isSafeInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= maximumTimeoutMs,
    `Smoke timeout must be a positive integer no greater than ${maximumTimeoutMs}`
  )
  if (config.providerCode !== undefined) {
    expect(isNonEmptyString(config.providerCode), 'Smoke provider code must not be empty')
  }

  return {
    ...config,
    allowGroupMutations: config.allowGroupMutations ?? false,
    baseUrl: normalizeManagementApiBaseUrl(config.baseUrl),
    groupId: config.groupId?.trim() || undefined,
    providerCode: config.providerCode?.trim() || undefined,
    systemAccountId: config.systemAccountId?.trim() || undefined,
    timeoutMs
  }
}

function selectMutationProvider(
  configuredProviderCode: string | undefined,
  selectedGroup: GroupRecord,
  providers: ProviderRecord[]
): ProviderRecord {
  const requestedCode = configuredProviderCode ?? selectedGroup.providerCode
  const provider = providers.find((item) => item.code === requestedCode)
  expect(provider, `Mutation provider ${requestedCode} was not returned by providers/options`)
  expect(provider.enabled, `Mutation provider ${requestedCode} must be enabled`)
  return provider
}

function assertCreatedGroup(value: unknown, expectedName: string, expectedProviderCode: string): TemporaryGroupIdentity {
  expect(isRecord(value), 'temporary group create data must be an object')
  expect(isNonEmptyString(value.id), 'temporary group create data.id must be a non-empty string')
  expect(value.name === expectedName, 'Temporary group create name does not match')
  expect(value.providerCode === expectedProviderCode, 'Temporary group create provider does not match')
  expect(value.enabled === true, 'Temporary group create must be enabled')
  expect(value.isDefault === false, 'Temporary group create must be non-default')
  expect(value.groupType === 'personal', 'Temporary group create must be personal')
  if (Object.hasOwn(value, 'accessType')) {
    expect(value.accessType === 'owner', 'Temporary group create access must be owner')
  }
  const ownerSystemAccountId = isNonEmptyString(value.ownerSystemAccountId)
    ? value.ownerSystemAccountId
    : isNonEmptyString(value.systemAccountId)
      ? value.systemAccountId
      : undefined
  return {
    id: value.id,
    name: expectedName,
    providerCode: expectedProviderCode,
    ownerSystemAccountId,
    cleanupNames: [expectedName]
  }
}

function assertTemporaryGroupIdentity(
  group: GroupRecord,
  expected: TemporaryGroupIdentity,
  label: string,
  allowedNames: readonly string[] = [expected.name]
): void {
  expect(group.id === expected.id, `${label} id does not match`)
  expect(allowedNames.includes(group.name), `${label} name does not match`)
  expect(group.providerCode === expected.providerCode, `${label} provider does not match`)
  expect(isNonEmptyString(group.ownerSystemAccountId), `${label} owner must be present`)
  if (expected.ownerSystemAccountId) {
    expect(group.ownerSystemAccountId === expected.ownerSystemAccountId, `${label} owner does not match`)
  }
  expect(!group.isDefault, `${label} must be non-default`)
  expect(group.accessType === 'owner', `${label} access must be owner`)
}

function assertPatchedTemporaryGroup(
  group: GroupRecord,
  expected: TemporaryGroupIdentity,
  patchedName: string,
  label: string
): void {
  assertTemporaryGroupIdentity(group, expected, label, [patchedName])
  expect(group.description === temporaryGroupDescription, `${label} description does not match`)
  expect(group.groupType === 'high_concurrency', `${label} must be high_concurrency`)
  const policy = group.schedulingPolicy
  expect(isRecord(policy), `${label}.schedulingPolicy must be an object`)
  for (const [field, value] of Object.entries(mutationSchedulingPolicy)) {
    expect(policy[field] === value, `${label}.schedulingPolicy.${field} does not match`)
  }
}

function groupsListUrl(config: NormalizedRealGoManagementSmokeConfig): URL {
  return endpointUrl(config.baseUrl, '/groups', {
    page: '1',
    pageSize: '500',
    systemAccountId: config.systemAccountId
  })
}

function groupDetailUrl(config: NormalizedRealGoManagementSmokeConfig, groupId: string): URL {
  return endpointUrl(config.baseUrl, `/groups/${encodeURIComponent(groupId)}`, {
    systemAccountId: config.systemAccountId
  })
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
  config: NormalizedRealGoManagementSmokeConfig,
  label: string
): Promise<unknown> {
  return requestEnvelopeData(url, config, label, { method: 'GET', expectedStatus: 200 })
}

async function requestEnvelopeData(
  url: URL,
  config: NormalizedRealGoManagementSmokeConfig,
  label: string,
  options: {
    method: 'GET' | 'POST' | 'PATCH'
    body?: Record<string, unknown>
    expectedStatus: number
  }
): Promise<unknown> {
  const response = await sendRequest(url, config, label, options)
  if (response.status !== options.expectedStatus) {
    await response.body?.cancel()
    throw new Error(`${label} failed with HTTP ${response.status}`)
  }
  return parseEnvelopeData(response, label)
}

async function expectResponseStatus(
  url: URL,
  config: NormalizedRealGoManagementSmokeConfig,
  label: string,
  options: {
    method: 'GET' | 'DELETE'
    expectedStatuses: number[]
  }
): Promise<number> {
  const response = await sendRequest(url, config, label, options)
  await response.body?.cancel()
  if (!options.expectedStatuses.includes(response.status)) {
    throw new Error(`${label} failed with HTTP ${response.status}`)
  }
  assertNoStore(response, label)
  return response.status
}

async function sendRequest(
  url: URL,
  config: NormalizedRealGoManagementSmokeConfig,
  label: string,
  options: {
    method: 'GET' | 'POST' | 'PATCH' | 'DELETE'
    body?: Record<string, unknown>
  }
): Promise<Response> {
  const headers: Record<string, string> = {
    accept: 'application/json',
    cookie: config.cookie,
    'user-agent': smokeUserAgent,
    'x-juhe-ai-smoke': smokeHeaderValue
  }
  if (options.body) {
    headers['content-type'] = 'application/json'
  }

  try {
    return await fetch(url, {
      method: options.method,
      headers,
      body: options.body ? JSON.stringify(options.body) : undefined,
      redirect: 'error',
      signal: AbortSignal.timeout(config.timeoutMs)
    })
  } catch (error) {
    const errorName = error instanceof Error && error.name ? error.name : 'transport error'
    throw new Error(`${label} request failed: ${errorName}`)
  }
}

async function parseEnvelopeData(response: Response, label: string): Promise<unknown> {
  assertNoStore(response, label)
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

function assertNoStore(response: Response, label: string): void {
  expect(response.headers.get('cache-control') === 'no-store', `${label} must return Cache-Control: no-store`)
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
  return group as GroupDetailRecord
}

function assertGroup(value: unknown, label: string): GroupRecord {
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.ownerSystemAccountId), `${label}.ownerSystemAccountId must be a non-empty string`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(isNonEmptyString(value.providerCode), `${label}.providerCode must be a non-empty string`)
  if (Object.hasOwn(value, 'description')) {
    expect(typeof value.description === 'string', `${label}.description must be a string`)
  }
  expect(typeof value.enabled === 'boolean', `${label}.enabled must be boolean`)
  expect(typeof value.isDefault === 'boolean', `${label}.isDefault must be boolean`)
  expect(
    value.groupType === 'personal' || value.groupType === 'high_concurrency',
    `${label}.groupType must be personal or high_concurrency`
  )
  if (value.groupType === 'high_concurrency') {
    expect(isRecord(value.schedulingPolicy), `${label}.schedulingPolicy must be an object for high_concurrency`)
  }
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

function optionalBinaryFlag(env: SmokeEnvironment, name: string): boolean {
  const value = optionalEnvironmentValue(env, name)
  if (value === undefined || value === '0') {
    return false
  }
  if (value === '1') {
    return true
  }
  throw new Error(`${name} must be 0 or 1`)
}

function optionalPositiveIntegerEnvironmentValue(env: SmokeEnvironment, name: string): number | undefined {
  const value = optionalEnvironmentValue(env, name)
  if (value === undefined) {
    return undefined
  }
  expect(/^[1-9]\d*$/.test(value), `${name} must be a positive integer`)
  const parsed = Number(value)
  expect(Number.isSafeInteger(parsed) && parsed <= maximumTimeoutMs, `${name} must not exceed ${maximumTimeoutMs}`)
  return parsed
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

function throwSmokeErrors(primaryError: unknown, cleanupError: unknown): void {
  if (primaryError && cleanupError) {
    const primary = safeError(primaryError, 'PLAN-0081 real Go management smoke failed')
    const cleanup = safeError(cleanupError, 'Temporary group cleanup failed')
    throw new AggregateError(
      [primary, cleanup],
      `${primary.message}; cleanup failed: ${cleanup.message}`
    )
  }
  if (primaryError) {
    throw safeError(primaryError, 'PLAN-0081 real Go management smoke failed')
  }
  if (cleanupError) {
    throw safeError(cleanupError, 'Temporary group cleanup failed')
  }
}

function safeError(error: unknown, fallbackMessage: string): Error {
  return error instanceof Error ? error : new Error(fallbackMessage)
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
