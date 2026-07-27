import assert from 'node:assert/strict'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import type { ProviderModelOption } from '@/types/domain'

type ModelOptionsLoader = typeof api.providers.modelOptions
const originalLoader = api.providers.modelOptions
const calls: Array<Record<string, unknown> | undefined> = []
const pending = new Map<string, Deferred<ProviderModelOption[]>>()

;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
  calls.push(params)
  const key = String(params?.providerCode ?? '')
  const request = deferred<ProviderModelOption[]>()
  pending.set(key, request)
  return request.promise
}

try {
  const providerCode = ref('openai')
  const selectedIds = ref(['gpt-selected'])
  const resource = useProviderModelSelectOptions({
    providerCode: computed(() => providerCode.value),
    selectedIds: computed(() => selectedIds.value)
  })

  assert.equal(calls.length, 0, '组合式创建时不得预取模型选项')

  const openaiLoad = resource.loadModelOptions({ keyword: ' gpt ', limit: 20 })
  await waitFor(() => calls.length === 1)
  assert.deepEqual(calls[0], {
    providerCode: 'openai',
    keyword: 'gpt',
    limit: 20,
    selectedIds: ['gpt-selected']
  })

  providerCode.value = 'anthropic'
  const anthropicLoad = resource.loadModelOptions({ keyword: 'claude', limit: 10 })
  await waitFor(() => calls.length === 2)
  pending.get('anthropic')?.resolve([{
    id: 'claude-sonnet',
    name: 'Claude Sonnet',
    supportedApiProtocols: ['messages'],
    supportedServiceTiers: [],
    supportedReasoningEfforts: []
  }])
  await anthropicLoad
  pending.get('openai')?.resolve([{
    id: 'gpt-5',
    name: 'GPT-5',
    supportedApiProtocols: ['chat_completions', 'responses'],
    supportedServiceTiers: [],
    supportedReasoningEfforts: []
  }])
  await openaiLoad

  assert.deepEqual(resource.providerModelOptions.value, [{
    id: 'claude-sonnet',
    name: 'Claude Sonnet',
    supportedApiProtocols: ['messages'],
    supportedServiceTiers: [],
    supportedReasoningEfforts: []
  }])
  assert.deepEqual(resource.selectOptions.value, [{
    label: 'Claude Sonnet',
    value: 'claude-sonnet',
    providerCodes: [],
    supportedApiProtocols: ['messages']
  }])
  assert.equal(resource.loading.value, false)

  const batchedCalls: Array<Record<string, unknown> | undefined> = []
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
    batchedCalls.push(params)
    return []
  }
  const batchedSelectedIds = Array.from({ length: 51 }, (_, index) => `selected-${index + 1}`)
  const batchedResource = useProviderModelSelectOptions({
    providerCode: ref('openai'),
    selectedIds: ref(batchedSelectedIds)
  })
  await batchedResource.loadModelOptions()
  assert.equal(batchedCalls.length, 2, '超过单次接口上限的已选模型必须分批补齐')
  assert.deepEqual(batchedCalls[0]?.selectedIds, batchedSelectedIds.slice(0, 50))
  assert.deepEqual(batchedCalls[1]?.selectedIds, batchedSelectedIds.slice(50))
} finally {
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = originalLoader
}

console.log('前端供应商模型轻量选项回归通过：零预取、远程窗口、供应商作用域和竞态隔离均符合预期')

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
  throw new Error('等待模型选项请求超时')
}
