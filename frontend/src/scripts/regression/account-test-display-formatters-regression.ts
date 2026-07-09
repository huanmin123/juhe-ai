import type { AccountDraftTestAccountPayload } from '@/api/client'
import type { AccountSummary, AccountTestResult, AccountTestTask } from '@/types/domain'
import { draftApiKeyTestRuntimeDetailsForPayload } from '../../views/accounts/accountDraftApiKeyTestRuntime'
import {
  accountTestBatchCounts,
  accountTestBatchItemJson,
  accountTestBatchItemMessage,
  accountTestBatchItemModelText,
  accountTestBatchItemStatusColor,
  accountTestBatchItemStatusText,
  accountTestBatchOutputLines,
  accountTestBatchResultSnapshot,
  accountTestBatchStatusColor,
  accountTestBatchStatusText,
  accountTestSingleOutputLines
} from '../../views/accounts/accountTestDisplayFormatters'
import type { AccountBatchTestItem } from '../../views/accounts/accountTestFlow'

const apiKeyAccount = accountFixture({
  id: 'account_test_display_api_key',
  name: 'API Key 测试账户',
  type: 'api_key',
  clientCompatibility: 'openai_standard'
})
const oauthAccount = accountFixture({
  id: 'account_test_display_oauth',
  name: 'OAuth 测试账户',
  type: 'oauth',
  clientCompatibility: 'codex_responses'
})
const anthropicAccount = accountFixture({
  id: 'account_test_display_anthropic',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  name: 'Anthropic 测试账户',
  type: 'api_key',
  clientCompatibility: 'openai_standard'
})

const successResult = resultFixture(apiKeyAccount, {
  success: true,
  statusCode: 200,
  message: '测试成功',
  model: 'gpt-5.1',
  outputText: 'pong',
  traceId: 'trace_test_display',
  durationMs: 1234,
  firstTokenMs: 320,
  testEndpointMode: 'chat_sse',
  apiKeyPool: {
    total: 2,
    tested: 2,
    successCount: 1,
    failedCount: 1,
    requiredSuccessCount: 1,
    results: [
      {
        keyIndex: 0,
        keyPrefix: 'sk-a',
        keySuffix: 'good',
        success: true,
        statusCode: 200,
        message: 'OpenAI Responses 测试通过',
        durationMs: 900
      },
      {
        keyIndex: 1,
        keyPrefix: 'sk-b',
        keySuffix: 'fail',
        success: false,
        statusCode: 401,
        errorCode: 'invalid_api_key',
        message: 'invalid api key',
        durationMs: 120
      }
    ]
  }
})
const successLines = accountTestSingleOutputLines({
  account: apiKeyAccount,
  testEndpointMode: 'account_default',
  selectedEndpointModeText: 'Chat Completions (Streaming)',
  model: 'gpt-5.1',
  providerLabel: () => 'OpenAI',
  result: successResult,
  running: false
})
assertLineIncludes(successLines, '开始测试账号：API Key 测试账户', '单账号输出应展示账户名')
assertLineIncludes(successLines, '供应商：OpenAI', '单账号输出应展示供应商')
assertLineIncludes(successLines, '测试请求形态：Chat Completions (Streaming)', 'API Key 默认请求形态应展示当前 endpoint mode')
assertLineIncludes(successLines, 'traceId：trace_test_display', '成功输出应展示 traceId')
assertLineIncludes(successLines, '实际请求形态：Chat Completions (Streaming)', '成功输出应展示实际请求形态')
assertLineIncludes(successLines, 'API Key 池结果：可用 1/2，已测试 2 个', 'Key 池输出应展示汇总')
assertLineIncludes(successLines, 'API Key sk-a...good 测试结果：通过，HTTP 200，耗时 0.90s', 'Key 池输出应展示成功 Key 的前后缀和结果')
assertLineIncludes(successLines, 'API Key sk-b...fail 测试结果：失败，HTTP 401，耗时 0.12s，错误码 invalid_api_key，invalid api key', 'Key 池输出应展示失败 Key 的前后缀和结果')
assertLineIncludes(successLines, 'pong', '成功输出应展示返回内容')
assertLineIncludes(successLines, '✓ 测试完成！  总耗时：1.2s，首 token：0.32s', '成功输出应展示总耗时和首 token')
const mappedResult = resultFixture(apiKeyAccount, {
  success: false,
  statusCode: 400,
  message: '上游模型不存在',
  model: 'gpt-5.5',
  upstreamModel: 'gpt-5.6-terra',
  modelMappingApplied: true,
  sourceEndpointFamily: 'responses',
  upstreamEndpointFamily: 'responses',
  responseText: 'model not found',
  durationMs: 280,
  testEndpointMode: 'responses_sse'
})
const mappedLines = accountTestSingleOutputLines({
  account: apiKeyAccount,
  testEndpointMode: 'responses_sse',
  selectedEndpointModeText: 'Responses API (Streaming)',
  model: 'gpt-5.5',
  providerLabel: () => 'OpenAI',
  result: mappedResult,
  running: false
})
assertLineIncludes(mappedLines, '请求模型：gpt-5.5', '命中模型映射时应先展示用户请求模型')
assertLineIncludes(mappedLines, '模型映射：Responses / gpt-5.5 -> Responses / gpt-5.6-terra', '命中模型映射时应展示协议和模型改写关系')
assertLineIncludes(mappedLines, '实际上游模型：gpt-5.6-terra', '命中模型映射时应展示实际上游模型')
const draftTestPayload = draftApiKeyPayload(['sk-a-draft-good', 'sk-b-draft-fail'])
const draftRuntimeDetails = draftApiKeyTestRuntimeDetailsForPayload({ account: draftTestPayload, result: successResult }, draftTestPayload)
assertEqual(draftRuntimeDetails?.length, 2, '草稿 Key 池测试结果应映射到每个输入 Key')
assertEqual(draftRuntimeDetails?.[0]?.status, 'active', '草稿测试成功 Key 应展示可用状态')
assertEqual(draftRuntimeDetails?.[1]?.status, 'temporary_unavailable', '草稿测试失败 Key 应展示临时避让状态')
assertEqual(draftRuntimeDetails?.[1]?.lastErrorMessage, 'invalid api key', '草稿测试失败 Key 应保留 tooltip 错误消息')
const changedDraftRuntimeDetails = draftApiKeyTestRuntimeDetailsForPayload(
  { account: draftTestPayload, result: successResult },
  draftApiKeyPayload(['sk-a-draft-good', 'sk-c-draft-new'])
)
assertEqual(changedDraftRuntimeDetails, undefined, '草稿 Key 改动后不应继续展示旧测试状态')
const singleDraftTestPayload = draftApiKeyPayload(['sk-single-draft'])
const singleDraftRuntimeDetails = draftApiKeyTestRuntimeDetailsForPayload(
  { account: singleDraftTestPayload, result: resultFixture(apiKeyAccount, { success: false, message: '单 Key 测试失败' }) },
  singleDraftTestPayload
)
assertEqual(singleDraftRuntimeDetails, undefined, '单个 API Key 草稿测试不应展示 Key 池逐项状态')

