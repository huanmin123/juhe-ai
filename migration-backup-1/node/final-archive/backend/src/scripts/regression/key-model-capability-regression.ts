import assert from 'node:assert/strict'

import { resolveGatewayKeyModelCapability } from '../../modules/gateway/runtime/key-model-capability.js'

const account = {
  id: 'account-1', credentialSourceAccountId: 'source-1', selectedApiKeyFingerprint: 'key-a', dispatchRevision: 9,
  providerCode: 'hybrid', providerProtocolProfileId: 'profile_hybrid_openai_chat_v1', protocolCode: 'openai', protocolVersion: 'v1',
  healthCheckModel: 'A', healthCheckEndpointMode: 'chat_json', modelMappings: [{ sourceModel: 'B', sourceEndpointFamily: 'chat_completions', upstreamModel: 'B-upstream', upstreamEndpointFamily: 'messages', enabled: true }]
}
const request = { method: 'POST', originalUrl: '/v1/chat/completions', path: '/v1/chat/completions', body: { model: 'B', stream: false } }
const routed = resolveGatewayKeyModelCapability(request as never, account as never)!
assert.equal(routed.capability.credentialSourceAccountId, 'source-1')
assert.equal(routed.capability.finalUpstreamModel, 'B-upstream')
assert.equal(routed.capability.upstreamEndpointMode, 'messages_json')
assert.equal(routed.isMainProbe, false)
const main = resolveGatewayKeyModelCapability({ ...request, body: { model: 'A', stream: false } } as never, { ...account, modelMappings: [] } as never)!
assert.equal(main.isMainProbe, true)
const nonMainEndpoint = resolveGatewayKeyModelCapability({ ...request, originalUrl: '/v1/responses', path: '/v1/responses', body: { model: 'A', stream: false } } as never, { ...account, modelMappings: [] } as never)!
assert.equal(nonMainEndpoint.isMainProbe, false, '同模型不同入口不能被误判为主探测')
assert.equal(resolveGatewayKeyModelCapability(request as never, { ...account, selectedApiKeyFingerprint: undefined } as never), undefined, '没有物理 Key 时不得建 state')
console.log('key-model-capability regression passed')
