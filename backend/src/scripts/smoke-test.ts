import { runtimeConfig } from '../config/runtime.js'
import { createSession, listSystemAccounts } from '../storage/repositories.js'

const backendUrl = trimTrailingSlash(runtimeConfig.smokeTest.backendUrl)
const accountName = runtimeConfig.smokeTest.accountName
const model = runtimeConfig.smokeTest.model
const prompt = runtimeConfig.smokeTest.prompt
const sessionCookie = createSmokeTestSessionCookie()
const defaultRequestTimeoutMs = 60_000
const accountTestTimeoutMs = 30_000
const gatewayKeyTestTimeoutMs = 30_000
const streamRequestTimeoutMs = 90_000

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
}

interface ApiKeySummary {
  id: string
  name: string
  key?: string
  status: string
}

interface SelectedGatewayKey {
  apiKey: ApiKeySummary & { key: string }
  models: Record<string, unknown>
}

interface AccountTestResult {
  accountId: string
  accountName: string
  success: boolean
  statusCode?: number
  message: string
}

interface TestedAccount {
  account: AccountSummary
  test: AccountTestResult
}

interface UsageRecordSummary {
  traceId: string
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

interface SystemSettings {
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  auditLogEnabled?: boolean
  auditLogSuccessSampleRate?: number
  auditLogFlushIntervalSeconds?: number
  auditLogBatchSize?: number
  auditLogQueueMaxItems?: number
  auditLogQueueMaxBytesMb?: number
  auditLogActiveCaptureMaxBytesMb?: number
  auditLogRetentionDays?: number
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

async function main(): Promise<void> {
  const summary: string[] = []

  await checkHealth()
  summary.push('health ok')

  const settings = await getEnvelope<SystemSettings>('/api/settings')
  assert(!Object.prototype.hasOwnProperty.call(settings, 'defaultErrorPolicyId'), '系统设置不应返回 defaultErrorPolicyId')
  assert(typeof settings.defaultTemporaryUnschedulableMinutes === 'number', '系统设置缺少 defaultTemporaryUnschedulableMinutes')
  assert(typeof settings.temporaryUnschedulableRetryIntervalSeconds === 'number', '系统设置缺少 temporaryUnschedulableRetryIntervalSeconds')
  assert(typeof settings.temporaryUnschedulableRetryAttempts === 'number', '系统设置缺少 temporaryUnschedulableRetryAttempts')
  assert(typeof settings.streamCircuitBreakerEnabled === 'boolean', '系统设置缺少 streamCircuitBreakerEnabled')
  assert(typeof settings.streamRequestTimeoutSeconds === 'number', '系统设置缺少 streamRequestTimeoutSeconds')
  assert(typeof settings.streamIdleTimeoutSeconds === 'number', '系统设置缺少 streamIdleTimeoutSeconds')
  assert(typeof settings.streamFailureThresholdCount === 'number', '系统设置缺少 streamFailureThresholdCount')
  assert(typeof settings.streamFailureThresholdWindowMinutes === 'number', '系统设置缺少 streamFailureThresholdWindowMinutes')
  assert(typeof settings.auditLogEnabled === 'boolean', '系统设置缺少 auditLogEnabled')
  assert(typeof settings.auditLogSuccessSampleRate === 'number', '系统设置缺少 auditLogSuccessSampleRate')
  assert(typeof settings.auditLogFlushIntervalSeconds === 'number', '系统设置缺少 auditLogFlushIntervalSeconds')
  assert(typeof settings.auditLogBatchSize === 'number', '系统设置缺少 auditLogBatchSize')
  assert(typeof settings.auditLogQueueMaxItems === 'number', '系统设置缺少 auditLogQueueMaxItems')
  assert(typeof settings.auditLogQueueMaxBytesMb === 'number', '系统设置缺少 auditLogQueueMaxBytesMb')
  assert(typeof settings.auditLogActiveCaptureMaxBytesMb === 'number', '系统设置缺少 auditLogActiveCaptureMaxBytesMb')
  assert(typeof settings.auditLogRetentionDays === 'number', '系统设置缺少 auditLogRetentionDays')
  summary.push('settings ok')

  const accounts = await getEnvelope<AccountSummary[]>('/api/accounts')
  assert(accounts.length > 0, '账户列表为空')
  const selectedAccount = accountName
    ? await testNamedAccount(accounts, accountName)
    : await selectFirstUsableOpenAIAccount(accounts)
  const { account: targetAccount, test: accountTest } = selectedAccount
  summary.push(`account ok: ${targetAccount.name}`)
  summary.push(`account test ok: ${accountTest.message}`)

  const apiKeys = await getEnvelope<ApiKeySummary[]>('/api/api-keys')
  const { apiKey: gatewayKey, models } = await selectFirstUsableGatewayKey(apiKeys)
  assert(models.object === 'list', '/v1/models 未返回 list')
  assert(Array.isArray(models.data), '/v1/models 未返回 data 数组')
  summary.push(`gateway key ok: ${gatewayKey.name}`)
  summary.push(`/v1/models ok: ${(models.data as unknown[]).length} models`)

  const responsePayload = await requestJson<ResponsePayload>('/v1/responses', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${gatewayKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ model, input: prompt, max_output_tokens: 16, stream: false })
  }, streamRequestTimeoutMs)
  assert(responsePayload.status === 'completed', `非流式 responses 状态异常：${responsePayload.status ?? 'unknown'}`)
  assert(typeof responsePayload.usage?.input_tokens === 'number', '非流式 responses 未返回 input_tokens')
  assert(typeof responsePayload.usage?.output_tokens === 'number', '非流式 responses 未返回 output_tokens')
  summary.push(`responses ok: ${responsePayload.usage.input_tokens}+${responsePayload.usage.output_tokens} tokens`)

