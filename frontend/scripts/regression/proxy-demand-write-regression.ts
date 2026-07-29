import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { ProxyProfileSummary } from '../../src/types/domain'
import {
  applyProxyMutation,
  buildProxyPatchPayload,
  hasProxyPatchChanges,
  reconcileCreatedProxyList,
  reconcileProxyMutationList,
  type ProxyFormState
} from '../../src/views/proxies/proxyMutation'

const frontendRoot = resolve(import.meta.dirname, '../..')
const viewSource = readFileSync(resolve(frontendRoot, 'src/views/proxies/ProxiesView.vue'), 'utf8')

const baseline: ProxyFormState = {
  name: '代理 A',
  description: '原说明',
  type: 'socks5h',
  host: '127.0.0.1',
  port: 7890,
  username: 'proxy-user',
  password: '',
  enabled: true
}

assert.deepEqual(
  buildProxyPatchPayload(baseline, { ...baseline, name: '  代理 A  ', host: ' 127.0.0.1 ' }),
  {},
  '仅输入首尾空白时不得生成代理 PATCH'
)
assert.equal(hasProxyPatchChanges(baseline, { ...baseline }), false, '未修改表单必须在请求前识别为 no-op')

const delta = buildProxyPatchPayload(baseline, {
  ...baseline,
  description: ' ',
  port: 7891,
  enabled: false
})
assert.deepEqual(
  delta,
  { description: null, port: 7891, enabled: false },
  '代理编辑只能提交实际变化字段，并将清空说明归一化为 null'
)
assert.equal(hasProxyPatchChanges(baseline, { ...baseline, port: 7891 }), true, '实际字段变化必须生成 PATCH')
assert.deepEqual(
  buildProxyPatchPayload(baseline, { ...baseline, password: 'new-secret' }),
  { password: 'new-secret' },
  '编辑密码时只能提交 password，不得回传列表行其他字段'
)

const row: ProxyProfileSummary = {
  id: 'proxy_1',
  name: baseline.name,
  description: baseline.description,
  type: baseline.type,
  host: baseline.host,
  port: baseline.port,
  username: baseline.username,
  enabled: baseline.enabled,
  testStatus: 'success',
  latencyMs: 23,
  outboundIp: '203.0.113.8',
  outboundRegion: 'JP',
  lastTestMessage: 'ok',
  lastTestedAt: '2026-07-28T00:00:00.000Z',
  updatedAt: '2026-07-28T00:00:00.000Z'
}
const merged = applyProxyMutation(row, {
  id: row.id,
  updatedAt: '2026-07-29T00:00:00.000Z',
  changed: true,
  values: { description: null, port: 7891, enabled: false }
})
assert.equal(merged.description, undefined, '最小回执应把清空说明局部合并到列表行')
assert.equal(merged.port, 7891, '最小回执应局部更新端口')
assert.equal(merged.enabled, false, '最小回执应局部更新状态')
assert.equal(merged.updatedAt, '2026-07-29T00:00:00.000Z', '最小回执必须推进列表编辑版本')
assert.equal(merged.host, row.host, '最小回执不得覆盖未返回的列表字段')
assert.equal(merged.outboundIp, row.outboundIp, '编辑保存不得丢失列表运行态字段')

const anotherRow: ProxyProfileSummary = { ...row, id: 'proxy_2', name: '代理 B', updatedAt: '2026-07-28T00:00:01.000Z' }
const reordered = reconcileProxyMutationList([anotherRow, row], {
  id: row.id,
  updatedAt: '2026-07-29T00:00:00.000Z',
  changed: true,
  values: { description: '最新说明' }
}, { keyword: '', page: 1, pageSize: 20, total: 2, accumulated: false })
assert.deepEqual(reordered.items.map((item) => item.id), [row.id, anotherRow.id], '第一列表页 PATCH 后必须按 updated_at DESC 将目标行移到首位')
assert.equal(reordered.total, 2, '局部重排不得改变列表总数')

const filteredOut = reconcileProxyMutationList([row, anotherRow], {
  id: row.id,
  updatedAt: '2026-07-29T00:00:00.000Z',
  changed: true,
  values: { name: '其他名称' }
}, { keyword: '代理', page: 1, pageSize: 20, total: 2, accumulated: false })
assert.deepEqual(filteredOut.items.map((item) => item.id), [anotherRow.id], '改名后不再匹配当前关键词的代理必须从本地结果中移除')
assert.equal(filteredOut.total, 1, '改名移出当前筛选结果时必须同步收窄总数')

const movedFromLaterPage = reconcileProxyMutationList([row, anotherRow], {
  id: row.id,
  updatedAt: '2026-07-29T00:00:00.000Z',
  changed: true,
  values: { description: '从第二页更新' }
}, { keyword: '', page: 2, pageSize: 20, total: 22, accumulated: false })
assert.deepEqual(movedFromLaterPage.items.map((item) => item.id), [anotherRow.id], '后续页 PATCH 的行已移到第一列表页，当前页必须本地移除')
assert.equal(movedFromLaterPage.total, 22, '跨页重排不得误减全局总数')

