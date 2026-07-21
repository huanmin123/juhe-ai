import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = readFileSync(fileURLToPath(new URL('../../views/system-teams/SystemTeamsView.vue', import.meta.url)), 'utf8')
const openMemberSource = sourceBetween(source, 'async function openMemberModal', 'function handleTeamAction')
assert.match(openMemberSource, /memberModalOpen\.value\s*=\s*true/, '成员弹窗应先打开壳再异步加载详情')
assert.doesNotMatch(openMemberSource, /void loadMemberOptions\(\)/, '成员弹窗打开时不得预取候选账户')
assert.match(source, /@dropdown-visible-change="handleMemberOptionsDropdown"/, '成员候选必须保留下拉展开触发')
assert.doesNotMatch(source, /record\.activeMemberCount/, '团队列表不得依赖 activeMemberCount')

console.log('系统团队成员详情与候选按需加载回归通过')

function sourceBetween(value: string, start: string, end: string): string {
  const startIndex = value.indexOf(start)
  const endIndex = value.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return value.slice(startIndex, endIndex)
}
