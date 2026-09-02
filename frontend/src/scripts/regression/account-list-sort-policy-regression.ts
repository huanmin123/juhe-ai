import assert from 'node:assert/strict'

import { buildAccountTableColumns, normalizeAccountTableSortParams, normalizeAccountTableSorts } from '@/views/accounts/accountTableColumns'
import { accountExportFiltersFromState } from '@/views/accounts/accountExportHelpers'

const columns = buildAccountTableColumns(false, () => null)
for (const key of ['name', 'type', 'providerCode', 'concurrency']) {
  const column = columns.find((item) => item.key === key)
  assert(column, `必须存在 ${key} 列`)
  assert.equal(Object.hasOwn(column, 'sorter'), false, `${key} 列不得显示或接受表格排序`)
}

const normalized = normalizeAccountTableSortParams([
  { field: 'lastUsedAt', order: 'desc' },
  { field: 'status', order: 'desc' },
  { field: 'concurrency', order: 'asc' },
  { field: 'priority', order: 'desc' }
])
assert.deepEqual(normalized, [
  { field: 'priority', order: 'desc' },
  { field: 'status', order: 'desc' },
  { field: 'lastUsedAt', order: 'desc' }
], '反向缓存排序必须固定为优先级、状态和其余合法字段')
assert.deepEqual(
  normalizeAccountTableSortParams([{ field: 'concurrency', order: 'asc' }]),
  [{ field: 'priority', order: 'asc' }],
  '旧并发数缓存必须被过滤并回落到默认优先级排序'
)
const clickedSorts = normalizeAccountTableSorts([
  { columnKey: 'status', order: 'descend', priority: 2 }
])
assert.deepEqual(
  clickedSorts,
  [
    { field: 'priority', order: 'asc' },
    { field: 'status', order: 'desc' }
  ],
  '点击表格排序后，排序状态只能保留后端契约中的 field/order'
)
assert.deepEqual(
  accountExportFiltersFromState({
    keyword: '',
    providerCode: 'all',
    type: 'all',
    groupId: '',
    tagIds: [],
    status: []
  }, clickedSorts).sorts,
  [
    { field: 'priority', order: 'asc' },
    { field: 'status', order: 'desc' }
  ],
  '排序后导出 payload 不得携带前端 priority 元数据'
)
const statusColumn = columns.find((item) => item.key === 'status')
assert(statusColumn, '必须存在状态列')
assert.equal(Object.hasOwn(statusColumn, 'sorter'), true, '状态列必须提供表格排序')

console.log('账户列表排序策略回归通过：状态可排序、并发数不可排序，缓存固定为优先级优先、状态次级')
