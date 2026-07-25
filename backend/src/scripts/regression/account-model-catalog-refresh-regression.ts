import assert from 'node:assert/strict'

import {
  accountModelCatalogAdditions,
  recommendedAccountHealthCheckModel
} from '../../modules/accounts/account-model-catalog-refresh.service.js'

const profile = {
  id: 'profile-openai-v1',
  providerCode: 'openai-compatible',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  endpointFamilies: ['chat_completions']
}

assert.deepEqual(accountModelCatalogAdditions({
  supportedModels: ['manual-keep', 'already-added'],
  upstreamModelIds: new Set(['already-added', 'new-model', 'not-in-local-catalog']),
  localModels: [
    { model: 'already-added', supportedApiProtocols: ['chat_completions'] },
    { model: 'new-model', supportedApiProtocols: ['chat_completions'] }
  ],
  profile
}), ['new-model'], '模型目录同步只能追加本地允许且上游可见、尚未选择的模型')

assert.deepEqual(accountModelCatalogAdditions({
  supportedModels: ['manual-keep'],
  upstreamModelIds: new Set(),
  localModels: [{ model: 'manual-keep', supportedApiProtocols: ['chat_completions'] }],
  profile
}), [], '上游目录缺少人工选择时不得返回删除项或覆盖当前选择')

const healthCheckCandidates = [
  { model: 'gpt-5.6-sol', supportedApiProtocols: ['chat_completions'] },
  { model: 'gpt-5.6-terra', supportedApiProtocols: ['chat_completions'] },
  { model: 'gpt-5.6-luna', supportedApiProtocols: ['chat_completions'] },
  { model: 'gpt-5.5', supportedApiProtocols: ['chat_completions'] }
]
assert.equal(recommendedAccountHealthCheckModel({
  configuredHealthCheckModel: 'gpt-5.6-sol',
  upstreamModelIds: new Set(['gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5']),
  testModels: healthCheckCandidates,
  profile
}), 'gpt-5.6-terra', '当前检查模型不在上游目录时必须按已排序的发布时间候选回退')
assert.equal(recommendedAccountHealthCheckModel({
  configuredHealthCheckModel: 'gpt-5.6-sol',
  upstreamModelIds: new Set(['gpt-5.6-sol', 'gpt-5.6-terra']),
  testModels: healthCheckCandidates,
  profile
}), 'gpt-5.6-sol', '当前检查模型仍在上游目录时必须保留用户选择')

console.log('账户上游模型目录增量同步回归通过：仅追加交集模型，保留用户手动选择并按目录顺序回退检查模型')
