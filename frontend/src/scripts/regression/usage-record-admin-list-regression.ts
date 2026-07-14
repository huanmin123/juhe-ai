import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { defaultUsageRecordsPageState } from '../../views/usage-records/usageRecordPageState'
import { usageRecordListParams } from '../../views/usage-records/usageRecordFilters'
import { usageRecordTableColumns } from '../../views/usage-records/usageRecordTableConfig'

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
assert.match(viewSource, /const autoDateRolloverTimer = ref</, '零点计时器句柄必须保存在组件实例 ref 中')
assert.match(viewSource, /onActivated\(/, 'KeepAlive 页面重新激活时应恢复 auto 日期生命周期')
assert.match(viewSource, /onDeactivated\(/, 'KeepAlive 页面停用时应清理当前实例的计时器和监听器')
assert.match(viewSource, /dateMode/, '页面应显式维护 auto/manual 日期模式')
const tableChangeSource = viewSource.match(/async function handleTableChange[\s\S]*?\n}/)?.[0] ?? ''
assert.match(tableChangeSource, /businessFiltersDisabled\.value[\s\S]*field: 'createdAt'[\s\S]*order: 'descend'/, '全用户列表处理表格排序时必须强制 createdAt 降序')
assert.match(toolbarSource, /businessFiltersDisabled/, '全用户模式应统一禁用业务筛选')
assert.match(toolbarSource, /:disabled="businessFiltersDisabled"/, '全用户模式的业务筛选控件应处于禁用态')

const columns = usageRecordTableColumns({ isManagementView: false, columnSortOrder: () => null })
const columnKeys = columns.map((column) => column.key)
assert.deepEqual(
  columnKeys.slice(columnKeys.indexOf('createdAt'), columnKeys.indexOf('trafficSource') + 1),
  ['createdAt', 'endpoint', 'trafficSource'],
  '时间后必须依次展示接口和请求来源'
)
assert.equal(columnKeys.includes('success'), false, '结果状态不应继续占用独立列')
assert.equal(columnKeys.includes('statusCode'), false, 'HTTP 状态码不应继续占用独立列')
assert.equal(columnKeys.includes('status'), true, '结果状态和 HTTP 状态码应合并为状态列')
const tableSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordsTable.vue'), 'utf8')
assert.match(tableSource, /column\.key === 'status'[\s\S]*UsageRecordResultCell[\s\S]*statusCodeText\(record\)/, '状态列必须显示结果和 HTTP 状态码两个标签，并保留失败原因提示')

console.log('管理员使用记录前端回归通过：列表状态合并、列顺序、auto/manual 日期模式、跨日刷新与筛选锁定均已接入')
