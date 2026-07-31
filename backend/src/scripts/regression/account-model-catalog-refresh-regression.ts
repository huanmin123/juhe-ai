import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  accountModelCatalogAdditions,
  intersectAccountUpstreamModelCatalogs,
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

assert.deepEqual(
  [...intersectAccountUpstreamModelCatalogs([
    new Set(['gpt-5.6-sol', 'gpt-5.6-terra', 'shared-model']),
    new Set(['gpt-5.6-terra', 'shared-model', 'key-two-only']),
    new Set(['gpt-5.6-terra', 'shared-model', 'key-three-only'])
  ])],
  ['gpt-5.6-terra', 'shared-model'],
  '多 API Key 的模型目录只能保留每把 Key 都可见的模型'
)
assert.deepEqual(
  [...intersectAccountUpstreamModelCatalogs([
    new Set(['gpt-5.6-sol']),
    new Set<string>()
  ])],
  [],
  '任一 API Key 返回空目录时，多 Key 模型交集必须为空'
)

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

const refreshServiceSource = readFileSync(new URL('../../modules/accounts/account-model-catalog-refresh.service.ts', import.meta.url), 'utf8')
const accountsRoutesSource = readFileSync(new URL('../../modules/accounts/accounts.routes.ts', import.meta.url), 'utf8')
assert.match(refreshServiceSource, /signal\?: AbortSignal[\s\S]*openAIDraftAccountSecret\(input\.draftAccount, input\.signal/, '模型目录同步必须把调用方取消信号传到草稿凭据处理')
assert.match(refreshServiceSource, /for \(const entry of entries\) \{\s*throwIfAborted\(signal\)[\s\S]*discoverDraftCandidateUpstreamModelIds\(account, fixedCandidate, signal\)/, '多 Key 目录同步必须在每把 Key 前检查取消，并把信号传给上游请求')
assert.match(accountsRoutesSource, /req\.once\('aborted'[\s\S]*?refreshAccountDraftModelCatalogAsync\(\{[\s\S]*signal: clientAbortController\.signal/, '客户端中止目录同步请求时，路由必须向服务传递取消信号')

console.log('账户上游模型目录增量同步回归通过：多 Key 仅取全量成功目录交集，保留用户手动选择并按目录顺序回退检查模型')