const mobileAccumulatedRows = Array.from({ length: 21 }, (_, index): ProxyProfileSummary => ({
  ...row,
  id: `mobile_proxy_${index}`,
  name: `代理 ${index}`,
  updatedAt: `2026-07-28T00:00:${String(index).padStart(2, '0')}.000Z`
}))
const mobileTarget = mobileAccumulatedRows[20]
assert(mobileTarget)
const reorderedMobileAccumulation = reconcileProxyMutationList(mobileAccumulatedRows, {
  id: mobileTarget.id,
  updatedAt: '2026-07-29T00:00:00.000Z',
  changed: true,
  values: { description: '移动端累计列表更新' }
}, { keyword: '', page: 2, pageSize: 20, total: 21, accumulated: true })
assert.equal(reorderedMobileAccumulation.items[0]?.id, mobileTarget.id, '移动端累计加载到第二页后，PATCH 行必须移动到累计集合首位')
assert.equal(reorderedMobileAccumulation.items.length, 21, '移动端累计集合局部重排不得按单页 pageSize 截断已加载行')

const createdMobileProxy: ProxyProfileSummary = {
  ...row,
  id: 'mobile_proxy_created',
  name: '代理 新增',
  updatedAt: '2026-07-29T00:00:01.000Z'
}
const insertedIntoMobileAccumulation = reconcileCreatedProxyList(mobileAccumulatedRows, createdMobileProxy, {
  keyword: '',
  page: 2,
  pageSize: 20,
  total: 21,
  accumulated: true
})
assert.equal(insertedIntoMobileAccumulation?.items[0]?.id, createdMobileProxy.id, '移动端累计加载到后续页时，新建代理必须插入累计集合首位')
assert.equal(insertedIntoMobileAccumulation?.items.length, 22, '移动端累计列表插入新代理不得截断已加载行')
assert.equal(insertedIntoMobileAccumulation?.total, 22, '移动端累计列表插入新代理必须增加总数')

assert.doesNotMatch(viewSource, /api\.proxies\.detail\s*\(/, '代理页面不得保留详情预取接口调用')
const openEditBranch = sourceBetween(viewSource, 'function openEdit(', 'function openTestReport')
assert.match(openEditBranch, /proxyFormFromSummary\(proxy\)/, '打开编辑必须直接复用列表行可编辑字段')
assert.match(openEditBranch, /editingUpdatedAt\.value\s*=\s*proxy\.updatedAt/, '打开编辑必须保存列表版本用于 CAS')
assert.match(openEditBranch, /editingBaseline\.value\s*=\s*\{ \.\.\.nextForm \}/, '打开编辑必须建立独立表单基线')
assert.doesNotMatch(openEditBranch, /\bawait\b|api\.|loadData\s*\(/, '打开代理编辑弹窗不得发请求或重载列表')

const editSaveBranch = sourceBetween(viewSource, 'if (targetId) {', '} else {')
assert.match(editSaveBranch, /buildProxyPatchPayload\(baseline, form\)/, '编辑保存必须相对打开时基线构造 delta')
assert.match(editSaveBranch, /Object\.keys\(patch\)\.length === 0/, '空修改必须在请求前被拦截')
assert.match(editSaveBranch, /api\.proxies\.update\(targetId, \{ \.\.\.patch, expectedUpdatedAt \}\)/, 'PATCH 必须只携带 delta 与列表版本')
assert.match(editSaveBranch, /applyEditedProxyMutation\(mutation\)/, '保存成功必须使用最小回执局部协调列表行')
assert.doesNotMatch(editSaveBranch, /loadData\s*\(/, '编辑保存后不得重新加载列表')
assert.ok(
  editSaveBranch.indexOf('Object.keys(patch).length === 0') < editSaveBranch.indexOf('api.proxies.update'),
  '空修改判断必须发生在 PATCH 请求之前'
)

const insertCreatedBranch = sourceBetween(viewSource, 'function insertCreatedProxy(', 'function applyEditedProxyMutation(')
assert.match(insertCreatedBranch, /const accumulated = proxies\.value\.length > pagination\.pageSize/, '新建后的响应式协调必须识别移动端累计列表')
assert.match(insertCreatedBranch, /if \(accumulated\) \{[\s\S]*proxies\.value = reconciled\.items[\s\S]*pagination\.total = reconciled\.total[\s\S]*return/, '移动端累计列表插入必须直接更新集合和总数，不能重置分页 hook 的当前页计数')

const applyEditedBranch = sourceBetween(viewSource, 'function applyEditedProxyMutation(', 'function defaultProxiesPageState(')
assert.match(applyEditedBranch, /const accumulated = proxies\.value\.length > pagination\.pageSize/, '编辑后的响应式协调必须识别移动端累计列表')
assert.match(applyEditedBranch, /if \(accumulated\) \{[\s\S]*proxies\.value = reconciled\.items[\s\S]*pagination\.total = reconciled\.total[\s\S]*return/, '移动端累计列表编辑必须直接更新集合和总数，不能把累计条数写成当前页条数')

const removeBranch = sourceBetween(viewSource, 'async function removeProxy(', 'function searchProxies')
assert.match(removeBranch, /await api\.proxies\.delete\(id\)/, '删除必须只调用目标代理删除接口')
assert.match(removeBranch, /removeProxyItems\(\(item\) => item\.id === id\)/, '删除成功必须本地移除列表行')
assert.doesNotMatch(removeBranch, /loadData\s*\(/, '删除成功后不得重新加载列表')

console.log('代理前端按需写回归通过：编辑零预取、delta/no-op、保存后排序/筛选协调与删除局部更新')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
