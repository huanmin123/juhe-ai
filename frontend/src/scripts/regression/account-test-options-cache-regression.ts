import { strict as assert } from 'node:assert'

import { computed, reactive } from 'vue'

import { api } from '@/api/client'
import type { AccountTestOptions } from '@/api/domains/accounts'
import { authState } from '@/composables/useAuth'
import type { AccountSummary } from '@/types/domain'
import type { AccountTestForm } from '@/views/accounts/accountTestFlow'
import { invalidateAccountProviderModelOptionsCache } from '@/views/accounts/useAccountProviderModelOptions'
import { useAccountTestModels } from '@/views/accounts/useAccountTestModels'

const originalManagementLoader = api.accounts.testOptions
const originalSelfLoader = api.myAccounts.testOptions

try {
  await verifyConcurrentLoadsAndCacheIsolation()
  await verifyInvalidationRejectsPendingCacheWrites()
  await verifyMissingConfigRevisionBypassesCache()
  await verifyRejectedLoadsAreRetried()
  console.log('账户测试模型选项短时缓存回归通过')
} finally {
  api.accounts.testOptions = originalManagementLoader
  api.myAccounts.testOptions = originalSelfLoader
  authState.currentUser.value = undefined
  invalidateAccountProviderModelOptionsCache()
}

async function verifyInvalidationRejectsPendingCacheWrites(): Promise<void> {
  invalidateAccountProviderModelOptionsCache()
  authState.currentUser.value = currentUser('invalidation-user')
  const account = accountFixture(5)
  let calls = 0
  let releaseLoad = () => undefined
  const gate = new Promise<void>((resolve) => {
    releaseLoad = resolve
  })
  api.accounts.testOptions = async (accountId) => {
    calls += 1
    if (calls === 1) await gate
    return testOptions(accountId)
  }

  const pending = testModels(true, 'invalidation-owner').loadSavedAccountTestOptions(account)
  await Promise.resolve()
  invalidateAccountProviderModelOptionsCache()
  releaseLoad()
  await pending
  await testModels(true, 'invalidation-owner').loadSavedAccountTestOptions(account)
  assert.equal(calls, 2, '失效前发起的旧请求完成后不能重新填充当前缓存')
}

async function verifyMissingConfigRevisionBypassesCache(): Promise<void> {
  invalidateAccountProviderModelOptionsCache()
  authState.currentUser.value = currentUser('revision-user')
  const account = accountFixture(undefined)
  let calls = 0
  api.accounts.testOptions = async (accountId) => {
    calls += 1
    return testOptions(accountId)
  }

  await testModels(true, 'revision-owner').loadSavedAccountTestOptions(account)
  await testModels(true, 'revision-owner').loadSavedAccountTestOptions(account)
  assert.equal(calls, 2, '缺少配置版本时不能缓存账户测试选项')
}

async function verifyConcurrentLoadsAndCacheIsolation(): Promise<void> {
  invalidateAccountProviderModelOptionsCache()
  authState.currentUser.value = currentUser('user-a')
  const account = accountFixture(1)
  let managementCalls = 0
  let selfCalls = 0
  let releaseFirstLoad = () => undefined
  const firstLoadGate = new Promise<void>((resolve) => {
    releaseFirstLoad = resolve
  })

  api.accounts.testOptions = async (accountId) => {
    managementCalls += 1
    if (managementCalls <= 2) await firstLoadGate
    return testOptions(accountId)
  }
  api.myAccounts.testOptions = async (accountId) => {
    selfCalls += 1
    return testOptions(accountId)
  }

  const first = testModels(true, 'owner-a')
  const second = testModels(true, 'owner-a')
  const firstLoad = first.loadSavedAccountTestOptions(account)
  const secondLoad = second.loadSavedAccountTestOptions(account)
  await waitFor(() => managementCalls === 1, '账户测试选项请求未进入 loader')
  assert.equal(managementCalls, 1, '相同用户、视图、范围、账户和配置版本的并发请求应合并')
  releaseFirstLoad()
  await Promise.all([firstLoad, secondLoad])

  await testModels(true, 'owner-a').loadSavedAccountTestOptions(account)
  assert.equal(managementCalls, 1, '5 分钟内重复打开同一账户应复用测试选项')

  await testModels(true, 'owner-a').loadSavedAccountTestOptions(accountFixture(2))
  assert.equal(managementCalls, 2, '账户配置版本变化后应重新加载')

  await testModels(true, 'owner-a').loadSavedAccountTestOptions(accountFixture(2, 'account-cache-test-2'))
  assert.equal(managementCalls, 3, '不同账户不能复用测试选项')

  await testModels(true, 'owner-b').loadSavedAccountTestOptions(accountFixture(2))
  assert.equal(managementCalls, 4, '不同管理范围不能复用测试选项')

  await testModels(false).loadSavedAccountTestOptions(accountFixture(2))
  assert.equal(selfCalls, 1, '个人视图不能复用管理视图测试选项')

  authState.currentUser.value = currentUser('user-b')
  await testModels(true, 'owner-a').loadSavedAccountTestOptions(accountFixture(2))
  assert.equal(managementCalls, 5, '不同登录用户不能复用测试选项')

  invalidateAccountProviderModelOptionsCache()
  await testModels(true, 'owner-a').loadSavedAccountTestOptions(accountFixture(2))
  assert.equal(managementCalls, 6, '模型目录缓存失效时应同步清理测试选项')
}

async function verifyRejectedLoadsAreRetried(): Promise<void> {
  invalidateAccountProviderModelOptionsCache()
  authState.currentUser.value = currentUser('retry-user')
  const account = accountFixture(7)
  let calls = 0
  api.accounts.testOptions = async (accountId) => {
    calls += 1
    if (calls === 1) throw new Error('expected loader failure')
    return testOptions(accountId)
  }

  await assert.rejects(
    testModels(true, 'retry-owner').loadSavedAccountTestOptions(account),
    /expected loader failure/
  )
  await testModels(true, 'retry-owner').loadSavedAccountTestOptions(account)
  assert.equal(calls, 2, '失败请求不能写入缓存，下一次必须重新加载')
}

function testModels(isManagementView: boolean, systemAccountId?: string) {
  return useAccountTestModels({
    accountScopeParams: computed(() => systemAccountId ? { systemAccountId } : undefined),
    isManagementView: computed(() => isManagementView),
    testForm: reactive<AccountTestForm>({
      model: '',
      testEndpointMode: 'account_default'
    })
  })
}

function accountFixture(configRevision: number | undefined, id = 'account-cache-test'): AccountSummary {
  return {
    id,
    configRevision,
    name: '缓存测试账户',
    healthCheckEndpointMode: 'responses_sse'
  } as AccountSummary
}

function testOptions(accountId: string): AccountTestOptions {
  return {
    accountId,
    defaultModel: 'gpt-5.6-sol',
    models: [{
      model: 'gpt-5.6-sol',
      supportedApiProtocols: ['openai_responses'],
      testEndpointModes: ['responses_sse']
    }],
    testEndpointModes: ['responses_sse'],
    defaultTestEndpointMode: 'responses_sse'
  }
}

function currentUser(id: string) {
  return {
    id,
    username: id,
    displayName: id,
    role: 'admin' as const,
    mustChangePassword: false
  }
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return
    await Promise.resolve()
  }
  throw new Error(message)
}
