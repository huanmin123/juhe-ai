import assert from 'node:assert/strict'

import {
  createChatModelOptionsSnapshotCache,
  loadChatModelOptionsFromProviderCatalogs,
  resolveChatModelOptionsFromAccountSnapshot
} from '../../modules/chat/chat-model-availability.js'

const accounts = [{
  supportedModels: ['gpt-fast', 'gpt-image'],
  supportedEndpointModes: ['chat_sse', 'responses_sse'],
  modelMappings: [{
    sourceModel: 'gpt-mapped',
    sourceEndpointFamily: 'responses',
    upstreamModel: 'gpt-image',
    upstreamEndpointFamily: 'responses'
  }]
}, {
  supportedModels: ['gpt-fast'],
  supportedEndpointModes: ['chat_sse']
}]

const modelOptions = resolveChatModelOptionsFromAccountSnapshot({
  accounts,
  modelOptions: [{ id: 'gpt-fast' }, { id: 'gpt-image' }, { id: 'gpt-mapped' }, { id: 'unavailable' }]
})

assert.deepEqual(modelOptions, [
  { id: 'gpt-fast', supportedApiProtocols: ['chat_completions', 'responses'] },
  { id: 'gpt-image', supportedApiProtocols: ['chat_completions', 'responses'] },
  { id: 'gpt-mapped', supportedApiProtocols: ['responses'] }
], '模型协议应只根据已加载账号快照完成，不为每个模型重复读取账号')

assert.deepEqual(resolveChatModelOptionsFromAccountSnapshot({
  accounts: [{
    supportedModels: ['claude-upstream'],
    supportedEndpointModes: ['messages_sse'],
    modelMappings: [{
      sourceModel: 'gpt-bridge',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'claude-upstream',
      upstreamEndpointFamily: 'messages'
    }]
  }],
  modelOptions: [{ id: 'gpt-bridge' }]
}), [{ id: 'gpt-bridge', supportedApiProtocols: ['responses'] }], '跨协议映射必须依据上游 messages 端点模式判定候选模型可用性')

let now = 1_000
let loads = 0
const cache = createChatModelOptionsSnapshotCache({ ttlMs: 5_000, now: () => now })
const load = async () => {
  loads += 1
  await new Promise((resolve) => setTimeout(resolve, 5))
  return [{ id: 'gpt-fast', supportedApiProtocols: ['chat_completions'] }]
}

const concurrent = await Promise.all([
  cache.getOrLoad('owner:key', load),
  cache.getOrLoad('owner:key', load),
  cache.getOrLoad('owner:key', load)
])
assert.equal(loads, 1, '同一路由身份并发读取必须 single-flight，避免模型列表请求放大')
assert.deepEqual(concurrent[0], concurrent[1])
assert.deepEqual(await cache.getOrLoad('owner:key', load), concurrent[0])
assert.equal(loads, 1, '短 TTL 内重复读取必须命中快照缓存')

now += 5_001
await cache.getOrLoad('owner:key', load)
assert.equal(loads, 2, 'TTL 到期后必须重新加载，以便展示配置变更')

const loadedProviderCodes: string[] = []
const providerModels = await loadChatModelOptionsFromProviderCatalogs({
  bindings: [
    { status: 'active', providerCode: 'gpt' },
    { status: 'active', providerCode: 'gpt' },
    { status: 'disabled', providerCode: 'gemini' }
  ],
  loadCatalog: async (providerCode) => {
    loadedProviderCodes.push(providerCode)
    return [{
      model: 'gpt-fast',
      supportsPromptCaching: true,
      supportedReasoningEfforts: ['medium'],
      defaultReasoningEffort: 'medium',
      supportedServiceTiers: ['default', 'priority', 'flex'],
      supportedApiProtocols: ['chat_completions', 'responses'],
      inputModalities: ['text'],
      outputModalities: ['text'],
      supportedTools: ['web_search']
    }]
  }
})
assert.deepEqual(loadedProviderCodes, ['gpt'], '模型目录只能按 API Key 有效分组的供应商合集读取，不能按账户数放大')
assert.deepEqual(providerModels.map((item) => item.id), ['gpt-fast'], '模型选项必须直接由共享供应商目录构建')
assert.deepEqual(providerModels[0]?.supportedServiceTiers, ['default', 'priority'], 'GPT 稳定目录能力不得暴露依赖 API Key 账户类型的 flex 档位')

console.log('chat model availability regression passed')
