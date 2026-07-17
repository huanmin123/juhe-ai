import assert from 'node:assert/strict'

import {
  createChatModelOptionsSnapshotCache,
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

console.log('chat model availability regression passed')
