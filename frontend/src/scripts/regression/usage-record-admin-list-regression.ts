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
const modelOptionsSource = readFileSync(resolve('../frontend/src/views/usage-records/useUsageRecordModelOptions.ts'), 'utf8')
assert.doesNotMatch(viewSource, /return emptyUsageRecordListResult\(pageState, true\)/, '管理端全用户列表不应在前端空列表短路')
assert.match(viewSource, /visibilitychange/, 'auto 日期模式应在页面重新可见时检查跨日')
assert.match(viewSource, /addEventListener\('focus'/, 'auto 日期模式应在窗口聚焦时检查跨日')
assert.match(viewSource, /setTimeout\(/, 'auto 日期模式应设置零点计时器')
assert.match(viewSource, /const autoDateRolloverTimer = ref</, '零点计时器句柄必须保存在组件实例 ref 中')
assert.match(viewSource, /onActivated\(/, 'KeepAlive 页面重新激活时应恢复 auto 日期生命周期')
assert.match(viewSource, /onDeactivated\(/, 'KeepAlive 页面停用时应清理当前实例的计时器和监听器')
assert.match(viewSource, /dateMode/, '页面应显式维护 auto/manual 日期模式')
const fetchPageStart = viewSource.indexOf('fetchPage: async (options, pageState) => {')
const fetchPageEnd = viewSource.indexOf('requestSignature:', fetchPageStart)
const fetchPageSource = fetchPageStart >= 0 && fetchPageEnd > fetchPageStart
  ? viewSource.slice(fetchPageStart, fetchPageEnd)
  : ''
assert.doesNotMatch(fetchPageSource, /loadModelOptions\(/, '列表请求不得自动加载模型筛选项')
assert.match(fetchPageSource, /resetModelOptions\(\)/, '刷新或强制刷新列表时必须清除模型候选本地缓存')
assert.match(fetchPageSource, /return await fetchRecords\(pageState\)/, '使用记录列表必须直接等待 scoped 列表请求')
assert.doesNotMatch(fetchPageSource, /Promise\.all/, '使用记录首屏不得等待模型筛选项加载')
assert.match(toolbarSource, /dropdown-visible-change/, '模型筛选下拉打开时应触发按需加载')
assert.match(toolbarSource, /@search="emit\('model-options-search', \$event\)"/, '模型筛选搜索时应触发按需加载')
assert.match(viewSource, /handleDropdown: handleModelOptionsDropdown/, '页面应接入模型筛选下拉按需加载处理器')
assert.match(viewSource, /handleSearch: handleModelOptionsSearch/, '页面应接入模型筛选搜索按需加载处理器')
assert.match(viewSource, /useUsageRecordModelOptions\(\{[\s\S]*scopeParams: modelOptionsScopeParams,[\s\S]*selectedModel:/, '模型候选必须使用当前系统账户作用域并保留已选模型')
assert.match(modelOptionsSource, /loadingQueryKey === queryKey/, '相同关键词的并发搜索必须复用进行中的请求')
assert.match(
  viewSource,
  /async function refreshMobileRecords\(\): Promise<void>\s*\{\s*await refreshMobileRecordsList\(\{ forceOptions: true \}\)/,
  '移动端手动刷新必须同步清理模型候选缓存'
)
assert.match(modelOptionsSource, /setTimeout\([\s\S]*searchDebounceMs/, '模型搜索必须防抖，避免每次按键确认缓存')
assert.match(modelOptionsSource, /watch\(currentScopeKey, resetModelOptions/, '切换系统账户作用域必须清除旧候选')
assert.match(viewSource, /loadUsageStatsWindow\(\{ viewScope: isManagementView\.value \? 'admin' : 'self' \}\)/, '管理端和个人端日期时区应复用对应作用域的轻量 usage-window 接口')
assert.doesNotMatch(viewSource, /api\.settings\.get\(\)/, '使用记录首屏不得读取完整系统设置')
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
assert.match(tableSource, /column\.key === 'status'[\s\S]*UsageRecordResultCell[\s\S]*statusCodeText\(record\)/, '状态列必须显示结果和 HTTP 状态码两个标签')
assert.match(tableSource, /<a-tag v-if="typeof record\.statusCode === 'number'" :color="statusCodeColor\(record\)">/, '状态码缺失时不得渲染空标签')

const resultCellSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordResultCell.vue'), 'utf8')
assert.match(resultCellSource, /record\.success \? '成功' : '失败'/, '结果单元格只显示列表可直接渲染的成功或失败标签')
assert.doesNotMatch(resultCellSource, /InfoCircleOutlined|failureReason|a-tooltip/, '轻量列表不得依赖失败详情或悬浮提示')

const costCellSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordCostCell.vue'), 'utf8')
assert.doesNotMatch(costCellSource, />计价信息</, '成本明细不得保留独立计价信息分组')
assert.match(costCellSource, /formatCost\(record\.costUsd\)/, '列表成本单元格只显示可直接渲染的最终成本')
assert.doesNotMatch(costCellSource, /finalPriceRows|metadataRows|unitPriceRows/, '轻量列表不得依赖计价详情行')
assert.doesNotMatch(viewSource, /openUsageRecordDetail|UsageRecordDetailDrawer|createUsageRecordDetailRequestGate/, '使用记录页面不得保留详情组件或详情请求门禁')
assert.equal(columnKeys.includes('actions'), false, '使用记录列表不得保留操作列')
assert.doesNotMatch(tableSource, /open-detail|open-trace-target|查看运行日志|查看审计日志|查看详情/, '桌面列表不得保留详情或日志跳转')
const mobileSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordMobileCard.vue'), 'utf8')
assert.doesNotMatch(mobileSource, /open-detail|openDetail|openAuditLogs|openRuntimeLogs|查看详情|运行日志|审计日志/, '移动端使用记录卡片不得保留详情或日志跳转')
const apiSource = readFileSync(resolve('../frontend/src/api/domains/usageRecords.ts'), 'utf8')
assert.doesNotMatch(apiSource, /detail\s*:|usage-records\/\$\{id\}/, '前端使用记录 API 不得保留按 ID 详情请求')

console.log('使用记录前端回归通过：列表状态、列顺序、日期模式和筛选保留，详情、操作列与日志跳转已退场')
