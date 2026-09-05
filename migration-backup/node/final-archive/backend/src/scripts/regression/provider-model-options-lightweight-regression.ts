import assert from 'node:assert/strict'

import {
  mergeProviderModelOptionRows,
  normalizeProviderModelOptionQuery,
  type ProviderModelOptionRow
} from '../../modules/providers/provider-model-options.service.js'

const query = normalizeProviderModelOptionQuery({
  providerCode: ' openai ',
  keyword: ' gpt ',
  limit: '3',
  selectedIds: ['gpt-selected', ' gpt-selected ', 'gpt-missing']
})

assert.deepEqual(query, {
  providerCode: 'openai',
  keyword: 'gpt',
  limit: 3,
  selectedIds: ['gpt-selected', 'gpt-missing']
})

assert.throws(
  () => normalizeProviderModelOptionQuery({ limit: '0' }),
  /limit/,
  'limit 小于 1 时必须拒绝，而不是退回无界列表'
)
assert.throws(
  () => normalizeProviderModelOptionQuery({ limit: '51' }),
  /limit/,
  'limit 超过 50 时必须拒绝，而不是放大查询窗口'
)

const rows: ProviderModelOptionRow[] = [
  optionRow('builtin-selected', 'openai', 'gpt-selected', 'built_in', '2024-01-01'),
  optionRow('global-selected', 'openai', 'gpt-selected', 'global', '2024-01-01'),
  optionRow('personal-selected', 'openai', 'gpt-selected', 'personal', '2024-01-01'),
  optionRow('builtin-alpha', 'openai', 'gpt-alpha', 'built_in', '2025-01-01'),
  {
    ...optionRow('builtin-beta', 'openai', 'gpt-beta', 'built_in', '2026-01-01'),
    supportedApiProtocols: ['responses'],
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['high']
  },
  optionRow('builtin-unknown', 'openai', 'gpt-unknown', 'built_in'),
  optionRow('builtin-invalid', 'openai', 'gpt-invalid-date', 'built_in', 'not-a-date'),
  optionRow('anthropic-alpha', 'anthropic', 'claude-alpha', 'built_in', '2025-06-01')
]

assert.deepEqual(
  mergeProviderModelOptionRows(rows.filter((row) => row.providerCode === 'openai'), {
    providerCode: 'openai',
    keyword: 'gpt',
    limit: 2,
    selectedIds: ['gpt-selected']
  }),
  [
    {
      id: 'gpt-beta',
      name: 'gpt-beta',
      supportedApiProtocols: ['responses'],
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    },
    {
      id: 'gpt-alpha',
      name: 'gpt-alpha',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    },
    {
      id: 'gpt-selected',
      name: 'gpt-selected',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  ],
  '单供应商选项必须补齐已选项，同时保持发布时间倒序并只返回选择所需标识与小型能力字段'
)

assert.deepEqual(
  mergeProviderModelOptionRows(rows, {
    keyword: 'alpha',
    limit: 10,
    selectedIds: []
  }),
  [
    {
      id: 'claude-alpha',
      name: 'claude-alpha',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    },
    {
      id: 'gpt-alpha',
      name: 'gpt-alpha',
      supportedApiProtocols: [],
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  ],
  '跨供应商选项也必须按模型 ID 合并并保持窄能力 DTO'
)

assert.deepEqual(
  mergeProviderModelOptionRows(rows.filter((row) => row.providerCode === 'openai'), {
    providerCode: 'openai',
    limit: 10,
    selectedIds: []
  }).map((item) => item.id),
  ['gpt-beta', 'gpt-alpha', 'gpt-selected', 'gpt-invalid-date', 'gpt-unknown'],
  '无发布时间或发布时间非法的模型必须排在有发布时间的模型之后，并按模型 ID 稳定排序'
)

console.log('供应商模型轻量选项回归通过：发布时间倒序、未知日期兜底、已选补齐和窄能力 DTO 均符合预期')

function optionRow(
  id: string,
  providerCode: string,
  model: string,
  scope: ProviderModelOptionRow['scope'],
  releaseDate?: string
): ProviderModelOptionRow {
  return { id, providerCode, model, scope, releaseDate }
}
