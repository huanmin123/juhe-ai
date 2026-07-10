import { strict as assert } from 'node:assert'

import { authState } from '@/composables/useAuth'
import type { AccountSummary } from '@/types/domain'
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

const account = accountFixture()
writeAccountTestRunSession({
  sessionId: 'session_self',
  isManagementView: false,
  mode: 'single',
  model: 'gpt-5.5',
  testEndpointMode: 'responses_sse',
  testingAccount: account,
  batchTestingAccounts: [],
  batchTestItems: [],
  running: true
})

assert.equal(storage.length, 1, '个人端测试应写入一个 sessionStorage 快照')
const rawSnapshot = storage.getItem(storage.key(0) ?? '') ?? ''
assert.equal(rawSnapshot.includes('sk-session-secret'), false, 'sessionStorage 快照不得保存账户 API Key')
const restored = readAccountTestRunSession(false)
assert.equal(restored?.sessionId, 'session_self', '个人端测试应恢复同一个 session')
assert.deepEqual(restored?.testingAccount?.credentials, {}, '恢复账户只保留展示和轮询字段，不恢复凭据')
assert.equal(restored?.testingAccount?.modelMappings?.[0]?.upstreamModel, 'gpt-5.6-sol', '恢复快照应保留模型映射展示信息')

writeAccountTestRunSession({
  sessionId: 'session_management',
  isManagementView: true,
  scopeParams: { systemAccountId: 'sys_1' },
  mode: 'batch',
  model: 'gpt-5.5',
  testEndpointMode: 'chat_sse',
  batchTestingAccounts: [account],
  batchTestItems: [{ account, status: 'queued', taskId: 'task_1' }],
  running: true
})
assert.equal(storage.length, 2, '管理端和个人端测试快照应使用独立存储键')
assert.equal(readAccountTestRunSession(true)?.scopeParams?.systemAccountId, 'sys_1', '管理端恢复应保留测试作用域')

const managementKey = findStorageKey(storage, ':management:')
const managementPayload = JSON.parse(storage.getItem(managementKey) ?? '{}') as { expiresAt?: number }
managementPayload.expiresAt = Date.now() - 1
storage.setItem(managementKey, JSON.stringify(managementPayload))
assert.equal(readAccountTestRunSession(true), undefined, '过期测试快照不应恢复')
assert.equal(storage.getItem(managementKey), null, '读取过期快照时应同步清理存储')

clearAccountTestRunSession(false)
assert.equal(storage.length, 0, '主动清理当前用户快照后不应残留测试终端内容')

console.log('账户测试终端会话回归通过：12 小时 TTL、用户/视图隔离和凭据脱敏均符合预期')

function accountFixture(): AccountSummary {
  return {
    id: 'account_session_storage',
    providerCode: 'gpt',
    name: '会话存储测试账户',
    type: 'api_key',
    credentials: { api_key: 'sk-session-secret' },
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.5'],
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
