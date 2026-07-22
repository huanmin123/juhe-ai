import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const mutationsPath = fileURLToPath(new URL('../../views/groups/groupListMutations.ts', import.meta.url))

assert.equal(existsSync(mutationsPath), false, '分组列表不得保留快照合并 helper')
assert.doesNotMatch(groupsViewSource, /statusSnapshot\(/, '分组列表不得请求状态快照接口')
assert.doesNotMatch(groupsViewSource, /dynamicSnapshot/, '分组列表不得保留动态快照补齐状态')
assert.doesNotMatch(groupsViewSource, /loadDynamicSnapshotsInBatches/, '分组列表不得保留分批快照请求')
assert.match(groupsViewSource, /const page = await groupsApi\.listPage/, '分组列表必须直接消费列表响应内的动态字段')

console.log('分组列表内联动态字段回归通过：无额外状态快照请求')
