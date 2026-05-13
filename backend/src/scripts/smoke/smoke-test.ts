import { runtimeConfig } from '../../config/runtime.js'
import { systemSettingKeys } from '../../storage/settings.repository.js'

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

interface GroupSummary {
  id: string
  name: string
  accountIds?: string[]
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

interface SystemSettings {
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
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
  accountQualityRefreshIntervalSeconds?: number
  accountQualityWindowMinutes?: number
  cooldownAccountRetestEnabled?: boolean
  cooldownAccountRetestIntervalSeconds?: number
  cooldownAccountRetestBatchSize?: number
  cooldownAccountRetestModel?: string
  oauthAccessTokenRefreshIntervalSeconds?: number
  oauthAccessTokenRefreshLeadSeconds?: number
  oauthAccessTokenRefreshBatchSize?: number
  oauthAccessTokenRefreshRetryBackoffSeconds?: number
  usageRecordRetentionDays?: number
  usageStatsDailyRetentionDays?: number
  usageStatsHourlyRetentionDays?: number
  systemMetricsRetentionDays?: number
  systemMetricsHourlyRetentionDays?: number
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
  originalGroupId?: string
  ownerSystemAccountId?: string
  temporaryGroupId?: string
  temporaryApiKeyId?: string
}

async function main(): Promise<void> {
  const summary: string[] = []
  const resourceState: SmokeResourceState = {}

  try {
    await checkHealth()
    summary.push('健康检查通过')

    await loginAsAdmin()
    summary.push('登录通过')

    const settings = await getEnvelope<SystemSettings>('/api/settings')
    assert(typeof settings.defaultTemporaryUnschedulableMinutes === 'number', '系统设置缺少 defaultTemporaryUnschedulableMinutes')
    assert(typeof settings.temporaryUnschedulableRetryIntervalSeconds === 'number', '系统设置缺少 temporaryUnschedulableRetryIntervalSeconds')
    assert(typeof settings.temporaryUnschedulableRetryAttempts === 'number', '系统设置缺少 temporaryUnschedulableRetryAttempts')
    assert(typeof settings.streamCircuitBreakerEnabled === 'boolean', '系统设置缺少 streamCircuitBreakerEnabled')
    assert(typeof settings.streamRequestTimeoutSeconds === 'number', '系统设置缺少 streamRequestTimeoutSeconds')
    assert(typeof settings.streamIdleTimeoutSeconds === 'number', '系统设置缺少 streamIdleTimeoutSeconds')
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
    assert(typeof settings.accountQualityRefreshIntervalSeconds === 'number', '系统设置缺少 accountQualityRefreshIntervalSeconds')
    assert(typeof settings.accountQualityWindowMinutes === 'number', '系统设置缺少 accountQualityWindowMinutes')
    assert(typeof settings.cooldownAccountRetestEnabled === 'boolean', '系统设置缺少 cooldownAccountRetestEnabled')
    assert(typeof settings.cooldownAccountRetestIntervalSeconds === 'number', '系统设置缺少 cooldownAccountRetestIntervalSeconds')
    assert(typeof settings.cooldownAccountRetestBatchSize === 'number', '系统设置缺少 cooldownAccountRetestBatchSize')
    assert(typeof settings.cooldownAccountRetestModel === 'string', '系统设置缺少 cooldownAccountRetestModel')
    assert(typeof settings.oauthAccessTokenRefreshIntervalSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshIntervalSeconds')
    assert(typeof settings.oauthAccessTokenRefreshLeadSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshLeadSeconds')
    assert(typeof settings.oauthAccessTokenRefreshBatchSize === 'number', '系统设置缺少 oauthAccessTokenRefreshBatchSize')
    assert(typeof settings.oauthAccessTokenRefreshRetryBackoffSeconds === 'number', '系统设置缺少 oauthAccessTokenRefreshRetryBackoffSeconds')
    assert(typeof settings.usageRecordRetentionDays === 'number', '系统设置缺少 usageRecordRetentionDays')
    assert(typeof settings.usageStatsDailyRetentionDays === 'number', '系统设置缺少 usageStatsDailyRetentionDays')
    assert(typeof settings.usageStatsHourlyRetentionDays === 'number', '系统设置缺少 usageStatsHourlyRetentionDays')
    assert(typeof settings.systemMetricsRetentionDays === 'number', '系统设置缺少 systemMetricsRetentionDays')
    assert(typeof settings.systemMetricsHourlyRetentionDays === 'number', '系统设置缺少 systemMetricsHourlyRetentionDays')
    assert(typeof settings.dataRetentionCleanupBatchSize === 'number', '系统设置缺少 dataRetentionCleanupBatchSize')
    assert(typeof settings.dataRetentionCleanupMaxBatchesPerRun === 'number', '系统设置缺少 dataRetentionCleanupMaxBatchesPerRun')
    assertExactSettingKeys(settings)
    summary.push('系统设置检查通过')

    const accounts = await getEnvelope<AccountSummary[]>('/api/accounts')
    assert(accounts.length > 0, '账户列表为空')
    const selectedAccount = accountName
      ? await testNamedAccount(accounts, accountName)
      : await selectFirstUsableOpenAIAccount(accounts)
    const { account: targetAccount, test: accountTest } = selectedAccount
    assert(targetAccount.ownerSystemAccountId, `账户 ${targetAccount.name} 缺少 ownerSystemAccountId，无法按正规管理流程创建临时分组`)
    assert(targetAccount.boundGroupId, `账户 ${targetAccount.name} 缺少当前绑定分组，无法在烟测后恢复`)
    resourceState.accountId = targetAccount.id
    resourceState.ownerSystemAccountId = targetAccount.ownerSystemAccountId
    resourceState.originalGroupId = targetAccount.boundGroupId
    summary.push(`账户检查通过：${targetAccount.name}`)
    summary.push(`账户测试通过：${accountTest.message}${accountTest.proxyUrl ? '，代理已配置' : ''}`)

    const gatewayKey = await createTemporaryGatewayKeyForAccount(targetAccount, resourceState)
    const models = await requestJson<Record<string, unknown>>('/v1/models', {
      headers: { authorization: `Bearer ${gatewayKey.key}` }
    }, gatewayKeyTestTimeoutMs)
    assert(models.object === 'list', '/v1/models 未返回 list')
    assert(Array.isArray(models.data), '/v1/models 未返回 data 数组')
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

    const auditRuntime = await getEnvelope<Record<string, unknown>>('/api/audit-logs/runtime')
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
  const health = await requestJson<Record<string, unknown>>('/api/health')
  assert(health.status === 'ok', `健康检查失败：${JSON.stringify(health)}`)
}

async function loginAsAdmin(): Promise<void> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>('/api/auth/captcha')
  const captchaCode = parseCaptchaCode(captcha.image)
  assert(captchaCode, '无法解析登录验证码')
  const loginResult = await requestJson<ApiEnvelope<{ role?: string; username?: string }>>('/api/auth/login', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: runtimeConfig.smokeTest.adminUsername,
      password: runtimeConfig.smokeTest.adminPassword,
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  assert(loginResult.data.role === 'admin', `烟测登录账号不是管理员：${loginResult.data.username ?? 'unknown'}`)
}

function parseCaptchaCode(image: string): string {
  const base64 = image.replace(/^data:image\/svg\+xml;base64,/, '')
  const svg = Buffer.from(base64, 'base64').toString('utf8')
  return [...svg.matchAll(/<text[^>]*>([^<]+)<\/text>/g)].map((match) => match[1]).join('')
}

async function createTemporaryGatewayKeyForAccount(
  account: AccountSummary,
  resourceState: SmokeResourceState
): Promise<ApiKeySummary & { key: string }> {
  const ownerScope = ownerScopeQuery(resourceState)
  const stamp = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14)
  const group = await postEnvelope<GroupSummary>(`/api/groups${ownerScope}`, {
    name: `${temporaryResourcePrefix}-OpenAI-${stamp}`,
    providerCode: account.providerCode,
    description: '真实网关链路临时烟测分组',
    enabled: true
  })
  resourceState.temporaryGroupId = group.id

  await postEnvelope<AccountSummary>(`/api/accounts/${account.id}/group${ownerScope}`, { groupId: group.id })
  const apiKey = await postEnvelope<ApiKeySummary>(`/api/api-keys${ownerScope}`, {
    name: `${temporaryResourcePrefix}-Key-${stamp}`,
    groupId: group.id,
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
      return Boolean(resourceState.temporaryGroupId && record.groupId === resourceState.temporaryGroupId)
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
    return Boolean(resourceState.temporaryGroupId && record.groupId === resourceState.temporaryGroupId)
  })
}

async function fetchSmokeUsageRecords(resourceState: SmokeResourceState): Promise<UsageRecordSummary[]> {
  const scopeQuery = ownerScopeQuery(resourceState)
  const separator = scopeQuery.includes('?') ? '&' : '?'
  const result = await getEnvelope<UsageRecordListResult>(`/api/usage-records${scopeQuery}${separator}page=1&pageSize=200`)
  return result.items
}

async function cleanupSmokeResources(resourceState: SmokeResourceState): Promise<void> {
  if (!resourceState.ownerSystemAccountId) return
  const ownerScope = ownerScopeQuery(resourceState)
  if (!ownerScope) return

  if (resourceState.temporaryApiKeyId) {
    await ignoreCleanupError(() => requestNoContent(`/api/api-keys/${resourceState.temporaryApiKeyId}${ownerScope}`, { method: 'DELETE' }))
  }
  if (resourceState.accountId && resourceState.originalGroupId) {
    await ignoreCleanupError(() => postEnvelope<AccountSummary>(`/api/accounts/${resourceState.accountId}/group${ownerScope}`, { groupId: resourceState.originalGroupId }))
  }
  if (resourceState.temporaryGroupId) {
    await ignoreCleanupError(() => requestNoContent(`/api/groups/${resourceState.temporaryGroupId}${ownerScope}`, { method: 'DELETE' }))
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
  return postEnvelope<AccountTestResult>(`/api/accounts/${account.id}/test?systemAccountId=${encodeURIComponent(account.ownerSystemAccountId)}`, {
    model,
    prompt
  }, accountTestTimeoutMs)
}

function assertAccountCanBeTested(account: AccountSummary, prefix: string): void {
  assert(account.providerCode === 'openai', `${prefix}，供应商不是 openai：${account.providerCode}`)
  assert(account.status === 'active', `${prefix}，状态不是正常：${account.status}`)
  assert(account.schedulable !== false, `${prefix}，账号已设为不可调度`)
  assert(!isCooling(account), `${prefix}，账号冷却中至 ${account.cooldownUntil}`)
  assert(Boolean(account.ownerSystemAccountId), `${prefix}，缺少所属系统账户`)
  assert(Boolean(account.boundGroupId), `${prefix}，缺少当前绑定分组`)
}

function isOpenAIAccountCandidate(account: AccountSummary): boolean {
  return account.providerCode === 'openai'
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

main().catch((error) => {
  console.error('\njuhe-ai 烟测失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
