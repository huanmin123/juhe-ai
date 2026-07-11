import { api, type AccountDraftTestPayload, type AccountTestPayload } from '@/api/client'
import type { AccountSummary, AccountTestSession, AccountTestTask } from '@/types/domain'
import {
  cancelAccountTestSession,
  cancelAccountTestTask,
  completeAccountTestSession,
  createAccountTestSession,
  fetchAccountTestTask,
  heartbeatAccountTestSession,
  submitAccountTestTask
} from '../../views/accounts/accountTestSessionClient'
import { buildAccountTestPayload } from '../../views/accounts/accountTestFlow'

interface ApiCall {
  method: string
  args: unknown[]
}

const apiCalls: ApiCall[] = []
const now = new Date().toISOString()

stubApiMethod('accounts', 'createTestSession', (...args) => recordCall('accounts.createTestSession', args, sessionFixture('session_management')))
stubApiMethod('accounts', 'heartbeatTestSession', (...args) => recordCall('accounts.heartbeatTestSession', args, sessionFixture('session_management')))
stubApiMethod('accounts', 'completeTestSession', (...args) => recordCall('accounts.completeTestSession', args, sessionFixture('session_management')))
stubApiMethod('accounts', 'cancelTestSession', (...args) => recordCall('accounts.cancelTestSession', args, sessionFixture('session_management')))
stubApiMethod('accounts', 'test', (...args) => recordCall('accounts.test', args, taskFixture('task_management_test')))
stubApiMethod('accounts', 'testDraft', (...args) => recordCall('accounts.testDraft', args, taskFixture('task_management_draft')))
stubApiMethod('accounts', 'testTask', (...args) => recordCall('accounts.testTask', args, taskFixture('task_management_fetch')))
stubApiMethod('accounts', 'cancelTestTask', (...args) => recordCall('accounts.cancelTestTask', args, taskFixture('task_management_cancel')))
stubApiMethod('myAccounts', 'createTestSession', (...args) => recordCall('myAccounts.createTestSession', args, sessionFixture('session_personal')))
stubApiMethod('myAccounts', 'heartbeatTestSession', (...args) => recordCall('myAccounts.heartbeatTestSession', args, sessionFixture('session_personal')))
stubApiMethod('myAccounts', 'completeTestSession', (...args) => recordCall('myAccounts.completeTestSession', args, sessionFixture('session_personal')))
stubApiMethod('myAccounts', 'cancelTestSession', (...args) => recordCall('myAccounts.cancelTestSession', args, sessionFixture('session_personal')))
stubApiMethod('myAccounts', 'test', (...args) => recordCall('myAccounts.test', args, taskFixture('task_personal_test')))
stubApiMethod('myAccounts', 'testDraft', (...args) => recordCall('myAccounts.testDraft', args, taskFixture('task_personal_draft')))
stubApiMethod('myAccounts', 'testTask', (...args) => recordCall('myAccounts.testTask', args, taskFixture('task_personal_fetch')))
stubApiMethod('myAccounts', 'cancelTestTask', (...args) => recordCall('myAccounts.cancelTestTask', args, taskFixture('task_personal_cancel')))

const scopeParams = { systemAccountId: 'sys_fallback' }
const boundAccount = accountFixture({
  accessType: 'authorized',
  bindingSystemAccountId: 'sys_bound',
  ownerSystemAccountId: 'sys_owner',
  systemAccountId: 'sys_account'
})

await createAccountTestSession({ isManagementView: true, scopeParams })
assertLastCall('accounts.createTestSession', [scopeParams], '管理端创建测试 session 应透传当前 scope')

await createAccountTestSession({ isManagementView: false, scopeParams })
assertLastCall('myAccounts.createTestSession', [], '个人端创建测试 session 不应透传管理端 scope')

await heartbeatAccountTestSession({ isManagementView: true, scopeParams, sessionId: 'session_1' })
assertLastCall('accounts.heartbeatTestSession', ['session_1', scopeParams], '管理端 heartbeat 应携带 session 与 scope')

await heartbeatAccountTestSession({ isManagementView: false, scopeParams, sessionId: 'session_2' })
assertLastCall('myAccounts.heartbeatTestSession', ['session_2'], '个人端 heartbeat 不应携带管理端 scope')

await completeAccountTestSession({ isManagementView: true, scopeParams, sessionId: 'session_complete_1' })
assertLastCall(
  'accounts.completeTestSession',
  ['session_complete_1', scopeParams],
  '管理端完成测试 session 应携带 scope'
)

await completeAccountTestSession({ isManagementView: false, scopeParams, sessionId: 'session_complete_2' })
assertLastCall(
  'myAccounts.completeTestSession',
  ['session_complete_2'],
  '个人端完成测试 session 不应携带管理端 scope'
)

await cancelAccountTestSession({ isManagementView: true, scopeParams, sessionId: 'session_3' })
assertLastCall('accounts.cancelTestSession', ['session_3', scopeParams], '管理端取消 session 应携带 scope')

await cancelAccountTestSession({ isManagementView: false, scopeParams, sessionId: 'session_4' })
assertLastCall('myAccounts.cancelTestSession', ['session_4'], '个人端取消 session 不应携带管理端 scope')

const testPayload: AccountTestPayload = { model: 'gpt-5.1', testEndpointMode: 'chat_sse', prompt: 'ping' }

