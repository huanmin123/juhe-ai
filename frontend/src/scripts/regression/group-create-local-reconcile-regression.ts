import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { useResponsivePagedList } from '../../composables/useResponsivePagedList'
import type { GroupListItem } from '../../types/domain'
import { reconcileCreatedGroup } from '../../views/groups/groupListMutation'

const first = group('grp_first', '2026-07-29T10:00:00.000Z')
const second = group('grp_second', '2026-07-29T09:00:00.000Z')
const third = group('grp_third', '2026-07-29T08:00:00.000Z')
const fourth = group('grp_fourth', '2026-07-29T07:00:00.000Z')
const created = group('grp_created', '2026-07-29T11:00:00.000Z')

const firstPage = reconcileCreatedGroup([first, second], created, {
  accumulated: false,
  hasMore: true,
  page: 1,
  pageSize: 2,
  total: 3
})
assert.equal(firstPage.requiresReload, false, '桌面第一页创建应直接本地协调')
assert.deepEqual(firstPage.items.map((item) => item.id), ['grp_created', 'grp_first'], '创建行应按 updatedAt/id 固定倒序插入并维持页宽')
assert.equal(firstPage.total, 3, '存在后续页时 total 是分页上界，创建不得把上界误当精确总数累加')
assert.equal(firstPage.hasMore, true, '本地创建不得丢失服务端 hasMore 状态')

const partialFirstPage = reconcileCreatedGroup([first], created, {
  accumulated: false,
  hasMore: false,
  page: 1,
  pageSize: 2,
  total: 1
})
assert.deepEqual(partialFirstPage.items.map((item) => item.id), ['grp_created', 'grp_first'], '未满第一页应增长一个可见行')
assert.equal(partialFirstPage.total, 2, '已加载完整列表时创建应推进精确总数')

const fullFirstPage = reconcileCreatedGroup([first, second], created, {
  accumulated: false,
  hasMore: false,
  page: 1,
  pageSize: 2,
  total: 2
})
assert.equal(fullFirstPage.hasMore, true, '已加载的完整第一页满页时，头部创建应产生新的后续页')
assert.equal(fullFirstPage.total, 3, '满页创建后的分页上界应包含被挤到下一页的记录')

const desktopLaterPage = reconcileCreatedGroup([third, fourth], created, {
  accumulated: false,
  hasMore: false,
  page: 2,
  pageSize: 2,
  total: 4
})
assert.equal(desktopLaterPage.requiresReload, true, '桌面后续页受头部插入偏移影响，必须只在该场景条件刷新')
assert.deepEqual(desktopLaterPage.items, [third, fourth], '条件刷新前不得错误地把新建行插进桌面后续页')

const accumulatedMobile = reconcileCreatedGroup([first, second, third, fourth], created, {
  accumulated: true,
  hasMore: true,
  page: 2,
  pageSize: 2,
  total: 5
})
assert.equal(accumulatedMobile.requiresReload, false, '移动累计窗口创建不得重拉列表')
assert.deepEqual(accumulatedMobile.items.map((item) => item.id), ['grp_created', 'grp_first', 'grp_second', 'grp_third'], '移动累计窗口应插入头部并裁剪到已加载窗口')

const replay = reconcileCreatedGroup([created, first], created, {
  accumulated: false,
  hasMore: false,
  page: 1,
  pageSize: 2,
  total: 2
})
assert.equal(replay.total, 2, '相同创建回执重放不得重复推进总数')
assert.deepEqual(replay.items.map((item) => item.id), ['grp_created', 'grp_first'], '相同创建回执重放不得产生重复行')

let resolveStalePage!: (value: { items: GroupListItem[]; page: number; pageSize: number; total: number; hasMore: boolean }) => void
const stalePage = new Promise<{ items: GroupListItem[]; page: number; pageSize: number; total: number; hasMore: boolean }>((resolve) => {
  resolveStalePage = resolve
})
const list = useResponsivePagedList<GroupListItem>({
  pageSize: 2,
  showTotal: (total) => String(total),
  fetchPage: async () => stalePage
})
const pendingLoad = list.loadData()
list.invalidatePendingLoads()
list.applyResult({ items: [created, first], page: 1, pageSize: 2, total: 2, hasMore: false })
resolveStalePage({ items: [first], page: 1, pageSize: 2, total: 1, hasMore: false })
assert.equal(await pendingLoad, false, '创建落地前失效的旧 GET 不得再应用')
assert.deepEqual(list.items.value.map((item) => item.id), ['grp_created', 'grp_first'], 'stale GET 不得覆盖本地创建结果')

