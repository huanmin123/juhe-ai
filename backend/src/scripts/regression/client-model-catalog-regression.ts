import assert from 'node:assert/strict'

import type { ProviderDefinition } from '../../domain/types.js'
import type { ProviderModelCatalogItem } from '../../modules/model-pricing/model-catalog.service.js'
import {
  resolveClientModelCatalogProviderCodes,
  selectClientModelCatalog
} from '../../modules/model-pricing/client-model-catalog.service.js'

assert.deepEqual(
  resolveClientModelCatalogProviderCodes([
    provider('gpt', true),
    provider('glm', false),
    provider('hybrid', true),
    provider('gemini', true)
  ]),
  ['gemini', 'gpt'],
  '公开目录必须包含全部启用真实供应商，排除停用供应商和 hybrid 虚拟供应商'
)
assert.deepEqual(
  resolveClientModelCatalogProviderCodes([], []),
  [],
  '认证 API Key 没有有效供应商绑定时必须返回空作用域，不得回退公开全量'
)
assert.deepEqual(
  resolveClientModelCatalogProviderCodes([], [' hybrid ', 'gpt', 'gpt']),
  ['gpt', 'hybrid'],
  '认证作用域必须去重并保留显式 hybrid 目录，以便复用其真实供应商聚合语义'
)

const selected = selectClientModelCatalog([
  model('current-model', { releaseDate: '2026-07-22' }),
  model('old-model', { releaseDate: '2020-01-01' }),
  model('missing-release-date'),
  model('hidden-current', { releaseDate: '2026-01-01', catalogVisible: false }),
  model('disabled-current', { releaseDate: '2026-01-01', status: 'disabled' }),
  model('unpriced-current', { releaseDate: '2026-01-01', inputUsdPer1M: undefined }),
  model('old-custom', { scope: 'global', source: 'custom-global', releaseDate: '2020-01-01' }),
  model('shared-model', { providerCode: 'gpt', releaseDate: '2026-01-01' }),
  model('shared-model', { providerCode: 'glm', scope: 'global', source: 'custom-global', releaseDate: '2025-01-01' }),
  model('shared-model', { providerCode: 'gemini', scope: 'personal', source: 'custom-personal', releaseDate: '2024-01-01' })
])

const selectedIds = selected.map((item) => item.model)
assert(selectedIds.includes('current-model'), '当前模型必须保留')
assert(selectedIds.includes('old-model'), '旧模型也必须按最终需求保留')
assert(selectedIds.includes('missing-release-date'), '缺少发布时间的可用模型不得被误删')
assert.equal(selectedIds.includes('hidden-current'), false, '隐藏模型不得发布')
assert.equal(selectedIds.includes('disabled-current'), false, '停用模型不得发布')
assert.equal(selectedIds.includes('unpriced-current'), false, '无价模型不得发布')
assert(selectedIds.includes('old-custom'), '旧的自定义模型也必须保留')
assert.equal(selected.filter((item) => item.model === 'shared-model').length, 1, '跨供应商重复模型 ID 必须去重')
assert.equal(selected.find((item) => item.model === 'shared-model')?.scope, 'personal', '重复模型必须优先个人自定义事实')

console.log('client-model-catalog-regression passed')

function provider(code: string, enabled: boolean): ProviderDefinition {
  return { code, enabled } as ProviderDefinition
}

function model(
  modelId: string,
  overrides: Partial<ProviderModelCatalogItem> = {}
): ProviderModelCatalogItem {
  return {
    providerCode: 'gpt',
    model: modelId,
    scope: 'built_in',
    status: 'active',
    releaseDate: undefined,
    supportedApiProtocols: ['responses'],
    inputModalities: ['text'],
    outputModalities: ['text'],
    supportedTools: [],
    inputUsdPer1M: 1,
    supportsPromptCaching: false,
    supportedServiceTiers: [],
    supportedReasoningEfforts: [],
    defaultReasoningEffort: null,
    codexSupportedReasoningLevels: [],
    supportsServiceTier: false,
    source: 'built-in',
    ...overrides
  }
}
