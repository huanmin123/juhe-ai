import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { createSession, updateSystemAccountLastLogin, verifySystemAccountCredentialsAsync } from '../../storage/repositories.js'
import { systemSettingKeys } from '../../storage/settings.repository.js'
import { createMockGatewayFixture } from '../maintenance/mockdata/fixtures.js'

const backendUrl = trimTrailingSlash(runtimeConfig.smokeTest.backendUrl)
const accountName = runtimeConfig.smokeTest.accountName
const model = runtimeConfig.smokeTest.model
const prompt = runtimeConfig.smokeTest.prompt
const defaultRequestTimeoutMs = 60_000
const accountTestTimeoutMs = 180_000
const gatewayKeyTestTimeoutMs = 30_000
const streamRequestTimeoutMs = 240_000
const usageRecordPollTimeoutMs = 15_000
const temporaryResourcePrefix = '回归'
const systemApiPrefix = '/__aisys__/api'

let sessionCookie = ''

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountSummary {
  id: string
  name: string
  type: string
  providerCode: string
  status: string
  schedulable?: boolean
  cooldownUntil?: string
  boundGroupId?: string
  ownerSystemAccountId?: string
  proxyProfileId?: string
}

interface ApiKeySummary {
  id: string
  name: string
  key?: string
  status: string
  groupId?: string
}

interface AccountTestResult {
  accountId: string
  accountName: string
  success: boolean
  statusCode?: number
  message: string
  proxyUrl?: string
  tokenRefreshed?: boolean
}

interface AccountTestTask {
  id: string
  status: 'queued' | 'running' | 'success' | 'failed' | 'canceled'
  message?: string
  result?: AccountTestResult
}

interface TestedAccount {
  account: AccountSummary
  test: AccountTestResult
}

interface UsageRecordSummary {
  traceId: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  endpoint?: string
  model?: string
  stream: boolean
  success: boolean
  statusCode?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  createdAt: string
}

interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  page: number
  pageSize: number
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface SystemAccountSummary {
  id: string
  username?: string
  role?: string
  status?: string
}