assertDeepEqual(
  buildAccountTestPayload({ model: '  gpt-5.1  ', testEndpointMode: 'account_default' }, accountFixture({
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1'
  })),
  { model: 'gpt-5.1', testEndpointMode: 'chat_sse' },
  'GPT API Key 默认测试形态应从接口能力限制中明确提交 endpoint mode'
)
assertDeepEqual(
  buildAccountTestPayload({ model: '', testEndpointMode: 'responses_sse' }, accountFixture({
    providerCode: 'deepseek',
    providerProtocolProfileId: 'profile_deepseek_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1'
  })),
  { testEndpointMode: 'responses_sse' },
  'OpenAI v1 账户选择 Responses SSE 时应原样提交测试形态'
)
assertDeepEqual(
  buildAccountTestPayload({ model: 'claude-opus-4-8', testEndpointMode: 'account_default' }, accountFixture({
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    protocolCode: 'anthropic',
    protocolVersion: 'v1'
  })),
  { model: 'claude-opus-4-8', testEndpointMode: 'messages_sse' },
  'Anthropic 测试应从接口能力限制中明确提交 Messages endpoint mode'
)

await submitAccountTestTask({
  account: boundAccount,
  accountScopeParams: scopeParams,
  isManagementView: true,
  payload: testPayload,
  sessionId: 'session_test'
})
assertLastCall(
  'accounts.test',
  [boundAccount.id, { ...testPayload, testSessionId: 'session_test' }, { systemAccountId: 'sys_bound' }],
  '管理端普通测试应使用账号归属 scope，并注入 testSessionId'
)

const draftAccount = draftAccountFixture()
await submitAccountTestTask({
  account: boundAccount,
  accountScopeParams: scopeParams,
  draftMode: 'create',
  draftPayload: draftAccount,
  isManagementView: true,
  payload: testPayload,
  sessionId: 'session_create_draft'
})
assertLastCall(
  'accounts.testDraft',
  [{ account: draftAccount, ...testPayload, testSessionId: 'session_create_draft' }, scopeParams],
  '管理端新建草稿测试应走 testDraft，并使用当前页面 scope'
)

await submitAccountTestTask({
  account: boundAccount,
  accountScopeParams: scopeParams,
  draftMode: 'saved',
  draftPayload: draftAccount,
  isManagementView: true,
  payload: testPayload,
  sessionId: 'session_saved_draft'
})
assertLastCall(
  'accounts.test',
  [boundAccount.id, { account: draftAccount, ...testPayload, testSessionId: 'session_saved_draft' }, { systemAccountId: 'sys_bound' }],
  '管理端已保存草稿测试应走账号测试，并使用账号归属 scope'
)

await submitAccountTestTask({
  account: boundAccount,
  accountScopeParams: scopeParams,
  draftMode: 'create',
  draftPayload: draftAccount,
  isManagementView: false,
  payload: testPayload,
  sessionId: 'session_personal_draft'
})
assertLastCall(
  'myAccounts.testDraft',
  [{ account: draftAccount, ...testPayload, testSessionId: 'session_personal_draft' }],
  '个人端新建草稿测试应走 myAccounts.testDraft，且不携带管理端 scope'
)

const controller = new AbortController()
await fetchAccountTestTask({ isManagementView: true, scopeParams, signal: controller.signal, taskId: 'task_1' })
assertLastCall('accounts.testTask', ['task_1', scopeParams, { signal: controller.signal }], '管理端拉取测试任务应携带 scope 和 signal')

await fetchAccountTestTask({ isManagementView: false, scopeParams, signal: controller.signal, taskId: 'task_2' })
assertLastCall('myAccounts.testTask', ['task_2', { signal: controller.signal }], '个人端拉取测试任务只应携带 signal')

await cancelAccountTestTask({ isManagementView: true, scopeParams, taskId: 'task_3' })
assertLastCall('accounts.cancelTestTask', ['task_3', scopeParams], '管理端取消测试任务应携带 scope')

await cancelAccountTestTask({ isManagementView: false, scopeParams, taskId: 'task_4' })
assertLastCall('myAccounts.cancelTestTask', ['task_4'], '个人端取消测试任务不应携带管理端 scope')

console.log('账户测试 session client 回归通过：管理端/个人端分流、独立 session、草稿 scope 和单任务轮询/取消均符合预期')

function stubApiMethod(domain: 'accounts' | 'myAccounts', method: string, handler: (...args: unknown[]) => Promise<unknown>): void {
  const target = api[domain] as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>
  target[method] = handler
}

async function recordCall<T>(method: string, args: unknown[], result: T): Promise<T> {
  apiCalls.push({ method, args })
  return result
}

function assertLastCall(method: string, expectedArgs: unknown[], message: string): void {
  const call = apiCalls.at(-1)
  if (!call) {
    throw new Error(`${message}，未记录到 API 调用`)
  }
  assertEqual(call.method, method, message)
  assertDeepEqual(call.args, expectedArgs, message)
}

function sessionFixture(id: string): AccountTestSession {
  return {
    id,
    status: 'running',
    lastHeartbeatAt: now,
    createdAt: now,
    updatedAt: now
  }
}

function taskFixture(id: string): AccountTestTask {
  return {
    id,
    accountId: 'account_session_client',
    accountName: 'Session Client 测试账户',
    providerCode: 'openai',
    type: 'api_key',
    status: 'queued',
    createdAt: now,
    queuedAt: now,
    updatedAt: now
  }
}

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account_session_client',
    providerCode: 'openai',
    name: 'Session Client 测试账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckModel: 'gpt-5.5',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function draftAccountFixture(): AccountDraftTestPayload['account'] {
  return {
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    name: '草稿测试账户',
    type: 'api_key',
    credentials: { api_key: 'sk-test' },
    concurrencyLimit: 1,
    priority: 0,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    modelMappings: [],
    groupId: 'group_1'
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

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，实际 ${String(actual)}，期望 ${String(expected)}`)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualText = JSON.stringify(actual)
  const expectedText = JSON.stringify(expected)
  if (actualText !== expectedText) {
    throw new Error(`${message}，实际 ${actualText}，期望 ${expectedText}`)
  }
}
