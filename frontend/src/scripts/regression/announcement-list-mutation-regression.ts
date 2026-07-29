import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import type { AnnouncementListItem } from '../../types/domain/announcements.js'
import {
  announcementContentPreview,
  applyAnnouncementCreateMutation,
  applyAnnouncementDeleteMutation,
  applyAnnouncementPatchMutation
} from '../../views/announcements/announcementListMutation.js'

const original: AnnouncementListItem[] = [
  {
    id: 'ann_old',
    title: '旧公告',
    contentPreview: '旧正文',
    contentTruncated: false,
    level: 'info',
    status: 'draft',
    updatedByName: '旧管理员',
    revision: '2026-07-29T00:00:00.000Z'
  },
  {
    id: 'ann_other',
    title: '其他公告',
    contentPreview: '其他正文',
    contentTruncated: false,
    level: 'normal',
    status: 'archived',
    revision: '2026-07-28T00:00:00.000Z'
  }
]

const patched = applyAnnouncementPatchMutation(
  original,
  { id: 'ann_other', revision: '2026-07-30T00:00:00.000Z' },
  { title: '已更新公告', status: 'published' },
  '当前管理员',
  { currentPage: 1, pageSize: 50 }
)
assert.equal(patched.items[0].id, 'ann_other', '更新后公告必须按服务端更新时间语义移动到当前页首位')
assert.equal(patched.items[0].title, '已更新公告', '局部合并必须应用 delta 标题')
assert.equal(patched.items[0].publishedAt, '2026-07-30T00:00:00.000Z', '首次发布的发布时间应使用 mutation revision')
assert.equal(patched.items[0].updatedByName, '当前管理员', '局部合并应直接展示当前操作人')
assert.equal(patched.items[1], original[0], '未变更行应保持对象引用，避免无关重渲染')

const created = applyAnnouncementCreateMutation(
  patched.items,
  { id: 'ann_new', revision: '2026-07-31T00:00:00.000Z' },
  { title: '新公告', content: '新正文', level: 'warning', status: 'draft' },
  '当前管理员',
  { currentPage: 1, pageSize: 2 }
)
assert.deepEqual(created.items.map((item) => item.id), ['ann_new', 'ann_other'], '当前第一页创建后应局部前插并维持页容量')
assert.equal(created.totalDelta, 1, '创建应推进列表总量上界')

const deleted = applyAnnouncementDeleteMutation(created.items, 'ann_other')
assert.deepEqual(deleted.items.map((item) => item.id), ['ann_new'], '删除应只移除目标行')
assert.equal(deleted.totalDelta, -1, '删除已加载行应减少列表总量上界')

const desktopLaterPage = applyAnnouncementPatchMutation(
  original,
  { id: 'ann_old', revision: '2026-08-01T00:00:00.000Z' },
  { title: '迁出当前页' },
  '当前管理员',
  { currentPage: 2, pageSize: 2 }
)
assert.deepEqual(
  desktopLaterPage.items.map((item) => item.id),
  ['ann_other'],
  '桌面后续页更新后，按 updated_at 倒序的目标行必须迁出当前分页窗口'
)

const accumulatedItems = [...original, {
  id: 'ann_third',
  title: '第三条公告',
  contentPreview: '第三条正文',
  contentTruncated: false,
  level: 'info' as const,
  status: 'draft' as const,
  revision: '2026-07-27T00:00:00.000Z'
}]
const mobileAccumulated = applyAnnouncementPatchMutation(
  accumulatedItems,
  { id: 'ann_third', revision: '2026-08-02T00:00:00.000Z' },
  { title: '移动端置顶' },
  '当前管理员',
  { currentPage: 2, pageSize: 2 }
)
assert.deepEqual(
  mobileAccumulated.items.map((item) => item.id),
  ['ann_third', 'ann_old', 'ann_other'],
  '移动端累计窗口更新后必须置顶目标行，并保留其余已加载行'
)

const longUnicodeContent = `${'测'.repeat(240)}试`
const preview = announcementContentPreview(longUnicodeContent)
assert.equal(Array.from(preview.contentPreview.replace(/\.\.\.$/, '')).length, 240, '正文预览必须按 Unicode 字符而不是 UTF-16 code unit 截断')
assert.equal(preview.contentTruncated, true, '超过 240 字的正文必须要求编辑时按需读取详情')

const viewSource = readFileSync(new URL('../../views/announcements/AnnouncementsView.vue', import.meta.url), 'utf8')
assert.match(viewSource, /const mutationRevision = announcementMutationRevision/, '列表请求必须捕获发起时的 mutation revision')
assert.match(
  viewSource,
  /mutationRevision === announcementMutationRevision \? result : \{ \.\.\.result, superseded: true \}/,
  '成功 mutation 后迟到的旧列表响应必须标记为 superseded，不能覆盖局部合并结果'
)

console.log('公告列表局部合并回归通过：桌面分页、移动端累计、迟到 GET 和 Unicode 预览均按需协调')