interface SystemAccountListResult {
  items: SystemAccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface SystemSettings {
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
  streamClientTotalWaitTimeoutSeconds?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  operationLogEnabled?: boolean
  operationLogRetentionDays?: number
  operationLogMaxChangesPerRecord?: number
  statsAggregationIntervalSeconds?: number
  statsAggregationBatchSize?: number
  statsAggregationMaxBatchesPerRun?: number
  groupAccountStatsRefreshIntervalSeconds?: number
  systemMetricsSampleIntervalSeconds?: number
  tableMonitorMaxTablesPerRun?: number
  accountQualityRefreshIntervalSeconds?: number
  accountQualityWindowMinutes?: number
  accountTestTaskConcurrency?: number
  cooldownAccountRetestIntervalSeconds?: number
  cooldownAccountRetestBatchSize?: number
  cooldownAccountRetestMaxBackoffHours?: number
  cooldownAccountRetestLongTermIntervalHours?: number
  oauthAccessTokenRefreshIntervalSeconds?: number
  oauthAccessTokenRefreshLeadSeconds?: number
  oauthAccessTokenRefreshBatchSize?: number
  oauthAccessTokenRefreshRetryBackoffSeconds?: number
  modelCheckRetentionDays?: number
  usageRecordRetentionDays?: number
  usageStatsDailyRetentionDays?: number
  usageStatsHourlyRetentionDays?: number
  systemMetricsRetentionDays?: number
  systemMetricsHourlyRetentionDays?: number
  dataRetentionCleanupIntervalMinutes?: number
  dataRetentionCleanupBatchSize?: number
  dataRetentionCleanupMaxBatchesPerRun?: number
}

interface ResponsePayload {
  id?: string
  status?: string
  model?: string
  usage?: {
    input_tokens?: number
    output_tokens?: number
    input_tokens_details?: {
      cached_tokens?: number
    }
  }
}

interface SmokeResourceState {
  accountId?: string
  ownerSystemAccountId?: string
  temporaryGroupIds: string[]
  activeGatewayGroupId?: string
  temporaryApiKeyId?: string
  temporaryRouteStrategyId?: string
  temporaryAccountId?: string
  temporaryMockUpstream?: http.Server
}

async function main(): Promise<void> {
  const summary: string[] = []
  const resourceState: SmokeResourceState = { temporaryGroupIds: [] }
  let seededGatewayKey: (ApiKeySummary & { key: string }) | undefined

  try {
    await checkHealth()
    summary.push('健康检查通过')

    await loginAsAdmin()
    summary.push('登录通过')

    const settings = await getEnvelope<SystemSettings>(apiPath('/settings'))
    assert(typeof settings.defaultTemporaryUnschedulableMinutes === 'number', '系统设置缺少 defaultTemporaryUnschedulableMinutes')
    assert(typeof settings.temporaryUnschedulableRetryIntervalSeconds === 'number', '系统设置缺少 temporaryUnschedulableRetryIntervalSeconds')
    assert(typeof settings.temporaryUnschedulableRetryAttempts === 'number', '系统设置缺少 temporaryUnschedulableRetryAttempts')
    assert(typeof settings.streamCircuitBreakerEnabled === 'boolean', '系统设置缺少 streamCircuitBreakerEnabled')
    assert(typeof settings.streamRequestTimeoutSeconds === 'number', '系统设置缺少 streamRequestTimeoutSeconds')
    assert(typeof settings.streamIdleTimeoutSeconds === 'number', '系统设置缺少 streamIdleTimeoutSeconds')
    assert(typeof settings.streamClientTotalWaitTimeoutSeconds === 'number', '系统设置缺少 streamClientTotalWaitTimeoutSeconds')
    assert(typeof settings.streamFailureThresholdCount === 'number', '系统设置缺少 streamFailureThresholdCount')
    assert(typeof settings.streamFailureThresholdWindowMinutes === 'number', '系统设置缺少 streamFailureThresholdWindowMinutes')
    assert(typeof settings.operationLogEnabled === 'boolean', '系统设置缺少 operationLogEnabled')
    assert(typeof settings.operationLogRetentionDays === 'number', '系统设置缺少 operationLogRetentionDays')
    assert(typeof settings.operationLogMaxChangesPerRecord === 'number', '系统设置缺少 operationLogMaxChangesPerRecord')
    assert(typeof settings.statsAggregationIntervalSeconds === 'number', '系统设置缺少 statsAggregationIntervalSeconds')
    assert(typeof settings.statsAggregationBatchSize === 'number', '系统设置缺少 statsAggregationBatchSize')
    assert(typeof settings.statsAggregationMaxBatchesPerRun === 'number', '系统设置缺少 statsAggregationMaxBatchesPerRun')
    assert(typeof settings.groupAccountStatsRefreshIntervalSeconds === 'number', '系统设置缺少 groupAccountStatsRefreshIntervalSeconds')
    assert(typeof settings.systemMetricsSampleIntervalSeconds === 'number', '系统设置缺少 systemMetricsSampleIntervalSeconds')
    assert(typeof settings.tableMonitorMaxTablesPerRun === 'number', '系统设置缺少 tableMonitorMaxTablesPerRun')
    assert(typeof settings.accountQualityRefreshIntervalSeconds === 'number', '系统设置缺少 accountQualityRefreshIntervalSeconds')
    assert(typeof settings.accountQualityWindowMinutes === 'number', '系统设置缺少 accountQualityWindowMinutes')
    assert(typeof settings.accountTestTaskConcurrency === 'number', '系统设置缺少 accountTestTaskConcurrency')
    assert(typeof settings.cooldownAccountRetestIntervalSeconds === 'number', '系统设置缺少 cooldownAccountRetestIntervalSeconds')
    assert(typeof settings.cooldownAccountRetestBatchSize === 'number', '系统设置缺少 cooldownAccountRetestBatchSize')
    assert(typeof settings.cooldownAccountRetestMaxBackoffHours === 'number', '系统设置缺少 cooldownAccountRetestMaxBackoffHours')
    assert(typeof settings.cooldownAccountRetestLongTermIntervalHours === 'number', '系统设置缺少 cooldownAccountRetestLongTermIntervalHours')
    assert(typeof settings.oauthAccessTokenRefreshIntervalSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshIntervalSeconds')
    assert(typeof settings.oauthAccessTokenRefreshLeadSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshLeadSeconds')
    assert(typeof settings.oauthAccessTokenRefreshBatchSize === 'number', '系统设置缺少 oauthAccessTokenRefreshBatchSize')
    assert(typeof settings.oauthAccessTokenRefreshRetryBackoffSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshRetryBackoffSeconds')
    assert(typeof settings.modelCheckRetentionDays === 'number', '系统设置缺少 modelCheckRetentionDays')
    assert(typeof settings.usageRecordRetentionDays === 'number', '系统设置缺少 usageRecordRetentionDays')
    assert(typeof settings.usageStatsDailyRetentionDays === 'number', '系统设置缺少 usageStatsDailyRetentionDays')
    assert(typeof settings.usageStatsHourlyRetentionDays === 'number', '系统设置缺少 usageStatsHourlyRetentionDays')
    assert(typeof settings.systemMetricsRetentionDays === 'number', '系统设置缺少 systemMetricsRetentionDays')
    assert(typeof settings.systemMetricsHourlyRetentionDays === 'number', '系统设置缺少 systemMetricsHourlyRetentionDays')
    assert(typeof settings.dataRetentionCleanupIntervalMinutes === 'number', '系统设置缺少 dataRetentionCleanupIntervalMinutes')
    assert(typeof settings.dataRetentionCleanupBatchSize === 'number', '系统设置缺少 dataRetentionCleanupBatchSize')
    assert(typeof settings.dataRetentionCleanupMaxBatchesPerRun === 'number', '系统设置缺少 dataRetentionCleanupMaxBatchesPerRun')
    assertExactSettingKeys(settings)
    summary.push('系统设置检查通过')

    let accounts = await fetchSmokeAccounts()
    if (!accountName && !accounts.some(isOpenAIAccountCandidate)) {
      const seeded = await createTemporaryMockOpenAIGateway(resourceState)
      seededGatewayKey = seeded.apiKey
      accounts = [seeded.account, ...accounts.filter((account) => account.id !== seeded.account.id)]
      summary.push(`空库通过 Mockdata 生成临时 mock OpenAI 账户：${seeded.account.name}`)
    }
    assert(accounts.length > 0, '账户列表为空，且未能创建临时 mock OpenAI 账户')
    const selectedAccount = accountName
      ? await testNamedAccount(accounts, accountName)
      : await selectFirstUsableOpenAIAccount(accounts)
    const { account: targetAccount, test: accountTest } = selectedAccount
    assert(targetAccount.ownerSystemAccountId, `账户 ${targetAccount.name} 缺少 ownerSystemAccountId，无法按正规管理流程创建临时 API Key`)
    assert(targetAccount.boundGroupId, `账户 ${targetAccount.name} 缺少当前绑定分组，无法创建临时网关 API Key`)
    resourceState.accountId = targetAccount.id
    resourceState.ownerSystemAccountId = targetAccount.ownerSystemAccountId
    resourceState.activeGatewayGroupId = targetAccount.boundGroupId
    summary.push(`账户检查通过：${targetAccount.name}`)
    summary.push(`账户测试通过：${accountTest.message}${accountTest.proxyUrl ? '，代理已配置' : ''}`)

    const gatewayKey = seededGatewayKey && targetAccount.id === resourceState.temporaryAccountId
      ? seededGatewayKey
      : await createTemporaryGatewayKeyForAccount(targetAccount, resourceState)
    const models = await requestJson<Record<string, unknown>>('/v1/models', {
      headers: { authorization: `Bearer ${gatewayKey.key}` }
    }, gatewayKeyTestTimeoutMs)
    assert(models.object === 'list', '/v1/models 未返回 list')
    assert(Array.isArray(models.data), '/v1/models 未返回 data 数组')
    for (const item of models.data as unknown[]) {
      const model = item as Record<string, unknown>
      assert(JSON.stringify(Object.keys(model).sort()) === JSON.stringify(['created', 'id', 'object', 'owned_by']), '/v1/models 模型项包含非标准字段')
      assert(typeof model.id === 'string' && model.id.length > 0, '/v1/models 模型项缺少 id')
      assert(model.object === 'model', '/v1/models 模型项 object 不是 model')
      assert(Number.isInteger(model.created), '/v1/models 模型项 created 不是 Unix 秒整数')
      assert(typeof model.owned_by === 'string' && model.owned_by.length > 0, '/v1/models 模型项缺少 owned_by')
    }
    summary.push(`临时网关 API Key 检查通过：${gatewayKey.name}`)
    summary.push(`/v1/models 检查通过：${(models.data as unknown[]).length} 个模型`)

    if (targetAccount.type === 'oauth') {
      const streamText = await requestText('/v1/responses', {
        method: 'POST',
        headers: {
          authorization: `Bearer ${gatewayKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify(createOAuthResponsesPayload(true))
      }, streamRequestTimeoutMs)
      assert(streamText.includes('response.completed'), 'OAuth 流式 responses 未包含 response.completed')
      assert(!streamText.includes('response.failed'), 'OAuth 流式 responses 包含 response.failed')
      assert(!streamText.includes('response.incomplete'), 'OAuth 流式 responses 包含 response.incomplete')
      summary.push('OAuth 流式 responses 检查通过')
    } else {
      const responsePayload = await requestJson<ResponsePayload>('/v1/responses', {
        method: 'POST',
        headers: {
          authorization: `Bearer ${gatewayKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify(createApiKeyResponsesPayload(false))
      }, streamRequestTimeoutMs)
      assert(responsePayload.status === 'completed', `非流式 responses 状态异常：${responsePayload.status ?? '未知'}`)
      assert(typeof responsePayload.usage?.input_tokens === 'number', '非流式 responses 未返回 input_tokens')
      assert(typeof responsePayload.usage?.output_tokens === 'number', '非流式 responses 未返回 output_tokens')
      summary.push(`非流式 responses 检查通过：${responsePayload.usage.input_tokens}+${responsePayload.usage.output_tokens} tokens`)

      const streamText = await requestText('/v1/responses', {
        method: 'POST',
        headers: {
          authorization: `Bearer ${gatewayKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify(createApiKeyResponsesPayload(true))
      }, streamRequestTimeoutMs)
      assert(streamText.includes('response.completed'), '流式 responses 未包含 response.completed')
      assert(!streamText.includes('response.failed'), '流式 responses 包含 response.failed')
      summary.push('流式 responses 检查通过')
    }

    const usageRecords = await waitForSmokeUsageRecords(resourceState)
    assert(usageRecords.some((record) => record.endpoint === 'GET /v1/models' && record.success), '找不到本次 /v1/models 接口使用记录')
    const responseRecords = usageRecords.filter((record) => record.endpoint === 'POST /v1/responses' && record.success)
    assert(responseRecords.length > 0, '找不到本次 /v1/responses 接口使用记录')
    assert(responseRecords.some((record) => typeof record.inputTokens === 'number' && typeof record.outputTokens === 'number' && typeof record.costUsd === 'number'), '找不到本次 responses token/cost 使用记录')
    summary.push('使用记录检查通过')

    const auditRuntime = await getEnvelope<Record<string, unknown>>(apiPath('/audit-logs/runtime'))
    assert(typeof auditRuntime.queueLength === 'number', '审计运行态缺少 queueLength')
    assert(typeof auditRuntime.activeCaptureCount === 'number', '审计运行态缺少 activeCaptureCount')
    assert(typeof auditRuntime.settings === 'object' && auditRuntime.settings !== null, '审计运行态缺少 settings')
    summary.push('审计运行态检查通过')

    console.log('\njuhe-ai 烟测通过')
    for (const item of summary) {
      console.log(`- ${item}`)
    }
  } finally {
    await cleanupSmokeResources(resourceState)
  }
}

async function checkHealth(): Promise<void> {
  const health = await requestJson<Record<string, unknown>>('/__aisys__/health')
  assert(health.status === 'ok', `健康检查失败：${JSON.stringify(health)}`)
}

async function fetchSmokeAccounts(): Promise<AccountSummary[]> {
  const data = await getEnvelope<AccountSummary[] | AccountListResult>(apiPath('/accounts'))
  if (Array.isArray(data)) return data
  if (Array.isArray(data.items)) return data.items
  throw new Error('账户列表返回格式异常')
}

async function fetchSmokeSystemAccounts(): Promise<SystemAccountSummary[]> {
  const data = await getEnvelope<SystemAccountSummary[] | SystemAccountListResult>(apiPath('/system-accounts'))
  if (Array.isArray(data)) return data
  if (Array.isArray(data.items)) return data.items
  throw new Error('系统账户列表返回格式异常')
}

async function loginAsAdmin(): Promise<void> {
  assertSmokeBackendSupportsLocalSession()
  const account = await verifySystemAccountCredentialsAsync(
    runtimeConfig.smokeTest.adminUsername,
    runtimeConfig.smokeTest.adminPassword
  )
  assert(account, `烟测管理员账号或密码错误：${runtimeConfig.smokeTest.adminUsername}`)
  assert(account.role === 'super_admin' || account.role === 'admin', `烟测登录账号不是管理员：${account.username}`)
  const session = createSession(account.id, 1)
  updateSystemAccountLastLogin(account.id)
  sessionCookie = `juhe_ai_session=${session.token}`

  let currentUser: { role?: string; username?: string }
  try {
    currentUser = await getEnvelope<{ role?: string; username?: string }>(apiPath('/auth/me'))
  } catch (error) {
    throw new Error(
      `烟测会话未被后端识别。pnpm test:smoke 当前会在脚本进程校验管理员凭据并写入短时会话，`
      + `JUHE_AI_BACKEND_URL 必须指向共享同一 JUHE_AI_DATABASE_PATH 和 JUHE_AI_SECRET 的本机后端。`
      + `当前后端地址：${backendUrl}；原始错误：${error instanceof Error ? error.message : String(error)}`
    )
  }
  assert(
    currentUser.role === 'super_admin' || currentUser.role === 'admin',
    `烟测会话未被后端识别为管理员：${currentUser.username ?? 'unknown'}`
  )
}

function assertSmokeBackendSupportsLocalSession(): void {
  const url = new URL(backendUrl)
  const hostname = url.hostname.toLowerCase()
  if (hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]') {
    return
  }
  throw new Error(
    `pnpm test:smoke 当前会在脚本进程写入短时会话，JUHE_AI_BACKEND_URL 只能指向共享同一业务库和密钥的本机后端；`
    + `当前配置为 ${backendUrl}。请在目标后端所在环境内运行 smoke，或改用本机 http://127.0.0.1:<port>。`
  )
}

async function createTemporaryMockOpenAIGateway(
  resourceState: SmokeResourceState
): Promise<{ account: AccountSummary; apiKey: ApiKeySummary & { key: string } }> {
  const ownerSystemAccountId = await resolveSmokeOwnerSystemAccountId()
  const upstream = createMockOpenAIUpstream()
  await listen(upstream)
  resourceState.temporaryMockUpstream = upstream
  resourceState.ownerSystemAccountId = ownerSystemAccountId

  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
  const fixture = createMockGatewayFixture({
    label: '烟测',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: ownerSystemAccountId,
    accountCount: 1,
    accountConcurrencyLimit: 20
  })
  const account = fixture.accounts[0]
  assert(account, 'Mockdata 烟测夹具未生成账户')
  assert(fixture.apiKey?.key, 'Mockdata 烟测夹具未生成本地网关 Key')
  resourceState.activeGatewayGroupId = fixture.group.id
  resourceState.temporaryGroupIds.push(fixture.group.id)
  resourceState.accountId = account.id
  resourceState.temporaryAccountId = account.id
  resourceState.temporaryApiKeyId = fixture.apiKey.id
  return {
    account: {
      ...account,
      ownerSystemAccountId: account.ownerSystemAccountId ?? ownerSystemAccountId,
      boundGroupId: account.boundGroupId ?? fixture.group.id
    },
    apiKey: fixture.apiKey
  }
}

async function resolveSmokeOwnerSystemAccountId(): Promise<string> {
  const accounts = await fetchSmokeSystemAccounts()
  const configuredAdmin = accounts.find((account) => account.username === runtimeConfig.smokeTest.adminUsername)
  const activeAdmin = configuredAdmin ?? accounts.find((account) => (account.role === 'super_admin' || account.role === 'admin') && account.status !== 'disabled')
  const activeAccount = activeAdmin ?? accounts.find((account) => account.status !== 'disabled')
  assert(activeAccount?.id, '无法定位烟测系统账户，不能创建临时账号')
  return activeAccount.id
}

async function createTemporaryGatewayKeyForAccount(
  account: AccountSummary,
  resourceState: SmokeResourceState
): Promise<ApiKeySummary & { key: string }> {
  const ownerScope = ownerScopeQuery(resourceState)
  const groupId = account.boundGroupId ?? resourceState.activeGatewayGroupId
  assert(groupId, `账户 ${account.name} 缺少可用分组，无法创建临时网关 API Key`)
  resourceState.activeGatewayGroupId = groupId
  const routeStrategy = await postEnvelope<{ id: string }>(apiPath(`/route-strategies${ownerScope}`), {
    name: `${temporaryResourcePrefix}-Route-${smokeRunId()}`,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active',
    description: '真实网关链路临时烟测策略路由'
  })
  resourceState.temporaryRouteStrategyId = routeStrategy.id
  const apiKey = await postEnvelope<ApiKeySummary>(apiPath(`/api-keys${ownerScope}`), {
    name: `${temporaryResourcePrefix}-Key-${smokeRunId()}`,
    routeStrategyId: routeStrategy.id,
    status: 'active',
    description: '真实网关链路临时烟测 Key'
  })
  resourceState.temporaryApiKeyId = apiKey.id
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  return apiKey as ApiKeySummary & { key: string }
}

async function waitForSmokeUsageRecords(resourceState: SmokeResourceState): Promise<UsageRecordSummary[]> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < usageRecordPollTimeoutMs) {
    const usageRecords = await fetchSmokeUsageRecords(resourceState)
    const matched = usageRecords.filter((record) => {
      if (resourceState.temporaryApiKeyId && record.apiKeyId === resourceState.temporaryApiKeyId) return true
      return Boolean(resourceState.activeGatewayGroupId && record.groupId === resourceState.activeGatewayGroupId)
    })
    if (
      matched.some((record) => record.endpoint === 'GET /v1/models' && record.success)
      && matched.some((record) => record.endpoint === 'POST /v1/responses' && record.success)
    ) {
      return matched
    }
    await sleep(500)
  }
  return (await fetchSmokeUsageRecords(resourceState)).filter((record) => {
    if (resourceState.temporaryApiKeyId && record.apiKeyId === resourceState.temporaryApiKeyId) return true
    return Boolean(resourceState.activeGatewayGroupId && record.groupId === resourceState.activeGatewayGroupId)
  })
}

async function fetchSmokeUsageRecords(resourceState: SmokeResourceState): Promise<UsageRecordSummary[]> {
  const scopeQuery = ownerScopeQuery(resourceState)
  const separator = scopeQuery.includes('?') ? '&' : '?'
  const result = await getEnvelope<UsageRecordListResult>(apiPath(`/usage-records${scopeQuery}${separator}page=1&pageSize=200`))
  return result.items
}

async function cleanupSmokeResources(resourceState: SmokeResourceState): Promise<void> {
  try {
    if (!resourceState.ownerSystemAccountId) return
    const ownerScope = ownerScopeQuery(resourceState)
    if (!ownerScope) return

    if (resourceState.temporaryApiKeyId) {
      await ignoreCleanupError(() => requestNoContent(apiPath(`/api-keys/${resourceState.temporaryApiKeyId}${ownerScope}`), { method: 'DELETE' }))
    }
    if (resourceState.temporaryRouteStrategyId) {
      await ignoreCleanupError(() => requestNoContent(apiPath(`/route-strategies/${resourceState.temporaryRouteStrategyId}${ownerScope}`), { method: 'DELETE' }))
    }
    if (resourceState.temporaryAccountId) {
      await ignoreCleanupError(() => requestNoContent(apiPath(`/accounts/${resourceState.temporaryAccountId}${ownerScope}`), { method: 'DELETE' }))
    }
    for (const groupId of [...resourceState.temporaryGroupIds].reverse()) {
      await ignoreCleanupError(() => requestNoContent(apiPath(`/groups/${groupId}${ownerScope}`), { method: 'DELETE' }))
    }
  } finally {
    await closeServer(resourceState.temporaryMockUpstream)
  }
}

async function ignoreCleanupError(action: () => Promise<unknown>): Promise<void> {
  try {
    await action()
  } catch (error) {
    console.warn(`烟测清理警告：${error instanceof Error ? error.message : String(error)}`)
  }
}

function ownerScopeQuery(resourceState: SmokeResourceState): string {
  assert(resourceState.ownerSystemAccountId, '缺少目标系统账户，无法按管理作用域调用接口')
  return `?systemAccountId=${encodeURIComponent(resourceState.ownerSystemAccountId)}`
}

function smokeRunId(): string {
  const time = new Date().toISOString().replace(/[-:TZ.]/g, '')
  return `${time}-${Math.random().toString(36).slice(2, 8)}`
}

async function testNamedAccount(accounts: AccountSummary[], name: string): Promise<TestedAccount> {
  const account = accounts.find((item) => item.name === name)
  assert(account, `找不到目标账户：${name}`)
  assertAccountCanBeTested(account, `目标账户不可用于烟测：${name}`)

  const test = await testAccount(account)
  assert(test.success, `账户测试失败：${test.message}，状态码=${test.statusCode ?? '未知'}`)
  return { account, test }
}

async function selectFirstUsableOpenAIAccount(accounts: AccountSummary[]): Promise<TestedAccount> {
  const candidates = accounts.filter(isOpenAIAccountCandidate)
  assert(candidates.length > 0, '找不到启用且可调度的 OpenAI 账户')

  const failures: string[] = []
  for (const account of candidates) {
    console.log(`烟测：正在测试账户 ${account.name}`)
    const test = await testAccount(account)
    if (test.success) {
      return { account, test }
    }
    failures.push(`${account.name}: ${test.message}${test.statusCode ? `，状态码=${test.statusCode}` : ''}`)
  }

  throw new Error(`找不到可用于烟测的 OpenAI 账户；已测试 ${candidates.length} 个启用账户均失败：${failures.join('；')}`)
}

async function testAccount(account: AccountSummary): Promise<AccountTestResult> {
  assert(account.ownerSystemAccountId, `账户 ${account.name} 缺少 ownerSystemAccountId`)
  const ownerScope = `systemAccountId=${encodeURIComponent(account.ownerSystemAccountId)}`
  const task = await postEnvelope<AccountTestTask>(apiPath(`/accounts/${account.id}/test?${ownerScope}`), {
    model,
    prompt
  }, accountTestTimeoutMs)
  return waitForAccountTestTask(task, apiPath(`/accounts/test-tasks/${encodeURIComponent(task.id)}?${ownerScope}`))
}

async function waitForAccountTestTask(initialTask: AccountTestTask, taskPath: string): Promise<AccountTestResult> {
  const startedAt = Date.now()
  let task = initialTask
  while (Date.now() - startedAt < accountTestTimeoutMs) {
    if (task.status === 'success' || task.status === 'failed') {
      assert(task.result, `账户测试任务已结束但缺少结果：${task.message ?? task.status}`)
      return task.result
    }
    if (task.status === 'canceled') {
      throw new Error(`账户测试任务已取消：${task.message ?? '已停止测试'}`)
    }
    await sleep(1000)
    task = await getEnvelope<AccountTestTask>(taskPath)
  }
  throw new Error(`账户测试任务等待超时：${task.id}，当前状态 ${task.status}`)
}

function assertAccountCanBeTested(account: AccountSummary, prefix: string): void {
  assert(account.providerCode === 'gpt', `${prefix}，供应商不是 gpt：${account.providerCode}`)
  assert(account.status === 'active', `${prefix}，状态不是正常：${account.status}`)
  assert(account.schedulable !== false, `${prefix}，账号已设为不可调度`)
  assert(!isCooling(account), `${prefix}，账号冷却中至 ${account.cooldownUntil}`)
  assert(Boolean(account.ownerSystemAccountId), `${prefix}，缺少所属系统账户`)
  assert(Boolean(account.boundGroupId), `${prefix}，缺少当前绑定分组`)
}

function isOpenAIAccountCandidate(account: AccountSummary): boolean {
  return account.providerCode === 'gpt'
    && account.status === 'active'
    && account.schedulable !== false
    && Boolean(account.ownerSystemAccountId)
    && Boolean(account.boundGroupId)
    && !isCooling(account)
}

function isCooling(account: AccountSummary): boolean {
  return typeof account.cooldownUntil === 'string'
    && account.cooldownUntil.length > 0
    && new Date(account.cooldownUntil).getTime() > Date.now()
}

function createOAuthResponsesPayload(stream: boolean): Record<string, unknown> {
  return {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: 'You are ChatGPT, a helpful assistant.',
    store: false,
    stream
  }
}

function createApiKeyResponsesPayload(stream: boolean): Record<string, unknown> {
  return {
    model,
    input: prompt,
    max_output_tokens: 16,
    stream
  }
}

async function getEnvelope<T>(path: string): Promise<T> {
  return (await requestJson<ApiEnvelope<T>>(path)).data
}

async function postEnvelope<T>(path: string, body: unknown, timeoutMs = defaultRequestTimeoutMs): Promise<T> {
  return (await requestJson<ApiEnvelope<T>>(path, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body)
  }, timeoutMs)).data
}

async function requestNoContent(path: string, init?: RequestInit, timeoutMs = defaultRequestTimeoutMs): Promise<void> {
  const response = await fetchWithTimeout(path, init, timeoutMs)
  if (!response.ok) {
    const text = await response.text()
    throw new Error(`${path} HTTP ${response.status}: ${text.slice(0, 500)}`)
  }
}

async function requestJson<T>(path: string, init?: RequestInit, timeoutMs = defaultRequestTimeoutMs): Promise<T> {
  const response = await fetchWithTimeout(path, init, timeoutMs)
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text.slice(0, 500)}`)
  }
  try {
    return JSON.parse(text) as T
  } catch (error) {
    throw new Error(`${path} 返回非 JSON：${text.slice(0, 500)}`)
  }
}

async function requestText(path: string, init?: RequestInit, timeoutMs = defaultRequestTimeoutMs): Promise<string> {
  const response = await fetchWithTimeout(path, init, timeoutMs)
  const text = Buffer.from(await response.arrayBuffer()).toString('utf8')
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text.slice(0, 500)}`)
  }
  return text
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, '')
}

function apiPath(path: string): string {
  return `${systemApiPrefix}${path.startsWith('/') ? path : `/${path}`}`
}

async function fetchWithTimeout(path: string, init?: RequestInit, timeoutMs = defaultRequestTimeoutMs): Promise<Response> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(new Error(`${path} 请求超时 ${Math.ceil(timeoutMs / 1000)}s`)), timeoutMs)
  try {
    const response = await fetch(`${backendUrl}${path}`, withSmokeHeaders({
      ...init,
      signal: controller.signal
    }))
    updateSessionCookie(response)
    return response
  } finally {
    clearTimeout(timeout)
  }
}