const runningTask = taskFixture(oauthAccount, {
  status: 'running',
  startedAt: new Date(Date.now() - 1500).toISOString(),
  message: 'worker 已接收'
})
const runningLines = accountTestSingleOutputLines({
  account: oauthAccount,
  activeTask: runningTask,
  testEndpointMode: 'responses_sse',
  selectedEndpointModeText: 'Responses API (Streaming)',
  model: 'gpt-5.1',
  providerLabel: () => 'OpenAI',
  running: true
})
assertLineIncludes(runningLines, '测试请求形态：Responses API (Streaming)', 'OAuth 账户应展示固定 endpoint mode')
assertLineIncludes(runningLines, '后台任务：task_account_test_display_oauth（测试中）', '运行输出应展示后台任务状态')
assertLineIncludes(runningLines, '当前窗口估计：第 1/3 次', '运行输出应展示当前等待窗口')
assertLineIncludes(runningLines, 'OAuth Token 刷新也包含在当前等待窗口内', 'OAuth 运行输出应展示 token 刷新提示')

const anthropicRunningLines = accountTestSingleOutputLines({
  account: anthropicAccount,
  testEndpointMode: 'account_default',
  selectedEndpointModeText: 'Messages API (Streaming)',
  model: 'claude-opus-4-8',
  providerLabel: () => 'Anthropic',
  running: true
})
assertLineIncludes(anthropicRunningLines, '测试请求形态：Messages API (Streaming)', 'Anthropic 运行输出应展示 Messages endpoint mode')
assertLineExcludes(anthropicRunningLines, '测试请求形态：跟随账号能力（OpenAI-compatible）', 'Anthropic 运行输出不得回落到 OpenAI-compatible 文案')

const anthropicSuccessLines = accountTestSingleOutputLines({
  account: anthropicAccount,
  testEndpointMode: 'account_default',
  selectedEndpointModeText: 'Messages API (Streaming)',
  model: 'claude-opus-4-8',
  providerLabel: () => 'Anthropic',
  result: resultFixture(anthropicAccount, {
    success: true,
    statusCode: 200,
    message: '测试成功',
    model: 'claude-opus-4-8',
    outputText: 'pong',
    testEndpointMode: 'messages_sse'
  }),
  running: false
})
assertLineIncludes(anthropicSuccessLines, '实际请求形态：Messages API (Streaming)', 'Anthropic 成功输出应展示实际请求形态')
assertLineExcludes(anthropicSuccessLines, '实际请求形态：OpenAI-compatible 请求', 'Anthropic 成功输出不得展示 OpenAI-compatible 实际请求形态')

