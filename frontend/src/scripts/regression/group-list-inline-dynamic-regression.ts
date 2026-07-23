import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const groupsListSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsList.vue', import.meta.url)), 'utf8')
const groupsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/groups.ts', import.meta.url)), 'utf8')

assert.doesNotMatch(groupsViewSource, /statusSnapshot|loadGroupStatusSnapshot|groupStatusRequestSeq/, '分组列表不得在列表响应后补发状态快照')
assert.doesNotMatch(groupsApiSource, /status-snapshot|statusSnapshot/, '分组前端 API 不得保留独立状态快照入口')
assert.match(groupsViewSource, /const page = await groupsApi\.listPage/, '分组列表必须通过列表接口获取当前页完整数据')
assert.match(groupsListSource, /groupConcurrencyText\(record\)/, '分组列表必须消费列表响应中的当前并发')

console.log('分组列表单接口完整响应回归通过：不再补发状态快照，当前并发和今日用量随列表返回')