function withSmokeHeaders(init?: RequestInit): RequestInit | undefined {
  if (!sessionCookie) {
    return init
  }
  return {
    ...init,
    headers: {
      cookie: sessionCookie,
      ...init?.headers
    }
  }
}

function updateSessionCookie(response: Response): void {
  const setCookie = response.headers.get('set-cookie')
  if (!setCookie) {
    return
  }
  const cookie = setCookie.split(';')[0]
  if (cookie) {
    sessionCookie = cookie
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function assertExactSettingKeys(settings: object): void {
  const expectedKeys = [...systemSettingKeys].sort()
  const actualKeys = Object.keys(settings).sort()
  assert(
    actualKeys.length === expectedKeys.length && actualKeys.every((key, index) => key === expectedKeys[index]),
    `系统设置字段不匹配：expected=${expectedKeys.join(',')} actual=${actualKeys.join(',')}`
  )
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      chunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
      const body = parseJson(Buffer.concat(chunks).toString('utf8'))
      if (url.pathname === '/v1/models') {
        sendMockModels(res)
        return
      }
      if (url.pathname === '/v1/responses') {
        if (body.stream === true) {
          sendMockResponseStream(res)
        } else {
          sendMockResponseJson(res)
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'mock upstream path not found' } }))
    })
  })
}

function sendMockModels(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    object: 'list',
    data: [
      { id: model, object: 'model', created: 0, owned_by: 'mock' }
    ]
  }))
}

function sendMockResponseJson(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify({
    id: 'resp_smoke_mock',
    object: 'response',
    status: 'completed',
    model,
    output: [
      {
        type: 'message',
        role: 'assistant',
        content: [{ type: 'output_text', text: 'OK' }]
      }
    ],
    usage: {
      input_tokens: 3,
      output_tokens: 2,
      input_tokens_details: {
        cached_tokens: 0
      }
    }
  }))
}

function sendMockResponseStream(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: 'response.output_text.delta', delta: 'OK' })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({ type: 'response.completed', response: { status: 'completed', usage: { input_tokens: 3, output_tokens: 2 } } })}\n\n`)
  res.end()
}

function parseJson(value: string): Record<string, unknown> {
  if (!value.trim()) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'mock upstream 未监听端口')
  return address.port
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}

main().catch((error) => {
  console.error('\njuhe-ai 烟测失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
