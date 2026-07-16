import { randomUUID } from 'node:crypto'
import { pathToFileURL } from 'node:url'

export const realGoManagementSmokeEnv = {
  accountId: 'JUHE_REAL_GO_MANAGEMENT_ACCOUNT_ID',
  allowExternalIntegrationSourceMutations: 'JUHE_REAL_GO_MANAGEMENT_ALLOW_EXTERNAL_INTEGRATION_SOURCE_MUTATIONS',
  allowGroupMutations: 'JUHE_REAL_GO_MANAGEMENT_ALLOW_GROUP_MUTATIONS',
  baseUrl: 'JUHE_REAL_GO_MANAGEMENT_BASE_URL',
  clientIpHash: 'JUHE_REAL_GO_MANAGEMENT_CLIENT_IP_HASH',
  cookie: 'JUHE_REAL_GO_MANAGEMENT_COOKIE',
  externalIntegrationSourceId: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_ID',
  externalIntegrationSourceTokenId: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_TOKEN_ID',
  externalIntegrationSourceMutationFixtureConfirmation: 'JUHE_REAL_GO_MANAGEMENT_EXTERNAL_INTEGRATION_SOURCE_MUTATION_FIXTURE_CONFIRMATION',
  groupId: 'JUHE_REAL_GO_MANAGEMENT_GROUP_ID',
  providerCode: 'JUHE_REAL_GO_MANAGEMENT_GROUP_PROVIDER_CODE',
  publicApiLogId: 'JUHE_REAL_GO_MANAGEMENT_PUBLIC_API_LOG_ID',
  requireClientIpDetail: 'JUHE_REAL_GO_MANAGEMENT_REQUIRE_CLIENT_IP_DETAIL',
  routeStrategyId: 'JUHE_REAL_GO_MANAGEMENT_ROUTE_STRATEGY_ID',
  systemAccountId: 'JUHE_REAL_GO_MANAGEMENT_SYSTEM_ACCOUNT_ID',
  temporaryExternalIntegrationSourceId: 'JUHE_REAL_GO_MANAGEMENT_TEMP_EXTERNAL_INTEGRATION_SOURCE_ID',
  timeoutMs: 'JUHE_REAL_GO_MANAGEMENT_TIMEOUT_MS'
} as const

const managementApiPrefix = '/__aisys__/api'
const smokeUserAgent = 'juhe-ai-plan0081-real-go-management-smoke/1.0'
const smokeHeaderValue = 'plan0081-real-go-management'
const temporaryGroupNamePrefix = 'PLAN-0081 real Go management smoke '
const temporaryGroupDescription = 'PLAN-0081 W5 group CRUD real Go smoke'
const externalIntegrationSourceMutationFixtureNamePrefix = 'PLAN-0081 external source smoke fixture '
const externalIntegrationSourceMutationFixtureNotes = 'PLAN-0081 external source smoke fixture'
const externalIntegrationSourceMutationFixtureConfirmationPrefix = 'plan0081-external-source-fixture-v1:'
const defaultTimeoutMs = 15_000
const maximumTimeoutMs = 2_147_483_647
const externalIntegrationSourceScopeOptions = [
  { value: 'juhe_ai_public:api_key_list:read', label: 'GET API Key 列表' },
  { value: 'juhe_ai_public:route_strategy_list:read', label: 'GET 路由策略列表' },
  { value: 'juhe_ai_public:group_list:read', label: 'GET 分组列表' },
  { value: 'juhe_ai_public:account_list:read', label: 'GET 账号列表' },
  { value: 'juhe_ai_public:api_key_add:write', label: 'POST API Key 新增' },
  { value: 'juhe_ai_public:api_key_update:write', label: 'POST API Key 修改' },
  { value: 'juhe_ai_public:api_key_delete:write', label: 'POST API Key 删除' },
  { value: 'juhe_ai_public:route_strategy_add:write', label: 'POST 路由策略新增' },
  { value: 'juhe_ai_public:route_strategy_update:write', label: 'POST 路由策略修改' },
  { value: 'juhe_ai_public:route_strategy_delete:write', label: 'POST 路由策略删除' },
  { value: 'juhe_ai_public:group_add:write', label: 'POST 分组新增' },
  { value: 'juhe_ai_public:group_update:write', label: 'POST 分组修改' },
  { value: 'juhe_ai_public:group_delete:write', label: 'POST 分组删除' },
  { value: 'juhe_ai_public:account_add:write', label: 'POST 账号新增' },
  { value: 'juhe_ai_public:account_update:write', label: 'POST 账号修改' },
  { value: 'juhe_ai_public:account_delete:write', label: 'POST 账号删除' }
] as const
const externalIntegrationSourceScopeSet = new Set<string>(
  externalIntegrationSourceScopeOptions.map((option) => option.value)
)
const externalIntegrationSourceRateLimitMaximumRules = 8
const externalIntegrationSourceRateLimitMaximumWindowSeconds = 86_400
const externalIntegrationSourceRateLimitMaximumRequests = 100_000
const externalIntegrationSourceListItemFieldSet = new Set([
  'id',
  'name',
  'status',
  'scopes',
  'rateLimits',
  'expiresAt',
  'notes',
  'lastUsedAt',
  'createdAt',
  'updatedAt',
  'tokenCount',
  'activeTokenCount',
  'primaryToken',
  'isBuiltIn'
])
const externalIntegrationSourceDetailFieldSet = new Set([
  'id',
  'name',
  'status',
  'scopes',
  'rateLimits',
  'expiresAt',
  'notes',
  'lastUsedAt',
  'createdAt',
  'updatedAt',
  'tokenCount',
  'activeTokenCount',
  'tokens',
  'isBuiltIn'
])
const externalIntegrationSourcePrimaryTokenFieldSet = new Set([
  'id',
  'name',
  'tokenPrefix',
  'tokenSuffix',
  'status',
  'scopes',
  'expiresAt',
  'lastUsedAt',
  'createdAt',
  'updatedAt',
  'revokedAt',
  'isBuiltIn'
])
const externalIntegrationSourceTokenSecretFieldSet = new Set(['token'])
const apiKeyListItemFieldSet = new Set([
  'id',
  'systemAccountId',
  'systemAccountName',
  'name',
  'description',
  'keyPrefix',
  'keySuffix',
  'status',
  'isDefault',
  'routeStrategyId',
  'routeStrategyName',
  'routeStrategyMode',
  'routeStrategyStatus',
  'expiresAt',
  'quotaLimits',
  'availabilitySchedule',
  'usage'
])
const externalIntegrationSourceApiDocContracts = externalIntegrationSourceScopeOptions.map((option) =>
  externalIntegrationSourceApiDocContract(option.value)
)
const accountTestEndpointModeValues = [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse'
] as const
const accountTestEndpointModeSet = new Set<string>(accountTestEndpointModeValues)
const mutationSchedulingPolicy = {
  defaultSoftConcurrency: 7,
  maxQueueWaitMs: 45_000,
  clientIpConcurrencyLimit: 3,
  clientIpConcurrencyOverflowMode: 'queue',
  imageLaneMaxConcurrency: 2
} as const

export type SmokeEnvironment = Readonly<Record<string, string | undefined>>

export interface RealGoManagementSmokeConfig {
  accountId?: string
  allowExternalIntegrationSourceMutations?: boolean
  allowGroupMutations?: boolean
  baseUrl: string
  clientIpHash?: string
  cookie: string
  externalIntegrationSourceId?: string
  externalIntegrationSourceTokenId?: string
  externalIntegrationSourceMutationFixtureConfirmation?: string
  groupId?: string
  providerCode?: string
  publicApiLogId?: string
  requireClientIpDetail?: boolean
  routeStrategyId?: string
  systemAccountId?: string
  temporaryExternalIntegrationSourceId?: string
  timeoutMs?: number
}

export interface RealGoManagementSmokeSummary {
  accountTestOptionsChecked: boolean
  adminApiKeyCount: number
  groupCount: number
  selectedGroupId: string
  providerCount: number
  modelOptionCount: number
  publicApiLogCount: number
  publicApiLogDetailChecked: boolean
  clientIpItemCount: number
  clientIpRangeReady: boolean
  clientIpDetailChecked: boolean
  routeStrategyCount: number
  routeStrategyDetailChecked: boolean
  selfApiKeyCount: number
  externalIntegrationSourceTokenSecretChecked: boolean
  externalIntegrationSourcePatchChecked: boolean
}

interface NormalizedRealGoManagementSmokeConfig extends RealGoManagementSmokeConfig {
  allowExternalIntegrationSourceMutations: boolean
  allowGroupMutations: boolean
  requireClientIpDetail: boolean
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

type RouteStrategyMode = 'normal' | 'hybrid_smart' | 'weighted' | 'failover' | 'round_robin'

interface RouteStrategyListItem {
  id: string
  systemAccountId: string
  systemAccountName: string
  mode: RouteStrategyMode
  status: 'active' | 'disabled'
  normalRoutingConfig?: unknown
  groupBindingPreview: unknown[]
  bindingCount: number
  apiKeyCount: number
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

type AccountTestEndpointMode = typeof accountTestEndpointModeValues[number]

interface AccountTestOptionModel {
  model: string
  supportedApiProtocols: string[]
  testEndpointModes: AccountTestEndpointMode[]
}

interface AccountTestOptionsRecord {
  accountId: string
  defaultModel: string
  models: AccountTestOptionModel[]
  testEndpointModes: AccountTestEndpointMode[]
  defaultTestEndpointMode: AccountTestEndpointMode
}

type ExternalIntegrationSourceStatus = 'active' | 'disabled'
type ExternalIntegrationSourceTokenStatus = 'active' | 'disabled' | 'revoked'

interface ExternalIntegrationSourcePrimaryToken {
  id: string
  name: string
  tokenPrefix: string
  tokenSuffix: string
  status: ExternalIntegrationSourceTokenStatus
  scopes: string[]
  expiresAt?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  revokedAt?: string
  isBuiltIn: boolean
}

interface ExternalIntegrationSourceListItem {
  id: string
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: Array<{
    windowSeconds: number
    maxRequests: number
  }>
  expiresAt?: string
  notes?: string
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
  tokenCount: number
  activeTokenCount: number
  primaryToken?: ExternalIntegrationSourcePrimaryToken
  isBuiltIn: boolean
}

interface ExternalIntegrationSourceListResult {
  items: ExternalIntegrationSourceListItem[]
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
}

type ExternalIntegrationSourceDetail = Omit<ExternalIntegrationSourceListItem, 'primaryToken'> & {
  tokens: ExternalIntegrationSourcePrimaryToken[]
}

type PublicApiLogCaptureStatus = 'complete' | 'truncated' | 'empty' | 'dropped'

interface PublicApiLogSummary {
  id: string
  traceId?: string
  sourceRefId?: string
  sourceName?: string
  tokenId?: string
  tokenName?: string
  tokenPrefix?: string
  isTestToken: boolean
  method: string
  path: string
  queryString?: string
  clientIp?: string
  userAgent?: string
  statusCode?: number
  success: boolean
  durationMs?: number
  requestSizeBytes: number
  responseSizeBytes: number
  requestCaptureStatus: PublicApiLogCaptureStatus
  responseCaptureStatus: PublicApiLogCaptureStatus
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt: string
  createdAt: string
}

interface PublicApiLogListResult {
  items: PublicApiLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface ClientIPStatsRange {
  startDate: string
  endDate: string
  days: number
  maxDays: number
}

interface ClientIPStatsListResult {
  items: ClientIPStatsItem[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: ClientIPStatsRange
  rangeReady: boolean
}

interface ClientIPStatsItem {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  status: 'normal' | 'blacklisted' | 'allowlisted'
  rangeUsage: Record<string, unknown>
}

interface ClientIPAccountUsageItem {
  accountId: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  rangeUsage: Record<string, unknown>
}

interface ClientIPStatsDetailResult {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  items: ClientIPAccountUsageItem[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: ClientIPStatsRange
  rangeReady: boolean
}

interface ClientIPStatsDetailTarget {
  ipHash: string
  listItem: ClientIPStatsItem | undefined
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
  const externalIntegrationSourceId = optionalEnvironmentValue(env, realGoManagementSmokeEnv.externalIntegrationSourceId)
  const externalIntegrationSourceTokenId = optionalEnvironmentValue(env, realGoManagementSmokeEnv.externalIntegrationSourceTokenId)
  const temporaryExternalIntegrationSourceId = optionalEnvironmentValue(
    env,
    realGoManagementSmokeEnv.temporaryExternalIntegrationSourceId
  )
  const externalIntegrationSourceMutationFixtureConfirmation = optionalEnvironmentValue(
    env,
    realGoManagementSmokeEnv.externalIntegrationSourceMutationFixtureConfirmation
  )
  expect(
    Boolean(externalIntegrationSourceId) === Boolean(externalIntegrationSourceTokenId),
    `${realGoManagementSmokeEnv.externalIntegrationSourceId} and ${realGoManagementSmokeEnv.externalIntegrationSourceTokenId} must be configured together`
  )

