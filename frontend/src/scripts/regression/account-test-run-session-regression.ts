import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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
  'credentials' in (readAccountTestRunSession(false, accountA.id)?.testingAccount ?? {}),
  false,
  '恢复账户只保留列表展示和轮询字段，不伪造凭据字段'
)
assert.equal(
  readAccountTestRunSession(false, accountA.id)?.testingAccount.healthCheckEndpointMode,
  'responses_sse',
  'sessionStorage 恢复必须接受精确 JSON / Streaming 健康检查请求形态'
)

const interactionsSnapshot = readAccountTestRunSession(false, accountA.id)
assert.ok(interactionsSnapshot)
interactionsSnapshot.testEndpointMode = 'interactions_sse'
interactionsSnapshot.testEndpointModes = ['interactions_sse', 'interactions_json']
writeAccountTestRunSession(interactionsSnapshot)
assert.equal(readAccountTestRunSession(false, accountA.id)?.testEndpointMode, 'interactions_sse', '前端会话恢复必须保留 Gemini Interactions SSE mode')

const imageSnapshot = readAccountTestRunSession(false, accountA.id)
assert.ok(imageSnapshot)
imageSnapshot.testEndpointMode = 'images_json'
imageSnapshot.testEndpointModes = ['images_json']
writeAccountTestRunSession(imageSnapshot)
assert.equal(readAccountTestRunSession(false, accountA.id)?.testEndpointMode, 'images_json', '前端会话恢复必须保留 Images API 测试形态')

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
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
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

const accountTestModalSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountTestModal.ts', import.meta.url)), 'utf8')
assert.match(accountTestModalSource, /accountTestSessionHeartbeatIntervalMs\s*=\s*5000/, '测试会话心跳间隔应为 5000ms')
assert.doesNotMatch(accountTestModalSource, /accountTestSessionHeartbeatIntervalMs\s*=\s*2000/, '测试会话心跳间隔不得回退为 2000ms')
assert.match(accountTestModalSource, /function closeTestModal\([\s\S]*terminateAttachedTestRun\(true\)/, '关闭测试弹窗必须终止运行中的测试')
assert.match(accountTestModalSource, /function terminateAttachedTestRun[\s\S]*cancelAccountTestRunBackend\(run\)/, '终止测试必须请求后端取消')
assert.match(accountTestModalSource, /function terminateAttachedTestRun[\s\S]*clearRunSessionSnapshot\(run\)/, '终止测试必须清理本地可恢复会话')
assert.match(accountTestModalSource, /function closeTestModal\([\s\S]*detachCurrentTestView\(\)/, '无运行测试关闭时仍可安全脱离视图')
assert.doesNotMatch(
  accountTestModalSource.slice(accountTestModalSource.indexOf('function closeTestModal'), accountTestModalSource.indexOf('function closeTestModal') + 500),
  /persistAccountTestRunSession/,
  '关闭弹窗不得把运行中测试持久化为可恢复后台任务'
)
