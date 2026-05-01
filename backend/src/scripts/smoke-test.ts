const backendUrl = trimTrailingSlash(process.env.BACKEND_URL ?? 'http://127.0.0.1:3000')
const accountName = process.env.SMOKE_ACCOUNT_NAME ?? 'dli.li-300-15'
const model = process.env.SMOKE_MODEL ?? 'gpt-5.4-mini'
const prompt = process.env.SMOKE_PROMPT ?? '只输出 OK'

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

interface UsageRecordSummary {
  requestId: string
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
  streamCircuitBreakerEnabled?: boolean
  streamIdleTimeoutSeconds?: number
  streamFailureAction?: string
  streamAccountCooldownMinutes?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  overloadCooldownEnabled?: boolean
  overloadCooldownMinutes?: number
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
  assert(typeof settings.streamCircuitBreakerEnabled === 'boolean', '系统设置缺少 streamCircuitBreakerEnabled')
  assert(typeof settings.streamIdleTimeoutSeconds === 'number', '系统设置缺少 streamIdleTimeoutSeconds')
  assert(['cooldown', 'disable', 'none'].includes(String(settings.streamFailureAction)), `系统设置 streamFailureAction 异常：${settings.streamFailureAction ?? 'unknown'}`)
  assert(typeof settings.overloadCooldownEnabled === 'boolean', '系统设置缺少 overloadCooldownEnabled')
  summary.push('settings ok')

  const accounts = await getEnvelope<AccountSummary[]>('/api/accounts')
  assert(accounts.length > 0, '账户列表为空')
  const targetAccount = accounts.find((account) => account.name === accountName)
  assert(targetAccount, `找不到目标账户：${accountName}`)
  assert(targetAccount.providerCode === 'openai', `目标账户供应商不是 openai：${targetAccount.providerCode}`)
  assert(targetAccount.status === 'active', `目标账户未启用：${targetAccount.status}`)
  summary.push(`account ok: ${targetAccount.name}`)

  const accountTest = await postEnvelope<AccountTestResult>(`/api/accounts/${targetAccount.id}/test`, {})
  assert(accountTest.success, `账户测试失败：${accountTest.message} status=${accountTest.statusCode ?? 'unknown'}`)
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
  assert(modelUsageRecords.some((record) => record.stream && typeof record.inputTokens === 'number' && typeof record.outputTokens === 'number' && typeof record.costUsd === 'number'), '找不到流式 token/cost 使用记录')
  assert(modelUsageRecords.some((record) => !record.stream && typeof record.inputTokens === 'number' && typeof record.outputTokens === 'number' && typeof record.costUsd === 'number'), '找不到非流式 token/cost 使用记录')
  summary.push('usage records ok')

  console.log('\nsub2api-lite smoke test passed')
  for (const item of summary) {
    console.log(`- ${item}`)
  }
}

async function checkHealth(): Promise<void> {
  const health = await requestJson<Record<string, unknown>>('/api/health')
  assert(health.status === 'ok', `健康检查失败：${JSON.stringify(health)}`)
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
  console.error('\nsub2api-lite smoke test failed')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