  return {
    accountId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.accountId),
    allowExternalIntegrationSourceMutations: optionalBinaryFlag(
      env,
      realGoManagementSmokeEnv.allowExternalIntegrationSourceMutations
    ),
    allowGroupMutations: optionalBinaryFlag(env, realGoManagementSmokeEnv.allowGroupMutations),
    baseUrl: normalizeManagementApiBaseUrl(baseUrl),
    clientIpHash: optionalClientIPHashEnvironmentValue(env, realGoManagementSmokeEnv.clientIpHash),
    cookie,
    externalIntegrationSourceId,
    externalIntegrationSourceTokenId,
    externalIntegrationSourceMutationFixtureConfirmation,
    groupId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.groupId),
    providerCode: optionalEnvironmentValue(env, realGoManagementSmokeEnv.providerCode),
    publicApiLogId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.publicApiLogId),
    requireClientIpDetail: optionalBinaryFlag(env, realGoManagementSmokeEnv.requireClientIpDetail),
    routeStrategyId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.routeStrategyId),
    systemAccountId: optionalEnvironmentValue(env, realGoManagementSmokeEnv.systemAccountId),
    temporaryExternalIntegrationSourceId,
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
    if (normalizedConfig.allowExternalIntegrationSourceMutations) {
      await runExternalIntegrationSourceMutationSmoke(normalizedConfig)
      summary.externalIntegrationSourcePatchChecked = true
    }
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
    `providers=${summary.providerCount}`,
    `modelOptions=${summary.modelOptionCount}`,
    `adminApiKeyCount=${summary.adminApiKeyCount}`,
    `selfApiKeyCount=${summary.selfApiKeyCount}`,
    `externalIntegrationSourceTokenSecretChecked=${summary.externalIntegrationSourceTokenSecretChecked}`,
    `externalIntegrationSourcePatchChecked=${summary.externalIntegrationSourcePatchChecked}`,
    `clientIpItems=${summary.clientIpItemCount}`,
    `clientIpRangeReady=${summary.clientIpRangeReady}`,
    `clientIpDetailChecked=${summary.clientIpDetailChecked}`,
    `routeStrategies=${summary.routeStrategyCount}`,
    `routeStrategyDetailChecked=${summary.routeStrategyDetailChecked}`,
    `accountTestOptionsChecked=${summary.accountTestOptionsChecked}`,
    `publicApiLogCount=${summary.publicApiLogCount}`,
    `publicApiLogDetailChecked=${summary.publicApiLogDetailChecked}`
  ].join(' ')
}

async function runReadOnlySmoke(
  config: NormalizedRealGoManagementSmokeConfig
): Promise<ReadOnlySmokeResult> {
  const publicAPILogListData = await getEnvelopeData(
    publicAPILogsListUrl(config),
    config,
    'public API logs list'
  )
  const publicAPILogs = assertPublicAPILogList(publicAPILogListData)
  let publicApiLogDetailChecked = false
  if (config.publicApiLogId) {
    const publicAPILogDetailData = await getEnvelopeData(
      publicAPILogDetailUrl(config, config.publicApiLogId),
      config,
      'public API log detail'
    )
    assertPublicAPILogDetail(publicAPILogDetailData, config.publicApiLogId)
    publicApiLogDetailChecked = true
  }

  const externalIntegrationSourceScopesData = await getEnvelopeData(
    endpointUrl(config.baseUrl, '/external-integration-sources/scopes', {}),
    config,
    'external integration source scopes'
  )
  assertExternalIntegrationSourceScopes(externalIntegrationSourceScopesData)

  const externalIntegrationSourceApiDocsData = await getEnvelopeData(
    endpointUrl(config.baseUrl, '/external-integration-sources/api-docs', {}),
    config,
    'external integration source api docs'
  )
  assertExternalIntegrationSourceApiDocs(externalIntegrationSourceApiDocsData)

  const externalIntegrationSourceListData = await getEnvelopeData(
    externalIntegrationSourcesListUrl(config, 1),
    config,
    'external integration source list'
  )
  const externalIntegrationSourceFirstPage = assertExternalIntegrationSourceList(
    externalIntegrationSourceListData,
    1,
    20
  )
  const externalIntegrationSourceDetailTarget = config.externalIntegrationSourceId
    ? undefined
    : externalIntegrationSourceFirstPage.items.find((source) => !source.isBuiltIn)
  const externalIntegrationSourceDetailId = config.externalIntegrationSourceId
    ?? externalIntegrationSourceDetailTarget?.id
  let externalIntegrationSourceTokenSecretChecked = false
  if (externalIntegrationSourceDetailId) {
    const externalIntegrationSourceDetailData = await getEnvelopeData(
      externalIntegrationSourceDetailUrl(config, externalIntegrationSourceDetailId),
      config,
      'external integration source detail'
    )
    const detail = assertExternalIntegrationSourceDetail(
      externalIntegrationSourceDetailData,
      externalIntegrationSourceDetailTarget
    )
    expect(detail.id === externalIntegrationSourceDetailId, 'external integration source detail.id must match the requested source')
    if (config.externalIntegrationSourceTokenId) {
      const tokenSummary = detail.tokens.find((token) => token.id === config.externalIntegrationSourceTokenId)
      expect(tokenSummary, 'Configured external integration source token was not returned by source detail')
      const secretData = await getEnvelopeData(
        externalIntegrationSourceTokenSecretUrl(config, detail.id, tokenSummary.id),
        config,
        'external integration source token secret',
        true
      )
      assertExternalIntegrationSourceTokenSecret(secretData, tokenSummary)
      externalIntegrationSourceTokenSecretChecked = true
    }
  }
  if (externalIntegrationSourceFirstPage.hasMore) {
    const externalIntegrationSourceSecondPageData = await getEnvelopeData(
      externalIntegrationSourcesListUrl(config, 2),
      config,
      'external integration source list page 2'
    )
    const externalIntegrationSourceSecondPage = assertExternalIntegrationSourceList(
      externalIntegrationSourceSecondPageData,
      2,
      20
    )
    const firstPageIds = new Set(externalIntegrationSourceFirstPage.items.map((item) => item.id))
    for (const source of externalIntegrationSourceSecondPage.items) {
      expect(
        !firstPageIds.has(source.id),
        `external integration source list pages must not contain duplicate id ${source.id}`
      )
    }
    expect(
      externalIntegrationSourceSecondPage.pageUpperBound >= externalIntegrationSourceFirstPage.pageUpperBound,
      'external integration source list pageUpperBound must not decrease across pages'
    )
  }

  const listData = await getEnvelopeData(groupsListUrl(config), config, 'groups list')
  const groupList = assertGroupList(listData)
  const selectedGroup = selectOwnerNonDefaultGroup(groupList.items, config.groupId)

  const detailData = await getEnvelopeData(groupDetailUrl(config, selectedGroup.id), config, 'group detail')
  const detail = assertGroupDetail(detailData)
  assertTemporaryGroupIdentity(detail, selectedGroup, 'group detail')

  const routeStrategyListData = await getEnvelopeData(
    endpointUrl(config.baseUrl, '/route-strategies', {
      page: '1', pageSize: '200', systemAccountId: config.systemAccountId
    }),
    config,
    'route strategies list'
  )
  const routeStrategies = assertRouteStrategyList(routeStrategyListData)
  const selectedRouteStrategy = selectRouteStrategy(routeStrategies, config.routeStrategyId)
  let routeStrategyDetailChecked = false
  if (selectedRouteStrategy) {
    const routeStrategyDetailData = await getEnvelopeData(
      endpointUrl(config.baseUrl, `/route-strategies/${encodeURIComponent(selectedRouteStrategy.id)}`, {
        systemAccountId: config.systemAccountId
      }),
      config,
      'route strategy detail'
    )
    assertRouteStrategyDetail(routeStrategyDetailData, selectedRouteStrategy)
    routeStrategyDetailChecked = true
  }

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

  const adminApiKeyListData = await getEnvelopeData(
    endpointUrl(config.baseUrl, '/api-keys', {
      page: '1', pageSize: '20', status: 'all', systemAccountId: config.systemAccountId
    }),
    config,
    'admin API keys list'
  )
  const adminApiKeys = assertApiKeyList(adminApiKeyListData, 'admin')
  const selfApiKeyListData = await getEnvelopeData(
    endpointUrl(config.baseUrl, '/my-api-keys', { page: '1', pageSize: '20', status: 'all' }),
    config,
    'self API keys list'
  )
  const selfApiKeys = assertApiKeyList(selfApiKeyListData, 'self')

  let accountTestOptionsChecked = false
  if (config.accountId) {
    const accountTestOptionsData = await getEnvelopeData(
      endpointUrl(config.baseUrl, `/accounts/${encodeURIComponent(config.accountId)}/test-options`, {
        systemAccountId: config.systemAccountId
      }),
      config,
      'account test options'
    )
    assertAccountTestOptions(accountTestOptionsData, config.accountId)
    accountTestOptionsChecked = true
  }

  const clientIPStatsData = await getEnvelopeData(
    clientIPStatsListUrl(config),
    config,
    'client IP stats list'
  )
  const clientIPStats = assertClientIPStatsList(clientIPStatsData)
  const clientIPDetailRequested = config.requireClientIpDetail || config.clientIpHash !== undefined
  const clientIPDetailTarget = selectClientIPStatsDetailTarget(config, clientIPStats)
  let clientIpDetailChecked = false
  if (clientIPDetailTarget) {
    const clientIPDetailData = await getEnvelopeData(
      clientIPStatsDetailUrl(config, clientIPDetailTarget.ipHash, clientIPStats.range),
      config,
      'client IP stats detail'
    )
    const clientIPDetail = assertClientIPStatsDetail(
      clientIPDetailData,
      clientIPDetailTarget.ipHash,
      clientIPDetailTarget.listItem,
      clientIPStats.range
    )
    if (config.requireClientIpDetail) {
      expect(clientIPDetail.rangeReady, 'client IP stats detail is required but rangeReady is false')
      expect(clientIPDetail.items.length > 0, 'client IP stats detail is required but items is empty')
    }
    clientIpDetailChecked = true
  }
  expect(
    !clientIPDetailRequested || clientIpDetailChecked,
    `client IP detail is required but no verifiable target is available; set ${realGoManagementSmokeEnv.clientIpHash} to a known 64-character hexadecimal hash`
  )

  return {
    selectedGroup,
    providers,
    summary: {
      accountTestOptionsChecked,
      adminApiKeyCount: adminApiKeys.items.length,
      groupCount: groupList.items.length,
      selectedGroupId: detail.id,
      providerCount: providers.length,
      modelOptionCount: modelOptions.length,
      publicApiLogCount: publicAPILogs.items.length,
      publicApiLogDetailChecked,
      clientIpItemCount: clientIPStats.items.length,
      clientIpRangeReady: clientIPStats.rangeReady,
      clientIpDetailChecked,
      routeStrategyCount: routeStrategies.length,
      routeStrategyDetailChecked,
      selfApiKeyCount: selfApiKeys.items.length,
      externalIntegrationSourceTokenSecretChecked,
      externalIntegrationSourcePatchChecked: false
    }
  }
}

type ExternalIntegrationSourceMutableSnapshot = Pick<
  ExternalIntegrationSourceListItem,
  'name' | 'status' | 'scopes' | 'rateLimits'
> & {
  expiresAt: string | null
  notes: string | null
}

async function runExternalIntegrationSourceMutationSmoke(
  config: NormalizedRealGoManagementSmokeConfig
): Promise<void> {
  const sourceId = config.temporaryExternalIntegrationSourceId
  expect(sourceId, 'Temporary external integration source fixture ID is required')
  const detailUrl = externalIntegrationSourceDetailUrl(config, sourceId)
  const original = assertExternalIntegrationSourceDetail(
    await getEnvelopeData(detailUrl, config, 'temporary external integration source fixture detail')
  )
  expect(original.id === sourceId, 'Temporary external integration source fixture ID does not match its detail')
  expect(!original.isBuiltIn, 'Temporary external integration source fixture must not be built-in')
  expect(
    original.tokenCount === 0 && original.activeTokenCount === 0 && original.tokens.length === 0,
    'Temporary external integration source fixture must not contain any Token'
  )
  expect(
    original.name.startsWith(externalIntegrationSourceMutationFixtureNamePrefix),
    'Temporary external integration source fixture name marker does not match'
  )
  expect(
    original.notes === externalIntegrationSourceMutationFixtureNotes,
    'Temporary external integration source fixture notes marker does not match'
  )
  const originalSnapshot = externalIntegrationSourceMutableSnapshot(original)
  const runId = randomUUID()
  const patchedSnapshot: ExternalIntegrationSourceMutableSnapshot = {
    ...originalSnapshot,
    name: `PLAN-0081 external source smoke ${runId}`,
    notes: `PLAN-0081 external source smoke active ${runId}`
  }

  let mutationAttempted = false
  let primaryError: unknown
  try {
    mutationAttempted = true
    const patched = assertExternalIntegrationSourceDetail(
      await requestEnvelopeData(detailUrl, config, 'temporary external integration source PATCH', {
        method: 'PATCH',
        body: externalIntegrationSourceMutationPayload(patchedSnapshot),
        expectedStatus: 200
      })
    )
    expect(patched.id === sourceId, 'Temporary external integration source PATCH response ID does not match')
    assertExternalIntegrationSourceMutableSnapshot(
      patched,
      patchedSnapshot,
      'temporary external integration source PATCH response'
    )
    const readBack = assertExternalIntegrationSourceDetail(
      await getEnvelopeData(detailUrl, config, 'temporary external integration source detail after PATCH')
    )
    expect(readBack.id === sourceId, 'Temporary external integration source PATCH readback ID does not match')
    assertExternalIntegrationSourceMutableSnapshot(
      readBack,
      patchedSnapshot,
      'temporary external integration source detail after PATCH'
    )
  } catch (error) {
    primaryError = error
  }

  let cleanupError: unknown
  if (mutationAttempted) {
    try {
      const restoreFingerprintName = `${originalSnapshot.name}${externalIntegrationSourceRestoreFingerprintWhitespace()}`
      const restored = assertExternalIntegrationSourceDetail(
        await requestEnvelopeData(detailUrl, config, 'temporary external integration source restore PATCH', {
          method: 'PATCH',
          body: externalIntegrationSourceMutationPayload(originalSnapshot, restoreFingerprintName),
          expectedStatus: 200
        })
      )
      expect(restored.id === sourceId, 'Temporary external integration source restore response ID does not match')
      assertExternalIntegrationSourceMutableSnapshot(
        restored,
        originalSnapshot,
        'temporary external integration source restore response'
      )
      const restoredReadBack = assertExternalIntegrationSourceDetail(
        await getEnvelopeData(detailUrl, config, 'temporary external integration source detail after restore')
      )
      expect(restoredReadBack.id === sourceId, 'Temporary external integration source restore readback ID does not match')
      assertExternalIntegrationSourceMutableSnapshot(
        restoredReadBack,
        originalSnapshot,
        'temporary external integration source detail after restore'
      )
    } catch (error) {
      cleanupError = error
    }
  }

  if (primaryError && cleanupError) {
    const primary = safeError(primaryError, 'Temporary external integration source PATCH failed')
    const cleanup = safeError(cleanupError, 'Temporary external integration source restore failed')
    throw new AggregateError([primary, cleanup], `${primary.message}; restore failed: ${cleanup.message}`)
  }
  if (primaryError) throw safeError(primaryError, 'Temporary external integration source PATCH failed')
  if (cleanupError) throw safeError(cleanupError, 'Temporary external integration source restore failed')
}

function externalIntegrationSourceMutableSnapshot(
  detail: ExternalIntegrationSourceDetail
): ExternalIntegrationSourceMutableSnapshot {
  return {
    name: detail.name,
    status: detail.status,
    scopes: [...detail.scopes],
    rateLimits: detail.rateLimits.map((rule) => ({ ...rule })),
    expiresAt: detail.expiresAt ?? null,
    notes: detail.notes ?? null
  }
}

function externalIntegrationSourceMutationPayload(
  snapshot: ExternalIntegrationSourceMutableSnapshot,
  fingerprintName = snapshot.name
): Record<string, unknown> {
  return {
    name: fingerprintName,
    status: snapshot.status,
    scopes: snapshot.scopes,
    rateLimits: snapshot.rateLimits,
    expiresAt: snapshot.expiresAt,
    notes: snapshot.notes
  }
}

function externalIntegrationSourceRestoreFingerprintWhitespace(): string {
  return [...randomUUID().replaceAll('-', '')]
    .map((character) => Number.parseInt(character, 16) % 2 === 0 ? ' ' : '\t')
    .join('')
}

function assertExternalIntegrationSourceMutableSnapshot(
  detail: ExternalIntegrationSourceDetail,
  expected: ExternalIntegrationSourceMutableSnapshot,
  label: string
): void {
  expect(detail.name === expected.name, `${label}.name does not match`)
  expect(detail.status === expected.status, `${label}.status does not match`)
  expect(JSON.stringify(detail.scopes) === JSON.stringify(expected.scopes), `${label}.scopes do not match`)
  expect(JSON.stringify(detail.rateLimits) === JSON.stringify(expected.rateLimits), `${label}.rateLimits do not match`)
  expect((detail.expiresAt ?? null) === expected.expiresAt, `${label}.expiresAt does not match`)
  expect((detail.notes ?? null) === expected.notes, `${label}.notes do not match`)
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
    config.allowExternalIntegrationSourceMutations === undefined ||
      typeof config.allowExternalIntegrationSourceMutations === 'boolean',
    'Smoke external integration source mutation flag must be boolean'
  )
  expect(
    config.allowGroupMutations === undefined || typeof config.allowGroupMutations === 'boolean',
    'Smoke mutation flag must be boolean'
  )
  expect(
    config.requireClientIpDetail === undefined || typeof config.requireClientIpDetail === 'boolean',
    'Smoke client IP detail requirement flag must be boolean'
  )
  const timeoutMs = config.timeoutMs ?? defaultTimeoutMs
  expect(
    Number.isSafeInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= maximumTimeoutMs,
    `Smoke timeout must be a positive integer no greater than ${maximumTimeoutMs}`
  )
  if (config.providerCode !== undefined) {
    expect(isNonEmptyString(config.providerCode), 'Smoke provider code must not be empty')
  }
  if (config.clientIpHash !== undefined) {
    expect(
      typeof config.clientIpHash === 'string' && isClientIPHash(config.clientIpHash.trim()),
      'Smoke client IP hash must be a 64-character hexadecimal hash'
    )
  }
  const externalIntegrationSourceId = config.externalIntegrationSourceId?.trim() || undefined
  const externalIntegrationSourceTokenId = config.externalIntegrationSourceTokenId?.trim() || undefined
  const temporaryExternalIntegrationSourceId = config.temporaryExternalIntegrationSourceId?.trim() || undefined
  const externalIntegrationSourceMutationFixtureConfirmation =
    config.externalIntegrationSourceMutationFixtureConfirmation?.trim() || undefined
  expect(
    Boolean(externalIntegrationSourceId) === Boolean(externalIntegrationSourceTokenId),
    'Smoke external integration source ID and token ID must be configured together'
  )
  const allowExternalIntegrationSourceMutations = config.allowExternalIntegrationSourceMutations ?? false
  if (allowExternalIntegrationSourceMutations) {
    expect(
      temporaryExternalIntegrationSourceId !== undefined,
      'Smoke external integration source mutations require an explicit temporary fixture ID'
    )
    expect(
      externalIntegrationSourceMutationFixtureConfirmation ===
        `${externalIntegrationSourceMutationFixtureConfirmationPrefix}${temporaryExternalIntegrationSourceId}`,
      'Smoke external integration source mutation fixture confirmation does not match the temporary fixture ID'
    )
  } else {
    expect(
      temporaryExternalIntegrationSourceId === undefined &&
        externalIntegrationSourceMutationFixtureConfirmation === undefined,
      'Smoke external integration source mutation fixture settings require the explicit mutation flag'
    )
  }
  return {
    ...config,
    accountId: config.accountId?.trim() || undefined,
    allowExternalIntegrationSourceMutations,
    allowGroupMutations: config.allowGroupMutations ?? false,
    baseUrl: normalizeManagementApiBaseUrl(config.baseUrl),
    clientIpHash: config.clientIpHash?.trim().toLowerCase() || undefined,
    externalIntegrationSourceId,
    externalIntegrationSourceTokenId,
    externalIntegrationSourceMutationFixtureConfirmation,
    groupId: config.groupId?.trim() || undefined,
    providerCode: config.providerCode?.trim() || undefined,
    publicApiLogId: config.publicApiLogId?.trim() || undefined,
    requireClientIpDetail: config.requireClientIpDetail ?? false,
    routeStrategyId: config.routeStrategyId?.trim() || undefined,
    systemAccountId: config.systemAccountId?.trim() || undefined,
    temporaryExternalIntegrationSourceId,
    timeoutMs
  }
}

