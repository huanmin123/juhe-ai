import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { createAiHealthRequestCoordinator } from '@/views/ai-health/aiHealthRequestCoordinator'

const frontendRoot = resolve(import.meta.dirname, '../..')
const repositoryRoot = resolve(frontendRoot, '../..')
const viewSource = readFileSync(resolve(frontendRoot, 'views/ai-health/AiHealthView.vue'), 'utf8')
const statusBarSource = readFileSync(resolve(frontendRoot, 'views/ai-health/AiHealthStatusBar.vue'), 'utf8')
const routerSource = readFileSync(resolve(frontendRoot, 'router/index.ts'), 'utf8')
const statsApiSource = readFileSync(resolve(frontendRoot, 'api/domains/stats.ts'), 'utf8')
const statsTypesSource = readFileSync(resolve(frontendRoot, 'types/domain/usage-stats.ts'), 'utf8')
const monitorRepositorySource = readFileSync(resolve(repositoryRoot, 'backend/src/storage/account-health-monitor.repository.ts'), 'utf8')

assert.match(routerSource, /path: '\/ai-health'[\s\S]+title: 'AI健康监控'/, '管理菜单必须注册 AI 健康监控')
assert.match(routerSource, /path: '\/my-ai-health'[\s\S]+viewScope: 'self'/, '用户菜单必须注册自助健康监控')
assert.match(statsApiSource, /http\.get\('\/stats\/ai-health'/, '管理视图必须调用管理健康接口')
assert.match(statsApiSource, /http\.get\('\/my-stats\/ai-health'/, '用户视图必须调用自助健康接口')
assert.match(viewSource, /ResponsiveListToolbar/, '页面必须复用现有列表工具栏')
assert.match(viewSource, /search-placeholder="搜索账户名"/, '页面必须支持按账户名搜索')
assert.match(viewSource, />上一页<\/a-button>[\s\S]*>下一页<\/a-button>/, '页面必须使用与 hasMore 语义一致的上一页和下一页')
assert.match(viewSource, /:disabled="loading \|\| !hasMore"/, '没有下一页或请求中时必须禁用下一页')
assert.doesNotMatch(viewSource, /共 \{\{ pagination\.total \}\} 个账户|<a-pagination/, '页面不得把哨兵下界展示成真实总数或页码')
assert.match(viewSource, /visibilityState === 'hidden'[\s\S]*cancelList\(\)[\s\S]*cancelDetail\(\)/, '页面隐藏时必须同时取消列表和详情请求')
assert.match(viewSource, /<template #actions>[\s\S]*class="ai-health-legend"/, '状态图例必须位于工具栏最右侧 actions 区域')
assert.match(viewSource, /<span><i class="success" \/>可用<\/span>/, '绿色图例必须使用“可用”文案')
assert.match(viewSource, /<span><i class="failure" \/>不可用<\/span>/, '红色图例必须使用“不可用”文案')
assert.doesNotMatch(viewSource, /时区\s*\{\{/, '页面不应展示内部统计时区')
assert.match(viewSource, /class="ai-health-content"/, '账户列表必须使用独立的自适应内容区')
assert.match(viewSource, /最近独立检查/, '健康监控必须区分独立探针时间与成功健康信号')
assert.match(viewSource, /account\.lastHealthSuccessAt/, '健康监控必须展示会顺延下次探针的最近成功信号')
assert.match(viewSource, /status === 'quality_isolated'\) return '质量隔离'/, '健康监控必须使用标准质量隔离文案')
assert.match(viewSource, /\.ai-health-content\s*\{[^}]*min-height:\s*0;[^}]*flex:\s*1 1 auto;[^}]*overflow-y:\s*auto;/, '账户列表必须自适应剩余高度并内部滚动')
assert.doesNotMatch(viewSource, /\.ai-health-page-card\s*\{[^}]*min-height:/, '页面不得覆盖通用响应式卡片高度')
assert.doesNotMatch(viewSource, /<a-table|ResponsiveDataList/, '健康监控必须使用列表而不是表格')
assert.match(statusBarSource, /<canvas/, '小时状态条必须使用 Canvas 控制一个月视图的 DOM 数量')
assert.doesNotMatch(statusBarSource, /v-for=.*hours/, '小时点不能展开为 744 个 DOM 节点')
assert.match(statusBarSource, /\.ai-health-status-canvas\s*\{[^}]*max-width:\s*100%;/, '小时状态条不得在窄屏撑出横向滚动')
assert.match(statusBarSource, /@click="handleClick"/, '小时状态槽必须支持点击查看详情')
assert.match(statusBarSource, /emit\('select', hour\)/, '小时状态槽点击后必须返回对应小时点')
assert.match(viewSource, /<a-drawer[^>]+title="检查详情"/, '页面必须提供检查详情抽屉')
assert.match(viewSource, /point\.errorMessage \|\| point\.errorCode/, '详情必须优先展示实际失败原因')
assert.match(viewSource, /api\.(?:stats|myStats)\.aiHealthHourDetail/, '小时详情必须在用户点击后按需请求')
assert.match(viewSource, /if \(point\.status === 'unknown'\) return/, '无记录小时不得发起无意义的详情请求')
assert.match(viewSource, /document\.visibilityState === 'visible'/, '隐藏页面不得发起健康列表加载')
const initialVisibleLoadSource = viewSource.slice(viewSource.indexOf('function loadInitialVisiblePage'), viewSource.indexOf('onMounted(() =>'))
assert.match(initialVisibleLoadSource, /initialVisibleLoadStarted\s*\|\|\s*initialVisibleLoadCompleted\s*\|\|\s*accounts\.value\.length > 0/, '首次可见加载必须以开始、完成和已有数据作为一次性门禁')
assert.match(initialVisibleLoadSource, /initialVisibleLoadStarted = true[\s\S]*const generation = initialVisibleLoadGeneration[\s\S]*void loadData\(\)\.finally/, '首次可见加载必须以请求代次守护一次性初始化')
assert.match(initialVisibleLoadSource, /generation !== initialVisibleLoadGeneration\) return[\s\S]*initialVisibleLoadCompleted = true/, '仅当前且仍可见的首次请求才能完成初始化门禁')
assert.doesNotMatch(viewSource, /setInterval|setTimeout/, '健康监控页面不得建立无需求的定时轮询')
assert.match(viewSource, /onDeactivated\(\(\) => \{[\s\S]*requestCoordinator\.cancelList\(\)[\s\S]*invalidatePendingLoads\(\)/, 'KeepAlive 失活必须取消健康列表并使旧响应失效')
const activatedSource = viewSource.match(/onActivated\(\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
assert.match(activatedSource, /pageActive = true[\s\S]*loadInitialVisiblePage\(\)/, 'KeepAlive 重新激活必须恢复页面状态并仅检查首次可见初始化')
assert.doesNotMatch(activatedSource, /\bloadData\s*\(/, 'KeepAlive 重新激活不得直接常规刷新列表')
assert.match(viewSource, /watch\(\(\) => authState\.revision\.value[\s\S]*accounts\.value = \[\][\s\S]*pagination\.current = 1/, '身份变化必须清空旧健康数据并重置分页')
assert.doesNotMatch(viewSource.match(/watch\(\(\) => authState\.revision\.value[\s\S]*?\n\}\)/)?.[0] ?? '', /\bloadData\s*\(/, '身份变化不得自动加载健康数据')
assert.doesNotMatch(monitorRepositorySource, /\busage_records\b/i, '页面查询不得扫描使用记录明细')
assert.match(monitorRepositorySource, /31 \* 24/, '服务端必须限制最大 31 天')
assert.match(monitorRepositorySource, /FROM account_health_hourly/, '页面查询必须读取小时预聚合')
assert.match(monitorRepositorySource, /SELECT account_id, stat_hour, status, source_order/, '列表 SQL 必须只投影小时槽状态')
assert.match(monitorRepositorySource, /loadAccountHealthHourDetail/, '错误正文必须移入单点详情查询')
const aiHealthResultType = statsTypesSource.match(/export interface AiHealthListResult \{[\s\S]*?\n\}/)?.[0] ?? ''
assert.doesNotMatch(aiHealthResultType, /\btotal\b/, 'AI 健康响应类型不得声明未经 COUNT 证明的 total')

const coordinator = createAiHealthRequestCoordinator()
const listA = coordinator.beginList()
const listB = coordinator.beginList()
assert.equal(listA.signal.aborted, true, '新列表请求必须取消旧列表请求')
assert.equal(listA.isCurrent(), false, '旧列表响应不得覆盖新列表状态')
assert.equal(listB.isCurrent(), true, '最新列表响应必须保留提交权')
const detailA = coordinator.beginDetail()
const detailB = coordinator.beginDetail()
assert.equal(detailA.signal.aborted, true, '切换小时槽必须取消旧详情请求')
assert.equal(detailA.isCurrent(), false, '旧详情响应不得覆盖新抽屉状态')
assert.equal(detailB.isCurrent(), true, '最新详情响应必须保留提交权')
coordinator.dispose()
assert.equal(listB.isCurrent(), false, '卸载后列表请求不得提交')
assert.equal(detailB.isCurrent(), false, '卸载后详情请求不得提交')

const lateCoordinator = createAiHealthRequestCoordinator()
let appliedList = ''
const lateList = lateCoordinator.beginList()
const lateCommit = Promise.resolve().then(async () => {
  await Promise.resolve()
  if (lateList.isCurrent()) appliedList = 'late'
})
const currentList = lateCoordinator.beginList()
const currentCommit = Promise.resolve().then(() => {
  if (currentList.isCurrent()) appliedList = 'current'
})
await Promise.all([lateCommit, currentCommit])
assert.equal(appliedList, 'current', '迟到的旧列表响应不得覆盖当前请求结果')

let appliedDetail = ''
const lateDetail = lateCoordinator.beginDetail()
const lateDetailCommit = Promise.resolve().then(async () => {
  await Promise.resolve()
  if (lateDetail.isCurrent()) appliedDetail = 'late'
})
const currentDetail = lateCoordinator.beginDetail()
const currentDetailCommit = Promise.resolve().then(() => {
  if (currentDetail.isCurrent()) appliedDetail = 'current'
})
await Promise.all([lateDetailCommit, currentDetailCommit])
assert.equal(appliedDetail, 'current', '迟到的旧详情响应不得覆盖当前抽屉结果')
lateCoordinator.dispose()

console.log('AI 健康监控前端契约回归通过')
