import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { defaultUsageRecordsPageState } from '../../views/usage-records/usageRecordPageState'
import { usageRecordListParams } from '../../views/usage-records/usageRecordFilters'

const defaultState = defaultUsageRecordsPageState('all') as ReturnType<typeof defaultUsageRecordsPageState> & { dateMode?: string }
assert.equal(defaultState.dateMode, 'auto', '管理端使用记录默认日期模式应为 auto today')
const allUsersParams = usageRecordListParams({
  page: 1,
  pageSize: 20,
  accountName: '',
  clientIp: '',
  dateRange: undefined,
  groupId: undefined,
  model: '',
  result: 'all',
  sortBy: 'createdAt',
  sortOrder: 'desc',
  statusCode: '',
  systemAccountId: undefined,
  traceId: '',
  trafficSource: 'all'
})
assert.equal(allUsersParams.result, undefined, '全用户默认列表不应把“全部结果”序列化为业务筛选参数')

const viewSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordsView.vue'), 'utf8')
const toolbarSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordsFilterToolbar.vue'), 'utf8')
assert.doesNotMatch(viewSource, /return emptyUsageRecordListResult\(pageState, true\)/, '管理端全用户列表不应在前端空列表短路')
assert.match(viewSource, /visibilitychange/, 'auto 日期模式应在页面重新可见时检查跨日')
assert.match(viewSource, /addEventListener\('focus'/, 'auto 日期模式应在窗口聚焦时检查跨日')
assert.match(viewSource, /setTimeout\(/, 'auto 日期模式应设置零点计时器')
assert.match(viewSource, /dateMode/, '页面应显式维护 auto/manual 日期模式')
assert.match(toolbarSource, /businessFiltersDisabled/, '全用户模式应统一禁用业务筛选')
assert.match(toolbarSource, /:disabled="businessFiltersDisabled"/, '全用户模式的业务筛选控件应处于禁用态')

console.log('管理员使用记录前端回归通过：全用户当天列表、auto/manual 日期模式、跨日刷新与筛选锁定均已接入')
