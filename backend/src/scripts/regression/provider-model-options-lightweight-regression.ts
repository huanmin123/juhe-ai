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
  optionRow('builtin-selected', 'openai', 'gpt-selected', 'built_in'),
  optionRow('global-selected', 'openai', 'gpt-selected', 'global'),
  optionRow('personal-selected', 'openai', 'gpt-selected', 'personal'),
  optionRow('builtin-alpha', 'openai', 'gpt-alpha', 'built_in'),
  optionRow('builtin-beta', 'openai', 'gpt-beta', 'built_in'),
  optionRow('anthropic-alpha', 'anthropic', 'claude-alpha', 'built_in')
]

assert.deepEqual(
  mergeProviderModelOptionRows(rows.filter((row) => row.providerCode === 'openai'), {
    providerCode: 'openai',
    keyword: 'gpt',
    limit: 2,
    selectedIds: ['gpt-selected']
  }),
  [
    { id: 'gpt-selected', name: 'gpt-selected' },
    { id: 'gpt-alpha', name: 'gpt-alpha' },
    { id: 'gpt-beta', name: 'gpt-beta' }
  ],
  '单供应商选项必须优先补齐已选项，并只返回 id/name'
)

assert.deepEqual(
  mergeProviderModelOptionRows(rows, {
    keyword: 'alpha',
    limit: 10,
    selectedIds: []
  }),
  [
    { id: 'claude-alpha', name: 'claude-alpha' },
    { id: 'gpt-alpha', name: 'gpt-alpha' }
  ],
  '跨供应商选项也必须按模型 ID 合并并只返回 id/name'
)

console.log('供应商模型轻量选项回归通过：查询窗口、已选补齐、供应商维度和精确 DTO 均符合预期')

function optionRow(
  id: string,
  providerCode: string,
  model: string,
  scope: ProviderModelOptionRow['scope']
): ProviderModelOptionRow {
  return { id, providerCode, model, scope }
}
