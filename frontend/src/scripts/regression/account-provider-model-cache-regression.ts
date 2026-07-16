import { computed, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption, ProviderModelPricing, ProviderModelsParams } from '@/types/domain'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
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
    { ...providerModel('gpt-cache-old', ['chat_completions']), supportedServiceTiers: ['priority'], supportedReasoningEfforts: ['medium'] },
    { ...providerModel('gpt-cache-old', ['responses']), supportedServiceTiers: ['flex'], supportedReasoningEfforts: ['high'], defaultReasoningEffort: 'high' },
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
  assertDeepEqual(
    requestCapabilitiesByValue(modelOptions.providerModelOptions.value)['gpt-cache-old'],
    {
      supportedServiceTiers: ['priority', 'flex'],
      supportedReasoningEfforts: ['medium', 'high'],
      defaultReasoningEffort: 'high'
    },
    '重复模型去重时必须合并请求覆盖能力'
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

  invalidateAccountProviderModelOptionsCache()
  const providerScopeId = ref('sys_scope_a')
  const providerScopeRequests = new Map<string, Deferred<ProviderModelPricing[]>>()
  const providerScopeCalls: string[] = []
  ;(api.providers as unknown as { models: ModelsLoader }).models = async (_code, params) => {
    const systemAccountId = params?.systemAccountId ?? ''
    providerScopeCalls.push(systemAccountId)
    const request = deferred<ProviderModelPricing[]>()
    providerScopeRequests.set(systemAccountId, request)
    return request.promise
  }
  const racedProviderModels = useAccountProviderModelOptions({
    currentProviderCode: () => 'openai',
    extractApiErrorMessage: (error, fallback) => error instanceof Error ? error.message : fallback,
    isManagementView: computed(() => true),
    modelScopeParams: computed(() => ({ systemAccountId: providerScopeId.value }))
  })
  const providerScopeA = racedProviderModels.loadProviderModelOptions('openai')
  providerScopeId.value = 'sys_scope_b'
  const providerScopeB = racedProviderModels.loadProviderModelOptions('openai')
  assertDeepEqual(providerScopeCalls, ['sys_scope_a', 'sys_scope_b'], '不同系统账户作用域必须各自发起模型目录请求')
  providerScopeRequests.get('sys_scope_b')?.resolve([providerModel('scope-b-model', ['responses'])])
  await providerScopeB
  providerScopeRequests.get('sys_scope_a')?.resolve([providerModel('scope-a-model', ['chat_completions'])])
  await providerScopeA
  assertDeepEqual(
    optionValues(racedProviderModels.providerModelOptions.value),
    ['scope-b-model'],
    '同供应商旧作用域的慢响应不得覆盖当前系统账户模型目录'
  )
  assertEqual(racedProviderModels.providerModelsLoading.value, false, '当前作用域完成后 loading 必须及时结束')

  const globalScopeId = ref('sys_global_a')
  const globalScopeRequests = new Map<string, Deferred<ProviderModelOption[]>>()
  const globalScopeCalls: string[] = []
  ;(api.providers as unknown as { modelOptions: ModelOptionsLoader }).modelOptions = async (params) => {
    const systemAccountId = params?.systemAccountId ?? ''
    globalScopeCalls.push(systemAccountId)
    const request = deferred<ProviderModelOption[]>()
    globalScopeRequests.set(systemAccountId, request)
    return request.promise
  }
  const racedGlobalModels = useProviderModelSelectOptions({
    protocol: 'openai',
    scopeParams: computed(() => ({ systemAccountId: globalScopeId.value }))
  })
  const globalScopeA = racedGlobalModels.loadModelOptions()
  globalScopeId.value = 'sys_global_b'
  const globalScopeB = racedGlobalModels.loadModelOptions()
  assertDeepEqual(globalScopeCalls, ['sys_global_a', 'sys_global_b'], '全局模型选项切换作用域时不得复用旧 scope Promise')
  globalScopeRequests.get('sys_global_b')?.resolve([
    { providerCode: 'gpt', model: 'global-b-model', supportedApiProtocols: ['responses'] }
  ])
  await globalScopeB
  globalScopeRequests.get('sys_global_a')?.resolve([
    { providerCode: 'gpt', model: 'global-a-model', supportedApiProtocols: ['chat_completions'] }
  ])
  await globalScopeA
  assertDeepEqual(
    optionValues(racedGlobalModels.selectOptions.value),
    ['global-b-model'],
    '旧作用域全局模型选项响应不得覆盖当前系统账户候选'
  )
  assertEqual(racedGlobalModels.loading.value, false, '当前全局模型作用域完成后 loading 必须及时结束')

  console.log('账户模型选项缓存回归通过：缓存失效、作用域隔离和逆序响应均符合预期')
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

type Deferred<T> = {
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