  const streamText = await requestText('/v1/responses', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${gatewayKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ model, input: prompt, max_output_tokens: 16, stream: true })
  }, streamRequestTimeoutMs)
  assert(streamText.includes('response.completed'), '流式 responses 未包含 response.completed')
  assert(!streamText.includes('response.failed'), '流式 responses 包含 response.failed')
  summary.push('stream responses ok')

  const usageRecords = await getEnvelope<UsageRecordSummary[]>('/api/usage-records')
  const modelUsageRecords = usageRecords.filter((record) => record.model === model && record.success)
  assert(usageRecords.some((record) => record.endpoint === 'GET /v1/models'), '找不到 /v1/models 接口使用记录')
  assert(modelUsageRecords.some((record) => record.endpoint === 'POST /v1/responses'), '找不到 /v1/responses 接口使用记录')
  assert(modelUsageRecords.some((record) => record.stream && typeof record.inputTokens === 'number' && typeof record.outputTokens === 'number' && typeof record.costUsd === 'number'), '找不到流式 token/cost 使用记录')
  assert(modelUsageRecords.some((record) => !record.stream && typeof record.inputTokens === 'number' && typeof record.outputTokens === 'number' && typeof record.costUsd === 'number'), '找不到非流式 token/cost 使用记录')
  summary.push('usage records ok')

  const auditRuntime = await getEnvelope<Record<string, unknown>>('/api/audit-logs/runtime')
  assert(typeof auditRuntime.queueLength === 'number', '审计运行态缺少 queueLength')
  assert(typeof auditRuntime.activeCaptureCount === 'number', '审计运行态缺少 activeCaptureCount')
  assert(typeof auditRuntime.settings === 'object' && auditRuntime.settings !== null, '审计运行态缺少 settings')
  summary.push('audit runtime ok')

  console.log('\njuhe-ai smoke test passed')
  for (const item of summary) {
    console.log(`- ${item}`)
  }
}

async function checkHealth(): Promise<void> {
  const health = await requestJson<Record<string, unknown>>('/api/health')
  assert(health.status === 'ok', `健康检查失败：${JSON.stringify(health)}`)
}

