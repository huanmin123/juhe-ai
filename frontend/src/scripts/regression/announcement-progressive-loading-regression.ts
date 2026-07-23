import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const layoutSource = readFileSync(fileURLToPath(new URL('../../layouts/AppLayout.vue', import.meta.url)), 'utf8')
const modalSource = readFileSync(fileURLToPath(new URL('../../layouts/AnnouncementModal.vue', import.meta.url)), 'utf8')
const adminViewSource = readFileSync(fileURLToPath(new URL('../../views/announcements/AnnouncementsView.vue', import.meta.url)), 'utf8')
const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/announcements.ts', import.meta.url)), 'utf8')
const typeSource = readFileSync(fileURLToPath(new URL('../../types/domain/announcements.ts', import.meta.url)), 'utf8')

assert.match(typeSource, /interface PublishedAnnouncementListItem[\s\S]*?id: string[\s\S]*?title: string[\s\S]*?level: AnnouncementLevel[\s\S]*?publishedAt: string/, '公共公告列表类型应只表达轻量摘要')
assert.doesNotMatch(typeSource.match(/interface PublishedAnnouncementListItem[\s\S]*?\n}/)?.[0] ?? '', /content:/, '公共公告列表类型不能包含正文')
assert.match(typeSource, /interface AnnouncementListItem[\s\S]*?contentPreview: string/, '管理公告列表应使用显式 contentPreview 字段')
assert.match(typeSource, /interface AnnouncementMutationResult[\s\S]*?id: string[\s\S]*?revision: string/, '公告写操作必须使用轻量 id/revision 响应类型')
assert.match(apiSource, /publicDetail:\s*\(id: string\)[\s\S]*?\/announcements\/public\/\$\{id\}/, '公告 API 应提供公共详情按 ID 读取')
for (const actionName of ['create', 'update', 'publish', 'unpublish']) {
  assert.match(
    apiSource,
    new RegExp(`${actionName}:[\\s\\S]{0,240}unwrap<AnnouncementMutationResult>`),
    `${actionName} API 不能继续声明返回完整 AnnouncementSummary`
  )
}

const loadSummariesBody = layoutSource.match(/async function loadAnnouncements[\s\S]*?\n}/)?.[0] ?? ''
assert.match(loadSummariesBody, /api\.announcements\.publicList/, '登录首屏和轮询仍应加载轻量公告摘要')
assert.doesNotMatch(loadSummariesBody, /publicDetail/, '登录首屏和轮询不能加载公告正文')
assert.match(layoutSource, /async function loadAnnouncementContent[\s\S]*?api\.announcements\.publicDetail/, '公告正文必须有独立的按 ID 加载函数')
assert.match(layoutSource, /@load-content="loadAnnouncementContent"/, '公告弹窗展开动作必须连接按 ID 正文加载函数')
assert.match(layoutSource, /let announcementContentSessionId = 0/, '公告正文请求必须记录弹窗会话代次')
assert.match(layoutSource, /const requestSessionId = announcementContentSessionId/, '单条正文请求必须捕获发起时的弹窗会话')
assert.match(layoutSource, /announcementContentRequestIds\.get\(id\) !== requestId/, '旧正文请求不得覆盖或清理同 ID 的新请求状态')
assert.match(layoutSource, /requestSessionId !== announcementContentSessionId/, '关闭重开后的旧正文响应必须失效')

const toggleBody = modalSource.match(/function toggleExpand[\s\S]*?\n}/)?.[0] ?? ''
assert.match(toggleBody, /emit\('load-content', id\)/, '用户首次展开公告时才应请求正文')
assert.match(modalSource, /contentById/, '弹窗正文必须来自按 ID 缓存，不能来自摘要列表')
assert.match(toggleBody, /if \(!props\.contentById\[id\]\)[\s\S]*?emit\('load-content', id\)[\s\S]*?return/, '正文失败后展开态必须允许直接重试，不能先折叠一次')

assert.match(adminViewSource, /let announcementDetailRequestGeneration = 0/, '公告编辑详情必须记录请求代次')
assert.match(adminViewSource, /function openCreate[\s\S]*?announcementDetailRequestGeneration \+= 1/, '打开新增必须使未完成的编辑详情请求失效')
assert.match(adminViewSource, /async function openEdit[\s\S]*?const requestGeneration = \+\+announcementDetailRequestGeneration/, '每次打开编辑必须生成新的详情请求代次')
assert.match(adminViewSource, /requestGeneration !== announcementDetailRequestGeneration \|\| editingId\.value !== record\.id/, 'A→B 编辑切换时旧详情不得落到新目标表单')
assert.match(adminViewSource, /loadEntityDetailCached\([\s\S]*?force: true/, '公告编辑必须绕过可能被旧在途请求回填的短缓存')
assert.match(adminViewSource, /function invalidatePendingAnnouncementDetail[\s\S]*?announcementDetailRequestGeneration \+= 1/, '公告写操作必须使同一公告的在途详情请求失效')
for (const actionName of ['saveAnnouncement', 'publishAnnouncement', 'unpublishAnnouncement', 'removeAnnouncement']) {
  assert.match(
    adminViewSource,
    new RegExp(`${actionName}[\\s\\S]{0,1600}invalidatePendingAnnouncementDetail\\(`),
    `${actionName} 成功后必须失效公告详情缓存`
  )
}
assert.doesNotMatch(adminViewSource, /const\s+\w+\s*=\s*await api\.announcements\.(?:create|update|publish|unpublish)/, '公告管理页面不能依赖写操作返回完整公告摘要')
assert.match(adminViewSource, /await api\.announcements\.update[\s\S]{0,800}await loadData\(\)/, '公告编辑成功后应重新加载权威列表')
assert.match(adminViewSource, /async function publishAnnouncement[\s\S]{0,800}await loadData\(\)/, '公告发布成功后应重新加载权威列表')
assert.match(adminViewSource, /async function unpublishAnnouncement[\s\S]{0,800}await loadData\(\)/, '公告下线成功后应重新加载权威列表')

console.log('公告前端渐进加载回归通过：首屏与轮询仅取摘要，展开单条才按 ID 取正文')
