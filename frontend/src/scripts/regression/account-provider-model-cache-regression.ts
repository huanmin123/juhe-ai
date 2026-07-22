import assert from 'node:assert/strict'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'
import {
  invalidateAccountProviderModelOptionsCache,
  useAccountProviderModelOptions
} from '../../views/accounts/useAccountProviderModelOptions'

type ModelOptionsLoader = typeof api.providers.modelOptions
const originalModelOptions = api.providers.modelOptions
const calls: Array<Record<string, unknown> | undefined> = []
let latestModels: ProviderModelOption[] = [{ id: 'gpt-cache-old', name: 'GPT Cache Old' }]

;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
  calls.push(params)
  return latestModels
}

try {
  invalidateAccountProviderModelOptionsCache()
  const currentProviderCode = ref('openai')
  const modelOptions = useAccountProviderModelOptions({
    currentProviderCode: () => currentProviderCode.value,
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => false),
    modelScopeParams: computed(() => undefined)
  })

  await modelOptions.loadProviderModelOptions('openai')
  assert.deepEqual(modelOptions.providerModelOptions.value, [{ label: 'GPT Cache Old', value: 'gpt-cache-old' }])
  assert.deepEqual(calls[0], { providerCode: 'openai', limit: 50 })

  latestModels = [
    { id: 'gpt-cache-old', name: 'GPT Cache Old' },
    { id: 'gpt-cache-new', name: 'GPT Cache New' }
  ]
  await modelOptions.loadProviderModelOptions('openai')
  assert.deepEqual(modelOptions.providerModelOptions.value.map((item) => item.value), ['gpt-cache-old', 'gpt-cache-new'])
  assert.equal(calls.length, 2, '重新打开模型下拉必须重新请求，不能让旧浏览器缓存掩盖其他会话的模型修改')

  invalidateAccountProviderModelOptionsCache('openai')
  await modelOptions.loadProviderModelOptions('openai')
  assert.deepEqual(modelOptions.providerModelOptions.value.map((item) => item.value), ['gpt-cache-old', 'gpt-cache-new'])
  assert.equal(calls.length, 3, '显式失效后仍应正常请求轻量选项')

  invalidateAccountProviderModelOptionsCache()
  const scopeId = ref('sys_scope_a')
  const requests = new Map<string, Deferred<ProviderModelOption[]>>()
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
    const systemAccountId = params?.systemAccountId ?? ''
    const request = deferred<ProviderModelOption[]>()
    requests.set(systemAccountId, request)
    return request.promise
  }
  const raced = useAccountProviderModelOptions({
    currentProviderCode: () => currentProviderCode.value,
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => true),
    modelScopeParams: computed(() => ({ systemAccountId: scopeId.value }))
  })
  const scopeA = raced.loadProviderModelOptions('openai')
  scopeId.value = 'sys_scope_b'
  const scopeB = raced.loadProviderModelOptions('openai')
  await waitFor(() => requests.size === 2)
  requests.get('sys_scope_b')?.resolve([{ id: 'scope-b-model', name: 'Scope B' }])
  await scopeB
  requests.get('sys_scope_a')?.resolve([{ id: 'scope-a-model', name: 'Scope A' }])
  await scopeA
  assert.deepEqual(raced.providerModelOptions.value, [{ label: 'Scope B', value: 'scope-b-model' }])
  assert.equal(raced.providerModelsLoading.value, false)

  console.log('账户模型选项实时加载回归通过：轻量契约、跨会话刷新、作用域隔离和逆序响应均符合预期')
} finally {
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = originalModelOptions
  invalidateAccountProviderModelOptionsCache()
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve
  })
  return { promise, resolve }
}

async function waitFor(predicate: () => boolean): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return
    await Promise.resolve()
  }
  throw new Error('等待账户模型选项请求超时')
}
