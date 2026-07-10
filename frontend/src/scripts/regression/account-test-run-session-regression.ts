import { strict as assert } from 'node:assert'

import { authState } from '@/composables/useAuth'
import type { AccountSummary, AccountTestTask } from '@/types/domain'
import {
  accountTestRunSessionStorageTtlMs,
  clearAccountTestRunSession,
  readAccountTestRunSession,
  writeAccountTestRunSession
} from '../../views/accounts/accountTestRunSession'

const storage = createMemoryStorage()
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: { sessionStorage: storage }
})
authState.currentUser.value = {
  id: 'user_account_test_session',
  username: 'session-user',
  displayName: '测试用户',
  role: 'user',
  status: 'active'
} as typeof authState.currentUser.value

assert.equal(accountTestRunSessionStorageTtlMs, 12 * 60 * 60 * 1000, '账户测试终端快照应保留 12 小时')

const accountA = accountFixture('account_session_a', '账户 A')
const accountB = accountFixture('account_session_b', '账户 B')
writeAccountTestRunSession(snapshotFixture(accountA, 'session_a', 'task_a'))
writeAccountTestRunSession(snapshotFixture(accountB, 'session_b', 'task_b'))

assert.equal(storage.length, 2, '同一用户的 A/B 账户运行任务应写入两个独立快照')
assert.equal(readAccountTestRunSession(false, accountA.id)?.sessionId, 'session_a', '账户 A 应恢复自己的 session')
assert.equal(readAccountTestRunSession(false, accountB.id)?.sessionId, 'session_b', '账户 B 应恢复自己的 session')
assert.equal(readAccountTestRunSession(false, 'account_missing'), undefined, '不存在的账户不应读到其他账户快照')

const accountARaw = storage.getItem(findStorageKey(storage, encodeURIComponent(accountA.id))) ?? ''
assert.equal(accountARaw.includes('sk-session-secret'), false, 'sessionStorage 快照不得保存账户 API Key')
assert.equal(accountARaw.includes('secret-request-body'), false, 'sessionStorage 快照不得保存人工测试请求体')
assert.equal(accountARaw.includes('secret-response-body'), false, 'sessionStorage 快照不得保存人工测试响应正文')
assert.equal(accountARaw.includes('x-secret-header'), false, 'sessionStorage 快照不得保存人工测试响应 Header')
assert.deepEqual(
  readAccountTestRunSession(false, accountA.id)?.testingAccount.credentials,
  {},
  '恢复账户只保留展示和轮询字段，不恢复凭据'
)

const managementAccount = accountFixture('account_session_management', '管理账户')
writeAccountTestRunSession({
  ...snapshotFixture(managementAccount, 'session_management', 'task_management'),
  isManagementView: true,
  scopeParams: { systemAccountId: 'sys_1' }
})
assert.equal(storage.length, 3, '管理端和个人端账户快照应继续按视图隔离')
assert.equal(
  readAccountTestRunSession(true, managementAccount.id)?.scopeParams?.systemAccountId,
  'sys_1',
  '管理端恢复应保留测试作用域'
)

const managementKey = findStorageKey(storage, encodeURIComponent(managementAccount.id))
const managementPayload = JSON.parse(storage.getItem(managementKey) ?? '{}') as { expiresAt?: number }
managementPayload.expiresAt = Date.now() - 1
storage.setItem(managementKey, JSON.stringify(managementPayload))
assert.equal(
  readAccountTestRunSession(true, managementAccount.id),
  undefined,
  '过期账户快照不应恢复'
)
assert.equal(storage.getItem(managementKey), null, '读取过期快照时应同步清理对应账户存储')

clearAccountTestRunSession(false, accountA.id)
assert.equal(readAccountTestRunSession(false, accountA.id), undefined, '清理账户 A 不应残留其测试终端内容')
assert.equal(readAccountTestRunSession(false, accountB.id)?.sessionId, 'session_b', '清理账户 A 不应覆盖账户 B 快照')

console.log('账户测试终端会话回归通过：A/B 按账户隔离、12 小时 TTL、视图隔离和凭据脱敏均符合预期')

function snapshotFixture(
  account: AccountSummary,
  sessionId: string,
  taskId: string
) {
  return {
    sessionId,
    isManagementView: false,
    model: 'gpt-5.5',
    modelOptions: [
      { label: 'GPT 5.5', value: 'gpt-5.5' },
      { label: 'GPT 5.6', value: 'gpt-5.6' }
    ],
    testEndpointMode: 'responses_sse' as const,
    testEndpointModes: ['responses_sse', 'chat_sse'] as const,
    testingAccount: account,
    activeTask: taskFixture(account, taskId),
    running: true
  }
}

function accountFixture(id: string, name: string): AccountSummary {
  return {
    id,
    providerCode: 'gpt',
    name,
    type: 'api_key',
    credentials: { api_key: 'sk-session-secret' },
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'gpt-5.6-sol',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }],
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage()
  }
}

function taskFixture(account: AccountSummary, id: string): AccountTestTask {
  const now = new Date().toISOString()
  return {
    id,
    sessionId: `session_${id}`,
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    type: account.type,
    status: 'running',
    model: 'gpt-5.5',
    testEndpointMode: 'responses_sse',
    result: {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: false,
      message: '包含完整诊断的失败结果',
      requestBody: { prompt: 'secret-request-body' },
      responseHeaders: { 'x-secret-header': 'secret' },
      responseBody: { error: 'secret-response-body' }
    },
    createdAt: now,
    queuedAt: now,
    startedAt: now,
    updatedAt: now
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

function findStorageKey(storageValue: Storage, marker: string): string {
  for (let index = 0; index < storageValue.length; index += 1) {
    const key = storageValue.key(index)
    if (key?.includes(marker)) return key
  }
  throw new Error(`未找到包含 ${marker} 的 sessionStorage 键`)
}

function createMemoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() {
      return values.size
    },
    clear() {
      values.clear()
    },
    getItem(key: string) {
      return values.get(key) ?? null
    },
    key(index: number) {
      return [...values.keys()][index] ?? null
    },
    removeItem(key: string) {
      values.delete(key)
    },
    setItem(key: string, value: string) {
      values.set(key, value)
    }
  }
}
