import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountListItem } from '@/types/domain'
import type { AccountFilters } from '@/views/accounts/accountFormTypes'
import { filterAccounts } from '@/views/accounts/accountListFilters'
import {
  accountListHasAccumulatedPageWindow,
  accountListPageWindowChanged,
  accountListSortChanged,
  mergeAccountListPageWithRevisionOverlays,
  replaceAccountListRow,
  sortAccountListRows,
  type AccountListRevisionOverlay
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
  tags: input.tags ?? [],
  todayUsage: input.todayUsage ?? { requestCount: 0, totalTokens: 0, totalCost: 0 },
  ...input
})

const original = fixture({ id: 'account-a', name: 'Alpha', configRevision: 4, priority: 20 })
const stale = fixture({ id: 'account-a', name: 'Stale', configRevision: 3, priority: 1 })
const currentRows = [original, fixture({ id: 'account-b', name: 'Beta', priority: 10 })]
assert.equal(replaceAccountListRow(currentRows, stale), currentRows, '旧 PATCH/GET revision 不得覆盖当前账户行')
assert.equal(accountListHasAccumulatedPageWindow(40, 2, 20), true, '移动端累计加载窗口必须被识别')
assert.equal(accountListHasAccumulatedPageWindow(20, 2, 20), false, '桌面端普通第 2 页不得误判为累计窗口')

const overlays = new Map<string, AccountListRevisionOverlay>([
  [original.id, { configRevision: 4, row: original }]
])
assert.equal(
  mergeAccountListPageWithRevisionOverlays([stale], currentRows, overlays)[0],
  original,
  '整页 GET 也不得覆盖更高 configRevision 的 PATCH overlay'
)
assert.equal(overlays.size, 1, '服务端追上 revision 前必须保留 overlay')
const caughtUp = fixture({ id: 'account-a', name: 'Server current', configRevision: 4 })
assert.equal(mergeAccountListPageWithRevisionOverlays([caughtUp], currentRows, overlays)[0], caughtUp)
assert.equal(overlays.size, 0, '服务端追上 revision 后应释放 overlay')

const updated = fixture({ id: 'account-a', name: 'Alpha', configRevision: 5, priority: 5 })
assert.equal(accountListSortChanged(original, updated, [{ field: 'priority', order: 'asc' }]), true)
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
  '来源账户恢复后，权威刷新不得保留旧来源阻断派生状态'
)
assert.deepEqual(
  sortAccountListRows([
    fixture({ id: 'account-never-used', name: 'Never used' }),
    fixture({ id: 'account-recent', name: 'Recent', lastUsedAt: '2026-07-29T10:00:00.000Z' })
  ], [{ field: 'lastUsedAt', order: 'desc' }]).map((account) => account.id),
  ['account-recent', 'account-never-used'],
  '最近使用时间无论升降序都必须保持空值在末尾'
)

const filters: AccountFilters = {
  keyword: '',
  providerCode: 'all',
  type: 'all',
  groupId: '',
  group: undefined,
  tagIds: [],
  status: [],
  systemAccountId: 'all',
  systemAccount: undefined
}
assert.equal(accountListPageWindowChanged(original, fixture({ ...original, notes: 'display-only' }), {
  filters,
  isManagementView: false,
  sorts: [{ field: 'priority', order: 'asc' }]
}), false, '非筛选/排序字段仍应只做局部行合并')
assert.equal(accountListPageWindowChanged(original, updated, {
  filters,
  isManagementView: false,
  sorts: [{ field: 'priority', order: 'asc' }]
}), true, '排序字段变化必须重建服务端分页窗口')
assert.equal(accountListPageWindowChanged(original, fixture({ ...original, name: 'Renamed' }), {
  filters: { ...filters, keyword: 'alpha' },
  isManagementView: false,
  sorts: [{ field: 'priority', order: 'asc' }]
}), true, '活动筛选字段变化必须重建服务端分页窗口')

const filteredOut = fixture({ ...updated, status: 'disabled' })
assert.equal(filterAccounts({
  accounts: [filteredOut],
  filters: { ...filters, status: ['active'] },
  isManagementView: false
}).length, 0, '状态筛选辅助语义仍需识别 PATCH 后已不匹配的账户')

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const listDataSource = fs.readFileSync(path.resolve(scriptDir, '../../views/accounts/useAccountListData.ts'), 'utf8')
const accountsViewSource = fs.readFileSync(path.resolve(scriptDir, '../../views/accounts/AccountsView.vue'), 'utf8')
const accountsApiSource = fs.readFileSync(path.resolve(scriptDir, '../../api/domains/accounts.ts'), 'utf8')
assert.match(
  listDataSource,
  /get superseded\(\) \{\s*return requestAuthRevision !== authState\.revision\.value\s*\|\| requestMutationRevision !== listMutationRevision/,
  '列表 GET 应在真正提交响应时检查登录与本地写入代次'
)
assert.match(
  listDataSource,
  /accountListHasAccumulatedPageWindow\([\s\S]{0,160}resetAccountPagination\(\)[\s\S]{0,120}loadData\(\{ forceData: true, quiet: true, requestIdentity: listMutationRevision \}\)/,
  '移动端累计窗口 mutation 后必须先回到第 1 页再做权威刷新'
)
assert.match(listDataSource, /transformItems:[\s\S]{0,180}mergeAccountListPageWithRevisionOverlays/)
assert.match(
  listDataSource,
  /listRequestController\?\.abort\(\)[\s\S]{0,160}new AbortController\(\)[\s\S]{0,260}signal: controller\.signal/,
  '账户列表发起新查询时必须取消旧 HTTP 请求，并把新 AbortSignal 传到 API'
)
assert.match(
  listDataSource,
  /onError: \(error\) => \{\s*if \(isAbortError\(error\)\) return/,
  '用户切换筛选取消旧列表请求时不得显示加载失败'
)
assert.match(
  accountsApiSource,
  /list: \(params\?: AccountListParams, options\?: RequestControlOptions\)[\s\S]{0,180}signal: options\?\.signal/,
  '管理端账户列表 API 必须支持 AbortSignal'
)
assert.match(
  accountsApiSource,
  /myAccountsApi = \{[\s\S]{0,220}list: \(params\?: AccountListParams, options\?: RequestControlOptions\)[\s\S]{0,180}signal: options\?\.signal/,
  '个人账户列表 API 必须支持 AbortSignal'
)
assert.doesNotMatch(
  listDataSource,
  /accounts\.value = sortAccountListRows\(nextAccounts/,
  '影响服务端分页窗口的 PATCH 不得只在当前页复刻服务端排序'
)
assert.match(
  accountsViewSource,
  /markAccountMutation\(mutation\)[\s\S]{0,180}mutation\.authorizationInstancesAffected[\s\S]{0,120}await reloadAccountPageAfterMutation\(\)[\s\S]{0,40}return/,
  'mutation receipt 必须先推进 generation；来源账户影响授权实例时必须整页协调'
)
assert.doesNotMatch(
  accountsViewSource,
  /authorizationInstancesAffected[\s\S]{0,250}authorizationInstanceSourceAccountId/,
  '来源账户联动不得只刷新当前已加载授权实例'
)
assert.match(
  accountsViewSource,
  /pageReloadRequired = accountUpdateAffectsPageWindow\(account\)[\s\S]{0,220}if \(pageReloadRequired\) await reloadAccountPageAfterMutation\(\)/,
  '排序或筛选变化必须等待服务端分页窗口刷新'
)

console.log('account list mutation coordination regression passed')
