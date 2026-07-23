import assert from 'node:assert/strict'

import { createApp } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { useAccountProxyOptions } from '@/views/accounts/useAccountProxyOptions'

const mutableApi = api as unknown as {
  proxies: { options: (params?: Record<string, unknown>) => Promise<Option[]> }
}
const originalOptions = mutableApi.proxies.options
const messageApi = message as unknown as { error?: (content: unknown) => void }
const originalMessageError = messageApi.error
const originalConsoleError = console.error

interface Option {
  id: string
  name: string
  type: 'http'
  enabled: true
}

async function waitFor(predicate: () => boolean, label: string, timeoutMs = 1500): Promise<void> {
  const started = Date.now()
  while (!predicate()) {
    if (Date.now() - started > timeoutMs) throw new Error(label)
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
}

function createHarness() {
  const app = createApp({ render: () => null })
  return app.runWithContext(() => useAccountProxyOptions({
    searchDelayMs: 25,
    scope: () => ({ selectedIds: ['selected-proxy'] })
  }))
}

function option(id: string): Option {
  return { id, name: id, type: 'http', enabled: true }
}

function setCurrentUser(id: string): void {
  authState.currentUser.value = {
    id,
    username: id,
    displayName: id,
    role: 'admin' as const,
    mustChangePassword: false
  }
}

async function main() {
  try {
    await verifyLazyLoadCacheAndIdentityIsolation()
    await verifyFailureCanRetry()
    await verifyLateResponsesCannotWin()
    await verifySearchCanRevisitAnInvalidatedKey()
    console.log('账户代理 options 按需加载回归通过')
  } finally {
    mutableApi.proxies.options = originalOptions
    messageApi.error = originalMessageError
    console.error = originalConsoleError
    authState.currentUser.value = undefined
  }
}

async function verifyLazyLoadCacheAndIdentityIsolation(): Promise<void> {
  const calls: Array<Record<string, unknown> | undefined> = []
  mutableApi.proxies.options = async (params) => {
    calls.push(params)
    return [option(`proxy-${calls.length}`)]
  }
  setCurrentUser('cache-user-a')
  const first = createHarness()
  assert.equal(calls.length, 0, '创建 composable 时不得请求代理 options')
  first.handleDropdown(true)
  await waitFor(() => calls.length === 1, '首次展开后应请求代理 options')
  assert.deepEqual(calls[0]?.selectedIds, ['selected-proxy'])

  const second = createHarness()
  second.handleDropdown(true)
  await Promise.resolve()
  assert.equal(calls.length, 1, '相同身份和查询的重复展开应命中会话缓存')
  assert.deepEqual(second.proxies.value.map((item) => item.id), ['proxy-1'])

  setCurrentUser('cache-user-b')
  const third = createHarness()
  third.handleDropdown(true)
  await waitFor(() => calls.length === 2, '切换登录用户后不得复用旧缓存')

  authState.revision.value += 1
  await third.load()
  assert.equal(calls.length, 3, '认证 revision 变化后不得复用旧缓存')
}

async function verifyFailureCanRetry(): Promise<void> {
  let calls = 0
  mutableApi.proxies.options = async () => {
    calls += 1
    if (calls === 1) throw new Error('expected proxy options failure')
    return [option('retry-proxy')]
  }
  setCurrentUser('retry-user')
  messageApi.error = () => undefined
  console.error = () => undefined
  const harness = createHarness()
  await harness.load()
  assert.equal(harness.proxies.value.length, 0, '失败不能写入 options')
  await harness.load()
  assert.equal(calls, 2, '失败后下次展开必须重新请求')
  assert.deepEqual(harness.proxies.value.map((item) => item.id), ['retry-proxy'])
}

async function verifyLateResponsesCannotWin(): Promise<void> {
  const calls: Array<{ params?: Record<string, unknown>; resolve: (value: Option[]) => void }> = []
  mutableApi.proxies.options = async (params) => await new Promise<Option[]>((resolve) => {
    calls.push({ params, resolve })
  })
  setCurrentUser('race-user')
  const harness = createHarness()
  const alpha = harness.load('alpha', true)
  await waitFor(() => calls.length === 1, 'alpha 请求未发出')
  const beta = harness.load('beta', true)
  await waitFor(() => calls.length === 2, 'beta 请求未发出')
  calls[1].resolve([option('beta-proxy')])
  await beta
  calls[0].resolve([option('alpha-proxy')])
  await alpha
  assert.deepEqual(harness.proxies.value.map((item) => item.id), ['beta-proxy'], '迟到响应不得覆盖新搜索结果')
}

async function verifySearchCanRevisitAnInvalidatedKey(): Promise<void> {
  const calls: Array<{ params?: Record<string, unknown>; resolve: (value: Option[]) => void }> = []
  mutableApi.proxies.options = async (params) => await new Promise<Option[]>((resolve) => {
    calls.push({ params, resolve })
  })
  setCurrentUser('revisit-user')
  const harness = createHarness()
  const firstAlpha = harness.load('alpha', true)
  await waitFor(() => calls.length === 1, '首个 alpha 请求未发出')
  harness.handleSearch('beta')
  harness.handleSearch('alpha')
  await waitFor(() => calls.length === 2, '回到 alpha 后应发起新请求')
  assert.equal(calls[1].params?.keyword, 'alpha', '回到 alpha 不得复用已失效的在途请求')
  calls[1].resolve([option('fresh-alpha-proxy')])
  await waitFor(() => !harness.loading.value, '新 alpha 请求完成后 loading 应结束')
  calls[0].resolve([option('stale-alpha-proxy')])
  await firstAlpha
  assert.deepEqual(harness.proxies.value.map((item) => item.id), ['fresh-alpha-proxy'])
}

void main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