async function selectFirstUsableGatewayKey(apiKeys: ApiKeySummary[]): Promise<SelectedGatewayKey> {
  const candidates = apiKeys.filter((apiKey): apiKey is ApiKeySummary & { key: string } => apiKey.status === 'active' && Boolean(apiKey.key))
  assert(candidates.length > 0, '找不到可见且启用的本地网关 API Key')

  const failures: string[] = []
  for (const apiKey of candidates) {
    try {
      console.log(`smoke: testing gateway key ${apiKey.name}`)
      const models = await requestJson<Record<string, unknown>>('/v1/models', {
        headers: { authorization: `Bearer ${apiKey.key}` }
      }, gatewayKeyTestTimeoutMs)
      return { apiKey, models }
    } catch (error) {
      failures.push(`${apiKey.name}: ${error instanceof Error ? error.message : String(error)}`)
    }
  }

  throw new Error(`找不到可用于烟测的本地网关 API Key；已测试 ${candidates.length} 个启用 Key 均失败：${failures.join('；')}`)
}

async function testNamedAccount(accounts: AccountSummary[], name: string): Promise<TestedAccount> {
  const account = accounts.find((item) => item.name === name)
  assert(account, `找不到目标账户：${name}`)
  assertAccountCanBeTested(account, `目标账户不可用于烟测：${name}`)

  const test = await testAccount(account)
  assert(test.success, `账户测试失败：${test.message} status=${test.statusCode ?? 'unknown'}`)
  return { account, test }
}

async function selectFirstUsableOpenAIAccount(accounts: AccountSummary[]): Promise<TestedAccount> {
  const candidates = accounts.filter(isOpenAIAccountCandidate)
  assert(candidates.length > 0, '找不到启用且可调度的 OpenAI 账户')

  const failures: string[] = []
  for (const account of candidates) {
    console.log(`smoke: testing account ${account.name}`)
    const test = await testAccount(account)
    if (test.success) {
      return { account, test }
    }
    failures.push(`${account.name}: ${test.message}${test.statusCode ? ` status=${test.statusCode}` : ''}`)
  }

  throw new Error(`找不到可用于烟测的 OpenAI 账户；已测试 ${candidates.length} 个启用账户均失败：${failures.join('；')}`)
}

async function testAccount(account: AccountSummary): Promise<AccountTestResult> {
  return postEnvelope<AccountTestResult>(`/api/accounts/${account.id}/test`, {}, accountTestTimeoutMs)
}

function assertAccountCanBeTested(account: AccountSummary, prefix: string): void {
  assert(account.providerCode === 'openai', `${prefix}，供应商不是 openai：${account.providerCode}`)
  assert(account.status === 'active', `${prefix}，状态不是正常：${account.status}`)
  assert(account.schedulable !== false, `${prefix}，账号已设为不可调度`)
  assert(!isCooling(account), `${prefix}，账号冷却中至 ${account.cooldownUntil}`)
}

function isOpenAIAccountCandidate(account: AccountSummary): boolean {
  return account.providerCode === 'openai'
    && account.status === 'active'
    && account.schedulable !== false
    && !isCooling(account)
}

function isCooling(account: AccountSummary): boolean {
  return typeof account.cooldownUntil === 'string'
    && account.cooldownUntil.length > 0
    && new Date(account.cooldownUntil).getTime() > Date.now()
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
  const text = await response.text()
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
    return await fetch(`${backendUrl}${path}`, withSmokeHeaders(path, {
      ...init,
      signal: controller.signal
    }))
  } finally {
    clearTimeout(timeout)
  }
}

function withSmokeHeaders(path: string, init?: RequestInit): RequestInit | undefined {
  if (!path.startsWith('/api/') || path === '/api/health') {
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

function createSmokeTestSessionCookie(): string {
  const admin = listSystemAccounts().find((account) => account.role === 'admin' && account.status === 'active')
  assert(admin, '找不到可用于烟测的启用管理员系统账户')
  return `juhe_ai_session=${createSession(admin.id, 1).token}`
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\njuhe-ai smoke test failed')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
