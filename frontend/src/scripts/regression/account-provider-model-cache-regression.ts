import { computed } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelPricing, ProviderModelsParams } from '@/types/domain'
import {
  invalidateAccountProviderModelOptionsCache,
  useAccountProviderModelOptions
} from '../../views/accounts/useAccountProviderModelOptions'

type ModelsLoader = (code: string, params?: ProviderModelsParams) => Promise<ProviderModelPricing[]>

const originalModels = api.providers.models
const calls: Array<{ code: string; params?: ProviderModelsParams }> = []
let latestModels = [providerModel('gpt-cache-old')]

;(api.providers as unknown as { models: ModelsLoader }).models = async (code, params) => {
  calls.push({ code, params })
  return latestModels
}

try {
  invalidateAccountProviderModelOptionsCache()

  const modelOptions = useAccountProviderModelOptions({
    createScopeParams: computed(() => undefined),
    currentProviderCode: () => 'openai',
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => false)
  })

  await modelOptions.loadProviderModelOptions('openai')
  assertDeepEqual(optionValues(modelOptions.providerModelOptions.value), ['gpt-cache-old'], '首次加载应读取接口模型目录')
  assertEqual(calls.length, 1, '首次加载应请求一次模型目录')

  latestModels = [providerModel('gpt-cache-old'), providerModel('gpt-cache-new')]
  await modelOptions.loadProviderModelOptions('openai')
  assertDeepEqual(optionValues(modelOptions.providerModelOptions.value), ['gpt-cache-old'], '未失效时应继续使用缓存模型目录')
  assertEqual(calls.length, 1, '未失效时不应重复请求模型目录')

  invalidateAccountProviderModelOptionsCache('gpt')
  await modelOptions.loadProviderModelOptions('openai')
  assertDeepEqual(optionValues(modelOptions.providerModelOptions.value), ['gpt-cache-old'], '其他供应商失效不应清理当前供应商缓存')
  assertEqual(calls.length, 1, '其他供应商失效后不应重新请求当前供应商模型目录')

  invalidateAccountProviderModelOptionsCache('openai')
  await modelOptions.loadProviderModelOptions('openai')
  assertDeepEqual(optionValues(modelOptions.providerModelOptions.value), ['gpt-cache-old', 'gpt-cache-new'], '当前供应商失效后应重新读取模型目录')
  assertEqual(calls.length, 2, '当前供应商失效后应重新请求模型目录')

  console.log('账户模型选项缓存回归通过：自定义模型变更后可按供应商失效并重新拉取模型目录')
} finally {
  ;(api.providers as unknown as { models: ModelsLoader }).models = originalModels
  invalidateAccountProviderModelOptionsCache()
}

function optionValues(options: Array<{ value: string }>): string[] {
  return options.map((option) => option.value)
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}，实际 ${String(actual)}，预期 ${String(expected)}`)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
}

function providerModel(model: string): ProviderModelPricing {
  return {
    providerCode: 'openai',
    model,
    source: 'custom-personal',
    scope: 'personal',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false
  }
}