let resolveStaleMobilePage!: (value: { items: GroupListItem[]; page: number; pageSize: number; total: number; hasMore: boolean }) => void
const staleMobilePage = new Promise<{ items: GroupListItem[]; page: number; pageSize: number; total: number; hasMore: boolean }>((resolve) => {
  resolveStaleMobilePage = resolve
})
const mobileList = useResponsivePagedList<GroupListItem>({
  pageSize: 2,
  showTotal: (total) => String(total),
  fetchPage: async () => staleMobilePage
})
mobileList.applyResult({ items: [first, second], page: 1, pageSize: 2, total: 3, hasMore: true })
const pendingMobileLoad = mobileList.loadMoreMobile()
assert.equal(mobileList.pagination.current, 2, '移动加载更多发起后应暂时推进页码')
assert.equal(mobileList.mobileLoadingMore.value, true, '移动加载更多请求应进入进行中状态')
mobileList.invalidatePendingLoads()
mobileList.applyResult({
  items: [created, first, second],
  page: 2,
  pageSize: 2,
  total: 3,
  hasMore: true,
  currentPageCount: 1
})
resolveStaleMobilePage({ items: [third], page: 2, pageSize: 2, total: 3, hasMore: false })
await pendingMobileLoad
assert.equal(mobileList.pagination.current, 2, '被创建回执取代的移动旧请求不得回退已协调的累计页码')
assert.equal(mobileList.mobileLoadingMore.value, false, '取消移动旧请求后必须立即结束加载状态')
assert.deepEqual(mobileList.items.value.map((item) => item.id), ['grp_created', 'grp_first', 'grp_second'], '移动旧请求不得覆盖创建后的累计窗口')

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const createBranchStart = groupsViewSource.indexOf("    } else {\n      const payload = groupCreatePayload()")
const createBranchEnd = groupsViewSource.indexOf('    }\n    modalOpen.value = false', createBranchStart)
assert.notEqual(createBranchStart, -1)
assert.notEqual(createBranchEnd, -1)
const createBranch = groupsViewSource.slice(createBranchStart, createBranchEnd)
assert.match(createBranch, /const createScopeKey = groupListScopeKey\(\)[\s\S]*if \(createScopeKey === groupListScopeKey\(\)\)/, '创建结果只能写入发起请求时的相同账户作用域')
assert.match(createBranch, /invalidatePendingLoads\(\)[\s\S]*reconcileCreatedGroup/, '创建本地协调前必须先使旧列表 GET 失效')
assert.match(createBranch, /const mobileLoadWasPending = mobileLoadingMore\.value[\s\S]*mobileLoadWasPending \|\|/, '移动加载更多进行中时必须按累计窗口协调创建回执')
assert.match(createBranch, /if \(state\.requiresReload\)[\s\S]*loadData\(\{ quiet: true \}\)[\s\S]*else[\s\S]*applyGroupPageResult/, '仅桌面后续页允许条件刷新，其余场景必须本地应用')
assert.doesNotMatch(createBranch, /message\.success\('分组已创建'\)\s*\n\s*await loadData\(\)/, '分组创建成功后不得无条件重拉整页')

console.log('分组创建本地协调回归通过：窄回执覆盖排序、分页、移动累计、作用域与 stale GET')

function group(id: string, updatedAt: string): GroupListItem {
  return {
    id,
    name: id,
    providerCode: 'gpt',
    enabled: true,
    isDefault: false,
    groupType: 'personal',
    accessType: 'owner',
    updatedAt,
    accountStats: {
      total: 0,
      available: 0,
      active: 0,
      disabled: 0,
      error: 0,
      rateLimited: 0,
      concurrencyLimit: 0,
      currentConcurrency: 0
    },
    canEdit: true,
    canDelete: true,
    canReturn: false
  }
}
