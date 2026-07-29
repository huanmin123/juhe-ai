import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/system-teams/SystemTeamsView.vue', import.meta.url)), 'utf8')
const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/systemTeams.ts', import.meta.url)), 'utf8')

const openSource = sourceBetween(viewSource, 'function openMemberModal', 'function handleMemberTabChange')
assert.match(openSource, /memberView\.value = 'active'/, '成员弹窗首开必须固定在当前成员')
assert.doesNotMatch(openSource, /memberHistory|loadMemberHistory/, '打开成员弹窗不得预取历史成员')

const tabSource = sourceBetween(viewSource, 'function handleMemberTabChange', 'function handleMemberHistoryTableChange')
assert.match(tabSource, /key !== 'history'/, '只有点击历史成员标签才允许加载 history')
assert.match(tabSource, /loadMemberHistory\(teamId, 1\)/, '首次点击历史成员必须从第一页加载')

const historySource = sourceBetween(viewSource, 'async function loadMemberHistory', 'function isCurrentMemberHistoryContext')
assert.match(historySource, /systemTeamsApi\.memberHistory\(teamId/, '历史成员必须使用独立 scoped API')
assert.match(historySource, /scopeKey = currentMemberScopeKey\(\)/, '历史请求必须捕获 owner/view scope')
assert.match(historySource, /requestGeneration = \+\+memberHistoryRequestGeneration/, '历史请求必须使用独立 generation')
const historyContextSource = sourceBetween(viewSource, 'function isCurrentMemberHistoryContext', 'function currentMemberScopeKey')
assert.match(historyContextSource, /memberView\.value === 'history'/, '切回当前成员后历史迟到响应不得落地')
assert.match(historyContextSource, /isCurrentMemberContext\(teamId, contextGeneration\)/, '历史迟到响应必须校验团队详情 generation')
assert.match(historyContextSource, /currentMemberScopeKey\(\) === scopeKey/, '历史迟到响应必须校验 owner/view scope')
assert.match(viewSource, /@mobile-load-more="loadMoreMemberHistory"/, '历史成员移动端必须支持分页加载更多')
assert.match(viewSource, /<template #card="\{ record \}">[\s\S]*已移除/, '历史成员必须提供移动端状态卡片')

assert.match(apiSource, /http\.get\(`\/system-teams\/\$\{id\}\/members\/history`/, '管理侧必须暴露 history API')
assert.match(apiSource, /http\.get\(`\/my-teams\/\$\{id\}\/members\/history`/, '个人侧必须暴露 history API')

const conflictSource = sourceBetween(viewSource, 'async function refreshTeamAfterMemberConflict', 'function isVersionConflict')
assert.match(conflictSource, /resetPagination\(\)[\s\S]*loadData\(\{ quiet: true \}\)/, '冲突校准必须先回第一页，不能用后续页覆盖累计列表')

console.log('系统团队历史成员按需加载回归通过：点击触发、分页、scope/generation fencing 与移动端卡片均已覆盖')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
