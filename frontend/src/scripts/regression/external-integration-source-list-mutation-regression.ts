import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { ExternalIntegrationSourceListItem } from '@/types/domain'
import {
  matchesExternalSourceFilters,
  reconcileCreatedExternalSource,
  reconcileDeletedExternalSource,
  reconcilePatchedExternalSource,
  type ExternalSourceListMutationContext
} from '@/views/external-integration-sources/externalSourceListMutation'

function source(id: string, name: string, updatedAt: string, status: 'active' | 'disabled' = 'active'): ExternalIntegrationSourceListItem {
  return {
    id,
    name,
    status,
    scopes: [],
    rateLimits: [],
    updatedAt,
    isBuiltIn: false
  }
}

function context(overrides: Partial<ExternalSourceListMutationContext> = {}): ExternalSourceListMutationContext {
  return {
    accumulated: false,
    hasMore: false,
    keyword: '',
    page: 1,
    pageSize: 2,
    pageUpperBound: 2,
    status: 'all',
    ...overrides
  }
}

const older = source('extsrc_older', 'Older', '2026-07-29T10:00:00.000Z')
const oldest = source('extsrc_oldest', 'Oldest', '2026-07-29T09:00:00.000Z')
const created = source('extsrc_created', 'Created source', '2026-07-29T11:00:00.000Z')

const firstPageCreate = reconcileCreatedExternalSource([older, oldest], created, context())
assert.deepEqual(firstPageCreate.items.map((item) => item.id), ['extsrc_created', 'extsrc_older'])
assert.equal(firstPageCreate.hasMore, true, '已满且原本全部加载的首屏新增后必须保留下一页哨兵')
assert.equal(firstPageCreate.pageUpperBound, 3)
assert.equal(firstPageCreate.requiresReload, false, '首屏新增可由窄列表行直接本地协调')

const filteredOutCreate = reconcileCreatedExternalSource(
  [older],
  source('extsrc_disabled', 'Created source', '2026-07-29T11:00:00.000Z', 'disabled'),
  context({ pageUpperBound: 1, status: 'active' })
)
assert.equal(filteredOutCreate.items[0], older, '不命中当前状态筛选的新增不得污染列表')
assert.equal(filteredOutCreate.pageUpperBound, 1)
assert.equal(filteredOutCreate.requiresReload, false)
assert.equal(matchesExternalSourceFilters(created, context({ keyword: 'created' })), true, '名称筛选应与服务端前缀语义一致')
assert.equal(matchesExternalSourceFilters(created, context({ keyword: 'source' })), false, '名称筛选不得退化为包含匹配')

const laterPageCreate = reconcileCreatedExternalSource([older, oldest], created, context({ page: 2, pageUpperBound: 4 }))
assert.equal(laterPageCreate.requiresReload, true, '非累计的非首屏新增会平移分页窗口，必须定点刷新')

const accumulatedCreate = reconcileCreatedExternalSource(
  [source('a', 'A', '2026-07-29T10:00:00.000Z'), source('b', 'B', '2026-07-29T09:00:00.000Z'), source('c', 'C', '2026-07-29T08:00:00.000Z')],
  created,
  context({ accumulated: true, page: 2, pageUpperBound: 3 })
)
assert.deepEqual(accumulatedCreate.items.map((item) => item.id), ['extsrc_created', 'a', 'b', 'c'])
assert.equal(accumulatedCreate.requiresReload, false, '累计移动端窗口应本地插入并保留已加载页')

const localDelete = reconcileDeletedExternalSource([older, oldest], older.id, context())
assert.deepEqual(localDelete.items, [oldest])
assert.equal(localDelete.pageUpperBound, 1)
assert.equal(localDelete.requiresReload, false, '全部数据已加载时删除应零列表重拉')

const refillDelete = reconcileDeletedExternalSource([older, oldest], older.id, context({ hasMore: true, pageUpperBound: 3 }))
assert.equal(refillDelete.requiresReload, true, '存在未加载后继行时才允许定点刷新以补齐窗口')

const missingDelete = reconcileDeletedExternalSource([oldest], older.id, context({ pageUpperBound: 1 }))
assert.equal(missingDelete.requiresReload, true, '同查询参数刷新后目标行已离开当前窗口时，删除仍可能平移 offset，必须定点刷新')

const previousPageDelete = reconcileDeletedExternalSource([oldest], oldest.id, context({ page: 2, pageUpperBound: 3 }))
assert.equal(previousPageDelete.page, 1)
assert.equal(previousPageDelete.requiresReload, true, '删除非首屏最后一行必须回退并刷新有效页')

const reorderedPatch = reconcilePatchedExternalSource(
  [older, oldest],
  { ...oldest, updatedAt: '2026-07-29T12:00:00.000Z' },
  context()
)
assert.deepEqual(reorderedPatch.items.map((item) => item.id), ['extsrc_oldest', 'extsrc_older'], 'PATCH 推进版本后必须恢复服务端 updatedAt DESC 排序')
assert.equal(reorderedPatch.requiresReload, false)

const filteredPatch = reconcilePatchedExternalSource(
  [older, oldest],
  { ...older, status: 'disabled', updatedAt: '2026-07-29T12:00:00.000Z' },
  context({ status: 'active' })
)
assert.deepEqual(filteredPatch.items, [oldest], 'PATCH 后不匹配筛选的行必须立即移出当前列表')
assert.equal(filteredPatch.pageUpperBound, 1)
assert.equal(filteredPatch.requiresReload, false)

const laterPagePatch = reconcilePatchedExternalSource(
  [older, oldest],
  { ...oldest, updatedAt: '2026-07-29T12:00:00.000Z' },
  context({ page: 2, pageUpperBound: 4 })
)
assert.equal(laterPagePatch.requiresReload, true, '非累计后续页的 PATCH 会跨页移动，必须定点刷新')

const viewSource = readFileSync(fileURLToPath(new URL('../../views/external-integration-sources/ExternalIntegrationSourcesView.vue', import.meta.url)), 'utf8')
assert.match(
  viewSource,
  /const requestMutationRevision = listMutationRevision[\s\S]*requestMutationRevision === listMutationRevision/,
  '列表 GET 必须在提交响应时检查本地写入代次，避免旧 GET 覆盖写后状态'
)
const createBranch = viewSource.slice(viewSource.indexOf('const result = await api.externalIntegrationSources.create'), viewSource.indexOf('} catch (error)', viewSource.indexOf('const result = await api.externalIntegrationSources.create')))
assert.match(createBranch, /reconcileCreatedExternalSource/)
assert.match(createBranch, /if \(contextChanged \|\| !result\.item \|\| reconciliation\?\.requiresReload\) await loadData\(\)/, 'Node 窄 item 正常路径不得刷新；旧 owner 缺少 item 时必须安全回退')
const deleteFunction = viewSource.slice(viewSource.indexOf('async function deleteSource'), viewSource.indexOf('function sourceNotes'))
assert.match(deleteFunction, /reconcileDeletedExternalSource/)
assert.match(deleteFunction, /const mutationSignature = currentListSignature\(\)[\s\S]*contextChanged = mutationSignature !== currentListSignature\(\)/, '写入期间切换筛选或分页必须被识别')
assert.match(deleteFunction, /if \(contextChanged \|\| reconciliation\?\.requiresReload\) await loadData\(\)/, '删除只能在上下文变化或分页边界无法证明时定点刷新')

console.log('external integration source list mutation regression passed')
