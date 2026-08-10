import assert from 'node:assert/strict'

import { buildAccountTableColumns, normalizeAccountTableSortParams } from '@/views/accounts/accountTableColumns'

const columns = buildAccountTableColumns(false, () => null)
for (const key of ['name', 'type', 'providerCode', 'status']) {
  const column = columns.find((item) => item.key === key)
  assert(column, `必须存在 ${key} 列`)
  assert.equal(Object.hasOwn(column, 'sorter'), false, `${key} 列不得显示或接受表格排序`)
}

const normalized = normalizeAccountTableSortParams([
  { field: 'name', order: 'asc' },
  { field: 'status', order: 'desc' },
  { field: 'priority', order: 'desc' }
])
assert.deepEqual(normalized, [{ field: 'priority', order: 'desc' }], '旧页面缓存中的禁用排序必须被移除')
assert.deepEqual(
  normalizeAccountTableSortParams([{ field: 'providerCode', order: 'asc' }]),
  [{ field: 'priority', order: 'asc' }],
  '只包含禁用排序时必须回落到默认优先级排序'
)

console.log('账户列表排序策略回归通过：名称、类型、供应商和状态不可排序，旧缓存会回落到优先级排序')
