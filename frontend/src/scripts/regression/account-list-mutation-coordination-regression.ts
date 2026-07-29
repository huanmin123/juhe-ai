import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountListItem } from '@/types/domain'
import { filterAccounts } from '@/views/accounts/accountListFilters'
import {
  accountListSortChanged,
  replaceAccountListRow,
  sortAccountListRows
} from '@/views/accounts/accountListMutations'

const fixture = (input: Partial<AccountListItem> & Pick<AccountListItem, 'id' | 'name'>): AccountListItem => ({
  id: input.id,
  configRevision: input.configRevision ?? 1,
  providerCode: input.providerCode ?? 'gpt',
  name: input.name,
  type: input.type ?? 'api_key',
  status: input.status ?? 'active',
  concurrencyLimit: input.concurrencyLimit ?? 20,
  currentConcurrency: input.currentConcurrency ?? 0,
  priority: input.priority ?? 50,
  superPriorityEnabled: input.superPriorityEnabled ?? false,
  fallbackEnabled: input.fallbackEnabled ?? false,
  clientCompatibility: input.clientCompatibility ?? 'openai_standard',
  healthCheckModel: input.healthCheckModel ?? 'gpt-4o-mini',
  healthCheckEndpointMode: input.healthCheckEndpointMode ?? 'responses',
  schedulable: input.schedulable ?? true,
  todayUsage: input.todayUsage ?? { requestCount: 0, totalTokens: 0, totalCost: 0 },
  ...input
})

const original = fixture({ id: 'account-a', name: 'Alpha', configRevision: 4, priority: 20 })
const stale = fixture({ id: 'account-a', name: 'Stale', configRevision: 3, priority: 1 })
const currentRows = [original, fixture({ id: 'account-b', name: 'Beta', priority: 10 })]
assert.equal(replaceAccountListRow(currentRows, stale), currentRows, '旧 PATCH/GET revision 不得覆盖当前账户行')

const updated = fixture({ id: 'account-a', name: 'Alpha', configRevision: 5, priority: 5 })
const replaced = replaceAccountListRow(currentRows, updated)
assert.equal(accountListSortChanged(original, updated, [{ field: 'priority', order: 'asc' }]), true)
assert.deepEqual(
  sortAccountListRows(replaced, [{ field: 'priority', order: 'asc' }]).map((account) => account.id),
  ['account-a', 'account-b'],
  'PATCH 改动当前排序字段后必须立即重排已加载窗口'
)
const sourceBlocked = fixture({
  id: 'account-authorized',
  name: 'Authorized',
  accessType: 'authorized',
  runtimeAvailability: { available: true },
  effectiveAvailability: { available: false, status: 'source_disabled', blockerScope: 'source_account' }
})
const sourceRecovered = fixture({
  ...sourceBlocked,
  runtimeAvailability: { available: true },
  effectiveAvailability: { available: true, status: 'available' }
})
assert.equal(
  replaceAccountListRow([sourceBlocked], sourceRecovered)[0]?.effectiveAvailability?.available,
  true,
  '来源账户恢复后，定点刷新不得保留旧来源阻断派生状态'
)
assert.deepEqual(
  sortAccountListRows([
    fixture({ id: 'account-never-used', name: 'Never used' }),
    fixture({ id: 'account-recent', name: 'Recent', lastUsedAt: '2026-07-29T10:00:00.000Z' })
  ], [{ field: 'lastUsedAt', order: 'desc' }]).map((account) => account.id),
  ['account-recent', 'account-never-used'],
  '最近使用时间无论升降序都必须保持空值在末尾'
)

const filteredOut = fixture({ ...updated, status: 'disabled' })
assert.equal(filterAccounts({
  accounts: [filteredOut],
  filters: {
    keyword: '',
    providerCode: 'all',
    type: 'all',
    groupId: '',
    tagIds: [],
    status: ['active'],
    systemAccountId: 'all'
  },
  isManagementView: false
}).length, 0, 'PATCH 后不再满足当前筛选的账户必须移出本地页')

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const listDataSource = fs.readFileSync(path.resolve(scriptDir, '../../views/accounts/useAccountListData.ts'), 'utf8')
assert.match(
  listDataSource,
  /get superseded\(\) \{ return requestMutationRevision !== listMutationRevision \}/,
  '列表 GET 应在真正提交响应时检查本地写入代次'
)
assert.match(
  listDataSource,
  /listMutationRevision \+= 1[\s\S]*if \(!matchesCurrentFilters\)[\s\S]*removeAccountItems/,
  '本地 PATCH 应先推进写入代次，再按当前筛选维护分页窗口'
)
assert.match(
  listDataSource,
  /if \(!matchesCurrentFilters \|\| sortChanged\)[\s\S]*loadData\(\{ forceData: true, quiet: true, requestIdentity: listMutationRevision \}\)/,
  '只有筛选成员或分页排序边界变化时才应按最新写入代次静默校准权威页'
)

console.log('account list mutation coordination regression passed')
