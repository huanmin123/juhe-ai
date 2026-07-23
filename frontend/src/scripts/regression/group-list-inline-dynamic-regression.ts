import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const mutationsPath = fileURLToPath(new URL('../../views/groups/groupListMutations.ts', import.meta.url))

assert.equal(existsSync(mutationsPath), false, '分组列表不得保留快照合并 helper')
assert.match(groupsViewSource, /groupsApi\.statusSnapshot\(batch, groupScopeParams\.value\)/, '分组列表必须按当前页独立请求状态快照')
assert.match(groupsViewSource, /index \+= 100/, '移动端累计加载超过接口上限时必须分批请求状态快照')
assert.match(groupsViewSource, /groupStatusRequestSeq/, '分组状态快照必须丢弃过期响应')
assert.match(groupsViewSource, /authState\.revision\.value/, '分组状态快照业务签名必须包含认证 revision')
assert.match(groupsViewSource, /onDeactivated\(\(\) => \{[\s\S]*groupPageEpoch\.value \+= 1/, 'KeepAlive 失活必须推进分组页面 epoch')
assert.match(groupsViewSource, /onActivated\(\(\) => \{[\s\S]*loadData\(\{ quiet: true \}\)/, 'KeepAlive 激活必须重新加载分组列表')
assert.match(groupsViewSource, /hasActivated/, '分组页首次激活不得与 onMounted 重复请求')
assert.match(groupsViewSource, /todayUsage: undefined/, '快照加载和失败时必须清除旧用量，避免伪装为零')
assert.match(groupsViewSource, /currentConcurrencyAvailable: undefined/, '快照加载时并发必须显示未知而非零')
assert.match(groupsViewSource, /currentConcurrencyAvailable: concurrencyAvailable/, '分组列表必须消费快照可用性')
assert.match(groupsViewSource, /const page = await groupsApi\.listPage/, '分组列表必须先加载静态分页数据')

console.log('分组列表渐进式动态字段回归通过：静态分页先返回，当前页状态快照独立补齐')
