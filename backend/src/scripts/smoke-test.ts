import { runtimeConfig } from '../config/runtime.js'

const backendUrl = trimTrailingSlash(runtimeConfig.smokeTest.backendUrl)
const accountName = runtimeConfig.smokeTest.accountName
const model = runtimeConfig.smokeTest.model
const prompt = runtimeConfig.smokeTest.prompt

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
  requestId: string
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
  const gatewayKey = apiKeys.find((apiKey) => apiKey.status === 'active' && apiKey.key)?.key
  assert(gatewayKey, '找不到可见且启用的本地网关 API Key')
  summary.push('gateway key visible')

  const models = await requestJson<Record<string, unknown>>('/v1/models', {
    headers: { authorization: `Bearer ${gatewayKey}` }
  })
  assert(models.object === 'list', '/v1/models 未返回 list')
  assert(Array.isArray(models.data), '/v1/models 未返回 data 数组')
  summary.push(`/v1/models ok: ${(models.data as unknown[]).length} models`)

  const responsePayload = await requestJson<ResponsePayload>('/v1/responses', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${gatewayKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ model, input: prompt, max_output_tokens: 16, stream: false })
  })
  assert(responsePayload.status === 'completed', `非流式 responses 状态异常：${responsePayload.status ?? 'unknown'}`)
  assert(typeof responsePayload.usage?.input_tokens === 'number', '非流式 responses 未返回 input_tokens')
  assert(typeof responsePayload.usage?.output_tokens === 'number', '非流式 responses 未返回 output_tokens')
  summary.push(`responses ok: ${responsePayload.usage.input_tokens}+${responsePayload.usage.output_tokens} tokens`)

  const streamText = await requestText('/v1/responses', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${gatewayKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({ model, input: prompt, max_output_tokens: 16, stream: true })
  })
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

  console.log('\njuhe-ai smoke test passed')
  for (const item of summary) {
    console.log(`- ${item}`)
  }
}

async function checkHealth(): Promise<void> {
  const health = await requestJson<Record<string, unknown>>('/api/health')
  assert(health.status === 'ok', `健康检查失败：${JSON.stringify(health)}`)
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
    const test = await testAccount(account)
    if (test.success) {
      return { account, test }
    }
    failures.push(`${account.name}: ${test.message}${test.statusCode ? ` status=${test.statusCode}` : ''}`)
  }

  throw new Error(`找不到可用于烟测的 OpenAI 账户；已测试 ${candidates.length} 个启用账户均失败：${failures.join('；')}`)
}

async function testAccount(account: AccountSummary): Promise<AccountTestResult> {
  return postEnvelope<AccountTestResult>(`/api/accounts/${account.id}/test`, {})
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

async function postEnvelope<T>(path: string, body: unknown): Promise<T> {
  return (await requestJson<ApiEnvelope<T>>(path, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })).data
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${backendUrl}${path}`, init)
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

async function requestText(path: string, init?: RequestInit): Promise<string> {
  const response = await fetch(`${backendUrl}${path}`, init)
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text.slice(0, 500)}`)
  }
  return text
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, '')
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
