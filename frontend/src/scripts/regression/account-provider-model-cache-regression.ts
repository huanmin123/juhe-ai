import { computed } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption, ProviderModelPricing, ProviderModelsParams } from '@/types/domain'
import {
  invalidateAccountProviderModelOptionsCache,
  useAccountProviderModelOptions
} from '../../views/accounts/useAccountProviderModelOptions'

type ModelsLoader = (code: string, params?: ProviderModelsParams) => Promise<ProviderModelPricing[]>
type ModelOptionsLoader = typeof api.providers.modelOptions

const originalModels = api.providers.models
const originalModelOptions = api.providers.modelOptions
const calls: Array<{ code: string; params?: ProviderModelsParams }> = []
const modelOptionCalls: unknown[] = []
let latestModels: ProviderModelPricing[] = [{
  ...providerModel('gpt-cache-old', ['chat_completions']),
  supportedServiceTiers: ['priority'],
  supportedReasoningEfforts: ['medium', 'high'],
  defaultReasoningEffort: 'high'
}]
const latestGlobalModels: ProviderModelOption[] = [
  { providerCode: 'gpt', model: 'gpt-global-model', supportedApiProtocols: ['chat_completions'] },
  { providerCode: 'gpt', model: 'gpt-global-model', supportedApiProtocols: ['responses'] },
  { providerCode: 'anthropic', model: 'claude-global-model', supportedApiProtocols: ['messages'] },
  { providerCode: 'gemini', model: 'gemini-global-model', supportedApiProtocols: ['generate_content', 'stream_generate_content'] }
]

;(api.providers as unknown as { models: ModelsLoader }).models = async (code, params) => {
  calls.push({ code, params })
  return latestModels
}
;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
  modelOptionCalls.push(params ?? {})
  return latestGlobalModels
}

try {
  invalidateAccountProviderModelOptionsCache()

  const modelOptions = useAccountProviderModelOptions({
    currentProviderCode: () => 'openai',
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => false),
    modelScopeParams: computed(() => undefined)
  })

  await modelOptions.loadProviderModelOptions('openai')
  assertDeepEqual(optionValues(modelOptions.providerModelOptions.value), ['gpt-cache-old'], '首次加载应读取接口模型目录')
  assertDeepEqual(
    protocolsByValue(modelOptions.providerModelOptions.value),
    { 'gpt-cache-old': ['chat_completions'] },
    '普通供应商模型选项必须保留协议能力，供账号模型别名右侧按协议过滤'
  )
  assertDeepEqual(
    requestCapabilitiesByValue(modelOptions.providerModelOptions.value),
    {
      'gpt-cache-old': {
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['medium', 'high'],
        defaultReasoningEffort: 'high'
      }
    },
    'GPT 账户模型选项必须保留模型目录精确请求能力'
  )
  assertEqual(calls.length, 1, '首次加载应请求一次模型目录')

  latestModels = [
    providerModel('gpt-cache-old', ['chat_completions']),
    providerModel('gpt-cache-old', ['responses']),
    providerModel('gpt-cache-new', ['responses'])
  ]
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
  assertDeepEqual(
    protocolsByValue(modelOptions.providerModelOptions.value),
    { 'gpt-cache-old': ['chat_completions', 'responses'], 'gpt-cache-new': ['responses'] },
    '重复模型去重时必须合并协议能力'
  )
  assertEqual(calls.length, 2, '当前供应商失效后应重新请求模型目录')

  const scopedModelOptions = useAccountProviderModelOptions({
    currentProviderCode: () => 'openai',
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => true),
    modelScopeParams: computed(() => ({ systemAccountId: 'sys_user_model_scope' }))
  })
  await scopedModelOptions.loadProviderModelOptions('openai')
  assertEqual(calls.length, 3, '管理视图目标用户模型目录应使用独立缓存并重新请求')
  assertEqual(calls[2]?.params?.systemAccountId, 'sys_user_model_scope', '非混合供应商模型目录请求必须携带目标系统账户')

  const hybridModelOptions = useAccountProviderModelOptions({
    currentProviderCode: () => 'hybrid',
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => true),
    modelScopeParams: computed(() => ({ systemAccountId: 'sys_admin' }))
  })
  await hybridModelOptions.loadProviderModelOptions('hybrid')
  assertDeepEqual(
    optionValues(hybridModelOptions.providerModelOptions.value),
    ['gpt-global-model', 'claude-global-model', 'gemini-global-model'],
    '混合供应商应加载全局模型池而不是 hybrid 自身模型目录'
  )
  assertDeepEqual(
    protocolsByValue(hybridModelOptions.providerModelOptions.value),
    {
      'gpt-global-model': ['chat_completions', 'responses'],
      'claude-global-model': ['messages'],
      'gemini-global-model': ['generate_content', 'stream_generate_content']
    },
    '混合供应商全局模型池也必须保留并合并协议能力'
  )
  assertEqual(modelOptionCalls.length, 1, '混合供应商应请求一次全局模型选项接口')
  assertEqual(calls.length, 3, '混合供应商不应请求 /providers/hybrid/models 作为创建页模型候选')

  console.log('账户模型选项缓存回归通过：自定义模型变更后可按供应商失效并重新拉取模型目录')
} finally {
  ;(api.providers as unknown as { models: ModelsLoader }).models = originalModels
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = originalModelOptions
  invalidateAccountProviderModelOptionsCache()
}

function optionValues(options: Array<{ value: string }>): string[] {
  return options.map((option) => option.value)
}

function protocolsByValue(options: Array<{ value: string; supportedApiProtocols?: string[] }>): Record<string, string[] | undefined> {
  return Object.fromEntries(options.map((option) => [option.value, option.supportedApiProtocols]))
}

function requestCapabilitiesByValue(options: Array<{
  value: string
  supportedServiceTiers?: string[]
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort?: string
}>): Record<string, unknown> {
  return Object.fromEntries(options.map((option) => [option.value, {
    supportedServiceTiers: option.supportedServiceTiers,
    supportedReasoningEfforts: option.supportedReasoningEfforts,
    defaultReasoningEffort: option.defaultReasoningEffort
  }]))
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

function providerModel(model: string, supportedApiProtocols: ProviderModelPricing['supportedApiProtocols']): ProviderModelPricing {
  return {
    providerCode: 'openai',
    model,
    source: 'custom-personal',
    scope: 'personal',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false,
    supportedApiProtocols
  }
}
