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
const groupOptionsSource = readFileSync(resolve('../frontend/src/views/usage-records/useUsageRecordGroupOptions.ts'), 'utf8')
const toolbarSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordsFilterToolbar.vue'), 'utf8')
const modelOptionsSource = readFileSync(resolve('../frontend/src/views/usage-records/useUsageRecordModelOptions.ts'), 'utf8')
assert.doesNotMatch(viewSource, /return emptyUsageRecordListResult\(pageState, true\)/, '管理端全用户列表不应在前端空列表短路')
assert.doesNotMatch(viewSource, /visibilitychange|window\.addEventListener\('focus'/, 'auto 日期模式不得因可见性或焦点自动刷新列表')
const rolloverSource = viewSource.slice(viewSource.indexOf('function refreshAutoDateAfterRollover'), viewSource.indexOf('function scheduleAutoDateRollover'))
assert.match(rolloverSource, /dateRangeFilter\.value = nextRange[\s\S]*resetPagination\(\)/, 'auto 日期跨日必须维护日期范围和分页状态')
assert.doesNotMatch(rolloverSource, /\bloadData\s*\(/, 'auto 日期跨日不得自动请求列表')
assert.match(viewSource, /dateMode/, '页面应显式维护 auto/manual 日期模式')
const fetchPageStart = viewSource.indexOf('fetchPage: async (options, pageState) => {')
const fetchPageEnd = viewSource.indexOf('requestSignature:', fetchPageStart)
const fetchPageSource = fetchPageStart >= 0 && fetchPageEnd > fetchPageStart
  ? viewSource.slice(fetchPageStart, fetchPageEnd)
  : ''
assert.doesNotMatch(fetchPageSource, /loadModelOptions\(/, '列表请求不得自动加载模型筛选项')
assert.match(fetchPageSource, /resetModelOptions\(\)/, '刷新或强制刷新列表时必须清除模型候选本地缓存')
assert.match(fetchPageSource, /const result = await fetchRecords\(pageState\)[\s\S]*get superseded\(\) \{ return requestAuthRevision !== authState\.revision\.value \}/, '使用记录列表必须直接等待 scoped 列表请求并隔离身份变化后的旧响应')
assert.match(groupOptionsSource, /function invalidate\(\): void \{[\s\S]*clearSearchTimer\(\)[\s\S]*requestId \+= 1[\s\S]*loadingPromise = undefined/, '分组选项失效必须取消搜索并推进请求代次')
assert.match(viewSource, /watch\(\(\) => authState\.revision\.value, \(\) => \{[\s\S]*invalidateGroupOptions\(\)/, '身份变化必须使在途分组选项请求失效')
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
assert.match(viewSource, /loadUsageStatsWindow\(/, '使用记录必须读取部署时区以维护 auto 日期边界')
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
assert.match(tableSource, /<a-tag v-if="record\.upstreamModelMismatch && record\.upstreamResponseModel" color="red">上游响应 \{\{ record\.upstreamResponseModel \}\}<\/a-tag>/, '桌面端模型不一致提示必须显示上游实际响应模型')
assert.doesNotMatch(tableSource, /模型不一致/, '桌面端不得保留独立模型不一致标签')

const resultCellSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordResultCell.vue'), 'utf8')
assert.match(resultCellSource, /record\.success \? '成功' : '失败'/, '结果单元格必须显示语义成功或失败')
assert.match(resultCellSource, /InfoCircleOutlined/, '失败结果必须显示错误详情信息图标')
assert.match(resultCellSource, /failureDetail/, '失败详情必须通过单条摘要悬浮内容展示')
assert.match(
  resultCellSource,
  /const errorMessage = props\.record\.errorMessage\s*\n\s*if \(errorMessage !== undefined && errorMessage\.length > 0\) return errorMessage/,
  '非空错误消息必须原样优先返回，包含仅空白字符的消息'
)
assert.doesNotMatch(resultCellSource, /errorMessage\?\.trim\(\)|errorMessage\.trim\(\)/, '错误消息存在性不得裁剪空白字符')
assert.doesNotMatch(resultCellSource, /return.*errorMessage.*(?:statusCode|errorCode)/, '错误消息提示不得附加 HTTP 状态或错误码')
assert.match(resultCellSource, /record\.errorCode/, '无错误消息时错误详情应回退到错误码')
assert.match(resultCellSource, /record\.failureReason/, '错误详情悬浮内容必须优先展示最终失败摘要')
assert.doesNotMatch(resultCellSource, /statusCode.*failureDetail|failureDetail.*statusCode/, '错误详情不得附加 HTTP 状态码')
assert.match(resultCellSource, /a-tooltip/, '错误详情必须通过悬浮提示展示')
assert.doesNotMatch(resultCellSource, /usage-failure-reason|usage-failure-attribution/, '失败详情不得直接撑开列表单元格')
const formatterSource = readFileSync(resolve('../frontend/src/views/usage-records/usageRecordFormatters.ts'), 'utf8')
assert.match(formatterSource, /return String\(record\.statusCode\)/, '状态码必须展示上游原始数字')
assert.doesNotMatch(formatterSource, /HTTP \$\{record\.statusCode\}|上游非成功终态/, '状态码不得混入 HTTP 或语义解释文案')
assert.match(formatterSource, /return 'orange'/, '失败 2xx 状态码必须使用非成功颜色')
assert.match(formatterSource, /downstream_closed.*下游连接关闭/s, '下游关闭必须统一展示为下游连接关闭')
assert.doesNotMatch(formatterSource, /触发方未识别|历史记录.*下游连接关闭/, '下游连接关闭文案不得附带触发方或历史记录分类')

const costCellSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordCostCell.vue'), 'utf8')
assert.doesNotMatch(costCellSource, />计价信息</, '成本明细不得保留独立计价信息分组')
assert.match(costCellSource, /formatCost\(record\.costUsd\)/, '列表成本单元格只显示可直接渲染的最终成本')
assert.doesNotMatch(costCellSource, /finalPriceRows|metadataRows|unitPriceRows/, '轻量列表不得依赖计价详情行')
assert.doesNotMatch(viewSource, /openUsageRecordDetail|UsageRecordDetailDrawer|createUsageRecordDetailRequestGate/, '使用记录页面不得保留详情组件或详情请求门禁')
assert.equal(columnKeys.includes('actions'), false, '使用记录列表不得保留操作列')
assert.doesNotMatch(tableSource, /open-detail|open-trace-target|查看运行日志|查看审计日志|查看详情/, '桌面列表不得保留详情或日志跳转')
const mobileSource = readFileSync(resolve('../frontend/src/views/usage-records/UsageRecordMobileCard.vue'), 'utf8')
assert.match(mobileSource, /UsageRecordResultCell/, '移动端必须复用单一失败标识和错误详情入口')
assert.doesNotMatch(mobileSource, /失败说明|failureAttribution|failureReason/, '移动端不得直接重复展示失败摘要或内部归因')
assert.doesNotMatch(mobileSource, /open-detail|openDetail|openAuditLogs|openRuntimeLogs|查看详情|运行日志|审计日志/, '移动端使用记录卡片不得保留详情或日志跳转')
assert.match(mobileSource, /<a-tag v-if="record\.upstreamModelMismatch && record\.upstreamResponseModel" color="red">上游响应 \{\{ record\.upstreamResponseModel \}\}<\/a-tag>/, '移动端模型不一致提示必须显示上游实际响应模型')
assert.doesNotMatch(mobileSource, /模型不一致/, '移动端不得保留独立模型不一致标签')
const apiSource = readFileSync(resolve('../frontend/src/api/domains/usageRecords.ts'), 'utf8')
assert.doesNotMatch(apiSource, /detail\s*:|usage-records\/\$\{id\}/, '前端使用记录 API 不得保留按 ID 详情请求')

console.log('使用记录前端回归通过：列表状态、列顺序、日期模式和筛选保留，详情、操作列与日志跳转已退场')