const failedAccount = accountFixture({
  id: 'account_test_display_failed',
  name: '失败账户'
})
const failedResult = resultFixture(failedAccount, {
  success: false,
  statusCode: 500,
  message: '上游 500',
  responseText: 'upstream failed',
  durationMs: 600,
  testEndpointMode: 'responses_sse'
})
const batchItems: AccountBatchTestItem[] = [
  {
    account: apiKeyAccount,
    status: 'success',
    result: successResult,
    startedAt: 1000,
    finishedAt: 2234
  },
  {
    account: failedAccount,
    status: 'failed',
    result: failedResult,
    message: '上游 500',
    startedAt: 2000,
    finishedAt: 2600
  },
  {
    account: oauthAccount,
    status: 'stopped',
    message: '已停止测试'
  }
]
const batchCounts = accountTestBatchCounts(batchItems)
assertEqual(batchCounts.total, 3, '批量计数应包含全部账户')
assertEqual(batchCounts.completed, 3, '批量计数应把 success/failed/stopped 视为完成')
assertEqual(batchCounts.success, 1, '批量计数应包含成功数')
assertEqual(batchCounts.failed, 1, '批量计数应包含失败数')
assertEqual(batchCounts.stopped, 1, '批量计数应包含停止数')
assertEqual(accountTestBatchStatusColor(batchCounts, false), 'red', '批量状态颜色应优先展示失败')
assertEqual(accountTestBatchStatusText(batchCounts, false), '成功 1，失败 1', '批量状态文案应展示成功和失败数')
const batchLines = accountTestBatchOutputLines({
  batchItems,
  counts: batchCounts,
  model: 'gpt-5.1',
  running: false
})
assertLineIncludes(batchLines, '批量测试账号：3 个', '批量输出应展示账户数量')
assertLineIncludes(batchLines, '测试完成：成功 1 个，失败 1 个，已停止 1 个', '批量输出应展示完成摘要')
assertLineIncludes(batchLines, '失败摘要：', '批量输出应展示失败摘要标题')
assertLineIncludes(batchLines, '失败账户: 上游 500', '批量输出应展示失败账户消息')

assertEqual(accountTestBatchItemStatusColor(batchItems[1]!), 'red', '失败行状态颜色应为 red')
assertEqual(accountTestBatchItemStatusText(batchItems[1]!), '失败', '失败行状态文案应为失败')
assertEqual(accountTestBatchItemModelText(batchItems[0]!, 'gpt-5.1'), 'gpt-5.1', '批量行应优先展示结果模型')
assertEqual(accountTestBatchItemMessage(batchItems[2]!), '已停止测试', '批量行应优先展示显式消息')

const batchSnapshot = accountTestBatchResultSnapshot({
  batchItems,
  model: 'gpt-5.1'
})
assertEqual(batchSnapshot.summary.completed, 3, '批量快照应展示完成数')
assertEqual(batchSnapshot.summary.failed, 1, '批量快照应展示失败数')
assertEqual(batchSnapshot.results[1]?.accountId, failedAccount.id, '批量快照应保留账户 ID')
assertEqual(batchSnapshot.results[1]?.message, '上游 500', '批量快照应保留行消息')
const itemSnapshot = JSON.parse(accountTestBatchItemJson(batchItems[0]!)) as { accountId?: string; result?: { traceId?: string } }
assertEqual(itemSnapshot.accountId, apiKeyAccount.id, '单行复制 JSON 应保留账户 ID')
assertEqual(itemSnapshot.result?.traceId, 'trace_test_display', '单行复制 JSON 应保留测试结果')

console.log('账户测试展示 formatter 回归通过：单账号输出、OAuth 运行窗口、批量状态、批量摘要和 JSON 快照均符合预期')

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account_test_display',
    providerCode: 'openai',
    name: '测试账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function resultFixture(account: AccountSummary, overrides: Partial<AccountTestResult> = {}): AccountTestResult {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    type: account.type,
    success: true,
    message: '测试成功',
    model: 'gpt-5.1',
    durationMs: 0,
    ...overrides
  }
}

function taskFixture(account: AccountSummary, overrides: Partial<AccountTestTask> = {}): AccountTestTask {
  const now = new Date().toISOString()
  return {
    id: `task_${account.id}`,
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    type: account.type,
    status: 'queued',
    createdAt: now,
    queuedAt: now,
    updatedAt: now,
    ...overrides
  }
}

function emptyUsage() {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function assertLineIncludes(lines: Array<{ text: string }>, expected: string, message: string): void {
  if (!lines.some((line) => line.text.includes(expected))) {
    throw new Error(`${message}，未找到 ${expected}；实际输出：${lines.map((line) => line.text).join(' | ')}`)
  }
}

function draftApiKeyPayload(apiKeys: string[]): AccountDraftTestAccountPayload {
  return {
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    name: '草稿 Key 池账户',
    type: 'api_key',
    credentials: {
      api_key: apiKeys[0],
      api_keys: apiKeys,
      api_key_strategy: 'round_robin',
      base_url: 'https://api.test/v1'
    },
    concurrencyLimit: 1,
    priority: 0,
    supportedModels: ['gpt-5.1'],
    modelMappings: [],
    groupId: 'group_test_display'
  }
}

function assertLineExcludes(lines: Array<{ text: string }>, unexpected: string, message: string): void {
  if (lines.some((line) => line.text.includes(unexpected))) {
    throw new Error(`${message}，不应出现 ${unexpected}；实际输出：${lines.map((line) => line.text).join(' | ')}`)
  }
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，实际 ${String(actual)}`)
  }
}