function selectClientIPStatsDetailTarget(
  config: NormalizedRealGoManagementSmokeConfig,
  clientIPStats: ClientIPStatsListResult
): ClientIPStatsDetailTarget | undefined {
  const configuredIpHash = config.clientIpHash
  if (configuredIpHash) {
    return {
      ipHash: configuredIpHash,
      listItem: clientIPStats.items.find((item) => item.ipHash.toLowerCase() === configuredIpHash)
    }
  }
  if (!clientIPStats.rangeReady || clientIPStats.items.length === 0) {
    return undefined
  }

  const listItem = clientIPStats.items.find((item) => isClientIPHash(item.ipHash))
  expect(listItem, 'client IP stats list has no valid 64-character hexadecimal ipHash')
  return {
    ipHash: listItem.ipHash,
    listItem
  }
}

function selectMutationProvider(
  configuredProviderCode: string | undefined,
  selectedGroup: GroupRecord,
  providers: ProviderRecord[]
): ProviderRecord {
  const requestedCode = configuredProviderCode ?? selectedGroup.providerCode
  const provider = providers.find((item) => item.code === requestedCode)
  expect(provider, 'Mutation provider was not returned by providers/options')
  expect(provider.enabled, 'Mutation provider must be enabled')
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

function publicAPILogsListUrl(config: NormalizedRealGoManagementSmokeConfig): URL {
  return endpointUrl(config.baseUrl, '/public-api-logs', {
    page: '1',
    pageSize: '20'
  })
}

function publicAPILogDetailUrl(config: NormalizedRealGoManagementSmokeConfig, publicApiLogId: string): URL {
  return endpointUrl(config.baseUrl, `/public-api-logs/${encodeURIComponent(publicApiLogId)}`, {})
}

function externalIntegrationSourcesListUrl(
  config: NormalizedRealGoManagementSmokeConfig,
  page: number
): URL {
  return endpointUrl(config.baseUrl, '/external-integration-sources', {
    page: String(page),
    pageSize: '20'
  })
}

function externalIntegrationSourceDetailUrl(
  config: NormalizedRealGoManagementSmokeConfig,
  sourceId: string
): URL {
  return endpointUrl(config.baseUrl, `/external-integration-sources/${encodeURIComponent(sourceId)}`, {})
}

function externalIntegrationSourceTokenSecretUrl(
  config: NormalizedRealGoManagementSmokeConfig,
  sourceId: string,
  tokenId: string
): URL {
  return endpointUrl(config.baseUrl, `/external-integration-sources/${encodeURIComponent(sourceId)}/tokens/${encodeURIComponent(tokenId)}/secret`, {})
}

function clientIPStatsListUrl(config: NormalizedRealGoManagementSmokeConfig): URL {
  return endpointUrl(config.baseUrl, '/ip-stats', {
    page: '1',
    pageSize: '20',
    sortField: 'requestCount',
    sortOrder: 'desc'
  })
}

function clientIPStatsDetailUrl(
  config: NormalizedRealGoManagementSmokeConfig,
  ipHash: string,
  range: ClientIPStatsRange
): URL {
  return endpointUrl(config.baseUrl, `/ip-stats/${encodeURIComponent(ipHash)}/detail`, {
    startDate: range.startDate,
    endDate: range.endDate,
    page: '1',
    pageSize: '20',
    sortOrder: 'asc'
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
  label: string,
  requirePragmaNoCache = false
): Promise<unknown> {
  return requestEnvelopeData(url, config, label, {
    method: 'GET',
    expectedStatus: 200,
    requirePragmaNoCache
  })
}

async function requestEnvelopeData(
  url: URL,
  config: NormalizedRealGoManagementSmokeConfig,
  label: string,
  options: {
    method: 'GET' | 'POST' | 'PATCH'
    body?: Record<string, unknown>
    expectedStatus: number
    requirePragmaNoCache?: boolean
  }
): Promise<unknown> {
  const response = await sendRequest(url, config, label, options)
  if (response.status !== options.expectedStatus) {
    await response.body?.cancel()
    throw new Error(`${label} failed with HTTP ${response.status}`)
  }
  if (options.requirePragmaNoCache) {
    expect(response.headers.get('pragma') === 'no-cache', `${label} must return Pragma: no-cache`)
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

function assertPublicAPILogList(value: unknown): PublicApiLogListResult {
  expect(isRecord(value), 'public API logs list data must be an object')
  expect(Array.isArray(value.items), 'public API logs list items must be an array')
  expect(isNonNegativeInteger(value.total), 'public API logs list total must be a non-negative integer')
  expect(typeof value.hasMore === 'boolean', 'public API logs list hasMore must be boolean')
  expect(value.page === 1, 'public API logs list page must be 1')
  expect(value.pageSize === 20, 'public API logs list pageSize must be 20')

  const items = value.items.map((item, index) => assertPublicAPILogSummary(item, `public API logs list item ${index}`))
  expect(
    value.total === items.length + (value.hasMore ? 1 : 0),
    'public API logs list total must be the progressive first-page upper bound'
  )
  if (value.hasMore) {
    expect(items.length === value.pageSize, 'public API logs list hasMore requires a full page')
  }

  return {
    items,
    total: value.total,
    hasMore: value.hasMore,
    page: value.page,
    pageSize: value.pageSize
  }
}

function assertExternalIntegrationSourceList(
  value: unknown,
  expectedPage: number,
  expectedPageSize: number
): ExternalIntegrationSourceListResult {
  const label = 'external integration source list data'
  expect(isRecord(value), `${label} must be an object`)
  expect(Array.isArray(value.items), `${label}.items must be an array`)
  expect(value.page === expectedPage, `${label}.page must be ${expectedPage}`)
  expect(value.pageSize === expectedPageSize, `${label}.pageSize must be ${expectedPageSize}`)
  expect(
    isNonNegativeSafeInteger(value.pageUpperBound),
    `${label}.pageUpperBound must be a non-negative safe integer`
  )
  expect(typeof value.hasMore === 'boolean', `${label}.hasMore must be boolean`)
  expect(value.items.length <= value.pageSize, `${label}.items must not exceed pageSize`)

  const seenIds = new Set<string>()
  const items = value.items.map((item, index) => {
    const source = assertExternalIntegrationSourceListItem(item, index)
    expect(!seenIds.has(source.id), `${label}.items must not contain duplicate ids`)
    seenIds.add(source.id)
    return source
  })
  const offset = (expectedPage - 1) * expectedPageSize
  expect(
    value.pageUpperBound === offset + items.length + (value.hasMore ? 1 : 0),
    `${label}.pageUpperBound must be the progressive page upper bound`
  )
  if (value.hasMore) {
    expect(items.length === value.pageSize, `${label}.hasMore requires a full page`)
  }

  return {
    items,
    page: value.page,
    pageSize: value.pageSize,
    pageUpperBound: value.pageUpperBound,
    hasMore: value.hasMore
  }
}

function assertExternalIntegrationSourceListItem(
  value: unknown,
  index: number
): ExternalIntegrationSourceListItem {
  const label = `external integration source list item ${index}`
  expect(isRecord(value), `${label} must be an object`)
  assertExternalIntegrationSourceFields(value, externalIntegrationSourceListItemFieldSet, label)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(
    value.status === 'active' || value.status === 'disabled',
    `${label}.status must be active or disabled`
  )
  assertExternalIntegrationSourceScopesSubset(value.scopes, `${label}.scopes`)
  assertExternalIntegrationSourceRateLimits(value.rateLimits, `${label}.rateLimits`)
  assertOptionalISOString(value, 'expiresAt', label)
  if (Object.hasOwn(value, 'notes')) {
    expect(typeof value.notes === 'string', `${label}.notes must be a string when present`)
  }
  assertOptionalISOString(value, 'lastUsedAt', label)
  assertRequiredISOString(value.createdAt, `${label}.createdAt`)
  assertRequiredISOString(value.updatedAt, `${label}.updatedAt`)
  expect(
    isNonNegativeSafeInteger(value.tokenCount),
    `${label}.tokenCount must be a non-negative safe integer`
  )
  expect(
    isNonNegativeSafeInteger(value.activeTokenCount),
    `${label}.activeTokenCount must be a non-negative safe integer`
  )
  expect(
    value.activeTokenCount <= value.tokenCount,
    `${label}.activeTokenCount must not exceed tokenCount`
  )
  expect(typeof value.isBuiltIn === 'boolean', `${label}.isBuiltIn must be boolean`)

  const hasPrimaryToken = Object.hasOwn(value, 'primaryToken')
  if (value.tokenCount > 0) {
    expect(hasPrimaryToken, `${label}.primaryToken must be present when tokenCount is positive`)
  } else {
    expect(!hasPrimaryToken, `${label}.primaryToken must be absent when tokenCount is zero`)
  }
  if (hasPrimaryToken) {
    const primaryToken = assertExternalIntegrationSourcePrimaryToken(value.primaryToken, `${label}.primaryToken`)
    if (Number(value.activeTokenCount) > 0) {
      expect(
        primaryToken.status === 'active',
        `${label}.primaryToken.status must be active when activeTokenCount is positive`
      )
    } else {
      expect(
        primaryToken.status !== 'active',
        `${label}.primaryToken.status must not be active when activeTokenCount is zero`
      )
    }
  }
  return value as unknown as ExternalIntegrationSourceListItem
}

function assertExternalIntegrationSourceDetail(
  value: unknown,
  listItem?: ExternalIntegrationSourceListItem
): ExternalIntegrationSourceDetail {
  const label = 'external integration source detail'
  expect(isRecord(value), `${label} must be an object`)
  assertExternalIntegrationSourceFields(value, externalIntegrationSourceDetailFieldSet, label)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(
    value.status === 'active' || value.status === 'disabled',
    `${label}.status must be active or disabled`
  )
  const scopes = assertExternalIntegrationSourceScopesSubset(value.scopes, `${label}.scopes`)
  assertExternalIntegrationSourceRateLimits(value.rateLimits, `${label}.rateLimits`)
  const rateLimits = value.rateLimits as Array<{ windowSeconds: number; maxRequests: number }>
  assertOptionalISOString(value, 'expiresAt', label)
  if (Object.hasOwn(value, 'notes')) {
    expect(typeof value.notes === 'string', `${label}.notes must be a string when present`)
  }
  assertOptionalISOString(value, 'lastUsedAt', label)
  assertRequiredISOString(value.createdAt, `${label}.createdAt`)
  assertRequiredISOString(value.updatedAt, `${label}.updatedAt`)
  expect(
    isNonNegativeSafeInteger(value.tokenCount),
    `${label}.tokenCount must be a non-negative safe integer`
  )
  expect(
    isNonNegativeSafeInteger(value.activeTokenCount),
    `${label}.activeTokenCount must be a non-negative safe integer`
  )
  expect(typeof value.isBuiltIn === 'boolean', `${label}.isBuiltIn must be boolean`)
  expect(Array.isArray(value.tokens), `${label}.tokens must be an array`)

  const tokens = value.tokens.map((token, index) =>
    assertExternalIntegrationSourcePrimaryToken(token, `${label}.tokens item ${index}`)
  )
  const activeTokenCount = tokens.filter((token) => token.status === 'active').length
  expect(value.tokenCount === tokens.length, `${label}.tokenCount must equal tokens.length`)
  expect(
    value.activeTokenCount === activeTokenCount,
    `${label}.activeTokenCount must equal the number of active tokens`
  )

  for (let index = 1; index < tokens.length; index += 1) {
    const previousToken = tokens[index - 1]
    const token = tokens[index]
    expect(previousToken && token, `${label}.tokens must contain valid items`)
    const previousCreatedAt = Date.parse(previousToken.createdAt)
    const createdAt = Date.parse(token.createdAt)
    expect(
      previousCreatedAt > createdAt || (previousCreatedAt === createdAt && previousToken.id > token.id),
      `${label}.tokens must be sorted by createdAt descending, then id descending`
    )
  }

  if (!listItem) return value as unknown as ExternalIntegrationSourceDetail
  expect(value.id === listItem.id, `${label}.id must match the list item`)
  expect(value.createdAt === listItem.createdAt, `${label}.createdAt must match the list item`)
  expect(value.isBuiltIn === listItem.isBuiltIn, `${label}.isBuiltIn must match the list item`)
  const detailUpdatedAt = Date.parse(value.updatedAt)
  const listUpdatedAt = Date.parse(listItem.updatedAt)
  expect(detailUpdatedAt >= listUpdatedAt, `${label}.updatedAt must not precede the list item`)
  if (listItem.lastUsedAt !== undefined) {
    expect(value.lastUsedAt !== undefined, `${label}.lastUsedAt must not disappear after the list request`)
    expect(
      Date.parse(value.lastUsedAt) >= Date.parse(listItem.lastUsedAt),
      `${label}.lastUsedAt must not precede the list item`
    )
  }
  if (value.updatedAt === listItem.updatedAt) {
    for (const field of ['name', 'status', 'expiresAt', 'notes'] as const) {
      expect(value[field] === listItem[field], `${label}.${field} must match the same list snapshot`)
    }
    expect(
      scopes.length === listItem.scopes.length
        && scopes.every((scope, index) => scope === listItem.scopes[index]),
      `${label}.scopes must match the same list snapshot`
    )
    expect(
      rateLimits.length === listItem.rateLimits.length
        && rateLimits.every((rule, index) => {
          const listRule = listItem.rateLimits[index]
          return listRule
            && rule.windowSeconds === listRule.windowSeconds
            && rule.maxRequests === listRule.maxRequests
        }),
      `${label}.rateLimits must match the same list snapshot`
    )
  }
  return value as unknown as ExternalIntegrationSourceDetail
}

function assertExternalIntegrationSourceTokenSecret(
  value: unknown,
  tokenSummary: ExternalIntegrationSourcePrimaryToken
): void {
  const label = 'external integration source token secret data'
  expect(isRecord(value), `${label} must be an object`)
  assertExternalIntegrationSourceFields(value, externalIntegrationSourceTokenSecretFieldSet, label)
  expect(isNonEmptyString(value.token), `${label}.token must be a non-empty string`)
  expect(value.token.startsWith(tokenSummary.tokenPrefix), `${label}.token must match the summary prefix`)
  expect(value.token.endsWith(tokenSummary.tokenSuffix), `${label}.token must match the summary suffix`)
}

function assertExternalIntegrationSourcePrimaryToken(
  value: unknown,
  label: string
): ExternalIntegrationSourcePrimaryToken {
  expect(isRecord(value), `${label} must be an object`)
  assertExternalIntegrationSourceFields(value, externalIntegrationSourcePrimaryTokenFieldSet, label)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(
    typeof value.tokenPrefix === 'string' && /^juis_[A-Za-z0-9_-]{3}$/.test(value.tokenPrefix),
    `${label}.tokenPrefix must be an 8-character juis_ preview`
  )
  expect(
    typeof value.tokenSuffix === 'string' && /^[A-Za-z0-9_-]{8}$/.test(value.tokenSuffix),
    `${label}.tokenSuffix must be an 8-character base64url preview`
  )
  expect(
    value.status === 'active' || value.status === 'disabled' || value.status === 'revoked',
    `${label}.status must be active, disabled, or revoked`
  )
  assertExternalIntegrationSourceScopesSubset(value.scopes, `${label}.scopes`)
  assertOptionalISOString(value, 'expiresAt', label)
  assertOptionalISOString(value, 'lastUsedAt', label)
  assertRequiredISOString(value.createdAt, `${label}.createdAt`)
  assertRequiredISOString(value.updatedAt, `${label}.updatedAt`)
  assertOptionalISOString(value, 'revokedAt', label)
  expect(typeof value.isBuiltIn === 'boolean', `${label}.isBuiltIn must be boolean`)
  return value as unknown as ExternalIntegrationSourcePrimaryToken
}

function assertExternalIntegrationSourceScopesSubset(value: unknown, label: string): string[] {
  expect(Array.isArray(value), `${label} must be an array`)
  const seenScopes = new Set<string>()
  value.forEach((scope, index) => {
    expect(typeof scope === 'string', `${label} item ${index} must be a string`)
    expect(
      externalIntegrationSourceScopeSet.has(scope),
      `${label} item ${index} must be one of the 16 supported scopes`
    )
    expect(!seenScopes.has(scope), `${label} must not contain duplicate scopes`)
    seenScopes.add(scope)
  })
  return value
}

function assertExternalIntegrationSourceRateLimits(value: unknown, label: string): void {
  expect(Array.isArray(value), `${label} must be an array`)
  expect(
    value.length <= externalIntegrationSourceRateLimitMaximumRules,
    `${label} must contain at most ${externalIntegrationSourceRateLimitMaximumRules} rules`
  )

  const seenWindows = new Set<number>()
  let previousWindowSeconds = 0
  value.forEach((rule, index) => {
    const ruleLabel = `${label} item ${index}`
    expect(isRecord(rule), `${ruleLabel} must be an object`)
    const keys = Object.keys(rule)
    expect(
      keys.length === 2 && keys.includes('windowSeconds') && keys.includes('maxRequests'),
      `${ruleLabel} must contain only windowSeconds and maxRequests`
    )
    expect(
      Number.isSafeInteger(rule.windowSeconds)
        && Number(rule.windowSeconds) >= 1
        && Number(rule.windowSeconds) <= externalIntegrationSourceRateLimitMaximumWindowSeconds,
      `${ruleLabel}.windowSeconds must be an integer from 1 to ${externalIntegrationSourceRateLimitMaximumWindowSeconds}`
    )
    expect(
      Number.isSafeInteger(rule.maxRequests)
        && Number(rule.maxRequests) >= 1
        && Number(rule.maxRequests) <= externalIntegrationSourceRateLimitMaximumRequests,
      `${ruleLabel}.maxRequests must be an integer from 1 to ${externalIntegrationSourceRateLimitMaximumRequests}`
    )
    const windowSeconds = Number(rule.windowSeconds)
    expect(!seenWindows.has(windowSeconds), `${label} must not contain duplicate windows`)
    expect(
      index === 0 || windowSeconds > previousWindowSeconds,
      `${label} must be sorted by windowSeconds ascending`
    )
    seenWindows.add(windowSeconds)
    previousWindowSeconds = windowSeconds
  })
}

function assertExternalIntegrationSourceFields(
  value: Record<string, unknown>,
  allowedFields: ReadonlySet<string>,
  label: string
): void {
  for (const field of Object.keys(value)) {
    expect(allowedFields.has(field), `${label} must not contain undocumented field ${field}`)
  }
}

function assertRequiredISOString(value: unknown, label: string): asserts value is string {
  expect(isCanonicalISOString(value), `${label} must be a canonical UTC ISO timestamp`)
}

function assertOptionalISOString(value: Record<string, unknown>, field: string, label: string): void {
  if (Object.hasOwn(value, field)) {
    assertRequiredISOString(value[field], `${label}.${field}`)
  }
}

function isCanonicalISOString(value: unknown): value is string {
  if (typeof value !== 'string') {
    return false
  }
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && new Date(timestamp).toISOString() === value
}

function assertExternalIntegrationSourceScopes(value: unknown): void {
  const label = 'external integration source scopes data'
  expect(Array.isArray(value), `${label} must be an array`)
  expect(
    value.length === externalIntegrationSourceScopeOptions.length,
    `${label} must contain exactly ${externalIntegrationSourceScopeOptions.length} items`
  )

  const seenValues = new Set<string>()
  const seenLabels = new Set<string>()
  value.forEach((item, index) => {
    const itemLabel = `${label} item ${index}`
    expect(isRecord(item), `${itemLabel} must be an object`)
    const keys = Object.keys(item)
    expect(
      keys.length === 2 && keys.includes('value') && keys.includes('label'),
      `${itemLabel} must contain only value and label`
    )
    expect(typeof item.value === 'string', `${itemLabel}.value must be a string`)
    expect(typeof item.label === 'string', `${itemLabel}.label must be a string`)
    expect(!seenValues.has(item.value), `${label} must not contain duplicate values`)
    expect(!seenLabels.has(item.label), `${label} must not contain duplicate labels`)
    seenValues.add(item.value)
    seenLabels.add(item.label)

    const expectedOption = externalIntegrationSourceScopeOptions[index]
    expect(item.value === expectedOption.value, `${itemLabel}.value must equal ${expectedOption.value}`)
    expect(item.label === expectedOption.label, `${itemLabel}.label must equal ${expectedOption.label}`)
  })
}

function assertExternalIntegrationSourceApiDocs(value: unknown): void {
  const label = 'external integration source api docs data'
  expect(isRecord(value), `${label} must be an object`)
  expect(value.basePath === '/__aipublic__', `${label}.basePath must equal /__aipublic__`)
  expect(value.authType === 'Bearer', `${label}.authType must equal Bearer`)
  expect(Array.isArray(value.items), `${label}.items must be an array`)
  expect(
    value.items.length === externalIntegrationSourceScopeOptions.length,
    `${label}.items must contain exactly ${externalIntegrationSourceScopeOptions.length} items`
  )

  const itemsById = new Map<string, Record<string, unknown>>()
  const seenMethodPaths = new Set<string>()
  const seenScopes = new Set<string>()
  value.items.forEach((item, index) => {
    const itemLabel = `${label}.items item ${index}`
    expect(isRecord(item), `${itemLabel} must be an object`)
    expect(isNonEmptyString(item.id), `${itemLabel}.id must be a non-empty string`)
    expect(!itemsById.has(item.id), `${label}.items must not contain duplicate ids`)
    itemsById.set(item.id, item)

    expect(isNonEmptyString(item.method), `${itemLabel}.method must be a non-empty string`)
    expect(isNonEmptyString(item.path), `${itemLabel}.path must be a non-empty string`)
    expect(isNonEmptyString(item.scope), `${itemLabel}.scope must be a non-empty string`)
    const methodPath = `${item.method} ${item.path}`
    expect(!seenMethodPaths.has(methodPath), `${label}.items must not contain duplicate method/path pairs`)
    expect(!seenScopes.has(item.scope), `${label}.items must not contain duplicate scopes`)
    seenMethodPaths.add(methodPath)
    seenScopes.add(item.scope)
  })

  for (const expectedItem of externalIntegrationSourceApiDocContracts) {
    const item = itemsById.get(expectedItem.id)
    const itemLabel = `${label}.items item ${expectedItem.id}`
    expect(isRecord(item), `${itemLabel} must exist`)
    expect(item.id === expectedItem.id, `${itemLabel}.id must equal ${expectedItem.id}`)
    expect(item.method === expectedItem.method, `${itemLabel}.method must equal ${expectedItem.method}`)
    expect(item.path === expectedItem.path, `${itemLabel}.path must equal ${expectedItem.path}`)
    expect(item.scope === expectedItem.scope, `${itemLabel}.scope must equal ${expectedItem.scope}`)
    assertExternalIntegrationSourceApiDocRichFields(item, itemLabel, expectedItem.method)
  }
}

function assertExternalIntegrationSourceApiDocRichFields(
  item: Record<string, unknown>,
  label: string,
  method: 'GET' | 'POST'
): void {
  expect(isNonEmptyString(item.name), `${label}.name must be a non-empty string`)
  expect(isNonEmptyString(item.summary), `${label}.summary must be a non-empty string`)
  expect(item.status === 'available', `${label}.status must equal available`)
  expect(Array.isArray(item.headers) && item.headers.length > 0, `${label}.headers must be a non-empty array`)

  item.headers.forEach((header, index) => {
    const headerLabel = `${label}.headers item ${index}`
    expect(isRecord(header), `${headerLabel} must be an object`)
    expect(isNonEmptyString(header.name), `${headerLabel}.name must be a non-empty string`)
    expect(typeof header.required === 'boolean', `${headerLabel}.required must be boolean`)
    expect(isNonEmptyString(header.description), `${headerLabel}.description must be a non-empty string`)
    expect(isNonEmptyString(header.example), `${headerLabel}.example must be a non-empty string`)
  })

  const authorizationHeader = item.headers.find((header) =>
    isRecord(header) && header.name === 'Authorization'
  )
  expect(isRecord(authorizationHeader), `${label}.headers must document Authorization`)
  expect(authorizationHeader.required === true, `${label} Authorization header must be required`)
  expect(
    isNonEmptyString(authorizationHeader.description),
    `${label} Authorization header description must be a non-empty string`
  )
  expect(
    isNonEmptyString(authorizationHeader.example),
    `${label} Authorization header example must be a non-empty string`
  )

  expect(Array.isArray(item.query), `${label}.query must be an array`)
  if (method === 'GET') {
    expect(item.query.length > 0, `${label}.query must document at least one field`)
    item.query.forEach((field, index) => {
      assertExternalIntegrationSourceApiDocField(field, `${label}.query item ${index}`)
    })
    expect(!Object.hasOwn(item, 'requestBody'), `${label}.requestBody must be absent for GET`)
  } else {
    expect(item.query.length === 0, `${label}.query must be empty for POST`)
    expect(isRecord(item.requestBody), `${label}.requestBody must be an object`)
    expect(
      item.requestBody.contentType === 'application/json',
      `${label}.requestBody.contentType must equal application/json`
    )
    expect(
      Array.isArray(item.requestBody.fields) && item.requestBody.fields.length > 0,
      `${label}.requestBody.fields must be a non-empty array`
    )
    item.requestBody.fields.forEach((field, index) => {
      assertExternalIntegrationSourceApiDocField(field, `${label}.requestBody.fields item ${index}`)
    })
    expect(isRecord(item.requestBody.example), `${label}.requestBody.example must be an object`)
  }

  expect(
    Array.isArray(item.responseFields) && item.responseFields.length > 0,
    `${label}.responseFields must be a non-empty array`
  )
  item.responseFields.forEach((field, index) => {
    assertExternalIntegrationSourceApiDocField(field, `${label}.responseFields item ${index}`)
  })
  expect(isRecord(item.responseExample), `${label}.responseExample must be an object`)
}

function externalIntegrationSourceApiDocContract(scope: string): {
  id: string
  method: 'GET' | 'POST'
  path: string
  scope: string
} {
  const match = /^juhe_ai_public:(api_key|route_strategy|group|account)_(list|add|update|delete):(read|write)$/.exec(scope)
  const resource = match?.[1]?.replaceAll('_', '-')
  const action = match?.[2]
  if (!resource || !action) {
    throw new Error(`Unsupported external integration source scope: ${scope}`)
  }
  const method = action === 'list' ? 'GET' : 'POST'
  const publicAction = action === 'delete' ? 'del' : action
  return {
    id: `${resource}-${action}`,
    method,
    path: `/__aipublic__/${resource}/${publicAction}`,
    scope
  }
}

function assertExternalIntegrationSourceApiDocField(value: unknown, label: string): void {
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.name), `${label}.name must be a non-empty string`)
  expect(isNonEmptyString(value.type), `${label}.type must be a non-empty string`)
  expect(typeof value.required === 'boolean', `${label}.required must be boolean`)
  expect(isNonEmptyString(value.description), `${label}.description must be a non-empty string`)
}

function assertPublicAPILogDetail(value: unknown, expectedId: string): void {
  const detail = assertPublicAPILogSummary(value, 'public API log detail')
  expect(detail.id === expectedId, 'public API log detail id must match the configured id')
  expect(isRecord(value), 'public API log detail data must be an object')
  expect(isRecord(value.requestData), 'public API log detail.requestData must be a non-array object')
  expect(isRecord(value.responseData), 'public API log detail.responseData must be a non-array object')
}

function assertPublicAPILogSummary(value: unknown, label: string): PublicApiLogSummary {
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  for (const field of [
    'traceId',
    'sourceRefId',
    'sourceName',
    'tokenId',
    'tokenName',
    'tokenPrefix',
    'queryString',
    'clientIp',
    'userAgent',
    'errorCode',
    'errorMessage'
  ]) {
    assertOptionalString(value, field, label)
  }
  expect(typeof value.isTestToken === 'boolean', `${label}.isTestToken must be boolean`)
  expect(isNonEmptyString(value.method), `${label}.method must be a non-empty string`)
  expect(isNonEmptyString(value.path), `${label}.path must be a non-empty string`)
  if (Object.hasOwn(value, 'statusCode')) {
    expect(isNonNegativeInteger(value.statusCode), `${label}.statusCode must be a non-negative integer when present`)
  }
  expect(typeof value.success === 'boolean', `${label}.success must be boolean`)
  if (Object.hasOwn(value, 'durationMs')) {
    expect(isNonNegativeInteger(value.durationMs), `${label}.durationMs must be a non-negative integer when present`)
  }
  expect(isNonNegativeInteger(value.requestSizeBytes), `${label}.requestSizeBytes must be a non-negative integer`)
  expect(isNonNegativeInteger(value.responseSizeBytes), `${label}.responseSizeBytes must be a non-negative integer`)
  expect(isPublicAPILogCaptureStatus(value.requestCaptureStatus), `${label}.requestCaptureStatus must be a valid capture status`)
  expect(isPublicAPILogCaptureStatus(value.responseCaptureStatus), `${label}.responseCaptureStatus must be a valid capture status`)
  expect(isNonEmptyString(value.startedAt), `${label}.startedAt must be a non-empty string`)
  expect(isNonEmptyString(value.endedAt), `${label}.endedAt must be a non-empty string`)
  expect(isNonEmptyString(value.createdAt), `${label}.createdAt must be a non-empty string`)
  return value as unknown as PublicApiLogSummary
}

function isPublicAPILogCaptureStatus(value: unknown): value is PublicApiLogCaptureStatus {
  return value === 'complete' || value === 'truncated' || value === 'empty' || value === 'dropped'
}

function assertApiKeyList(value: unknown, scope: 'admin' | 'self'): { items: unknown[] } {
  const label = `${scope} API keys list`
  expect(isRecord(value), `${label} data must be an object`)
  const fields = Object.keys(value)
  expect(
    fields.length === 5
      && ['items', 'total', 'hasMore', 'page', 'pageSize'].every((field) => fields.includes(field)),
    `${label} data must contain only items, total, hasMore, page, and pageSize`
  )
  expect(Array.isArray(value.items), `${label} items must be an array`)
  expect(isNonNegativeInteger(value.total), `${label} total must be a non-negative integer`)
  expect(typeof value.hasMore === 'boolean', `${label} hasMore must be boolean`)
  expect(value.page === 1, `${label} page must be 1`)
  expect(value.pageSize === 20, `${label} pageSize must be 20`)
  expect(
    value.total === value.items.length + (value.hasMore ? 1 : 0),
    `${label} total must be the progressive first-page upper bound`
  )
  if (value.hasMore) {
    expect(value.items.length === 20, `${label} items must contain 20 entries when hasMore is true`)
  }
  value.items.forEach((item, index) => assertApiKeyListItem(item, scope, `${label} item ${index}`))
  return { items: value.items }
}

function assertApiKeyListItem(value: unknown, scope: 'admin' | 'self', label: string): void {
  expect(isRecord(value), `${label} must be an object`)
  assertNoApiKeySensitiveFields(value, label, true)
  for (const field of Object.keys(value)) {
    expect(apiKeyListItemFieldSet.has(field), `${label} must not contain undocumented field ${field}`)
  }
  for (const field of ['id', 'name', 'keyPrefix', 'keySuffix', 'routeStrategyId'] as const) {
    expect(isNonEmptyString(value[field]), `${label}.${field} must be a non-empty string`)
  }
  expect(value.status === 'active' || value.status === 'disabled', `${label}.status must be active or disabled`)
  expect(typeof value.isDefault === 'boolean', `${label}.isDefault must be boolean`)
  expect(isRecord(value.quotaLimits), `${label}.quotaLimits must be an object`)
  expect(isRecord(value.usage), `${label}.usage must be an object`)
  assertOptionalString(value, 'description', label)
  for (const field of ['routeStrategyName', 'expiresAt'] as const) {
    if (Object.hasOwn(value, field)) {
      expect(isNonEmptyString(value[field]), `${label}.${field} must be a non-empty string when present`)
    }
  }
  if (Object.hasOwn(value, 'routeStrategyMode')) {
    expect(
      ['normal', 'hybrid_smart', 'weighted', 'failover', 'round_robin'].includes(String(value.routeStrategyMode)),
      `${label}.routeStrategyMode must be a valid route strategy mode`
    )
  }
  if (Object.hasOwn(value, 'routeStrategyStatus')) {
    expect(
      value.routeStrategyStatus === 'active' || value.routeStrategyStatus === 'disabled',
      `${label}.routeStrategyStatus must be active or disabled`
    )
  }
  if (Object.hasOwn(value, 'availabilitySchedule')) {
    expect(isRecord(value.availabilitySchedule), `${label}.availabilitySchedule must be an object when present`)
  }
  if (scope === 'admin') {
    expect(isNonEmptyString(value.systemAccountId), `${label}.systemAccountId must be a non-empty string`)
    expect(isNonEmptyString(value.systemAccountName), `${label}.systemAccountName must be a non-empty string`)
  } else {
    expect(
      !Object.hasOwn(value, 'systemAccountId') && !Object.hasOwn(value, 'systemAccountName'),
      `${label} must not contain systemAccountId or systemAccountName`
    )
  }
}

function assertNoApiKeySensitiveFields(value: unknown, label: string, allowPreview: boolean): void {
  if (Array.isArray(value)) {
    value.forEach((item) => assertNoApiKeySensitiveFields(item, label, false))
    return
  }
  if (!isRecord(value)) {
    return
  }
  for (const [field, child] of Object.entries(value)) {
    const normalizedField = field.toLowerCase()
    const allowedPreviewField = allowPreview
      && (normalizedField === 'keyprefix' || normalizedField === 'keysuffix')
    expect(
      allowedPreviewField
        || (!normalizedField.includes('key')
          && !normalizedField.includes('secret')
          && !normalizedField.includes('hash')
          && !normalizedField.includes('ciphertext')),
      `${label} must not contain sensitive field ${field}`
    )
    assertNoApiKeySensitiveFields(child, label, false)
  }
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

function assertRouteStrategyList(value: unknown): RouteStrategyListItem[] {
  expect(isRecord(value), 'route strategies list data must be an object')
  expect(Array.isArray(value.items), 'route strategies list items must be an array')
  expect(isNonNegativeInteger(value.total), 'route strategies list total must be a non-negative integer')
  expect(typeof value.hasMore === 'boolean', 'route strategies list hasMore must be boolean')
  expect(value.page === 1, 'route strategies list page must be 1')
  expect(value.pageSize === 200, 'route strategies list pageSize must be 200')
  expect(value.total >= value.items.length, 'route strategies list total must cover returned items')
  return value.items.map((item, index) => assertRouteStrategyListItem(item, index))
}

function assertRouteStrategyListItem(value: unknown, index: number): RouteStrategyListItem {
  const label = `route strategies list item ${index}`
  expect(isRecord(value), `${label} must be an object`)
  assertRouteStrategyIdentity(value, label)
  expect(Array.isArray(value.groupBindingPreview), `${label}.groupBindingPreview must be an array`)
  expect(value.groupBindingPreview.length <= 3, `${label}.groupBindingPreview must contain at most 3 items`)
  expect(isNonNegativeInteger(value.bindingCount), `${label}.bindingCount must be a non-negative integer`)
  expect(isNonNegativeInteger(value.apiKeyCount), `${label}.apiKeyCount must be a non-negative integer`)
  expect(!Object.hasOwn(value, 'groupBindings'), `${label} must not expose groupBindings`)
  expect(!Object.hasOwn(value, 'hybridRoutingConfig'), `${label} must not expose hybridRoutingConfig`)
  assertRouteStrategyConfigForMode(value, label, false)
  return value as unknown as RouteStrategyListItem
}

function selectRouteStrategy(
  items: RouteStrategyListItem[],
  requestedRouteStrategyId?: string
): RouteStrategyListItem | undefined {
  if (requestedRouteStrategyId) {
    const requested = items.find((item) => item.id === requestedRouteStrategyId)
    expect(requested, 'Configured route strategy was not returned by route strategies list')
    return requested
  }
  return items[0]
}

function assertRouteStrategyDetail(value: unknown, listItem: RouteStrategyListItem): void {
  const label = 'route strategy detail'
  expect(isRecord(value), `${label} data must be an object`)
  assertRouteStrategyIdentity(value, label)
  expect(Array.isArray(value.groupBindings), `${label}.groupBindings must be an array`)
  expect(value.groupBindings.every(isRecord), `${label}.groupBindings must contain only objects`)
  expect(value.groupBindings.length === listItem.bindingCount, `${label}.groupBindings must be complete`)
  expect(isNonNegativeInteger(value.apiKeyCount), `${label}.apiKeyCount must be a non-negative integer`)
  assertRouteStrategyConfigForMode(value, label, true)
  for (const field of ['id', 'systemAccountId', 'systemAccountName', 'mode', 'status'] as const) {
    expect(value[field] === listItem[field], `${label}.${field} must match the list item`)
  }
  expect(value.apiKeyCount === listItem.apiKeyCount, `${label}.apiKeyCount must match the list item`)
}

function assertRouteStrategyIdentity(value: Record<string, unknown>, label: string): void {
  expect(isNonEmptyString(value.id), `${label}.id must be a non-empty string`)
  expect(isNonEmptyString(value.systemAccountId), `${label}.systemAccountId must be a non-empty string`)
  expect(typeof value.systemAccountName === 'string', `${label}.systemAccountName must be a string`)
  expect(isRouteStrategyMode(value.mode), `${label}.mode is invalid`)
  expect(value.status === 'active' || value.status === 'disabled', `${label}.status is invalid`)
}

function assertRouteStrategyConfigForMode(
  value: Record<string, unknown>,
  label: string,
  detail: boolean
): void {
  if (value.mode === 'normal') {
    expect(isRecord(value.normalRoutingConfig), `${label}.normalRoutingConfig must be an object for normal mode`)
    expect(!Object.hasOwn(value, 'hybridRoutingConfig'), `${label} must not expose hybridRoutingConfig for normal mode`)
    return
  }
  expect(!Object.hasOwn(value, 'normalRoutingConfig'), `${label} must not expose normalRoutingConfig outside normal mode`)
  if (detail && value.mode === 'hybrid_smart') {
    expect(isRecord(value.hybridRoutingConfig), `${label}.hybridRoutingConfig must be an object for hybrid_smart mode`)
    return
  }
  expect(!Object.hasOwn(value, 'hybridRoutingConfig'), `${label} must not expose hybridRoutingConfig for this mode`)
}

function isRouteStrategyMode(value: unknown): value is RouteStrategyMode {
  return value === 'normal'
    || value === 'hybrid_smart'
    || value === 'weighted'
    || value === 'failover'
    || value === 'round_robin'
}

function assertClientIPStatsList(value: unknown): ClientIPStatsListResult {
  expect(isRecord(value), 'client IP stats list data must be an object')
  expect(Array.isArray(value.items), 'client IP stats list items must be an array')
  expect(isNonNegativeInteger(value.pageUpperBound), 'client IP stats list pageUpperBound must be a non-negative integer')
  expect(typeof value.hasMore === 'boolean', 'client IP stats list hasMore must be boolean')
  expect(value.page === 1, 'client IP stats list page must be 1')
  expect(value.pageSize === 20, 'client IP stats list pageSize must be 20')
  expect(typeof value.rangeReady === 'boolean', 'client IP stats list rangeReady must be boolean')
  const range = assertClientIPStatsRange(value.range)
  expect(value.items.length <= value.pageSize, 'client IP stats list must not exceed pageSize')

  if (!value.rangeReady) {
    expect(value.items.length === 0, 'client IP stats list must be empty when rangeReady is false')
    expect(value.pageUpperBound === 0, 'client IP stats list pageUpperBound must be 0 when rangeReady is false')
    expect(value.hasMore === false, 'client IP stats list hasMore must be false when rangeReady is false')
  } else {
    value.items.forEach((item, index) => assertClientIPStatsItem(item, range.days, index))
    expect(
      value.pageUpperBound === value.items.length + (value.hasMore ? 1 : 0),
      'client IP stats list pageUpperBound must be the progressive first-page upper bound'
    )
    if (value.hasMore) {
      expect(value.items.length === value.pageSize, 'client IP stats list hasMore requires a full page')
    }
  }

  return value as unknown as ClientIPStatsListResult
}

function assertClientIPStatsDetail(
  value: unknown,
  expectedIpHash: string,
  expectedListItem: ClientIPStatsItem | undefined,
  expectedRange: ClientIPStatsRange
): ClientIPStatsDetailResult {
  expect(isRecord(value), 'client IP stats detail data must be an object')
  expect(value.ipHash === expectedIpHash, 'client IP stats detail ipHash must match the requested hash')
  expect(isNonEmptyString(value.aggregateIpKey), 'client IP stats detail aggregateIpKey must be a non-empty string')
  assertOptionalString(value, 'lastSeenAt', 'client IP stats detail')
  if (expectedListItem) {
    expect(
      value.aggregateIpKey === expectedListItem.aggregateIpKey,
      'client IP stats detail aggregateIpKey must match the list item'
    )
    expect(
      value.lastSeenAt === expectedListItem.lastSeenAt,
      'client IP stats detail lastSeenAt must match the list item'
    )
  }
  expect(Array.isArray(value.items), 'client IP stats detail items must be an array')
  expect(isNonNegativeInteger(value.pageUpperBound), 'client IP stats detail pageUpperBound must be a non-negative integer')
  expect(typeof value.hasMore === 'boolean', 'client IP stats detail hasMore must be boolean')
  expect(value.page === 1, 'client IP stats detail page must be 1')
  expect(value.pageSize === 20, 'client IP stats detail pageSize must be 20')
  expect(typeof value.rangeReady === 'boolean', 'client IP stats detail rangeReady must be boolean')
  const range = assertClientIPStatsRange(value.range, 'client IP stats detail')
  expect(
    range.startDate === expectedRange.startDate
      && range.endDate === expectedRange.endDate
      && range.days === expectedRange.days
      && range.maxDays === expectedRange.maxDays,
    'client IP stats detail range must match the list range'
  )
  expect(value.items.length <= value.pageSize, 'client IP stats detail must not exceed pageSize')

  if (!value.rangeReady) {
    expect(value.items.length === 0, 'client IP stats detail must be empty when rangeReady is false')
    expect(value.pageUpperBound === 0, 'client IP stats detail pageUpperBound must be 0 when rangeReady is false')
    expect(value.hasMore === false, 'client IP stats detail hasMore must be false when rangeReady is false')
  } else {
    const items = value.items.map((item, index) => assertClientIPAccountUsageItem(item, range.days, index))
    expect(
      value.pageUpperBound === items.length + (value.hasMore ? 1 : 0),
      'client IP stats detail pageUpperBound must be the progressive first-page upper bound'
    )
    if (value.hasMore) {
      expect(items.length === value.pageSize, 'client IP stats detail hasMore requires a full page')
    }
    for (let index = 1; index < items.length; index += 1) {
      expect(
        Number(items[index - 1].rangeUsage.requestCount) <= Number(items[index].rangeUsage.requestCount),
        'client IP stats detail must use requestCount ascending when only sortOrder=asc is provided'
      )
    }
  }

  return value as unknown as ClientIPStatsDetailResult
}

function assertClientIPStatsRange(value: unknown, label = 'client IP stats list'): ClientIPStatsRange {
  expect(isRecord(value), `${label} range must be an object`)
  expect(isDateKey(value.startDate), `${label} range.startDate must be YYYY-MM-DD`)
  expect(isDateKey(value.endDate), `${label} range.endDate must be YYYY-MM-DD`)
  expect(isNonNegativeInteger(value.days) && value.days > 0, `${label} range.days must be a positive integer`)
  expect(value.maxDays === 31, `${label} range.maxDays must be 31`)
  const expectedDays = inclusiveDateKeyDays(value.startDate, value.endDate)
  expect(expectedDays > 0, `${label} range must not end before it starts`)
  expect(value.days === expectedDays, `${label} range.days must match its inclusive date range`)
  expect(value.days <= value.maxDays, `${label} range.days must not exceed maxDays`)
  return value as unknown as ClientIPStatsRange
}

function assertClientIPStatsItem(value: unknown, rangeDays: number, index: number): ClientIPStatsItem {
  const label = `client IP stats list item ${index}`
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.ipHash), `${label}.ipHash must be a non-empty string`)
  expect(isNonEmptyString(value.aggregateIpKey), `${label}.aggregateIpKey must be a non-empty string`)
  assertOptionalString(value, 'lastSeenAt', label)
  expect(
    value.status === 'normal' || value.status === 'blacklisted' || value.status === 'allowlisted',
    `${label}.status is invalid`
  )
  expect(isRecord(value.rangeUsage), `${label}.rangeUsage must be an object`)
  assertClientIPUsageSummary(value.rangeUsage, rangeDays, `${label}.rangeUsage`)
  return value as unknown as ClientIPStatsItem
}

function assertClientIPAccountUsageItem(
  value: unknown,
  rangeDays: number,
  index: number
): ClientIPAccountUsageItem {
  const label = `client IP stats detail item ${index}`
  expect(isRecord(value), `${label} must be an object`)
  expect(isNonEmptyString(value.accountId), `${label}.accountId must be a non-empty string`)
  for (const field of [
    'accountName',
    'accountOwnerSystemAccountId',
    'accountOwnerSystemAccountName'
  ]) {
    assertOptionalString(value, field, label)
  }
  expect(isRecord(value.rangeUsage), `${label}.rangeUsage must be an object`)
  assertClientIPUsageSummary(value.rangeUsage, rangeDays, `${label}.rangeUsage`)
  return value as unknown as ClientIPAccountUsageItem
}

function assertClientIPUsageSummary(
  usage: Record<string, unknown>,
  rangeDays: number,
  label: string
): void {
  for (const field of [
    'requestCount',
    'successCount',
    'errorCount',
    'inputTokens',
    'outputTokens',
    'cacheReadTokens',
    'cacheWriteTokens',
    'cacheWrite1hTokens',
    'thinkingTokens',
    'inputImageTokens',
    'outputImageTokens',
    'totalTokens',
    'activeDays'
  ]) {
    expect(isNonNegativeInteger(usage[field]), `${label}.${field} must be a non-negative integer`)
  }
  for (const field of ['errorRate', 'cacheReadCost', 'cacheWriteCost', 'totalCost']) {
    expect(isNonNegativeFiniteNumber(usage[field]), `${label}.${field} must be a finite non-negative number`)
  }
  expect(
    usage.totalTokens === Number(usage.inputTokens) + Number(usage.outputTokens),
    `${label}.totalTokens must equal inputTokens plus outputTokens`
  )
  const expectedErrorRate = usage.requestCount === 0
    ? 0
    : Number(usage.errorCount) / Number(usage.requestCount)
  expect(
    numbersNearlyEqual(Number(usage.errorRate), expectedErrorRate),
    `${label}.errorRate must match errorCount divided by requestCount`
  )
  expect(Number(usage.activeDays) <= rangeDays, `${label}.activeDays must not exceed range.days`)

  for (const field of ['averageDurationMs', 'averageFirstTokenMs']) {
    if (Object.hasOwn(usage, field)) {
      expect(isNonNegativeFiniteNumber(usage[field]), `${label}.${field} must be a finite non-negative number`)
    }
  }
  if (Object.hasOwn(usage, 'maxDurationMs')) {
    expect(
      isNonNegativeInteger(usage.maxDurationMs) && Number(usage.maxDurationMs) > 0,
      `${label}.maxDurationMs must be a positive integer`
    )
  }
  assertOptionalString(usage, 'lastUsedAt', label)
  assertOptionalString(usage, 'lastErrorAt', label)
}

function isClientIPHash(value: string): boolean {
  return /^[0-9a-f]{64}$/i.test(value)
}

function assertOptionalString(value: Record<string, unknown>, field: string, label: string): void {
  if (Object.hasOwn(value, field)) {
    expect(typeof value[field] === 'string', `${label}.${field} must be a string when present`)
  }
}

function selectOwnerNonDefaultGroup(items: unknown[], requestedGroupId?: string): GroupRecord {
  const groups = items.map((item, index) => assertGroup(item, `groups list item ${index}`))
  if (requestedGroupId) {
    const requested = groups.find((group) => group.id === requestedGroupId)
    expect(requested, 'Configured group was not returned by groups list')
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
    expect(Object.hasOwn(item, 'defaultReasoningEffort'), `${label}.defaultReasoningEffort must be explicit`)
    expect(
      item.defaultReasoningEffort === null || isNonEmptyString(item.defaultReasoningEffort),
      `${label}.defaultReasoningEffort must be a non-empty string or null`
    )
    return item as unknown as ModelOptionRecord
  })
}

function assertAccountTestOptions(value: unknown, expectedAccountId: string): AccountTestOptionsRecord {
  const label = 'account test options data'
  expect(isRecord(value), `${label} must be an object`)
  expect(value.accountId === expectedAccountId, `${label}.accountId must match the configured account`)
  expect(isNonEmptyString(value.defaultModel), `${label}.defaultModel must be a non-empty string`)
  expect(Array.isArray(value.models) && value.models.length > 0, `${label}.models must be a non-empty array`)

  const models = value.models.map((item, index): AccountTestOptionModel => {
    const itemLabel = `${label}.models item ${index}`
    expect(isRecord(item), `${itemLabel} must be an object`)
    expect(isNonEmptyString(item.model), `${itemLabel}.model must be a non-empty string`)
    assertStringArray(item.supportedApiProtocols, `${itemLabel}.supportedApiProtocols`)
    assertAccountTestEndpointModes(item.testEndpointModes, `${itemLabel}.testEndpointModes`)
    return item as unknown as AccountTestOptionModel
  })
  const defaultModel = models.find((item) => item.model === value.defaultModel)
  expect(defaultModel, `${label}.defaultModel must reference a model`)

  const testEndpointModes = assertAccountTestEndpointModes(
    value.testEndpointModes,
    `${label}.testEndpointModes`
  )
  expect(
    endpointModesEqual(defaultModel.testEndpointModes, testEndpointModes),
    `${label}.testEndpointModes must equal the default model testEndpointModes`
  )
  expect(
    isAccountTestEndpointMode(value.defaultTestEndpointMode),
    `${label}.defaultTestEndpointMode must be a legal account test endpoint mode`
  )
  expect(
    value.defaultTestEndpointMode === testEndpointModes[0],
    `${label}.defaultTestEndpointMode must equal the first testEndpointModes item`
  )
  return value as unknown as AccountTestOptionsRecord
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

function optionalClientIPHashEnvironmentValue(env: SmokeEnvironment, name: string): string | undefined {
  const value = optionalEnvironmentValue(env, name)
  if (value === undefined) {
    return undefined
  }
  expect(isClientIPHash(value), `${name} must be a 64-character hexadecimal hash`)
  return value.toLowerCase()
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

function assertAccountTestEndpointModes(value: unknown, label: string): AccountTestEndpointMode[] {
  expect(Array.isArray(value) && value.length > 0, `${label} must be a non-empty array`)
  expect(
    value.every(isAccountTestEndpointMode),
    `${label} must contain only legal account test endpoint modes`
  )
  return value
}

function isAccountTestEndpointMode(value: unknown): value is AccountTestEndpointMode {
  return typeof value === 'string' && accountTestEndpointModeSet.has(value)
}

function endpointModesEqual(left: AccountTestEndpointMode[], right: AccountTestEndpointMode[]): boolean {
  return left.length === right.length && left.every((mode, index) => mode === right[index])
}

function isDateKey(value: unknown): value is string {
  if (typeof value !== 'string') {
    return false
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) {
    return false
  }
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const parsed = new Date(Date.UTC(year, month - 1, day))
  return parsed.getUTCFullYear() === year
    && parsed.getUTCMonth() === month - 1
    && parsed.getUTCDate() === day
}

function inclusiveDateKeyDays(startDate: string, endDate: string): number {
  const start = Date.parse(`${startDate}T00:00:00.000Z`)
  const end = Date.parse(`${endDate}T00:00:00.000Z`)
  return Math.trunc((end - start) / 86_400_000) + 1
}

function isNonNegativeFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function numbersNearlyEqual(actual: number, expected: number): boolean {
  const tolerance = Number.EPSILON * Math.max(1, Math.abs(actual), Math.abs(expected)) * 4
  return Math.abs(actual - expected) <= tolerance
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

function isNonNegativeSafeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && Number(value) >= 0
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
